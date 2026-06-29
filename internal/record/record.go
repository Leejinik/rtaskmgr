// Package record handles client-side recording state.
//
// Two independent things live here:
//   - A bounded in-memory ring of recent frames per host, always on, backing the
//     double-click detail view (a process's 1-second timeline).
//   - Explicit FILE recording: the user starts/stops it; while active, frames for
//     the target host are appended to a chosen .ndjson file on the local machine.
//     Recording never starts on its own, and is finalized on stop / disconnect /
//     app close so nothing keeps writing unattended.
package record

import (
	"encoding/json"
	"os"
	"sync"

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
	mu    sync.Mutex
	rings map[string]*ring

	recMu   sync.Mutex
	f       *os.File
	enc     *json.Encoder
	recHost string
	recPath string
}

func New() (*Recorder, error) {
	return &Recorder{rings: map[string]*ring{}}, nil
}

// Feed pushes every frame into the host's ring and, when file recording is
// active for that host, appends it to the recording file.
func (r *Recorder) Feed(f monitor.Frame) {
	r.mu.Lock()
	rg := r.rings[f.HostID]
	if rg == nil {
		rg = &ring{}
		r.rings[f.HostID] = rg
	}
	rg.push(f)
	r.mu.Unlock()

	r.recMu.Lock()
	if r.f != nil && f.HostID == r.recHost {
		_ = r.enc.Encode(f)
	}
	r.recMu.Unlock()
}

// StartFile begins appending the target host's frames to path. A previous
// recording (if any) is finalized first.
func (r *Recorder) StartFile(hostID, path string) error {
	r.StopFile()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	r.recMu.Lock()
	r.f = f
	r.enc = json.NewEncoder(f)
	r.recHost = hostID
	r.recPath = path
	r.recMu.Unlock()
	return nil
}

// StopFile finalizes the active recording and returns its path (or "").
func (r *Recorder) StopFile() string {
	r.recMu.Lock()
	defer r.recMu.Unlock()
	path := r.recPath
	if r.f != nil {
		r.f.Close()
		r.f = nil
		r.enc = nil
		r.recHost = ""
		r.recPath = ""
	}
	return path
}

func (r *Recorder) IsRecording() bool {
	r.recMu.Lock()
	defer r.recMu.Unlock()
	return r.f != nil
}

func (r *Recorder) RecordingHost() string {
	r.recMu.Lock()
	defer r.recMu.Unlock()
	return r.recHost
}

func (r *Recorder) RecordingPath() string {
	r.recMu.Lock()
	defer r.recMu.Unlock()
	return r.recPath
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
