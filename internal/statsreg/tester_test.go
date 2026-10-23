package statsreg

import (
	"context"
	"reflect"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// setTester sets TesterPrefix for one test and restores the product value.
func setTester(t *testing.T, prefix string) {
	old := TesterPrefix
	TesterPrefix = prefix
	t.Cleanup(func() { TesterPrefix = old })
}

var existingColumns = []string{"request_command", "id", "gid", "gname", "did", "dname", "iid"}

func existingRows() *sqlmock.Rows {
	return sqlmock.NewRows(existingColumns).
		AddRow("k.agy", int64(1), int64(10), "agy_System", int64(100), "agy server_1", int64(1000)).
		AddRow("k.codex", int64(2), int64(20), "codex_System", int64(200), "codex server_1", int64(2000)).
		AddRow("k.product", int64(3), int64(30), "System", int64(300), "server_1", int64(3000)).
		AddRow("k.nogroup", int64(4), int64(0), "", int64(400), "agy loose", int64(4000)).
		AddRow("k.like", int64(5), int64(50), "agyX_System", int64(500), "agy server_2", int64(5000))
}

func TestEmptyTesterPrefixChangesNothing(t *testing.T) {
	setTester(t, "")
	sel := Selection{GroupName: "System", Servers: []Server{{Hostname: "server_1", Name: "server_1"}, {Hostname: "common", Name: "common"}}}
	if got := testerSelection(sel); !reflect.DeepEqual(got, sel) {
		t.Fatalf("product selection changed: %+v", got)
	}
	p := Plan{GroupName: "System", Servers: []Server{{Hostname: "server_1", Name: "server_1"}}}
	testerPlanDefaults(&p)
	if p.GroupName != "System" || p.Servers[0].Name != "server_1" {
		t.Fatalf("product defaults changed: %+v", p)
	}
	regs := []registeredKey{{key: "a", groupID: 0}, {key: "b", groupID: 7, group: "codex_x"}}
	if got := testerScope(regs); !reflect.DeepEqual(got, regs) {
		t.Fatalf("product registrations scoped: %+v", got)
	}

	// The product queries run unchanged: every group counts.
	db, m, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	m.ExpectQuery(`SELECT c.request_command, c.id, COALESCE\(g.id,0\)`).WithArgs(int64(3053)).WillReturnRows(existingRows())
	m.ExpectQuery(`SELECT DISTINCT c.request_command FROM checkpoint c`).WithArgs(int64(3053)).WillReturnRows(sqlmock.NewRows([]string{"k"}).AddRow("k.codex"))
	m.ExpectQuery(`SELECT COUNT\(\*\) FROM checkpoint c JOIN device_interface`).WithArgs(int64(3053), "k.codex").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	ctx := context.Background()
	if got, e := loadExisting(ctx, db, 3053); e != nil || len(got) != 5 {
		t.Fatalf("product loadExisting: %d %v", len(got), e)
	}
	if got, e := deletedKeys(ctx, db, 3053); e != nil || !got["k.codex"] {
		t.Fatalf("product deletedKeys: %v %v", got, e)
	}
	if n, e := registeredCount(ctx, db, 3053, []string{"k.codex"}); e != nil || n != 1 {
		t.Fatalf("product registeredCount: %d %v", n, e)
	}
	if e = m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}

func TestTesterPrefixForcesNames(t *testing.T) {
	setTester(t, "agy")
	servers := []Server{{Hostname: "server_1", Name: "server_1"}, {Hostname: "server_2", Name: "agy server_2"}, {Hostname: "common", Name: ""}}
	sel := testerSelection(Selection{GroupName: "System", Servers: servers})
	if sel.GroupName != "agy_System" {
		t.Fatalf("group: %q", sel.GroupName)
	}
	if got := []string{sel.Servers[0].Name, sel.Servers[1].Name, sel.Servers[2].Name}; !reflect.DeepEqual(got, []string{"agy server_1", "agy server_2", ""}) {
		t.Fatalf("devices: %q", got)
	}
	if servers[0].Name != "server_1" {
		t.Fatal("caller's servers modified")
	}
	if got := testerSelection(Selection{GroupName: "agy_System"}).GroupName; got != "agy_System" {
		t.Fatalf("prefix doubled: %q", got)
	}
	// Names validation rejects stay rejected instead of becoming just the prefix.
	for _, n := range []string{"", " System"} {
		if got := testerSelection(Selection{GroupName: n}).GroupName; got != n {
			t.Fatalf("invalid name %q became %q", n, got)
		}
	}
	p := Point{Key: "liz.stats.vip.location", Name: "VIP", Host: "common", Format: 4, Interval: 10000, Selected: true}
	if e := validateSelection(Plan{Points: []Point{p}}, testerSelection(Selection{GroupName: "", Servers: []Server{{Hostname: "common", Name: "common"}}, Points: []Point{p}})); e == nil {
		t.Fatal("empty group name accepted")
	}

	plan := Plan{GroupName: "System", Servers: []Server{{Hostname: "server_1", Name: "server_1"}, {Hostname: "common", Name: "common"}}}
	testerPlanDefaults(&plan)
	if plan.GroupName != "agy_System" || plan.Servers[0].Name != "agy server_1" || plan.Servers[1].Name != "agy common" || plan.Servers[0].Hostname != "server_1" {
		t.Fatalf("plan defaults: %+v", plan)
	}
}

// Only the tester's own groups count as registered: keys of other testers'
// groups, product groups and devices outside any group look unregistered.
func TestTesterPrefixScopesExistingRegistrations(t *testing.T) {
	setTester(t, "agy")
	db, m, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	m.ExpectQuery(`SELECT c.request_command, c.id, COALESCE\(g.id,0\)`).WithArgs(int64(3053)).WillReturnRows(existingRows())
	regs, e := loadExisting(ctx, db, 3053)
	if e != nil || len(regs) != 1 || regs[0].key != "k.agy" {
		t.Fatalf("scoped loadExisting: %+v %v", regs, e)
	}

	plan := Plan{Servers: []Server{{Hostname: "server_1"}}, Points: []Point{
		{Key: "k.agy", Host: "server_1", Selected: true}, {Key: "k.codex", Host: "server_1", Selected: true},
		{Key: "k.product", Host: "server_1", Selected: true}, {Key: "k.nogroup", Host: "server_1", Selected: true}, {Key: "k.like", Host: "server_1", Selected: true}}}
	markExisting(&plan, regs, map[string]string{}, map[string]string{})
	if len(plan.Existing) != 1 || plan.Existing[0].Name != "agy_System" {
		t.Fatalf("update targets: %+v", plan.Existing)
	}
	for _, pt := range plan.Points {
		if pt.Registered != (pt.Key == "k.agy") {
			t.Fatalf("registered outside the tester's groups: %+v", pt)
		}
	}
	if _, e = updateTarget(plan, Selection{UpdateGroupID: 20}); e == nil {
		t.Fatal("another tester's group accepted as update target")
	}

	m.ExpectQuery(`SELECT DISTINCT c.request_command, COALESCE\(g.name,''\), COALESCE\(d.name,''\)`).WithArgs(int64(3053)).WillReturnRows(sqlmock.NewRows([]string{"k", "g", "d"}).
		AddRow("k.agy", "agy_System", "agy server_1").
		AddRow("k.unmapped", "", "agy server_3"). // deleted device that lost its group
		AddRow("k.codex", "codex_System", "codex server_1").
		AddRow("k.product", "System", "server_1"))
	deleted, e := deletedKeys(ctx, db, 3053)
	if e != nil || !reflect.DeepEqual(deleted, map[string]bool{"k.agy": true, "k.unmapped": true}) {
		t.Fatalf("scoped deletedKeys: %v %v", deleted, e)
	}

	// In-transaction re-check: a checkpoint in two groups counts once; other groups not at all.
	m.ExpectQuery(`SELECT c.id, COALESCE\(g.name,''\) FROM checkpoint c`).WithArgs(int64(3053), "k.agy", "k.codex").WillReturnRows(sqlmock.NewRows([]string{"id", "g"}).
		AddRow(int64(1), "agy_System").AddRow(int64(1), "agy_Other").AddRow(int64(2), "codex_System").AddRow(int64(3), "").AddRow(int64(5), "agyX_System"))
	if n, e := registeredCount(ctx, db, 3053, []string{"k.agy", "k.codex"}); e != nil || n != 1 {
		t.Fatalf("scoped registeredCount: %d %v", n, e)
	}
	m.ExpectQuery(`SELECT c.id, COALESCE\(g.name,''\) FROM checkpoint c`).WithArgs(int64(3053), "k.codex").WillReturnRows(sqlmock.NewRows([]string{"id", "g"}).AddRow(int64(2), "codex_System"))
	if n, e := registeredCount(ctx, db, 3053, []string{"k.codex"}); e != nil || n != 0 {
		t.Fatalf("another tester's key must look unregistered: %d %v", n, e)
	}
	if e = m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}
