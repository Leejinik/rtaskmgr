package statsreg

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

const testDriver = int64(3053)

func manageTestPlan() Plan {
	return Plan{
		Servers: []Server{{Hostname: "server_1", IP: "10.0.0.1"}, {Hostname: "server_2"}, {Hostname: "common"}},
		Existing: []ExistingGroup{{GroupID: 7, Name: "System", Devices: []ExistingDevice{
			{DeviceID: 100, Host: "server_1"}, {DeviceID: 200, Host: "server_2"},
		}}},
	}
}

func TestManagedShapeCollapsesTheServerPart(t *testing.T) {
	hosts := map[string]string{"server_1": "server_1", "10.0.0.1": "server_1", "server_2": "server_2"}
	mods := map[string]string{"16": "server_2", "3": "server_1"}
	cases := []struct{ key, host, want string }{
		{"liz.stats.server.{server_1}.free.memory", "server_1", "liz.stats.server.{서버별}.free.memory"},
		{"liz.stats.server.{server_2}.free.memory", "server_2", "liz.stats.server.{서버별}.free.memory"},
		{"liz.stats.server.{server_1}.{eth0}.network.traffic", "server_1", "liz.stats.server.{서버별}.{eth0}.network.traffic"},
		{"liz.stats.collector.{16}.cpu", "server_2", "liz.stats.collector.{서버별}.cpu"},
		{"liz.stats.api.{3}.snmp.{3}.rpm", "server_1", "liz.stats.api.{서버별}.snmp.{3}.rpm"},
		{"liz.stats.x.{eth9}.{10.0.0.1}.y", "server_1", "liz.stats.x.{eth9}.{서버별}.y"},
		{"liz.stats.vip.location", "common", "liz.stats.vip.location"},
		{"liz.stats.logdb.{lizlogdb01}.status", "", "liz.stats.logdb.{서버별}.status"},
	}
	for _, c := range cases {
		if got := managedShape(c.key, c.host, hosts, mods); got != c.want {
			t.Errorf("%s @%s: %s, want %s", c.key, c.host, got, c.want)
		}
	}
}

func groupDeviceRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "name", "dc", "iid", "driver"}).
		AddRow(int64(100), "server_1", int64(1), int64(1000), testDriver).
		AddRow(int64(100), "server_1", int64(1), int64(1000), testDriver). // mapped twice
		AddRow(int64(200), "server_2", int64(1), int64(2000), testDriver).
		AddRow(int64(300), "common", int64(1), int64(3000), testDriver).
		AddRow(int64(400), "other ups", nil, int64(4000), int64(9)).
		AddRow(int64(500), "empty", nil, nil, int64(0))
}

func lockRows() *sqlmock.Rows { return sqlmock.NewRows([]string{"d", "g", "n"}) }

var checkpointColumns = []string{"id", "device_id", "iface", "key", "code", "name", "interval", "tmpl", "fmt", "measure"}

func checkpointRows() *sqlmock.Rows {
	return sqlmock.NewRows(checkpointColumns).
		AddRow(int64(1), int64(100), int64(1000), "liz.stats.server.{server_1}.free.memory", "liz.stats.server.{server_1}.free.memory", "Free Memory", 10000, int64(29142), 3, "MB").
		AddRow(int64(2), int64(200), int64(2000), "liz.stats.server.{server_2}.free.memory", "liz.stats.server.{server_2}.free.memory", "Free Memory", 10000, int64(29142), 3, "MB").
		AddRow(int64(3), int64(100), int64(1000), "liz.stats.cachedb.{server_1}.cpu", "liz.stats.cachedb.{server_1}.cpu", "cachedb.{server_1}.cpu", 10000, int64(0), 3, "").
		AddRow(int64(4), int64(300), int64(3000), "liz.stats.vip.location", "liz.stats.vip.location", "liz.stats.vip.location", 30000, int64(0), 4, "").
		AddRow(int64(5), int64(100), int64(1999), "", "x", "point of another driver", 10000, int64(0), 3, "")
}

func expectLoad(m sqlmock.Sqlmock, group string) {
	m.ExpectQuery("SELECT name, explorer_id, device_specify_rule_id FROM device_group WHERE id=\\?").WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"name", "e", "r"}).AddRow(group, int64(1), int64(70)))
	m.ExpectQuery("FROM device_specify_rule_mapping_device m").WithArgs(int64(70)).WillReturnRows(groupDeviceRows())
	m.ExpectQuery("JOIN device_group g ON g.device_specify_rule_id=m.device_specify_rule_id WHERE m.device_id IN").WithArgs(int64(100), int64(200), int64(300), int64(70)).WillReturnRows(lockRows())
	m.ExpectQuery("FROM checkpoint WHERE device_id IN \\(\\?,\\?,\\?\\) AND flag=1").WithArgs(int64(100), int64(200), int64(300)).WillReturnRows(checkpointRows())
}

func loadTestSession(t *testing.T) *manageSession {
	t.Helper()
	db, m, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	expectLoad(m, "System")
	s, e := loadManaged(context.Background(), db, 7, testDriver, manageTestPlan(), []module{{ID: 16, Host: "server_2"}})
	if e != nil {
		t.Fatal(e)
	}
	if e = m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
	return s
}

