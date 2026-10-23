package statsreg

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// A rename or delete writes its record next to the registration receipts
// (~/.rtaskmgr/stats-registration/manage-<id>.json) just before COMMIT and
// after every step, like a receipt: notices that could not be sent and keys
// that could not be deleted can be finished later, after a restart too. The
// record keeps addresses only; passwords come from the current connection.

const (
	kindRename = "rename"
	kindDelete = "delete"
)

type manageRecord struct {
	ID        string
	Kind      string
	At        string
	GroupName string
	DBHost    string
	DBPort    int
	Brokers   string
	// Committed is false while the record was written just before COMMIT and
	// the outcome is not known; a resend checks the DB first.
	Committed bool
	Msgs      []json.RawMessage
	Sent      int
	Done      bool
	Note      string `json:",omitempty"`
	Learned   string `json:",omitempty"`
	// rename
	Points   []PointChange  `json:",omitempty"`
	Devices  []DeviceChange `json:",omitempty"`
	Watch    []int64        `json:",omitempty"`
	Interval int            `json:",omitempty"`
	Rename   ManageResult
	// delete
	DeviceIDs []int64  `json:",omitempty"`
	GroupID   int64    `json:",omitempty"`
	Whole     bool     `json:",omitempty"`
	Keys      []string `json:",omitempty"`
	Longest   int      `json:",omitempty"`
	Delete    DeleteResult
}

// PendingManage is an unfinished rename or delete, for the screen.
type PendingManage struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"` // rename | delete
	At        string `json:"at"`
	GroupName string `json:"groupName"`
	Committed bool   `json:"committed"`
	Sent      int    `json:"sent"`
	Total     int    `json:"total"`
	KeysLeft  int    `json:"keysLeft"`
	Message   string `json:"message"`
}

// ManageOutcome is what a resend or a Redis retry returns (one of the two results).
type ManageOutcome struct {
	Kind   string       `json:"kind"`
	Rename ManageResult `json:"rename"`
	Delete DeleteResult `json:"delete"`
}

func (s *Service) recordPath(id string) (string, error) {
	p, e := s.receiptPath(id) // validates the id
	if e != nil {
		return "", e
	}
	return filepath.Join(filepath.Dir(p), "manage-"+id+".json"), nil
}

func (s *Service) saveRecord(rec *manageRecord) error {
	path, e := s.recordPath(rec.ID)
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	data, e := json.MarshalIndent(rec, "", "  ")
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".manage-*")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, e = f.Write(data); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(tmp, path)
}

// recordNote saves the record; a failure is told in the result instead of lost.
func (s *Service) recordNote(rec *manageRecord) {
	if e := s.saveRecord(rec); e != nil {
		msg := " (작업 기록 저장 실패: " + e.Error() + ")"
		if rec.Kind == kindDelete {
			rec.Delete.Message += msg
		} else {
			rec.Rename.Message += msg
		}
	}
}

func (s *Service) loadRecord(id string) (*manageRecord, error) {
	path, e := s.recordPath(id)
	if e != nil {
		return nil, e
	}
	data, e := os.ReadFile(path)
	if e != nil {
		return nil, fmt.Errorf("작업 기록이 없습니다(%s): %w", id, e)
	}
	rec := &manageRecord{}
	if e = json.Unmarshal(data, rec); e != nil {
		return nil, fmt.Errorf("작업 기록을 읽을 수 없습니다(%s): %w", path, e)
	}
	return rec, nil
}

// ManagePending lists renames and deletes that are not finished, newest first.
func (s *Service) ManagePending() ([]PendingManage, error) {
	path, e := s.recordPath(uuid.Nil.String())
	if e != nil {
		return nil, e
	}
	files, e := filepath.Glob(filepath.Join(filepath.Dir(path), "manage-*.json"))
	if e != nil {
		return nil, e
	}
	out := []PendingManage{}
	for _, f := range files {
		id := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "manage-"), ".json")
		if _, err := uuid.Parse(id); err != nil {
			continue
		}
		rec, err := s.loadRecord(id)
		if err != nil || rec.Done {
			continue
		}
		p := PendingManage{ID: rec.ID, Kind: rec.Kind, At: rec.At, GroupName: rec.GroupName, Committed: rec.Committed, Sent: rec.Sent, Total: len(rec.Msgs), Message: rec.Rename.Message}
		if rec.Kind == kindDelete {
			p.Message, p.KeysLeft = rec.Delete.Message, len(rec.Delete.KeysLeft)
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At > out[j].At })
	return out, nil
}

