// Streaming download of one remote file.
//
// This is the plumbing the packet capture proved out, lifted so the log collector
// reuses it instead of growing a second, less careful copy. Everything here exists
// because of a failure that actually happened: the separate stderr (a shell warning
// ahead of the gzip header corrupts the whole file), the stall watchdog (a half-open
// TCP connection never returns an error from Read, so no amount of ctx checking
// between reads gets you out), the copy error being handled BEFORE Wait (otherwise a
// local disk-full is reported a minute later as "the connection stopped responding"),
// the atomic rename (the path the user chose never holds a half file) and the re-stat
// after it (a security agent that quarantines the file leaves the rename looking
// successful).
package monitor

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// remoteFileInfo is one probe round trip: the size, which also fixes the progress
// denominator, plus what the host can help with.
type remoteFileInfo struct {
	Size       int64
	HaveGzip   bool
	HaveIonice bool
}

// statRemoteFile probes one file. Size -1 means it could not be read at all, which
// callers must distinguish from an empty file (a capture that never started versus
// one that captured nothing are different problems).
func (m *Manager) statRemoteFile(s *session, remotePath string) (remoteFileInfo, error) {
	info := remoteFileInfo{Size: -1}
	out, _ := m.plainRun(s, `set -u; f=`+shellQuote(remotePath)+`; `+
		`sz=$(stat -c%s "$f" 2>/dev/null || echo -1); `+
		`g=0; command -v gzip >/dev/null 2>&1 && g=1; `+
		`i=0; command -v ionice >/dev/null 2>&1 && i=1; echo "SZ|$sz|$g|$i"`)
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "SZ|") {
			continue
		}
		p := strings.Split(strings.TrimSpace(line[3:]), "|")
		if len(p) != 3 {
			continue
		}
		info.Size, _ = strconv.ParseInt(p[0], 10, 64)
		info.HaveGzip, info.HaveIonice = p[1] == "1", p[2] == "1"
		return info, nil
	}
	return info, fmt.Errorf("파일 정보를 읽을 수 없습니다: %s", tailLines(out, 2))
}

// streamRemoteFile copies info.Size bytes of remotePath to localPath.
//
// wireGzip compresses on the wire. It is worth it for a pcap (mostly repetitive
// headers) and pointless for an already-compressed archive, where it would only burn
// CPU on a host that is by definition already in trouble. When it IS on, gzip's
// trailer verifies the transfer end to end for free; when it is off the caller is
// expected to have its own check (the log collector reads the archive back).
func (m *Manager) streamRemoteFile(ctx context.Context, s *session, remotePath, localPath string,
	info remoteFileInfo, wireGzip bool, progress func(copied, total int64)) (int64, error) {

	total := info.Size
	if err := ensureLocalRoom(localPath, total); err != nil {
		return 0, err
	}

	// head -c <total> pins the denominator: without it a file still being written
	// grows while we read and the progress bar sails past 100%.
	// nice/ionice keep us off the CPU and disk of a host that is already sick.
	pre := "nice -n 19 "
	if info.HaveIonice {
		pre += "ionice -c3 "
	}
	remote := `set -u; f=` + shellQuote(remotePath) + `; ` + pre +
		`head -c ` + strconv.FormatInt(total, 10) + ` -- "$f"`
	if wireGzip {
		remote += ` | nice -n 19 gzip -1 -c`
	}

	dctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sess, err := s.client.NewSession()
	if err != nil {
		return 0, err
	}
	defer sess.Close()
	stdout, err := sess.StdoutPipe()
	if err != nil {
		return 0, err
	}
	// Stderr MUST be separate: one shell warning ahead of the gzip header would
	// corrupt the whole file (this is why CombinedOutput is banned here).
	var errBuf bytes.Buffer
	sess.Stderr = &errBuf
	if err := sess.Start("bash -c " + shellQuote(remote)); err != nil {
		return 0, err
	}

	// Closing the session is what actually unblocks a stalled Read; the remote
	// head/gzip then dies on SIGPIPE at its next write.
	done := make(chan struct{})
	go func() {
		select {
		case <-dctx.Done():
			sess.Close()
		case <-done:
		}
	}()
	defer close(done)

	tmp, err := os.CreateTemp(filepath.Dir(localPath), ".rtm-dl-*")
	if err != nil {
		return 0, fmt.Errorf("임시 파일 생성 실패: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
	}

	pw := &progressWriter{total: total, fn: progress}
	pw.mark()
	// Stall watchdog: give up on a connection that has gone quiet rather than
	// hanging the download forever. `stalled` records that WE gave up, so a local
	// write failure is never misreported as a dead connection.
	var stalled atomic.Bool
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if time.Since(time.Unix(0, pw.lastAt.Load())) > dlStallSeconds*time.Second {
					stalled.Store(true)
					cancel()
					return
				}
			}
		}
	}()

	var src io.Reader = stdout
	if wireGzip {
		gzr, gerr := gzip.NewReader(stdout)
		if gerr != nil {
			cleanup()
			return 0, fmt.Errorf("전송 스트림을 열 수 없습니다: %v (%s)", gerr, tailLines(errBuf.String(), 2))
		}
		defer gzr.Close()
		src = gzr
	}
	buf := make([]byte, 1<<20)
	n, cerr := io.CopyBuffer(io.MultiWriter(tmp, pw), src, buf)
	// The copy error MUST be handled before Wait(). io.MultiWriter writes the temp
	// file first, so a local failure (disk full, an EDR agent locking the temp file)
	// returns with the remote command still streaming and nobody draining stdout —
	// the SSH channel window fills and Wait() blocks until the stall watchdog fires
	// a minute later, at which point the real cause has been replaced by "the
	// connection stopped responding". Closing the session first makes Wait() return
	// immediately and keeps the true error.
	if cerr != nil {
		cancel()
		_ = sess.Wait()
		cleanup()
		switch {
		case ctx.Err() != nil:
			return n, fmt.Errorf("다운로드가 취소되었습니다")
		case stalled.Load():
			return n, fmt.Errorf("다운로드가 응답하지 않습니다 (%d초간 진행 없음)", dlStallSeconds)
		}
		return n, fmt.Errorf("다운로드 실패: %v (%s)", cerr, tailLines(errBuf.String(), 2))
	}
	werr := sess.Wait()
	if werr != nil && n < total {
		cleanup()
		return n, fmt.Errorf("다운로드가 중간에 끊겼습니다: %v (%s)", werr, tailLines(errBuf.String(), 2))
	}
	if n != total {
		cleanup()
		return n, fmt.Errorf("전송 크기가 맞지 않습니다 (%d / %d 바이트)", n, total)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return n, fmt.Errorf("파일 기록 실패: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return n, fmt.Errorf("파일 닫기 실패: %w", err)
	}
	if err := os.Rename(tmpName, localPath); err != nil {
		os.Remove(tmpName)
		return n, fmt.Errorf("저장 실패: %w", err)
	}
	// Re-stat after the rename: a security agent that quarantines the file leaves
	// the rename looking successful (this app has been bitten by exactly that).
	if fi, serr := os.Stat(localPath); serr != nil || fi.Size() != total {
		return n, fmt.Errorf("저장된 파일을 확인할 수 없습니다 — 보안 소프트웨어가 파일을 격리했을 수 있습니다: %s", localPath)
	}
	if progress != nil {
		progress(n, total)
	}
	return n, nil
}