func TestLoadManagedGroupsByKeyShape(t *testing.T) {
	setTester(t, "")
	s := loadTestSession(t)
	g := s.Group
	if len(g.Devices) != 3 || !reflect.DeepEqual(g.OtherDevices, []string{"other ups (ID 400)", "empty (ID 500)"}) {
		t.Fatalf("devices %+v others %v", g.Devices, g.OtherDevices)
	}
	hosts := map[string]string{}
	for _, d := range g.Devices {
		hosts[d.Name] = d.Host
	}
	if !reflect.DeepEqual(hosts, map[string]string{"server_1": "server_1", "server_2": "server_2", "common": "common"}) {
		t.Fatalf("hosts %v", hosts)
	}
	if s.devices[100].Checkpoints != 3 || s.devices[100].OtherInterfaces != 0 {
		t.Fatalf("device 100: %+v", s.devices[100])
	}
	shapes := []string{}
	for _, r := range g.Rows {
		shapes = append(shapes, r.Shape)
	}
	want := []string{"liz.stats.cachedb.{서버별}.cpu", "liz.stats.server.{서버별}.free.memory", "liz.stats.vip.location"}
	if !reflect.DeepEqual(shapes, want) {
		t.Fatalf("shapes %v", shapes)
	}
	mem := g.Rows[1]
	if len(mem.Points) != 2 || mem.Devices != 2 || mem.RawName || mem.Template != "Free Memory" {
		t.Fatalf("memory row %+v", mem)
	}
	if !g.Rows[0].RawName || !g.Rows[2].RawName || g.RawNames != 2 || g.Checkpoints != 4 {
		t.Fatalf("raw names: %+v / %d", g.Rows, g.RawNames)
	}
	if _, ok := s.points[5]; ok {
		t.Fatal("a checkpoint of another driver's interface is managed")
	}
}

func TestLoadManagedRespectsTesterPrefix(t *testing.T) {
	setTester(t, "agy")
	db, m, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	m.ExpectQuery("SELECT name, explorer_id, device_specify_rule_id FROM device_group").WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"name", "e", "r"}).AddRow("codex_System", int64(1), int64(70)))
	if _, e = loadManaged(context.Background(), db, 7, testDriver, manageTestPlan(), nil); e == nil || !strings.Contains(e.Error(), "agy_") {
		t.Fatalf("another tester's group loaded: %v", e)
	}
	if e = m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}

func TestResolveEditFillsNamePerServer(t *testing.T) {
	setTester(t, "")
	s := loadTestSession(t)
	plan, errs, warns := resolveEdit(s, ManageEdit{Rows: []RowEdit{
		{Shape: "liz.stats.server.{서버별}.free.memory", Name: "{Host} 남은 메모리", Interval: 30000, Overrides: []PointOverride{{CheckpointID: 2, Name: "서버2 메모리"}}},
		{Shape: "liz.stats.cachedb.{서버별}.cpu", Name: "CacheDB CPU"},
	}, Devices: []DeviceEdit{{DeviceID: 300, Name: "공통"}}})
	if len(errs) != 0 {
		t.Fatalf("errors %v", errs)
	}
	got := map[int64][2]any{}
	for _, c := range plan.points {
		got[c.CheckpointID] = [2]any{c.NewName, c.NewInterval}
	}
	want := map[int64][2]any{1: {"Server_1 남은 메모리", 30000}, 2: {"서버2 메모리", 30000}, 3: {"CacheDB CPU", 10000}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("changes %v", got)
	}
	if len(plan.devices) != 1 || plan.devices[0].NewName != "공통" {
		t.Fatalf("devices %+v", plan.devices)
	}
	if len(warns) != 0 {
		t.Fatalf("warnings %v", warns)
	}
}

func TestResolveEditRefusesWhatCannotBeApplied(t *testing.T) {
	setTester(t, "")
	s := loadTestSession(t)
	_, errs, _ := resolveEdit(s, ManageEdit{Rows: []RowEdit{
		{Shape: "liz.stats.vip.location", Name: "{Host} VIP"},                                            // common device: no server name
		{Shape: "liz.stats.server.{서버별}.free.memory", Interval: 500},                                     // too short
		{Shape: "liz.stats.cachedb.{서버별}.cpu", Name: " CPU"},                                             // surrounding space
		{Shape: "no.such.shape", Name: "x"},                                                              // stale screen
		{Shape: "liz.stats.cachedb.{서버별}.cpu", Overrides: []PointOverride{{CheckpointID: 1, Name: "x"}}}, // repeated / foreign
	}, Devices: []DeviceEdit{{DeviceID: 100, Name: "dup"}, {DeviceID: 200, Name: "DUP"}, {DeviceID: 999, Name: "x"}}})
	if len(errs) != 7 {
		t.Fatalf("want 7 errors, got %d: %v", len(errs), errs)
	}
}

func TestResolveEditWarnsAboutSameNamesAndHostDrift(t *testing.T) {
	setTester(t, "")
	s := loadTestSession(t)
	_, errs, warns := resolveEdit(s, ManageEdit{Rows: []RowEdit{{Shape: "liz.stats.cachedb.{서버별}.cpu", Name: "Free Memory"}}, Devices: []DeviceEdit{{DeviceID: 100, Name: "main"}}})
	if len(errs) != 0 || len(warns) != 2 || !strings.Contains(warns[0], "2개가 됩니다") || !strings.Contains(warns[1], "키로 추정") {
		t.Fatalf("errs %v warns %v", errs, warns)
	}
}

func TestResolveEditForcesTesterDevicePrefix(t *testing.T) {
	setTester(t, "agy")
	s := &manageSession{points: map[int64]ManagedPoint{}, devices: map[int64]ManagedDevice{1: {DeviceID: 1, Name: "agy server_1"}}}
	plan, errs, _ := resolveEdit(s, ManageEdit{Devices: []DeviceEdit{{DeviceID: 1, Name: "main"}}})
	if len(errs) != 0 || len(plan.devices) != 1 || plan.devices[0].NewName != "agy main" {
		t.Fatalf("%+v %v", plan.devices, errs)
	}
}

