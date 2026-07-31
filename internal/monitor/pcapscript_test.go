package monitor

import (
	"bytes"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"strings"
	"testing"
)

// scriptSources returns every shell text this package can send to a host: the Go
// sources that build commands, plus the embedded wrapper.
//
// Comments are stripped first — these tests ban shell constructs, and the code
// that bans them names them in its own comments explaining why.
func scriptSources(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, f := range []string{"monitor.go", "pcap.go", "passwd.go", "download.go", "logcollect_run.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		out[f] = stripGoComments(t, f, b)
	}
	out["pcapd.sh"] = stripShellComments(string(pcapdScript))
	return out
}

// stripGoComments reprints a Go file without its comments.
func stripGoComments(t *testing.T, name string, src []byte) string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, 0) // no ParseComments → dropped
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, f); err != nil {
		t.Fatalf("print %s: %v", name, err)
	}
	return buf.String()
}

// stripShellComments drops whole-line "#" comments (the shebang is asserted
// separately) while leaving code lines, including any trailing comment, alone —
// a "#" can appear inside a shell string, so only unambiguous lines are removed.
func stripShellComments(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// TestNoPkillDashF pins invariant I5. `pkill -f <path>` matches on the whole
// command line of every process, which includes our own parent shell, another
// session's stop script and an operator's `tail`/`scp` on the same file — so
// stopping our recording could kill someone else's work. Every stop path must
// identify PIDs positively (comm + cmdline) instead.
func TestNoPkillDashF(t *testing.T) {
	for name, src := range scriptSources(t) {
		for _, banned := range []string{"pkill -f", "pkill -TERM -f", "pkill -KILL -f", "pgrep -f"} {
			if strings.Contains(src, banned) {
				t.Errorf("%s contains %q — see I5 (never match processes by command line)", name, banned)
			}
		}
	}
}

// TestNoGlobalPkillInCaptureAndRecording keeps the capture and scheduled-recording
// paths free of name-wide kills. (The nethogs install/rollback path still uses
// `pkill -x nethogs`; that is tracked separately as F22 and is not in scope here,
// so this test scopes itself to pcap.go and the wrapper.)
func TestNoGlobalPkillInCaptureAndRecording(t *testing.T) {
	src := scriptSources(t)
	for _, name := range []string{"pcap.go", "pcapd.sh"} {
		if strings.Contains(src[name], "pkill") {
			t.Errorf("%s must not use pkill at all", name)
		}
	}
	// The scheduled stop path must be PID-based now.
	if strings.Contains(src["monitor.go"], "pkill -TERM -f") {
		t.Error("StopScheduled regressed to pkill -f")
	}
	if !strings.Contains(src["monitor.go"], "samplerComm()") {
		t.Error("StopScheduled no longer identifies the sampler by comm")
	}
}

// TestScriptsHaveSetU pins invariant I2: an unset variable inside a generated
// script must abort it, not expand to nothing and act on the wrong path.
func TestScriptsHaveSetU(t *testing.T) {
	if !strings.HasPrefix(string(pcapdScript), "#!/bin/bash") {
		t.Error("pcapd.sh lost its bash shebang (the ARGS array and read -t need bash)")
	}
	if !strings.Contains(string(pcapdScript), "\nset -u") {
		t.Error("pcapd.sh must start with set -u")
	}
	// set -e would kill the wrapper on the first benign non-zero inside the poll
	// loop (a `kill -0` on an exited child, a missing stat) and it would then never
	// write its .done marker.
	for _, bad := range []string{"\nset -e", "set -eu", "set -ue"} {
		if strings.Contains(string(pcapdScript), bad) {
			t.Errorf("pcapd.sh must not use %q", strings.TrimSpace(bad))
		}
	}
	if !strings.Contains(string(pcapdScript), "set -C") {
		t.Error("pcapd.sh must set -C (noclobber) so '>' cannot follow a planted symlink")
	}
	if !strings.Contains(string(pcapdScript), "umask 0077") {
		t.Error("pcapd.sh must umask 0077")
	}
	// Every capture script we generate declares set -u.
	src := scriptSources(t)["pcap.go"]
	for _, want := range []string{"`set -u", "`set -u; ", "set -u\n"} {
		if strings.Contains(src, want) {
			return
		}
	}
	t.Error("pcap.go generates scripts without set -u")
}

// TestPcapdNoCRLF guards the embedded wrapper: a single CR would break the
// shebang and every line of it once decoded on the host.
func TestPcapdNoCRLF(t *testing.T) {
	if strings.Contains(string(pcapdScript), "\r") {
		t.Error("pcapd.sh contains CR — it must be stored with LF endings")
	}
}

// TestPcapdBackstops keeps the two enforcers that survive the wrapper being
// SIGKILLed. Without them a detached capture can fill the disk unbounded, and a
// detached process cannot be heartbeated the way the nethogs stream is (I7).
func TestPcapdBackstops(t *testing.T) {
	s := string(pcapdScript)
	for _, want := range []string{"ulimit -f", "ulimit -c 0", "timeout -s TERM -k 10"} {
		if !strings.Contains(s, want) {
			t.Errorf("pcapd.sh lost its backstop: %q", want)
		}
	}
	// The savefile must be handed to the login user, or the whole sudo-free
	// list/stop/download/delete path collapses.
	if !strings.Contains(s, `ARGS+=(-Z "$USER_")`) {
		t.Error("pcapd.sh no longer drops privileges for the savefile (-Z)")
	}
	// Regression: the pid sidecars must be handed over as soon as they exist, not
	// at the end. They are created root-owned 0600 under umask 0077, and the client
	// reads them as the login user with no sudo — when they were unreadable the
	// liveness check found no pids and reported every live capture as
	// "interrupted" while its file kept growing. Found on a real host, not here:
	// the local smoke test runs everything as one user.
	if !strings.Contains(s, `chown -h "$UID_" "$LOG" "$PIDF" "$TPIDF"`) {
		t.Error("pcapd.sh must chown .log/.pid/.tpid to the login user right after creating them")
	}
	// The handover must come BEFORE tcpdump is launched, or there is a window in
	// which the capture reads as interrupted.
	iChown := strings.Index(s, `chown -h "$UID_" "$LOG" "$PIDF" "$TPIDF"`)
	iExec := strings.Index(s, "exec timeout -s TERM")
	if iChown < 0 || iExec < 0 || iChown > iExec {
		t.Error("pcapd.sh must hand the sidecars over before starting tcpdump")
	}
	// df/stat must stay timeout-wrapped: a hung NFS mount would put the poll loop
	// in uninterruptible sleep, where neither the deadline nor .stop can act.
	if !strings.Contains(s, "timeout 5 stat") || !strings.Contains(s, "timeout 5 df") {
		t.Error("pcapd.sh must wrap df/stat in timeout")
	}
}

// TestStopScriptUsesPidFilesOnly pins the escalation path: it may only signal PIDs
// the wrapper recorded, and only after confirming comm and the capture path.
func TestStopScriptUsesPidFilesOnly(t *testing.T) {
	src := scriptSources(t)["pcap.go"]
	i := strings.Index(src, "func (m *Manager) StopCaptureForce")
	if i < 0 {
		t.Fatal("StopCaptureForce not found")
	}
	body := src[i:]
	if j := strings.Index(body, "\nfunc "); j > 0 {
		body = body[:j]
	}
	if !strings.Contains(body, `cat "$f.pid" "$f.tpid"`) {
		t.Error("force stop must read the PIDs the wrapper recorded")
	}
	if !strings.Contains(body, "rtm-pcapd|timeout|tcpdump") {
		t.Error("force stop must confirm /proc/<pid>/comm against the allowlist")
	}
	if !strings.Contains(body, `case "$c" in *"$f"*)`) {
		t.Error("force stop must confirm the capture path is in the target's cmdline")
	}
	if strings.Contains(body, "/proc/[0-9]*") {
		t.Error("force stop must not sweep all of /proc")
	}
	if !strings.Contains(body, `flock -n 9`) {
		t.Error("force stop must serialise with flock so two operators cannot race")
	}
}

// TestDeleteScriptIsExplicit keeps deletion narrow: named files only, never a
// recursive or glob-driven remove.
func TestDeleteScriptIsExplicit(t *testing.T) {
	src := scriptSources(t)["pcap.go"]
	i := strings.Index(src, "func (m *Manager) deleteCaptureFiles")
	if i < 0 {
		t.Fatal("deleteCaptureFiles not found")
	}
	body := src[i:]
	if j := strings.Index(body, "\nfunc "); j > 0 {
		body = body[:j]
	}
	if strings.Contains(body, "rm -rf") {
		t.Error("deleteCaptureFiles must not use rm -rf (a pcap is not a directory)")
	}
	if !strings.Contains(body, "rm -f -- ") {
		t.Error("deleteCaptureFiles must pass -- before the paths")
	}
	if !strings.Contains(body, "validCapID(id)") || !strings.Contains(body, "validAbsPath") {
		t.Error("deleteCaptureFiles must re-validate the id and every path")
	}
}

// TestCaptureListDoesNotTrustRemoteMeta pins the rule that a host cannot dictate
// an id, a local path, or a status.
func TestCaptureListDoesNotTrustRemoteMeta(t *testing.T) {
	src := scriptSources(t)["pcap.go"]
	i := strings.Index(src, "func (m *Manager) ListCaptures")
	if i < 0 {
		t.Fatal("ListCaptures not found")
	}
	body := src[i:]
	if j := strings.Index(body, "\nfunc "); j > 0 {
		body = body[:j]
	}
	if !strings.Contains(body, "validCapID(id)") {
		t.Error("ListCaptures must validate every id it parses")
	}
	if strings.Contains(body, "c.LocalPath = hm.LocalPath") || strings.Contains(body, "hm.LocalPath") {
		t.Error("ListCaptures must never copy LocalPath from the host's JSON")
	}
	if strings.Contains(body, "c.ID = hm.ID") || strings.Contains(body, "c.File = hm.File") {
		t.Error("ListCaptures must derive id/file from the remote basename, not the JSON")
	}
	if strings.Contains(body, "c.Status = hm.Status") {
		t.Error("ListCaptures must compute Status itself")
	}
}

// TestCaptureDirsAreVetted pins the fix for the worst bug an adversarial review
// found: the search bases deliberately include the 1777 dirs (/tmp, /var/tmp,
// /dev/shm) so a capture started under an earlier stageDir is never orphaned, so
// EVERY scan and resolve has to prove the directory is a real, login-user-owned,
// non-group-writable directory. Without it a local user could plant
// /tmp/.rtaskmgr-pcap with a fake "stopping" row and have the operator's 강제 중지
// make root truncate a file of their choosing.
func TestCaptureDirsAreVetted(t *testing.T) {
	src := scriptSources(t)["pcap.go"]
	if !strings.Contains(src, "rtm_trusted()") {
		t.Fatal("trustedDirSh no longer defines rtm_trusted")
	}
	// The predicate must test all four properties.
	for _, want := range []string{`[ -L "$d" ] && return 1`, `[ -d "$d" ] || return 1`,
		`stat -c%u "$d"`, `& 022`} {
		if !strings.Contains(src, want) {
			t.Errorf("rtm_trusted lost its %q check", want)
		}
	}
	// Both entry points must use it.
	for _, fn := range []string{"func capListScript", "func (m *Manager) resolveCapFile"} {
		i := strings.Index(src, fn)
		if i < 0 {
			t.Fatalf("%s not found", fn)
		}
		body := src[i:]
		if j := strings.Index(body, "\nfunc "); j > 0 {
			body = body[:j]
		}
		if !strings.Contains(body, `rtm_trusted "$d" || continue`) {
			t.Errorf("%s does not vet the directory before acting on it", fn)
		}
	}
}

// TestForceStopScriptIsSymlinkSafe pins the second line of defence on the one
// script that redirects into a capture path AS ROOT.
func TestForceStopScriptIsSymlinkSafe(t *testing.T) {
	src := scriptSources(t)["pcap.go"]
	i := strings.Index(src, "func (m *Manager) StopCaptureForce")
	if i < 0 {
		t.Fatal("StopCaptureForce not found")
	}
	body := src[i:]
	if j := strings.Index(body, "\nfunc "); j > 0 {
		body = body[:j]
	}
	if !strings.Contains(body, "set -u; set -C;") {
		t.Error("the root force-stop script must set -C so '>' cannot follow a symlink")
	}
	for _, want := range []string{`[ -L "$f.lock" ]`, `[ -L "$f.done" ]`} {
		if !strings.Contains(body, want) {
			t.Errorf("the root force-stop script must refuse a symlink at %s", want)
		}
	}
	// noclobber would reject a legitimate pre-existing lock, so the lock is opened
	// for append — which is exactly why the -L check above is mandatory.
	if !strings.Contains(body, `exec 9>>"$f.lock"`) {
		t.Error("the lock must be opened with >> under noclobber")
	}
}

// TestCaptureSidecarReadsAreBounded keeps invariant I8 on the polling path: the
// list script reads two host-controlled sidecars, and a sparse multi-GB file
// planted at either one would otherwise be materialised in remote bash, on the
// wire and in the app — every watcher tick.
func TestCaptureSidecarReadsAreBounded(t *testing.T) {
	src := scriptSources(t)["pcap.go"]
	i := strings.Index(src, "func capListScript")
	if i < 0 {
		t.Fatal("capListScript not found")
	}
	body := src[i:]
	if j := strings.Index(body, "\nfunc "); j > 0 {
		body = body[:j]
	}
	if !strings.Contains(body, `head -c 256 "$f.done"`) {
		t.Error("the .done marker read must be byte-bounded")
	}
	if !strings.Contains(body, `head -c 256`) || !strings.Contains(body, `cat "$f.pid" "$f.tpid" 2>/dev/null | head -c 256`) {
		t.Error("the .pid/.tpid read must be byte-bounded")
	}
	if !strings.Contains(body, `tail -c 2048 "$f.log"`) || !strings.Contains(body, `head -c 65536 "$mf"`) {
		t.Error("the .log tail and meta read must stay bounded")
	}
}

// TestOrphanIsRederived pins the fix for a permanently undeletable row: "orphan"
// is a snapshot the dying wrapper took microseconds after it SIGKILLed tcpdump,
// and nothing ever rewrites .done — so taking the marker at face value forever
// left the operator with a ghost row even after they cleaned the process up.
func TestOrphanIsRederived(t *testing.T) {
	src := scriptSources(t)["pcap.go"]
	i := strings.Index(src, "func (m *Manager) DeleteCapture")
	if i < 0 {
		t.Fatal("DeleteCapture not found")
	}
	body := src[i:]
	if j := strings.Index(body, "\nfunc "); j > 0 {
		body = body[:j]
	}
	if !strings.Contains(body, "captureStillHeld") {
		t.Error("the orphan branch must re-verify against /proc instead of trusting .done")
	}
	// captureStillHeld must be a positive, report-only test — never a kill.
	j := strings.Index(src, "func (m *Manager) captureStillHeld")
	if j < 0 {
		t.Fatal("captureStillHeld not found")
	}
	held := src[j:]
	if k := strings.Index(held, "\nfunc "); k > 0 {
		held = held[:k]
	}
	if strings.Contains(held, "kill") {
		t.Error("captureStillHeld must only report, never signal")
	}
	if !strings.Contains(held, `[ "$k" = tcpdump ]`) || !strings.Contains(held, `case "$c" in *"$f"*)`) {
		t.Error("captureStillHeld must confirm both comm and the capture path")
	}
}

// TestForgetKeepsTheCaptureFile pins the escape hatch for a row whose liveness
// cannot be determined (hidepid). It must never remove the pcap: something may
// still be writing it, and unlinking an open savefile leaks the disk invisibly.
func TestForgetKeepsTheCaptureFile(t *testing.T) {
	src := scriptSources(t)["pcap.go"]
	i := strings.Index(src, "func (m *Manager) ForgetCapture")
	if i < 0 {
		t.Fatal("ForgetCapture not found")
	}
	body := src[i:]
	if j := strings.Index(body, "\nfunc "); j > 0 {
		body = body[:j]
	}
	for _, banned := range []string{`base + ".pcap",`, `base + ".pcap.log"`, `base + ".pcap.done"`} {
		if strings.Contains(body, banned) {
			t.Errorf("ForgetCapture must not delete %s", banned)
		}
	}
	for _, want := range []string{`base + ".meta.json"`, `base + ".pcap.pid"`, `base + ".pcap.tpid"`} {
		if !strings.Contains(body, want) {
			t.Errorf("ForgetCapture must drop %s", want)
		}
	}
	// It may only be used where no other exit exists, or it becomes a way to
	// litter the host with untracked captures.
	if !strings.Contains(body, "if !uncertain && st != capReasonOrphan") {
		t.Error("ForgetCapture must be restricted to unverifiable or orphaned rows")
	}
}

// TestDownloadHandlesCopyErrorBeforeWait pins the ordering fix: with the remote
// still streaming and nobody draining stdout, sess.Wait() blocks until the stall
// watchdog fires a minute later — and the operator is then told the connection
// died when their disk was actually full.
func TestDownloadHandlesCopyErrorBeforeWait(t *testing.T) {
	src := scriptSources(t)["download.go"]
	i := strings.Index(src, "func (m *Manager) streamRemoteFile")
	if i < 0 {
		t.Fatal("streamRemoteFile not found")
	}
	body := src[i:]
	if j := strings.Index(body, "\nfunc "); j > 0 {
		body = body[:j]
	}
	iCopy := strings.Index(body, "io.CopyBuffer")
	iCerr := strings.Index(body, "if cerr != nil")
	iWait := strings.Index(body, "werr := sess.Wait()")
	if iCopy < 0 || iCerr < 0 || iWait < 0 {
		t.Fatal("could not locate the copy/error/wait sequence")
	}
	if !(iCopy < iCerr && iCerr < iWait) {
		t.Error("the copy error must be handled before sess.Wait(), or a local write failure hangs for a minute")
	}
	if !strings.Contains(body, "stalled.Load()") {
		t.Error("a stall must be distinguished from a local failure, not inferred from the context")
	}
}

// TestNoBase64WholeFileInCapturePath pins invariant I8 for the download path — now
// shared by the packet capture and the log archive, so a regression here breaks both.
func TestNoBase64WholeFileInCapturePath(t *testing.T) {
	src := scriptSources(t)["download.go"]
	i := strings.Index(src, "func (m *Manager) streamRemoteFile")
	if i < 0 {
		t.Fatal("streamRemoteFile not found")
	}
	body := src[i:]
	if j := strings.Index(body, "\nfunc "); j > 0 {
		body = body[:j]
	}
	for _, banned := range []string{"base64 -w0", "CombinedOutput"} {
		if strings.Contains(body, banned) {
			t.Errorf("the download path must not use %q on a multi-GB file", banned)
		}
	}
	if !strings.Contains(body, "StdoutPipe") || !strings.Contains(body, "sess.Stderr") {
		t.Error("the download must stream stdout with stderr kept separate")
	}
	if !strings.Contains(body, "head -c ") {
		t.Error("the download must pin the byte count with head -c (a file being written keeps growing)")
	}
}

// TestRecIDValidated pins the second of the pre-existing bugs the capture work
// was asked to fix: ids that arrive from the host must be validated before they
// are interpolated into a shell command.
func TestRecIDValidated(t *testing.T) {
	ok := []string{"rec-1753800000000", "rec-1234567890"}
	for _, id := range ok {
		if !validRecID(id) {
			t.Errorf("validRecID(%q) = false, want true", id)
		}
	}
	bad := []string{"", "rec-", "rec-abc", "rec-1 $(id)", "rec-1753800000000;rm -rf /",
		"../rec-1753800000000", "rec-1753800000000.ndjson", "$(reboot)"}
	for _, id := range bad {
		if validRecID(id) {
			t.Errorf("validRecID(%q) = true, want false", id)
		}
	}
	src := scriptSources(t)["monitor.go"]
	i := strings.Index(src, "func (m *Manager) resolveRecFile")
	if i < 0 {
		t.Fatal("resolveRecFile not found")
	}
	body := src[i : i+1200]
	if !strings.Contains(body, "validRecID(id)") {
		t.Error("resolveRecFile must validate the id before interpolating it")
	}
	if !strings.Contains(body, "validAbsPath(f)") {
		t.Error("resolveRecFile must validate the path the host returned")
	}
}

// TestSamplerComm keeps the comm-based identification honest about the kernel's
// 15-character limit.
func TestSamplerComm(t *testing.T) {
	c := samplerComm()
	if len(c) > 15 {
		t.Errorf("samplerComm() = %q (%d chars) — /proc/<pid>/comm truncates to 15", c, len(c))
	}
	if !strings.HasPrefix(samplerName, c) {
		t.Errorf("samplerComm() = %q is not a prefix of %q", c, samplerName)
	}
}