// resumeRecord loads a record for the current connection and settles a
// commit whose outcome was not known.
func (s *Service) resumeRecord(ctx context.Context, planID, id string) (*manageRecord, Config, error) {
	s.mu.Lock()
	v, e := s.session(planID)
	s.mu.Unlock()
	if e != nil {
		return nil, Config{}, e
	}
	cfg := v.Config
	rec, e := s.loadRecord(id)
	if e != nil {
		return nil, cfg, e
	}
	if cfg.DBHost != rec.DBHost || cfg.DBPort != rec.DBPort || cfg.Brokers != rec.Brokers {
		return nil, cfg, fmt.Errorf("이 작업을 한 때와 같은 MariaDB(%s:%d)·Kafka(%s)로 연결 확인한 뒤 다시 하세요", rec.DBHost, rec.DBPort, rec.Brokers)
	}
	db, e := openDB(ctx, cfg)
	if e != nil {
		return nil, cfg, e
	}
	defer db.Close()
	if !rec.Committed {
		done, e := recordCommitted(ctx, db, rec)
		if e != nil {
			return nil, cfg, fmt.Errorf("저장 여부 확인 실패: %w", e)
		}
		if !done {
			rec.Done = true
			msg := "이 작업은 DB에 저장되지 않았습니다(커밋 안 됨) — 알림을 보내지 않고 기록을 닫습니다. 필요하면 다시 하세요."
			rec.Rename.Message, rec.Delete.Message = msg, msg
			s.recordNote(rec)
			return rec, cfg, nil
		}
		rec.Committed, rec.Rename.Committed, rec.Delete.Committed = true, true, true
	}
	if rec.Kind == kindRename {
		if note, e := refreshDeviceUpdated(ctx, db, rec); e != nil {
			return nil, cfg, e
		} else if note != "" {
			rec.Note = note
		}
	}
	s.recordNote(rec)
	return rec, cfg, nil
}

// recordCommitted tells from the DB whether a record's transaction committed.
func recordCommitted(ctx context.Context, q queryer, rec *manageRecord) (bool, error) {
	if rec.Kind == kindDelete {
		if len(rec.DeviceIDs) > 0 {
			var n int
			if e := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM device WHERE id IN ("+inList(len(rec.DeviceIDs))+") AND flag=-1", idArgs(rec.DeviceIDs)...).Scan(&n); e != nil {
				return false, e
			}
			return n > 0, nil
		}
		var n int
		if e := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM device_group WHERE id=?", rec.GroupID).Scan(&n); e != nil {
			return false, e
		}
		return n == 0, nil
	}
	for _, c := range rec.Points {
		var n int
		if e := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM checkpoint WHERE id=? AND BINARY COALESCE(name,'')=BINARY ? AND COALESCE(check_interval,0)=?", c.CheckpointID, c.NewName, c.NewInterval).Scan(&n); e != nil {
			return false, e
		}
		if n > 0 {
			return true, nil
		}
	}
	for _, d := range rec.Devices {
		var n int
		if e := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM device WHERE id=? AND BINARY COALESCE(name,'')=BINARY ?", d.DeviceID, d.NewName).Scan(&n); e != nil {
			return false, e
		}
		if n > 0 {
			return true, nil
		}
	}
	return false, nil
}