func expectAuditShape(m sqlmock.Sqlmock, table string, aud, base []string) {
	a := sqlmock.NewRows([]string{"c"})
	for _, c := range aud {
		a.AddRow(c)
	}
	b := sqlmock.NewRows([]string{"c"})
	for _, c := range base {
		b.AddRow(c)
	}
	m.ExpectQuery("information_schema.COLUMNS").WithArgs(table + "_aud").WillReturnRows(a)
	m.ExpectQuery("information_schema.COLUMNS").WithArgs(table).WillReturnRows(b)
}

// expectMembers: the group's devices re-read (and locked) inside an apply.
func expectMembers(m sqlmock.Sqlmock) {
	m.ExpectQuery("FROM device_specify_rule_mapping_device m(.|\\n)*FOR UPDATE").WithArgs(int64(70)).WillReturnRows(sqlmock.NewRows([]string{"id", "name", "dc", "iid", "driver"}).AddRow(int64(100), "server_1", int64(1), int64(1000), testDriver))
	m.ExpectQuery("JOIN device_group g(.|\\n)*FOR UPDATE").WithArgs(int64(100), int64(70)).WillReturnRows(lockRows())
}

func TestApplyPlanSQLAuditAndConflicts(t *testing.T) {
	setTester(t, "")
	db, m, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	s := &manageSession{Group: ManagedGroup{GroupID: 7, Name: "System"}, DriverID: testDriver, rule: sql.NullInt64{Int64: 70, Valid: true}, devices: map[int64]ManagedDevice{
		100: {DeviceID: 100, Name: "server_1", dc: sql.NullInt64{Int64: 1, Valid: true}},
	}}
	plan := managePlan{points: []PointChange{
		{CheckpointID: 1, DeviceID: 100, InterfaceID: 1000, Key: "k1", OldName: "k1", NewName: "CPU", OldInterval: 10000, NewInterval: 10000},
		{CheckpointID: 2, DeviceID: 100, InterfaceID: 1000, Key: "k2", OldName: "old", NewName: "new", OldInterval: 10000, NewInterval: 10000},
		{CheckpointID: 3, DeviceID: 100, InterfaceID: 1000, Key: "k3", OldName: "same", NewName: "same", OldInterval: 10000, NewInterval: 30000},
	}, devices: []DeviceChange{{DeviceID: 100, OldName: "server_1", NewName: "main"}}}
	m.ExpectBegin()
	m.ExpectQuery("SELECT name FROM device_group WHERE id=\\? FOR UPDATE").WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow("System"))
	expectMembers(m)
	m.ExpectExec("UPDATE checkpoint SET name=\\?, display_name=\\?, update_date=NOW\\(3\\) WHERE id=\\? AND device_interface_id=\\? AND flag=1 AND BINARY COALESCE\\(name,''\\)=BINARY \\? AND COALESCE\\(check_interval,0\\)=\\?").
		WithArgs("CPU", "CPU", int64(1), int64(1000), "k1", 10000).WillReturnResult(sqlmock.NewResult(0, 1))
	// Changed in admin after loading: skipped and reported, not overwritten.
	m.ExpectExec("UPDATE checkpoint SET name=").WithArgs("new", "new", int64(2), int64(1000), "old", 10000).WillReturnResult(sqlmock.NewResult(0, 0))
	m.ExpectQuery("SELECT COALESCE\\(name,''\\), flag").WithArgs(int64(2)).WillReturnRows(sqlmock.NewRows([]string{"n", "f", "i", "if"}).AddRow("admin name", 1, 10000, int64(1000)))
	m.ExpectExec("UPDATE checkpoint SET check_interval=\\?, keep_interval=\\?, update_date=NOW\\(3\\) WHERE id=\\?").
		WithArgs(30000, 30000, int64(3), int64(1000), "same", 10000).WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectExec("UPDATE device SET name=\\?, update_date=NOW\\(3\\) WHERE id=\\? AND flag=1 AND BINARY COALESCE\\(name,''\\)=BINARY \\? AND EXISTS \\(SELECT 1 FROM device_specify_rule_mapping_device m WHERE m.device_id=device.id AND m.device_specify_rule_id=\\?\\)").WithArgs("main", int64(100), "server_1", int64(70)).WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectQuery("SELECT COUNT\\(\\*\\) FROM device WHERE flag=1 AND name=\\? AND id<>\\?").WithArgs("main", int64(100)).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	m.ExpectExec("INSERT INTO revinfo").WillReturnResult(sqlmock.NewResult(55, 1))
	expectAuditShape(m, "checkpoint",
		[]string{"revtype", "name", "name_mod", "display_name", "display_name_mod", "check_interval", "check_interval_mod", "keep_interval", "keep_interval_mod", "update_date", "update_date_mod", "checkpoint_labels_mod", "id", "rev"},
		[]string{"id", "name", "display_name", "check_interval", "keep_interval", "update_date"})
	m.ExpectExec("INSERT INTO `checkpoint_aud` .* SELECT \\?,`name`,1,`display_name`,1,`check_interval`,0,`keep_interval`,0,`update_date`,1,0,`id`,\\? FROM `checkpoint` WHERE id=\\?").
		WithArgs(1, int64(55), int64(1)).WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectExec("INSERT INTO revchanges").WithArgs(int64(55), "com.onion.liz.common.database.entity.Checkpoint", int64(55), "com.onion.liz.common.database.entity.Checkpoint").WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectExec("INSERT INTO liz_log").WithArgs(sql.NullInt64{Int64: 1, Valid: true}, "10.9.9.9", "rtaskmgr", "Point Updated.", 113, int64(1), "CPU", 2, "rtaskmgr").WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectExec("INSERT INTO `checkpoint_aud` .* SELECT \\?,`name`,0,`display_name`,0,`check_interval`,1,`keep_interval`,1,`update_date`,1,0,`id`,\\? FROM `checkpoint` WHERE id=\\?").
		WithArgs(1, int64(55), int64(3)).WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectExec("INSERT INTO revchanges").WillReturnResult(sqlmock.NewResult(0, 0))
	m.ExpectExec("INSERT INTO liz_log").WithArgs(sqlmock.AnyArg(), "10.9.9.9", "rtaskmgr", "Point Updated.", 113, int64(3), "same", 2, "rtaskmgr").WillReturnResult(sqlmock.NewResult(0, 1))
	expectAuditShape(m, "device", []string{"revtype", "name", "name_mod", "update_date", "update_date_mod", "cluster_mod", "id", "rev"}, []string{"id", "name", "update_date"})
	m.ExpectExec("INSERT INTO `device_aud` .* SELECT \\?,`name`,1,`update_date`,1,0,`id`,\\? FROM `device` WHERE id=\\?").WithArgs(1, int64(55), int64(100)).WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectExec("INSERT INTO revchanges").WithArgs(int64(55), "com.onion.liz.common.database.entity.Device", int64(55), "com.onion.liz.common.database.entity.Device").WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectExec("INSERT INTO liz_log").WithArgs(sqlmock.AnyArg(), "10.9.9.9", "rtaskmgr", "Device properties information has been modified.", 113, int64(100), "main", 1, "rtaskmgr").WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectRollback()
	ctx := context.Background()
	tx, _ := db.BeginTx(ctx, nil)
	points, devices, conflicts, e := applyPlan(ctx, tx, s, plan, "10.9.9.9")
	tx.Rollback()
	if e != nil {
		t.Fatal(e)
	}
	if len(points) != 2 || points[0].CheckpointID != 1 || points[1].CheckpointID != 3 || len(devices) != 1 {
		t.Fatalf("applied %+v %+v", points, devices)
	}
	if len(conflicts) != 1 || !strings.Contains(conflicts[0], `지금 이름 "admin name"`) {
		t.Fatalf("conflicts %v", conflicts)
	}
	if e = m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}

