package statsreg

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
)

func TestMappingUsesModuleHostnameAndExactKeys(t *testing.T) {
	values := map[string]string{"liz.stats.admin.{12}.cpu": "1.5", "liz.stats.server.{server01}.root.free.disk": "10", "liz.stats.vip.location": "server01", "liz.stats.admin.{99}.cpu": "1", "prefix.liz.stats.vip.location": "no", "liz.stats.logdb.{lizlogdb01}.status": "1"}
	points := makePoints(values, []Server{{Hostname: "server01"}, {Hostname: "server02"}}, []module{{12, "server02"}}, []template{{ID: 1, Name: "CPU", Pattern: "liz.stats.admin.{module_id}.cpu", Format: 3, Interval: 10000}})
	byKey := map[string]Point{}
	for _, p := range points {
		byKey[p.Key] = p
	}
	if len(points) != 5 {
		t.Fatalf("incorrect prefix filter: %d", len(points))
	}
	if p := byKey["liz.stats.admin.{12}.cpu"]; p.Host != "server02" || p.TemplateID != 1 || !p.Selected {
		t.Fatalf("module placement: %+v", p)
	}
	if p := byKey["liz.stats.admin.{99}.cpu"]; p.Selected || p.Host != "" {
		t.Fatalf("unknown module must not be guessed: %+v", p)
	}
	if p := byKey["liz.stats.vip.location"]; p.Host != "common" || p.Format != 4 {
		t.Fatalf("common: %+v", p)
	}
	if p := byKey["liz.stats.logdb.{lizlogdb01}.status"]; p.Selected {
		t.Fatal("unknown alias must not be assigned by numeric suffix")
	}
}
func TestTemplateMatching(t *testing.T) {
	for _, key := range []string{"lizXstats.admin.{1}.cpu", "liz.stats.admin.1.cpu", "liz.stats.admin.{1}.cpu.extra"} {
		if _, ok := matchTemplate("liz.stats.admin.{module_id}.cpu", key); ok {
			t.Fatal(key)
		}
	}
	c, ok := matchTemplate("liz.stats.server.{hostname}.{nic}.network.traffic", "liz.stats.server.{server_3}.{ens33}.network.traffic")
	if !ok || c["hostname"] != "server_3" || c["nic"] != "ens33" {
		t.Fatal(c)
	}
}
func TestSelectionValidation(t *testing.T) {
	p := Point{Key: "liz.stats.vip.location", Name: "VIP", Host: "common", Format: 4, Interval: 10000, Selected: true}
	plan := Plan{Points: []Point{p}}
	sel := Selection{GroupName: "System", Servers: []Server{{Hostname: "common", Name: "common"}}, Points: []Point{p}}
	if e := validateSelection(plan, sel); e != nil {
		t.Fatal(e)
	}
	sel.Points = append(sel.Points, p)
	if e := validateSelection(plan, sel); e == nil {
		t.Fatal("duplicate key accepted")
	}
	sel.Points = sel.Points[:1]
	sel.Points[0].Host = "missing"
	if e := validateSelection(plan, sel); e == nil {
		t.Fatal("unknown host accepted")
	}
}
func TestReceiptAndNotificationContract(t *testing.T) {
	s := Service{Directory: t.TempDir()}
	r := Result{ID: uuid.NewString(), DeviceIDs: []int64{100, 101}, InterfaceIDs: []int64{200, 201}}
	rec := receipt{Result: r, TwoPhase: true, DeviceMessages: deviceNotifications(r), PointMessages: pointNotifications(r)}
	if e := s.saveReceipt(rec); e != nil {
		t.Fatal(e)
	}
	rec.DeviceSent = 2
	if e := s.saveReceipt(rec); e != nil {
		t.Fatal(e)
	}
	path, _ := s.receiptPath(r.ID)
	data, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(data), `"DeviceSent": 2`) {
		t.Fatal("receipt replacement failed")
	}
	if _, e = s.receiptPath("../escape"); e == nil {
		t.Fatal("invalid receipt path accepted")
	}
	var msg map[string]any
	if e = json.Unmarshal(rec.PointMessages[0], &msg); e != nil {
		t.Fatal(e)
	}
	if _, ok := msg["deviceInterfaceIds"]; !ok {
		t.Fatal("checkpoint notification must contain interface IDs")
	}
	if _, ok := msg["deviceIds"]; ok {
		t.Fatal("wrong checkpoint notification IDs")
	}
}

