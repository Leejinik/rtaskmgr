// Package store holds small local side-tables that are not part of the host
// inventory: right now, where each downloaded pcap landed on this PC.
//
// The ledger exists so the capture list can offer "open folder" / "open in
// Wireshark" / "analyse" for a file we already have, and so phase 2 can find a
// pcap by capture id. It is a convenience index, never a source of truth: every
// lookup tolerates a missing entry, and an entry whose file has since been moved
// or deleted is pruned on read.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// PcapEntry is one downloaded capture on this machine.
type PcapEntry struct {
	HostID   string    `json:"hostId"`
	HostName string    `json:"hostName"`
	CapID    string    `json:"capId"`
	Path     string    `json:"path"`
	Bytes    int64     `json:"bytes"`
	SavedAt  time.Time `json:"savedAt"`
}

type pcapFile struct {
	Version int         `json:"version"`
	Entries []PcapEntry `json:"entries"`
}

type PcapLocal struct {
	mu   sync.Mutex
	path string
}

// NewPcapLocal opens (creating if needed) ~/.rtaskmgr/pcaplocal.json.
func NewPcapLocal() (*PcapLocal, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("home dir: %w", err)
	}
	dir := filepath.Join(home, ".rtaskmgr")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", dir, err)
	}
	return &PcapLocal{path: filepath.Join(dir, "pcaplocal.json")}, nil
}

func (p *PcapLocal) load() (pcapFile, error) {
	var f pcapFile
	data, err := os.ReadFile(p.path)
	if errors.Is(err, os.ErrNotExist) || len(data) == 0 {
		return pcapFile{Version: 1}, nil
	}
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return pcapFile{Version: 1}, nil // a corrupt index is not worth an error
	}
	if f.Version == 0 {
		f.Version = 1
	}
	return f, nil
}

func (p *PcapLocal) save(f pcapFile) error {
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := p.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p.path)
}

// Put records (or replaces) where a capture was saved.
func (p *PcapLocal) Put(e PcapEntry) error {
	if e.CapID == "" || e.Path == "" {
		return fmt.Errorf("capId and path are required")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	f, err := p.load()
	if err != nil {
		return err
	}
	if e.SavedAt.IsZero() {
		e.SavedAt = time.Now()
	}
	out := make([]PcapEntry, 0, len(f.Entries)+1)
	for _, x := range f.Entries {
		if x.HostID == e.HostID && x.CapID == e.CapID {
			continue
		}
		out = append(out, x)
	}
	f.Entries = append(out, e)
	return p.save(f)
}

// Get returns the local file for one capture, if we still have it. An entry whose
// file has vanished is dropped so the UI never offers to open a missing path.
func (p *PcapLocal) Get(hostID, capID string) (PcapEntry, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f, err := p.load()
	if err != nil {
		return PcapEntry{}, false
	}
	for i, x := range f.Entries {
		if x.HostID != hostID || x.CapID != capID {
			continue
		}
		if _, serr := os.Stat(x.Path); serr != nil {
			f.Entries = append(f.Entries[:i:i], f.Entries[i+1:]...)
			_ = p.save(f)
			return PcapEntry{}, false
		}
		return x, true
	}
	return PcapEntry{}, false
}

// All returns every still-present entry, newest first, pruning stale rows.
func (p *PcapLocal) All() ([]PcapEntry, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f, err := p.load()
	if err != nil {
		return nil, err
	}
	kept := make([]PcapEntry, 0, len(f.Entries))
	pruned := false
	for _, x := range f.Entries {
		if _, serr := os.Stat(x.Path); serr != nil {
			pruned = true
			continue
		}
		kept = append(kept, x)
	}
	if pruned {
		f.Entries = kept
		_ = p.save(f)
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].SavedAt.After(kept[j].SavedAt) })
	return kept, nil
}

// Forget removes one entry (the file itself is left alone).
func (p *PcapLocal) Forget(hostID, capID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	f, err := p.load()
	if err != nil {
		return err
	}
	out := make([]PcapEntry, 0, len(f.Entries))
	for _, x := range f.Entries {
		if x.HostID == hostID && x.CapID == capID {
			continue
		}
		out = append(out, x)
	}
	f.Entries = out
	return p.save(f)
}