func TestApplyPlanFailsWholeApplyOnDuplicateDeviceName(t *testing.T) {
	setTester(t, "")
	db, m, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	s := &manageSession{Group: ManagedGroup{GroupID: 7, Name: "System"}, DriverID: testDriver, rule: sql.NullInt64{Int64: 70, Valid: true}, devices: map[int64]ManagedDevice{100: {DeviceID: 100}}}
	m.ExpectBegin()
	m.ExpectQuery("SELECT name FROM device_group").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow("System"))
	expectMembers(m)
	m.ExpectExec("UPDATE device SET name=").WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectQuery("SELECT COUNT\\(\\*\\) FROM device WHERE flag=1 AND name=").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	ctx := context.Background()
	tx, _ := db.BeginTx(ctx, nil)
	defer tx.Rollback()
	if _, _, _, e = applyPlan(ctx, tx, s, managePlan{devices: []DeviceChange{{DeviceID: 100, OldName: "a", NewName: "b"}}}, ""); e == nil || !strings.Contains(e.Error(), "아무것도 바꾸지 않았습니다") {
		t.Fatalf("duplicate accepted: %v", e)
	}
}

func TestApplyPlanRefusesAnotherTestersGroup(t *testing.T) {
	setTester(t, "agy")
	db, m, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	m.ExpectBegin()
	m.ExpectQuery("SELECT name FROM device_group").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow("codex_System"))
	ctx := context.Background()
	tx, _ := db.BeginTx(ctx, nil)
	defer tx.Rollback()
	s := &manageSession{Group: ManagedGroup{GroupID: 7, Name: "codex_System"}}
	if _, _, _, e = applyPlan(ctx, tx, s, managePlan{points: []PointChange{{CheckpointID: 1}}}, ""); e == nil {
		t.Fatal("applied to another tester's group")
	}
	if e = m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}

type notice struct {
	Header struct {
		ProtocolCode   string   `json:"protocolCode"`
		RecipientTypes []string `json:"recipientTypes"`
	} `json:"header"`
	DeviceInterfaceIDs []int64 `json:"deviceInterfaceIds"`
	DeviceIDs          []int64 `json:"deviceIds"`
	RestartService     *bool   `json:"restartService"`
}

func decode(t *testing.T, msgs []json.RawMessage) []notice {
	t.Helper()
	out := []notice{}
	for _, m := range msgs {
		var n notice
		if e := json.Unmarshal(m, &n); e != nil {
			t.Fatal(e)
		}
		out = append(out, n)
	}
	return out
}

func codes(n []notice) []string {
	out := []string{}
	for _, v := range n {
		out = append(out, v.Header.ProtocolCode)
	}
	return out
}