// refreshDeviceUpdated re-checks an unsent DEVICE_UPDATED against the DB: the
// collector fails the whole message on a device it cannot load, so devices
// deleted meanwhile are dropped, and the message too if none is left.
func refreshDeviceUpdated(ctx context.Context, q queryer, rec *manageRecord) (string, error) {
	for i := rec.Sent; i < len(rec.Msgs); i++ {
		var m struct {
			Header struct {
				ProtocolCode string `json:"protocolCode"`
			} `json:"header"`
			DeviceIDs []int64 `json:"deviceIds"`
		}
		if json.Unmarshal(rec.Msgs[i], &m) != nil || m.Header.ProtocolCode != "DEVICE_UPDATED_NOTIFICATION" {
			continue
		}
		live, e := liveDevices(ctx, q, m.DeviceIDs)
		if e != nil {
			return "", fmt.Errorf("장비 확인 실패: %w", e)
		}
		if len(live) == len(m.DeviceIDs) {
			return "", nil
		}
		if len(live) == 0 {
			rec.Msgs = append(rec.Msgs[:i:i], rec.Msgs[i+1:]...)
			return "이름을 바꾼 장비가 그 사이 모두 지워져 DEVICE_UPDATED 알림은 보내지 않았습니다.", nil
		}
		rec.Msgs[i] = notification("DEVICE_UPDATED_NOTIFICATION", map[string]any{"deviceIds": live, "restartService": true}, []string{})
		return fmt.Sprintf("이름을 바꾼 장비 중 %d대가 그 사이 지워져 DEVICE_UPDATED 에서 뺐습니다.", len(m.DeviceIDs)-len(live)), nil
	}
	return "", nil
}

// ManageResendOutcome is ManageResend for the screen (one result per kind).
func (s *Service) ManageResendOutcome(ctx context.Context, planID, id string) (ManageOutcome, error) {
	r, d, e := s.ManageResend(ctx, planID, id)
	out := ManageOutcome{Rename: r, Delete: d, Kind: kindRename}
	if d.ID != "" {
		out.Kind = kindDelete
	}
	return out, e
}

// ManageRedisRetry deletes again the checkpoint:<id> keys of a finished delete,
// recomputed from the DB: every deleted (flag=-1) checkpoint of its devices.
func (s *Service) ManageRedisRetry(ctx context.Context, planID, id string) (DeleteResult, error) {
	s.manage.run.Lock()
	defer s.manage.run.Unlock()
	rec, cfg, e := s.resumeRecord(ctx, planID, id)
	if e != nil {
		return DeleteResult{}, e
	}
	if rec.Kind != kindDelete {
		return DeleteResult{}, fmt.Errorf("삭제 작업이 아닙니다")
	}
	if !rec.Committed {
		return rec.Delete, nil
	}
	if rec.Sent < len(rec.Msgs) {
		return rec.Delete, fmt.Errorf("알림 %d/%d건만 보냈습니다 — 「알림 다시 보내기」부터 하세요(Collector가 아직 이 장비를 수집하면 키가 다시 생깁니다)", rec.Sent, len(rec.Msgs))
	}
	keys, e := deletedKeysOf(ctx, cfg, rec.DeviceIDs)
	if e != nil {
		return rec.Delete, e
	}
	s.cleanRecordKeys(ctx, cfg, rec, keys)
	return rec.Delete, nil
}

func deletedKeysOf(ctx context.Context, cfg Config, devices []int64) ([]string, error) {
	if len(devices) == 0 {
		return []string{}, nil
	}
	db, e := openDB(ctx, cfg)
	if e != nil {
		return nil, e
	}
	defer db.Close()
	return deletedCheckpointKeys(ctx, db, devices)
}

func deletedCheckpointKeys(ctx context.Context, q queryer, devices []int64) ([]string, error) {
	rows, e := q.QueryContext(ctx, "SELECT id FROM checkpoint WHERE device_id IN ("+inList(len(devices))+") AND flag=-1 ORDER BY id", idArgs(devices)...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id int64
		if e = rows.Scan(&id); e != nil {
			return nil, e
		}
		out = append(out, checkpointValueKey+strconv.FormatInt(id, 10))
	}
	return out, rows.Err()
}
