package statsreg

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// Recent connections, so the three services do not have to be typed again.
// Stored like ~/.rtaskmgr/hosts.json: a local file readable only by the user,
// passwords included in plain text (the same trade-off the host list makes).

type KafkaRow struct {
	Hostname string `json:"hostname"`
	IP       string `json:"ip"`
	Port     int    `json:"port"`
}
type SavedConnection struct {
	Config    Config     `json:"config"`
	KafkaRows []KafkaRow `json:"kafkaRows"`
	SavedAt   string     `json:"savedAt"`
}

const maxSavedConnections = 10

func (s *Service) connectionsPath() (string, error) {
	if s.ConnectionFile != "" {
		return s.ConnectionFile, nil
	}
	home, e := os.UserHomeDir()
	if e != nil {
		return "", e
	}
	return filepath.Join(home, ".rtaskmgr", "stats-connections.json"), nil
}

// Connections returns the saved connections, most recent first.
func (s *Service) Connections() ([]SavedConnection, error) {
	path, e := s.connectionsPath()
	if e != nil {
		return nil, e
	}
	data, e := os.ReadFile(path)
	if errors.Is(e, os.ErrNotExist) {
		return []SavedConnection{}, nil
	}
	if e != nil {
		return nil, e
	}
	out := []SavedConnection{}
	if e = json.Unmarshal(data, &out); e != nil {
		return nil, e
	}
	return out, nil
}

// SaveConnection puts c first; an entry for the same MariaDB address is replaced.
func (s *Service) SaveConnection(c SavedConnection) error {
	list, e := s.Connections()
	if e != nil {
		list = []SavedConnection{}
	}
	c.SavedAt = time.Now().Format(time.RFC3339)
	out := []SavedConnection{c}
	for _, v := range list {
		if v.Config.DBHost == c.Config.DBHost && v.Config.DBPort == c.Config.DBPort {
			continue
		}
		if len(out) == maxSavedConnections {
			break
		}
		out = append(out, v)
	}
	path, e := s.connectionsPath()
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	data, e := json.MarshalIndent(out, "", "  ")
	if e != nil {
		return e
	}
	tmp, e := os.CreateTemp(filepath.Dir(path), ".connections-*")
	if e != nil {
		return e
	}
	defer os.Remove(tmp.Name())
	if _, e = tmp.Write(data); e != nil {
		tmp.Close()
		return e
	}
	if e = tmp.Close(); e != nil {
		return e
	}
	if e = os.Chmod(tmp.Name(), 0600); e != nil {
		return e
	}
	return os.Rename(tmp.Name(), path)
}