func TestManageNoticeOrder(t *testing.T) {
	n := decode(t, manageNotices([]int64{5, 6, 5}, nil))
	if !reflect.DeepEqual(codes(n), []string{"CHECKPOINT_UPDATED_NOTIFICATION", "SPECIFY_RULE_UPDATED_NOTIFICATION"}) || !reflect.DeepEqual(n[0].DeviceInterfaceIDs, []int64{5, 6}) {
		t.Fatalf("checkpoints only: %+v", n)
	}
	n = decode(t, manageNotices(nil, []int64{100}))
	if !reflect.DeepEqual(codes(n), []string{"SPECIFY_RULE_UPDATED_NOTIFICATION", "DEVICE_UPDATED_NOTIFICATION"}) || n[1].RestartService == nil || !*n[1].RestartService || !reflect.DeepEqual(n[0].Header.RecipientTypes, specifyRecipients) {
		t.Fatalf("devices only: %+v", n)
	}
	n = decode(t, manageNotices([]int64{5}, []int64{100, 200}))
	if !reflect.DeepEqual(codes(n), []string{"CHECKPOINT_UPDATED_NOTIFICATION", "SPECIFY_RULE_UPDATED_NOTIFICATION", "DEVICE_UPDATED_NOTIFICATION"}) || !reflect.DeepEqual(n[2].DeviceIDs, []int64{100, 200}) {
		t.Fatalf("both: %+v", n)
	}
	n = decode(t, deleteNotices([]int64{100, 200}))
	if !reflect.DeepEqual(codes(n), []string{"DEVICE_DELETED_NOTIFICATION", "SPECIFY_RULE_UPDATED_NOTIFICATION"}) || !reflect.DeepEqual(n[0].DeviceIDs, []int64{100, 200}) {
		t.Fatalf("delete: %+v", n)
	}
}

// expectBuildDelete mocks buildDelete for the device 100 (or the whole group).
func expectBuildDelete(m sqlmock.Sqlmock, whole bool, policies *sqlmock.Rows) {
	expectBuildDeleteWith(m, whole, policies, lockRows(), sqlmock.NewRows([]string{"r", "n", "c"}), false)
}

func expectBuildDeleteWith(m sqlmock.Sqlmock, whole bool, policies, locks, refs *sqlmock.Rows, inTx bool) {
	lock := ""
	if inTx {
		lock = " FOR UPDATE"
	}
	m.ExpectQuery("SELECT name, explorer_id, device_specify_rule_id FROM device_group WHERE id=\\?").WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"name", "e", "r"}).AddRow("System", int64(1), int64(70)))
	rows := sqlmock.NewRows([]string{"id", "name", "dc", "iid", "driver"}).
		AddRow(int64(100), "server_1", int64(1), int64(1000), testDriver).
		AddRow(int64(200), "server_2", int64(1), int64(2000), testDriver)
	m.ExpectQuery("FROM device_specify_rule_mapping_device m(.|\n)*ORDER BY d.id, i.id" + lock).WithArgs(int64(70)).WillReturnRows(rows)
	m.ExpectQuery("JOIN device_group g ON g.device_specify_rule_id=m.device_specify_rule_id WHERE m.device_id IN \\(\\?,\\?\\) AND m.device_specify_rule_id<>\\? ORDER BY g.id"+lock).WithArgs(int64(100), int64(200), int64(70)).WillReturnRows(locks)
	ids := []driver.Value{int64(100)}
	marks := "\\?"
	if whole {
		m.ExpectQuery("SELECT COUNT\\(\\*\\) FROM device_group WHERE parent_id=\\?").WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
		ids, marks = []driver.Value{int64(100), int64(200)}, "\\?,\\?"
	}
	m.ExpectQuery("SELECT id, COALESCE\\(check_interval,0\\) FROM checkpoint WHERE device_id IN \\(" + marks + "\\) AND flag=1 ORDER BY id" + lock).WithArgs(ids...).
		WillReturnRows(sqlmock.NewRows([]string{"id", "i"}).AddRow(int64(1), 10000).AddRow(int64(3), 60000))
	m.ExpectQuery("SELECT device_id, COUNT\\(\\*\\) FROM checkpoint").WithArgs(ids...).WillReturnRows(sqlmock.NewRows([]string{"d", "n"}).AddRow(int64(100), 2))
	m.ExpectQuery("FROM alarm_policy WHERE flag<>-1 AND checkpoint_id IN \\(\\?,\\?\\) ORDER BY id"+lock).WithArgs(int64(1), int64(3)).WillReturnRows(policies)
	m.ExpectQuery("FROM checkpoint_specify_rule_condition_value v").WithArgs(int64(1), int64(3)).WillReturnRows(refs)
	if !inTx {
		m.ExpectQuery("FROM alarm WHERE device_id IN").WithArgs(ids...).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
		m.ExpectQuery("SELECT id, device_id, COALESCE\\(name,''\\)").WithArgs(ids...).WillReturnRows(sqlmock.NewRows([]string{"id", "d", "n", "k"}).AddRow(int64(1), int64(100), "CPU", "liz.stats.a").AddRow(int64(3), int64(100), "MEM", "liz.stats.b"))
	}
}

func noPolicies() *sqlmock.Rows { return sqlmock.NewRows([]string{"id", "n", "c"}) }

