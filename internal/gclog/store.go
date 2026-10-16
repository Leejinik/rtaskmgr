package gclog

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Archive is everything kept locally for one module on one host.
//
// It exists because the JVM truncates its GC log on restart (-Xloggc reopens the
// file from the top), so whatever preceded a restart survives only if an earlier
// fetch copied it here. Every fetch is MERGED, never overwritten.
type Archive struct {
	Key     string `json:"key"`
	Name    string `json:"name"`
	GcLog   string `json:"gcLog"`
	User    string `json:"user"`
	MaxHeap int64  `json:"maxHeap"`
	// ArcScannedTo: unix second up to which the restart archives (gc-logs-*.tar.gz)
	// have been read; 0 = never, so the next fetch backfills them.
	ArcScannedTo int64    `json:"arcScannedTo,omitempty"`
	Events       []Event  `json:"events"`
	Samples      []Sample `json:"samples"` // one per fetch: the process as it was at that moment
}

// Sample is the process at fetch time — RSS and threads are outside the GC log and
// are the only window this tool has on leaks the heap never sees (native, threads).
type Sample struct {
	T       int64 `json:"t"`
	PID     int   `json:"pid"`
	RSSKB   int64 `json:"rssKB"`
	Threads int   `json:"threads"`
	FDs     int   `json:"fds"` // -1 when /proc/<pid>/fd is not readable
	UpSec   int64 `json:"upSec"`
}

// keepMs bounds the archive. A month and a half of a busy collector is a few MB.
const keepMs = 45 * 24 * 3600 * 1000

// Merge folds fresh events in, dropping duplicates (the same event fetched twice)
// and anything older than the retention window.
func (a *Archive) Merge(fresh []Event, nowMs int64) (added int) {
	type k struct {
		t, up int64
		kind  string
	}
	seen := make(map[k]bool, len(a.Events))
	for _, e := range a.Events {
		seen[k{e.T, e.Up, e.Kind}] = true
	}
	for _, e := range fresh {
		kk := k{e.T, e.Up, e.Kind}
		if seen[kk] {
			continue
		}
		seen[kk] = true
		a.Events = append(a.Events, e)
		added++
	}
	sort.SliceStable(a.Events, func(i, j int) bool { return a.Events[i].T < a.Events[j].T })
	cut := nowMs - keepMs
	i := sort.Search(len(a.Events), func(i int) bool { return a.Events[i].T >= cut })
	a.Events = a.Events[i:]
	return added
}

// LastT is the newest archived event, or 0.
func (a *Archive) LastT() int64 {
	if len(a.Events) == 0 {
		return 0
	}
	return a.Events[len(a.Events)-1].T
}

// Store is the on-disk home of archives: <root>/<host>/<key>.json.
type Store struct{ Root string }

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func safe(s string) string {
	s = unsafeName.ReplaceAllString(s, "_")
	if s == "" || s == "." || s == ".." {
		s = "_"
	}
	return s
}

// ModuleKey names a module's archive. The GC log path is what persists across
// restarts (the pid does not), and two modules can share a display name, so the key
// is the name plus a short hash of the path.
func ModuleKey(name, gcLog string) string {
	if gcLog == "" {
		return safe(name)
	}
	h := sha1.Sum([]byte(gcLog))
	return safe(name) + "-" + hex.EncodeToString(h[:4])
}

func (s Store) path(hostID, key string) string {
	return filepath.Join(s.Root, safe(hostID), safe(key)+".json")
}

// Load returns the archive, or an empty one when none exists yet.
func (s Store) Load(hostID, key string) (*Archive, error) {
	b, err := os.ReadFile(s.path(hostID, key))
	if os.IsNotExist(err) {
		return &Archive{Key: key}, nil
	}
	if err != nil {
		return nil, err
	}
	var a Archive
	if err := json.Unmarshal(b, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// Save writes atomically: a half-written archive would lose every event from before
// the last restart, which is exactly what this store is for.
func (s Store) Save(hostID string, a *Archive) error {
	p := s.path(hostID, a.Key)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// List returns the keys archived for a host.
func (s Store) List(hostID string) []string {
	ents, _ := os.ReadDir(filepath.Join(s.Root, safe(hostID)))
	var out []string
	for _, e := range ents {
		n := e.Name()
		if !e.IsDir() && strings.HasSuffix(n, ".json") {
			out = append(out, strings.TrimSuffix(n, ".json"))
		}
	}
	sort.Strings(out)
	return out
}
