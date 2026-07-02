// Package host persists the list of RHEL targets the user monitors. It mirrors
// the profile-store pattern used elsewhere: a single JSON file under the user's
// home, upsert-by-ID, with credentials stored inline (this is a local ops tool).
package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Host is one SSH target. Auth supports password and/or private-key. The same
// password is reused for sudo: the monitor automatically launches the sampler
// via `sudo -S` when the login user can elevate, so it can read every process's
// /proc/<pid>/io (disk I/O for processes the login user doesn't own). If sudo
// fails, it falls back to an unprivileged run with reduced data.
type Host struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Addr      string    `json:"addr"`
	Port      int       `json:"port"`
	User      string    `json:"user"`
	Password  string    `json:"password"`
	KeyPath   string    `json:"keyPath"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`

	// Cluster grouping. Empty ClusterID means a standalone host. Hosts sharing a
	// ClusterID are shown as one collapsible group in the sidebar and can be
	// connected/disconnected together and viewed on the cluster overview.
	ClusterID   string `json:"clusterId,omitempty"`
	ClusterName string `json:"clusterName,omitempty"`
}

func (h Host) port() int {
	if h.Port == 0 {
		return 22
	}
	return h.Port
}

type file struct {
	Version int    `json:"version"`
	Hosts   []Host `json:"hosts"`
}

type Store struct {
	mu   sync.Mutex
	path string
}

// New opens (creating if needed) ~/.rtaskmgr/hosts.json.
func New() (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("home dir: %w", err)
	}
	dir := filepath.Join(home, ".rtaskmgr")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", dir, err)
	}
	return &Store{path: filepath.Join(dir, "hosts.json")}, nil
}

func (s *Store) load() (file, error) {
	var f file
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) || len(data) == 0 {
		return file{Version: 1}, nil
	}
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return f, fmt.Errorf("parse %s: %w", s.path, err)
	}
	if f.Version == 0 {
		f.Version = 1
	}
	return f, nil
}

func (s *Store) persist(f file) error {
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o600)
}

func (s *Store) List() ([]Host, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return nil, err
	}
	sort.Slice(f.Hosts, func(i, j int) bool { return f.Hosts[i].Name < f.Hosts[j].Name })
	return f.Hosts, nil
}

// Get returns a single host by ID.
func (s *Store) Get(id string) (Host, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return Host{}, false, err
	}
	for _, h := range f.Hosts {
		if h.ID == id {
			return h, true, nil
		}
	}
	return Host{}, false, nil
}

// Save upserts by ID (assigning a UUID when empty) and returns the stored record.
func (s *Store) Save(h Host) (Host, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if h.Name == "" {
		h.Name = h.Addr
	}
	if h.Addr == "" {
		return h, fmt.Errorf("host address is required")
	}
	f, err := s.load()
	if err != nil {
		return h, err
	}
	now := time.Now().UTC()
	if h.ID == "" {
		h.ID = uuid.NewString()
		h.CreatedAt = now
	}
	h.UpdatedAt = now
	found := false
	for i, existing := range f.Hosts {
		if existing.ID == h.ID {
			if h.CreatedAt.IsZero() {
				h.CreatedAt = existing.CreatedAt
			}
			f.Hosts[i] = h
			found = true
			break
		}
	}
	if !found {
		f.Hosts = append(f.Hosts, h)
	}
	if err := s.persist(f); err != nil {
		return h, err
	}
	return h, nil
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return err
	}
	out := f.Hosts[:0]
	for _, h := range f.Hosts {
		if h.ID != id {
			out = append(out, h)
		}
	}
	f.Hosts = out
	return s.persist(f)
}