func TestBuildDeleteScopeAndBlockers(t *testing.T) {
	setTester(t, "")
	db, m, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	expectBuildDelete(m, false, noPolicies())
	plan, prev, e := buildDelete(ctx, db, 7, testDriver, DeleteRequest{DeviceID: 100}, false)
	if e != nil {
		t.Fatal(e)
	}
	if len(plan.devices) != 1 || plan.devices[0].DeviceID != 100 || prev.Confirm != "server_1" || prev.Checkpoints != 2 || len(prev.Blockers) != 0 || plan.longest != 60000 || len(prev.Points) != 2 || prev.Points[1].Name != "MEM" {
		t.Fatalf("device scope: %+v %+v", plan, prev)
	}
	expectBuildDelete(m, true, sqlmock.NewRows([]string{"id", "n", "c"}).AddRow(int64(9), "CPU 높음", int64(3)))
	plan, prev, e = buildDelete(ctx, db, 7, testDriver, DeleteRequest{WholeGroup: true}, false)
	if e != nil {
		t.Fatal(e)
	}
	if len(plan.devices) != 2 || prev.Confirm != "System" || len(prev.Blockers) != 1 || !strings.Contains(prev.Blockers[0], "CPU 높음") {
		t.Fatalf("group scope: %+v %+v", plan, prev)
	}
	if e = m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}