// DEVICE_ADDED and CHECKPOINT_UPDATED must travel in different phases, in the
// web console's order; sending them back to back is what killed the collector.
func TestNotificationsSplitIntoPhases(t *testing.T) {
	r := Result{DeviceIDs: []int64{1}, InterfaceIDs: []int64{2}}
	codes := func(msgs []json.RawMessage) []string {
		out := []string{}
		for _, m := range msgs {
			var v struct {
				Header struct {
					ProtocolCode string `json:"protocolCode"`
				} `json:"header"`
			}
			if e := json.Unmarshal(m, &v); e != nil {
				t.Fatal(e)
			}
			out = append(out, v.Header.ProtocolCode)
		}
		return out
	}
	if got := strings.Join(codes(deviceNotifications(r)), ","); got != "SPECIFY_RULE_UPDATED_NOTIFICATION,DEVICE_ADDED_NOTIFICATION" {
		t.Fatal("device phase:", got)
	}
	if got := strings.Join(codes(pointNotifications(r)), ","); got != "CHECKPOINT_UPDATED_NOTIFICATION,SPECIFY_RULE_UPDATED_NOTIFICATION" {
		t.Fatal("checkpoint phase:", got)
	}
}

func TestSettleCountsFromNotificationAndStopsOnCancel(t *testing.T) {
	s := Service{SettleWait: time.Hour}
	if !s.settle(context.Background(), time.Now().Add(-2*time.Hour)) {
		t.Fatal("a resumed run must not wait again for time already passed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if s.settle(ctx, time.Now()) {
		t.Fatal("cancelled wait reported as settled")
	}
}

// Existing checkpoints on the new interfaces are only accepted as this
// registration's own lost commit, never adopted otherwise.
func TestCommitCheckpointsResumesOnlyItsOwnLostCommit(t *testing.T) {
	db, m, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	s := Service{Directory: t.TempDir()}
	rec := receipt{Result: Result{ID: uuid.NewString(), InterfaceIDs: []int64{7, 8}}, TwoPhase: true, Points: []Point{{Key: "a"}, {Key: "b"}}}

	m.ExpectQuery("SELECT id FROM checkpoint WHERE device_interface_id IN").WithArgs(int64(7), int64(8), "a", "b").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(30)).AddRow(int64(31)))
	if e = s.commitCheckpoints(context.Background(), db, &rec); e == nil {
		t.Fatal("adopted checkpoints this receipt never tried to commit")
	}

	rec.PointMessages = pointNotifications(rec.Result)
	m.ExpectQuery("SELECT id FROM checkpoint WHERE device_interface_id IN").WithArgs(int64(7), int64(8), "a", "b").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(30)).AddRow(int64(31)))
	if e = s.commitCheckpoints(context.Background(), db, &rec); e != nil {
		t.Fatal(e)
	}
	if rec.Result.Stage != stageCheckpoints || !rec.Result.Committed || len(rec.Result.CheckpointIDs) != 2 {
		t.Fatalf("lost commit not resumed: %+v", rec.Result)
	}
	if e = m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}

func TestCollectionSummary(t *testing.T) {
	w := &collectWatch{ids: []int64{1, 2, 3}, seen: map[int64]map[int64]bool{1: {}, 2: {}, 3: {}}}
	for id, v := range map[int64][]string{1: {"1║4.2 %║100║4.2", "1║4.3 %║110║4.3"}, 2: {"2║x║100║x", "2║x║100║x"}, 3: {"garbage"}} {
		for _, s := range v {
			w.observe(id, s)
		}
	}
	w.observe(99, "99║x║5║x")
	seen, done := w.snapshot()
	if done != 1 || seen[2] != 1 || seen[3] != 0 {
		t.Fatalf("done=%d seen=%v", done, seen)
	}
	r := Result{CheckpointIDs: []int64{1, 2, 3}}
	summarizeCollection(&r, seen, nil)
	if r.Verified || r.Collected != 2 || len(r.Missing) != 1 || r.Missing[0] != 3 || !strings.Contains(r.Message, "재등록하지 마세요") {
		t.Fatalf("partial collection: %+v", r)
	}
	w.observe(2, "2║x║120║x")
	w.observe(3, "3║x║1║x")
	w.observe(3, "3║x║2║x")
	seen, _ = w.snapshot()
	summarizeCollection(&r, seen, nil)
	if !r.Verified || len(r.Missing) != 0 {
		t.Fatalf("full collection: %+v", r)
	}
	if verifyWindow([]Point{{Interval: 10000}}, 0) != 45*time.Second || verifyWindow([]Point{{Interval: 600000}}, 0) != 150*time.Second {
		t.Fatal("verify window bounds")
	}
}

