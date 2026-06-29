// Package record implements the logging state machine.
//
// Every frame is appended to a temp NDJSON file the moment it arrives, so the
// session is always being captured. Two ways it becomes permanent:
//
//   - Ctrl+S (SaveAs): the temp file is renamed to <name>.ndjson and recording
//     continues appending to it ("armed"). Further frames auto-persist.
//   - On exit: if not yet armed, the UI asks whether to keep it; KeepAs renames
//     the temp file, Discard deletes it.
//
// A bounded in-memory ring of recent frames per host backs the double-click
// detail view (a process's 1-second CPU/MEM/disk timeline) without re-reading
// the file.
package record

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"rtaskmgr/internal/monitor"
)

const ringCap = 900 // ~15 min of 1s frames per host

// Point is one sample of a single process for the detail timeline.
type Point struct {
	T      int64   `json:"t"`
	CPU    float64 `json:"cpu"`
	MemPct float64 `json:"memPct"`
	RSSKiB int64   `json:"rssKiB"`
	DiskR  int64   `json:"diskR"`
	DiskW  int64   `json:"diskW"`
}

type ring struct {
	frames []monitor.Frame
	start  int
	size   int
}

func (r *ring) push(f monitor.Frame) {
	if len(r.frames) < ringCap {
		r.frames = append(r.frames, f)
		r.size = len(r.frames)
		return
	}
	r.frames[r.start] = f
	r.start = (r.start + 1) % ringCap
}

func (r *ring) inOrder() []monitor.Frame {
	if r.size < ringCap {
		return r.frames
	}
	out := make([]monitor.Frame, 0, r.size)
	out = append(out, r.frames[r.start:]...)
	out = append(out, r.frames[:r.start]...)
	return out
}

type Recorder struct {
	mu      sync.Mutex
	dir     string
	tmpPath string
	f       *os.File
	enc     *json.Encoder
	named   bool
	saved   string
	frames  int
	rings   map[string]*ring
}

// New creates ~/.rtaskmgr/sessions and opens a fresh temp capture file.
func New() (*Recorder, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(home, ".rtaskmgr", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	tmp := filepath.Join(dir, fmt.Sprintf(".capture-%d.ndjson.tmp", time.Now().UnixMilli()))
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	return &Recorder{
		dir:     dir,
		tmpPath: tmp,
		f:       f,
		enc:     json.NewEncoder(f),
		rings:   map[string]*ring{},
	}, nil
}

// Record appends the frame to the capture file and the host's in-memory ring.
func (r *Recorder) Record(f monitor.Frame) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f != nil {
		_ = r.enc.Encode(f) // NDJSON: Encode writes one object + newline
		r.frames++
	}
	rg := r.rings[f.HostID]
	if rg == nil {
		rg = &ring{}
		r.rings[f.HostID] = rg
	}
	rg.push(f)
}

// SaveAs renames the temp capture to <name>.ndjson and keeps appending to it.
// This is the Ctrl+S path. No-op-with-rename if already armed (save under a new
// name and continue there).
func (r *Recorder) SaveAs(name string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	dest := filepath.Join(r.dir, sanitize(name)+".ndjson")

	if r.f != nil {
		r.f.Close()
		r.f = nil
	}
	if err := os.Rename(r.tmpPath, dest); err != nil {
		// Cross-device or already-moved: fall back to copy.
		if data, rerr := os.ReadFile(r.tmpPath); rerr == nil {
			if werr := os.WriteFile(dest, data, 0o600); werr == nil {
				os.Remove(r.tmpPath)
			} else {
				return "", werr
			}
		} else {
			return "", err
		}
	}
	f, err := os.OpenFile(dest, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	r.f = f
	r.enc = json.NewEncoder(f)
	r.tmpPath = dest
	r.named = true
	r.saved = dest
	return dest, nil
}

// Discard closes and deletes the temp capture (exit-without-save path). It does
// nothing if the session was already armed/saved.
func (r *Recorder) Discard() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.named {
		if r.f != nil {
			r.f.Close()
			r.f = nil
		}
		return nil
	}
	if r.f != nil {
		r.f.Close()
		r.f = nil
	}
	return os.Remove(r.tmpPath)
}

// IsNamed reports whether the session has been armed (saved under a name).
func (r *Recorder) IsNamed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.named
}

// HasData reports whether any frame has been captured (used to skip the on-exit
// save prompt for an empty session).
func (r *Recorder) HasData() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.frames > 0
}

func (r *Recorder) SavedPath() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.saved
}

// History returns a process's recent timeline from the in-memory ring.
func (r *Recorder) History(hostID string, pid int) []Point {
	r.mu.Lock()
	defer r.mu.Unlock()
	rg := r.rings[hostID]
	if rg == nil {
		return nil
	}
	var out []Point
	for _, f := range rg.inOrder() {
		for _, p := range f.Procs {
			if p.PID == pid {
				out = append(out, Point{
					T: f.T, CPU: p.CPU, MemPct: p.MemPct,
					RSSKiB: p.RSSKiB, DiskR: p.DiskR, DiskW: p.DiskW,
				})
				break
			}
		}
	}
	return out
}

// sanitize strips path separators and other awkward characters from a
// user-supplied log name so it stays a single safe filename.
func sanitize(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = fmt.Sprintf("session-%d", time.Now().Unix())
	}
	repl := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "*", "_",
		"?", "_", "\"", "_", "<", "_", ">", "_", "|", "_")
	return repl.Replace(name)
}