func TestDeleteTxSQLForWholeGroup(t *testing.T) {
	setTester(t, "")
	db, m, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	expectBuildDelete(m, true, noPolicies())
	plan, _, e := buildDelete(ctx, db, 7, testDriver, DeleteRequest{WholeGroup: true}, false)
	if e != nil {
		t.Fatal(e)
	}
	m.ExpectBegin()
	m.ExpectQuery("SELECT name, explorer_id FROM device_group WHERE id=\\? FOR UPDATE").WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"n", "e"}).AddRow("System", int64(1)))
	expectBuildDeleteWith(m, true, noPolicies(), lockRows(), sqlmock.NewRows([]string{"r", "n", "c"}), true)
	m.ExpectExec("UPDATE device SET flag=-1, update_date=NOW\\(3\\) WHERE id IN \\(\\?,\\?\\) AND flag=1").WithArgs(int64(100), int64(200)).WillReturnResult(sqlmock.NewResult(0, 2))
	m.ExpectExec("UPDATE device_interface SET flag=-1 WHERE device_id IN \\(\\?,\\?\\)").WithArgs(int64(100), int64(200)).WillReturnResult(sqlmock.NewResult(0, 2))
	m.ExpectExec("UPDATE checkpoint SET flag=-1 WHERE device_id IN \\(\\?,\\?\\)").WithArgs(int64(100), int64(200)).WillReturnResult(sqlmock.NewResult(0, 2))
	m.ExpectExec("UPDATE device_control SET flag=-1 WHERE device_interface_id IN \\(SELECT id FROM device_interface WHERE device_id IN \\(\\?,\\?\\)\\)").WithArgs(int64(100), int64(200)).WillReturnResult(sqlmock.NewResult(0, 0))
	m.ExpectExec("INSERT INTO liz_log").WithArgs(sqlmock.AnyArg(), nil, "rtaskmgr", "Device Deleted.", 114, int64(100), "server_1", 1, "rtaskmgr").WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectExec("INSERT INTO liz_log").WithArgs(sqlmock.AnyArg(), nil, "rtaskmgr", "Device Deleted.", 114, int64(200), "server_2", 1, "rtaskmgr").WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectExec("INSERT INTO revinfo").WillReturnResult(sqlmock.NewResult(56, 1))
	expectAuditShape(m, "device_group", []string{"revtype", "name", "name_mod", "childrens_mod", "id", "rev"}, []string{"id", "name"})
	// Copied before the DELETE, revtype 2, nothing marked modified (admin's row).
	m.ExpectExec("INSERT INTO `device_group_aud` .* SELECT \\?,`name`,0,0,`id`,\\? FROM `device_group` WHERE id=\\?").WithArgs(2, int64(56), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectExec("INSERT INTO revchanges").WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectExec("DELETE FROM device_group WHERE id=\\?").WithArgs(int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectQuery("SELECT COALESCE\\(name,''\\), data_center_id FROM device_explorer WHERE id=\\?").WithArgs(int64(1)).WillReturnRows(sqlmock.NewRows([]string{"n", "dc"}).AddRow("My Explorer", int64(1)))
	m.ExpectExec("INSERT INTO liz_log").WithArgs(sqlmock.AnyArg(), nil, "rtaskmgr", "Device Group Deleted.", 114, int64(7), "System", 3, "rtaskmgr").WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectExec("INSERT INTO liz_log").WithArgs(sqlmock.AnyArg(), nil, "rtaskmgr", "Device Explorer Updated.", 113, int64(1), "My Explorer", 5, "rtaskmgr").WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectRollback()
	tx, _ := db.BeginTx(ctx, nil)
	if e = deleteTx(ctx, tx, plan, ""); e != nil {
		t.Fatal(e)
	}
	tx.Rollback()
	if e = m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}

func TestDeleteTxRefusesWhenSomethingChanged(t *testing.T) {
	setTester(t, "")
	db, m, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	expectBuildDelete(m, false, noPolicies())
	plan, _, e := buildDelete(ctx, db, 7, testDriver, DeleteRequest{DeviceID: 100}, false)
	if e != nil {
		t.Fatal(e)
	}
	plan.fingerprint = "changed since"
	m.ExpectBegin()
	m.ExpectQuery("SELECT name, explorer_id FROM device_group").WillReturnRows(sqlmock.NewRows([]string{"n", "e"}).AddRow("System", int64(1)))
	expectBuildDeleteWith(m, false, noPolicies(), lockRows(), sqlmock.NewRows([]string{"r", "n", "c"}), true)
	tx, _ := db.BeginTx(ctx, nil)
	defer tx.Rollback()
	if e = deleteTx(ctx, tx, plan, ""); e == nil || !strings.Contains(e.Error(), "아무것도 지우지 않았습니다") {
		t.Fatalf("stale delete ran: %v", e)
	}
	// No UPDATE was expected: sqlmock fails on any unexpected statement.
	if e = m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
	// A new policy since the preview blocks it too.
	expectBuildDelete(m, false, noPolicies())
	plan, _, _ = buildDelete(ctx, db, 7, testDriver, DeleteRequest{DeviceID: 100}, false)
	m.ExpectBegin()
	m.ExpectQuery("SELECT name, explorer_id FROM device_group").WillReturnRows(sqlmock.NewRows([]string{"n", "e"}).AddRow("System", int64(1)))
	expectBuildDeleteWith(m, false, sqlmock.NewRows([]string{"id", "n", "c"}).AddRow(int64(9), "p", int64(1)), lockRows(), sqlmock.NewRows([]string{"r", "n", "c"}), true)
	tx2, _ := db.BeginTx(ctx, nil)
	defer tx2.Rollback()
	if e = deleteTx(ctx, tx2, plan, ""); e == nil || !strings.Contains(e.Error(), "막는") {
		t.Fatalf("delete with a new policy ran: %v", e)
	}
}

type fakeKeys struct {
	keys    map[string]bool
	rewrite int // times a collector writes the keys back after a delete
	dels    int
}

func (f *fakeKeys) del(_ context.Context, keys []string) error {
	f.dels++
	for _, k := range keys {
		delete(f.keys, k)
	}
	return nil
}

func (f *fakeKeys) exist(_ context.Context, keys []string) ([]string, error) {
	if f.rewrite > 0 {
		f.rewrite--
		f.keys[keys[0]] = true
	}
	out := []string{}
	for _, k := range keys {
		if f.keys[k] {
			out = append(out, k)
		}
	}
	return out, nil
}

func TestCleanKeysDeletesAgainOnceThenReports(t *testing.T) {
	keys := []string{"checkpoint:1", "checkpoint:2"}
	ctx := context.Background()
	tick := func(time.Duration) {}
	f := &fakeKeys{keys: map[string]bool{"checkpoint:1": true, "checkpoint:2": true}}
	if c := cleanKeys(ctx, f, keys, 20*time.Millisecond, time.Millisecond, tick); c.err != nil || len(c.left) != 0 || c.again || f.dels != 1 {
		t.Fatalf("plain: %+v dels %d", c, f.dels)
	}
	f = &fakeKeys{keys: map[string]bool{"checkpoint:1": true}, rewrite: 1}
	if c := cleanKeys(ctx, f, keys, 20*time.Millisecond, time.Millisecond, tick); c.err != nil || len(c.left) != 0 || !c.again || f.dels != 2 {
		t.Fatalf("written back once: %+v dels %d", c, f.dels)
	}
	f = &fakeKeys{keys: map[string]bool{}, rewrite: 5}
	if c := cleanKeys(ctx, f, keys, time.Second, time.Millisecond, tick); !reflect.DeepEqual(c.left, []string{"checkpoint:1"}) {
		t.Fatalf("kept coming back: %+v", c)
	}
}

func TestSummarizeDelete(t *testing.T) {
	r := DeleteResult{DeviceIDs: []int64{1, 2}, Checkpoints: 10, KeysDeleted: 10, GroupDeleted: true}
	summarizeDelete(&r, cleanup{left: []string{}}, nil)
	if !r.Verified || !strings.Contains(r.Message, "그룹 포함") || !strings.Contains(r.Message, "남은 행 0") {
		t.Fatalf("%+v", r)
	}
	r = DeleteResult{DeviceIDs: []int64{1}, DBLeft: 1}
	summarizeDelete(&r, cleanup{err: errors.New("READONLY"), left: []string{"checkpoint:1"}}, nil)
	if r.Verified || !strings.Contains(r.Message, "READONLY") {
		t.Fatalf("%+v", r)
	}
}

func TestDevicesInAnotherGroupAreLocked(t *testing.T) {
	setTester(t, "")
	db, m, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	m.ExpectQuery("SELECT name, explorer_id, device_specify_rule_id FROM device_group WHERE id=\\?").WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"name", "e", "r"}).AddRow("System", int64(1), int64(70)))
	m.ExpectQuery("FROM device_specify_rule_mapping_device m").WithArgs(int64(70)).WillReturnRows(groupDeviceRows())
	m.ExpectQuery("JOIN device_group g").WithArgs(int64(100), int64(200), int64(300), int64(70)).
		WillReturnRows(sqlmock.NewRows([]string{"d", "g", "n"}).AddRow(int64(200), int64(8), "Servers"))
	m.ExpectQuery("FROM checkpoint WHERE device_id IN").WillReturnRows(checkpointRows())
	s, e := loadManaged(context.Background(), db, 7, testDriver, manageTestPlan(), nil)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(s.devices[200].Locked, "Servers (ID 8)") || s.devices[100].Locked != "" || len(s.Group.Warnings) != 2 {
		t.Fatalf("locks: %+v / %v", s.devices, s.Group.Warnings)
	}
	// A row edit skips the shared device (warning); naming it explicitly is an error.
	plan, errs, warns := resolveEdit(s, ManageEdit{Rows: []RowEdit{{Shape: "liz.stats.server.{서버별}.free.memory", Name: "Mem"}}})
	if len(errs) != 0 || len(plan.points) != 1 || plan.points[0].CheckpointID != 1 || len(warns) != 1 {
		t.Fatalf("row edit: %+v %v %v", plan.points, errs, warns)
	}
	_, errs, _ = resolveEdit(s, ManageEdit{Rows: []RowEdit{{Shape: "liz.stats.server.{서버별}.free.memory", Overrides: []PointOverride{{CheckpointID: 2, Name: "x"}}}}, Devices: []DeviceEdit{{DeviceID: 200, Name: "y"}}})
	if len(errs) != 2 {
		t.Fatalf("explicit edits of a shared device: %v", errs)
	}
}