// Explicitly enabled only by the developer; all inserted data is rolled back.
func TestLiveRegistrationRollback(t *testing.T) {
	host := os.Getenv("RTM_STATS_TEST_HOST")
	if host == "" {
		t.Skip("set RTM_STATS_TEST_HOST for transaction rollback integration test")
	}
	c := Config{DBHost: host, DBPort: 3306, DBUser: "root", DBPassword: os.Getenv("RTM_STATS_TEST_PASSWORD"), RedisHost: host, RedisPort: 5000, RedisPassword: os.Getenv("RTM_STATS_TEST_PASSWORD"), Brokers: host + ":9092", KafkaSecurity: "PLAINTEXT"}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	s := Service{Directory: t.TempDir()}
	p, e := s.Inspect(ctx, c)
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("Discovery: %d keys, %d servers", len(p.Points), len(p.Servers))
	sel := Selection{PlanID: p.ID, GroupName: "RTM_rollback_" + uuid.NewString()[:8], Servers: p.Servers, Points: p.Points, ExplorerID: p.Explorers[0].ID, ClusterID: p.Clusters[0].ID}
	for i := range sel.Servers {
		sel.Servers[i].Name = sel.GroupName + "_" + sel.Servers[i].Hostname
	}
	for i := range sel.Points {
		sel.Points[i].Selected = sel.Points[i].Host != ""
	}
	if e = s.Validate(ctx, sel); e != nil {
		t.Fatal(e)
	}
	res, e := s.register(ctx, sel, true)
	if e != nil {
		t.Fatal(e)
	}
	if len(res.CheckpointIDs) < 4 {
		t.Fatal("too few inserted points")
	}
	db, e := openDB(ctx, c)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	var n int
	if e = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM device_group WHERE name=?", sel.GroupName).Scan(&n); e != nil || n != 0 {
		t.Fatalf("rollback failed n=%d err=%v", n, e)
	}
	t.Logf("Rolled back group %d, %d devices and %d checkpoints, including labels/audits; no Kafka writes", res.GroupID, len(res.DeviceIDs), len(res.CheckpointIDs))
}

// A key registered elsewhere during the settle wait must stop the checkpoint
// phase inside its transaction: nothing inserted, rolled back, no notification.
func TestCheckpointPhaseRechecksDuplicates(t *testing.T) {
	db, m, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	s := Service{Directory: t.TempDir()}
	rec := receipt{Result: Result{ID: uuid.NewString(), DeviceIDs: []int64{5}, InterfaceIDs: []int64{7}, Stage: stageDevices}, TwoPhase: true, Hosts: []string{"server01"}, Points: []Point{{Key: "a", Host: "server01"}}}
	m.ExpectQuery("SELECT id FROM checkpoint WHERE device_interface_id IN").WillReturnRows(sqlmock.NewRows([]string{"id"}))
	m.ExpectQuery("SELECT device_driver_id FROM device_interface").WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"d"}).AddRow(int64(3053)))
	m.ExpectBegin()
	m.ExpectQuery(`SELECT COUNT\(\*\) FROM checkpoint c JOIN device_interface`).WithArgs(int64(3053), "a").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	m.ExpectRollback()
	e = s.commitCheckpoints(context.Background(), db, &rec)
	if e == nil || !strings.Contains(e.Error(), "다른 곳에서 등록") {
		t.Fatalf("duplicate not stopped: %v", e)
	}
	if rec.Result.Committed || len(rec.PointMessages) != 0 {
		t.Fatalf("must not commit or prepare notifications: %+v", rec)
	}
	if e = m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}
