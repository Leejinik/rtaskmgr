// Package pwledger persists the managed-account (liz/root) password-rotation
// ledger and its config under ~/.rtaskmgr/pwledger.json.
//
// The ledger exists for one reason: a rotation changes real passwords on a
// remote host over several steps (e.g. current -> temp -> current), and the
// network or the app can die between steps. Every step is recorded as "pending"
// BEFORE it runs and flipped to "ok"/"fail" after, so if things break midway the
// engineer can read this file and see exactly which password an account is
// currently sitting on — and still log in. Passwords are therefore stored in
// plaintext (same posture as hosts.json); this is a local, single-user ops tool.
package pwledger

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
)

// DefaultTempPassword is the fixed intermediate password a "renew" cycles
// through (current -> temp -> current) so the shadow last-change date advances
// while the account ends on its original password. Strong enough for typical
// RHEL pwquality; editable in the UI.
const DefaultTempPassword = "Rtm!Temp$Rotate#2026"

// DefaultWarnDays is how many days before expiry the connect-time alert fires.
const DefaultWarnDays = 10

type Config struct {
	TempPassword   string `json:"tempPassword"`
	ExpiryWarnDays int    `json:"expiryWarnDays"`
}

// Entry is one password-set step against one account on one host.
type Entry struct {
	ID       string    `json:"id"`
	HostID   string    `json:"hostId"`
	HostName string    `json:"hostName"`
	Addr     string    `json:"addr"`
	Account  string    `json:"account"` // "root" | "liz"
	Op       string    `json:"op"`      // "renew" | "change"
	Step     string    `json:"step"`    // "to-temp" | "to-current" | "to-new"
	Password string    `json:"password"`// the password set (or being set) at this step
	Status   string    `json:"status"`  // "pending" | "ok" | "fail"
	Err      string    `json:"err,omitempty"`
	At       time.Time `json:"at"`
}

type file struct {
	Version int     `json:"version"`
	Config  Config  `json:"config"`
	Entries []Entry `json:"entries"`
}

type Store struct {
	mu   sync.Mutex
	path string
}

// New opens (creating if needed) ~/.rtaskmgr/pwledger.json.
func New() (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("home dir: %w", err)
	}
	dir := filepath.Join(home, ".rtaskmgr")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", dir, err)
	}
	return &Store{path: filepath.Join(dir, "pwledger.json")}, nil
}

func (s *Store) load() (file, error) {
	var f file
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) || len(data) == 0 {
		return file{Version: 1, Config: Config{TempPassword: DefaultTempPassword, ExpiryWarnDays: DefaultWarnDays}}, nil
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
	if f.Config.TempPassword == "" {
		f.Config.TempPassword = DefaultTempPassword
	}
	if f.Config.ExpiryWarnDays == 0 {
		f.Config.ExpiryWarnDays = DefaultWarnDays
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

// Config returns the current config (with defaults filled in).
func (s *Store) Config() (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return Config{}, err
	}
	return f.Config, nil
}

// SetConfig replaces the config, filling blanks with defaults.
func (s *Store) SetConfig(c Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return err
	}
	if c.TempPassword == "" {
		c.TempPassword = DefaultTempPassword
	}
	if c.ExpiryWarnDays <= 0 {
		c.ExpiryWarnDays = DefaultWarnDays
	}
	f.Config = c
	return s.persist(f)
}

// Entries returns every ledger entry, newest first. If hostID is non-empty, only
// that host's entries are returned.
func (s *Store) Entries(hostID string) ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(f.Entries))
	for i := len(f.Entries) - 1; i >= 0; i-- {
		if hostID == "" || f.Entries[i].HostID == hostID {
			out = append(out, f.Entries[i])
		}
	}
	return out, nil
}

// Append records a new entry (typically status "pending") and returns its ID.
// The file is written synchronously so a crash right after leaves the record.
func (s *Store) Append(e Entry) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return "", err
	}
	if e.ID == "" {
		e.ID = uuid.NewString()
	}
	if e.At.IsZero() {
		e.At = time.Now()
	}
	f.Entries = append(f.Entries, e)
	if err := s.persist(f); err != nil {
		return "", err
	}
	return e.ID, nil
}

// SetStatus flips an existing entry's status (e.g. pending -> ok/fail) and
// stamps the error if any. Persisted synchronously.
func (s *Store) SetStatus(id, status, errMsg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return err
	}
	for i := range f.Entries {
		if f.Entries[i].ID == id {
			f.Entries[i].Status = status
			f.Entries[i].Err = errMsg
			f.Entries[i].At = time.Now()
			return s.persist(f)
		}
	}
	return nil
}