func TestTesterBuildLocksUnprefixedDevices(t *testing.T) {
	setTester(t, "agy")
	db, m, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	m.ExpectQuery("FROM device_specify_rule_mapping_device m").WillReturnRows(sqlmock.NewRows([]string{"id", "name", "dc", "iid", "driver"}).
		AddRow(int64(1), "agy server_1", nil, int64(10), testDriver).AddRow(int64(2), "server_1", nil, int64(20), testDriver))
	m.ExpectQuery("JOIN device_group g").WillReturnRows(lockRows())
	devs, _, e := groupDevices(context.Background(), db, 70, testDriver, false)
	if e != nil {
		t.Fatal(e)
	}
	if devs[0].Locked != "" || !strings.Contains(devs[1].Locked, "agy ") {
		t.Fatalf("%+v", devs)
	}
}

func TestBuildDeleteBlocksLockedDevicesAndConditionRefs(t *testing.T) {
	setTester(t, "")
	db, m, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	expectBuildDeleteWith(m, true, noPolicies(), sqlmock.NewRows([]string{"d", "g", "n"}).AddRow(int64(200), int64(8), "Servers"),
		sqlmock.NewRows([]string{"r", "n", "c"}).AddRow(int64(31), "CPU 그룹", int64(3)), false)
	_, prev, e := buildDelete(context.Background(), db, 7, testDriver, DeleteRequest{WholeGroup: true}, false)
	if e != nil {
		t.Fatal(e)
	}
	if len(prev.Blockers) != 2 || !strings.Contains(prev.Blockers[0], "Servers (ID 8)") || !strings.Contains(prev.Blockers[1], "CPU 그룹") {
		t.Fatalf("%v", prev.Blockers)
	}
}

func TestRefreshDeviceUpdatedDropsVanishedDevices(t *testing.T) {
	db, m, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	rec := &manageRecord{Msgs: manageNotices([]int64{5}, []int64{100, 200}), Sent: 1}
	m.ExpectQuery("SELECT id FROM device WHERE flag=1 AND id IN").WithArgs(int64(100), int64(200)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(200)))
	if note, e := refreshDeviceUpdated(ctx, db, rec); e != nil || !strings.Contains(note, "1대") {
		t.Fatalf("%q %v", note, e)
	}
	n := decode(t, rec.Msgs)
	if len(n) != 3 || !reflect.DeepEqual(n[2].DeviceIDs, []int64{200}) {
		t.Fatalf("%+v", n)
	}
	m.ExpectQuery("SELECT id FROM device WHERE flag=1 AND id IN").WithArgs(int64(200)).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	if note, e := refreshDeviceUpdated(ctx, db, rec); e != nil || !strings.Contains(note, "보내지 않았습니다") || len(rec.Msgs) != 2 {
		t.Fatalf("%q %v %d", note, e, len(rec.Msgs))
	}
	if e = m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}

func TestManageRecordsSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	s := &Service{Directory: dir}
	done := &manageRecord{ID: "11111111-1111-4111-8111-111111111111", Kind: kindRename, At: "2026-10-23T10:00:00+09:00", Done: true}
	open := &manageRecord{ID: "22222222-2222-4222-8222-222222222222", Kind: kindDelete, At: "2026-10-23T11:00:00+09:00", GroupName: "System", Committed: true,
		Msgs: deleteNotices([]int64{1}), Sent: 1, Delete: DeleteResult{KeysLeft: []string{"checkpoint:9"}, Message: "남음"}}
	for _, r := range []*manageRecord{done, open} {
		if e := s.saveRecord(r); e != nil {
			t.Fatal(e)
		}
	}
	// A new Service (app restarted) still lists the unfinished one.
	list, e := (&Service{Directory: dir}).ManagePending()
	if e != nil || len(list) != 1 || list[0].ID != open.ID || list[0].Total != 2 || list[0].Sent != 1 || list[0].KeysLeft != 1 {
		t.Fatalf("%+v %v", list, e)
	}
	back, e := s.loadRecord(open.ID)
	if e != nil || back.Kind != kindDelete || len(back.Msgs) != 2 {
		t.Fatalf("%+v %v", back, e)
	}
}

func TestRecordCommittedChecksTheDB(t *testing.T) {
	db, m, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	m.ExpectQuery("SELECT COUNT\\(\\*\\) FROM device WHERE id IN \\(\\?\\) AND flag=-1").WithArgs(int64(1)).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	if ok, e := recordCommitted(ctx, db, &manageRecord{Kind: kindDelete, DeviceIDs: []int64{1}}); e != nil || ok {
		t.Fatalf("delete: %v %v", ok, e)
	}
	m.ExpectQuery("FROM checkpoint WHERE id=\\? AND BINARY COALESCE\\(name,''\\)=BINARY \\?").WithArgs(int64(5), "New", 10000).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	if ok, e := recordCommitted(ctx, db, &manageRecord{Kind: kindRename, Points: []PointChange{{CheckpointID: 5, NewName: "New", NewInterval: 10000}}}); e != nil || !ok {
		t.Fatalf("rename: %v %v", ok, e)
	}
}
