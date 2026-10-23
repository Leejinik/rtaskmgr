package statsreg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// The registration's record (receipt) is what 「남은 단계 마저 하기」 resumes.
// Its id used to be shown as a number to note down and type back; 2nd-round
// testers did not know what it was for. The unfinished ones are listed instead.

type RegistrationRecord struct {
	ID        string `json:"id"`
	GroupName string `json:"groupName"`
	At        string `json:"at"`
	DB        string `json:"db"`
	Stage     string `json:"stage"` // in screen words
	Message   string `json:"message"`
}

const recentRegistrations = 10

// RecentRegistrations lists the newest registrations of the last 30 days that
// did not finish (not saved, not notified, or collection not confirmed).
func (s *Service) RecentRegistrations() ([]RegistrationRecord, error) {
	path, e := s.receiptPath(uuid.Nil.String())
	if e != nil {
		return nil, e
	}
	files, e := filepath.Glob(filepath.Join(filepath.Dir(path), "*.json"))
	if e != nil {
		return nil, e
	}
	type item struct {
		rec RegistrationRecord
		at  time.Time
	}
	items := []item{}
	for _, f := range files {
		id := strings.TrimSuffix(filepath.Base(f), ".json")
		if _, err := uuid.Parse(id); err != nil {
			continue // manage-<id>.json and others
		}
		st, err := os.Stat(f)
		if err != nil || time.Since(st.ModTime()) > 30*24*time.Hour {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var rec receipt
		if json.Unmarshal(data, &rec) != nil {
			continue
		}
		r := rec.Result
		if r.Committed && r.Notified && r.Verified {
			continue
		}
		stage := "수집 확인이 덜 됨"
		switch {
		case r.Stage == stagePrepared:
			stage = "장비 저장 여부를 모름"
		case r.Stage == stageDevices:
			stage = "장비만 저장됨 — 체크포인트가 아직"
		case !r.Notified:
			stage = "저장됐지만 수집 서버에 아직 알리지 못함"
		}
		items = append(items, item{RegistrationRecord{ID: id, GroupName: rec.GroupName, At: st.ModTime().Format("2006-01-02 15:04"), DB: rec.DBHost, Stage: stage, Message: r.Message}, st.ModTime()})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].at.After(items[j].at) })
	out := []RegistrationRecord{}
	for i, it := range items {
		if i == recentRegistrations {
			break
		}
		out = append(out, it.rec)
	}
	return out, nil
}
