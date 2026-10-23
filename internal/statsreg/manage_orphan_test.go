package statsreg

import (
	"context"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

const testType = int64(88)

var orphanColumns = []string{"id", "name", "dc", "mine", "other", "points", "stats"}

func TestOrphanDevicesLockWhatTheToolDidNotRegister(t *testing.T) {
	setTester(t, "")
	db, m, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	m.ExpectQuery("FROM device d(.|\n)*NOT EXISTS \\(SELECT 1 FROM device_specify_rule_mapping_device m JOIN device_group g").
		WithArgs(testDriver, testDriver, testDriver, testType, testDriver).
		WillReturnRows(sqlmock.NewRows(orphanColumns).
			AddRow(int64(1), "server_1", int64(1), 1, 0, 32, 32). // registered by the tool
			AddRow(int64(2), "empty", nil, 0, 0, 0, 0).           // nothing on it
			AddRow(int64(3), "ups", nil, 0, 1, 5, 0).             // another driver
			AddRow(int64(4), "mixed", nil, 1, 0, 5, 3))           // other checkpoints
	got, e := orphanDevices(context.Background(), db, testType, testDriver, 0, false)
	if e != nil {
		t.Fatal(e)
	}
	if len(got) != 4 || got[0].Locked != "" {
		t.Fatalf("%+v", got)
	}
	for _, d := range got[1:] {
		if !strings.Contains(d.Locked, "admin 의 장비 트리 맨 위(그룹 밖 장비)") || !strings.Contains(d.Locked, "장비 ID") {
			t.Fatalf("locked without where to find it: %+v", d)
		}
	}
}

func TestOrphanDevicesInTesterBuild(t *testing.T) {
	setTester(t, "agy")
	db, m, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	m.ExpectQuery("FROM device d").WillReturnRows(sqlmock.NewRows(orphanColumns).
		AddRow(int64(1), "agy server_1", nil, 1, 0, 2, 2).AddRow(int64(2), "server_1", nil, 1, 0, 2, 2))
	got, e := orphanDevices(context.Background(), db, testType, testDriver, 0, false)
	if e != nil || len(got) != 1 || got[0].DeviceID != 1 {
		t.Fatalf("%+v %v", got, e)
	}
}

func expectOrphanDelete(m sqlmock.Sqlmock, inTx bool) {
	lock := ""
	if inTx {
		lock = " FOR UPDATE"
	}
	m.ExpectQuery("FROM device d(.|\n)*AND d.id=\\?(.|\n)*LIMIT 500"+lock).WithArgs(testDriver, testDriver, testDriver, testType, testDriver, int64(1)).
		WillReturnRows(sqlmock.NewRows(orphanColumns).AddRow(int64(1), "server_1", int64(1), 1, 0, 2, 2))
	m.ExpectQuery("SELECT id, COALESCE\\(check_interval,0\\) FROM checkpoint WHERE device_id IN \\(\\?\\) AND flag=1 ORDER BY id" + lock).WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "i"}).AddRow(int64(11), 10000).AddRow(int64(12), 10000))
	m.ExpectQuery("FROM alarm_policy WHERE flag<>-1").WithArgs(int64(11), int64(12)).WillReturnRows(noPolicies())
	m.ExpectQuery("FROM checkpoint_specify_rule_condition_value v").WithArgs(int64(11), int64(12)).WillReturnRows(sqlmock.NewRows([]string{"r", "n", "c"}))
	if !inTx {
		m.ExpectQuery("FROM alarm WHERE device_id IN").WithArgs(int64(1)).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
		m.ExpectQuery("SELECT id, device_id, COALESCE").WithArgs(int64(1)).WillReturnRows(sqlmock.NewRows([]string{"id", "d", "n", "k"}))
	}
}

func TestOrphanDeleteIsCheckedAgainInTheTransaction(t *testing.T) {
	setTester(t, "")
	db, m, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	expectOrphanDelete(m, false)
	plan, prev, e := buildOrphanDelete(ctx, db, testType, testDriver, 1, false)
	if e != nil || len(prev.Blockers) != 0 || prev.Confirm != "server_1" || prev.Checkpoints != 2 || !plan.orphan {
		t.Fatalf("%+v %+v %v", plan, prev, e)
	}
	m.ExpectBegin()
	expectOrphanDelete(m, true)
	m.ExpectExec("UPDATE device SET flag=-1").WithArgs(int64(1)).WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectExec("UPDATE device_interface SET flag=-1").WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectExec("UPDATE checkpoint SET flag=-1").WillReturnResult(sqlmock.NewResult(0, 2))
	m.ExpectExec("UPDATE device_control SET flag=-1").WillReturnResult(sqlmock.NewResult(0, 0))
	m.ExpectExec("INSERT INTO liz_log").WithArgs(sqlmock.AnyArg(), nil, "rtaskmgr", "Device Deleted.", 114, int64(1), "server_1", 1, "rtaskmgr").WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectRollback()
	tx, _ := db.BeginTx(ctx, nil)
	if e = deleteTx(ctx, tx, plan, ""); e != nil {
		t.Fatal(e)
	}
	tx.Rollback()
	if e = m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
	// A checkpoint added in admin after the preview: refused, nothing written.
	m.ExpectBegin()
	m.ExpectQuery("FROM device d").WillReturnRows(sqlmock.NewRows(orphanColumns).AddRow(int64(1), "server_1", int64(1), 1, 0, 3, 2))
	m.ExpectQuery("FROM checkpoint WHERE device_id IN").WillReturnRows(sqlmock.NewRows([]string{"id", "i"}).AddRow(int64(11), 10000).AddRow(int64(12), 10000).AddRow(int64(13), 10000))
	m.ExpectQuery("FROM alarm_policy").WillReturnRows(noPolicies())
	m.ExpectQuery("FROM checkpoint_specify_rule_condition_value v").WillReturnRows(sqlmock.NewRows([]string{"r", "n", "c"}))
	tx2, _ := db.BeginTx(ctx, nil)
	defer tx2.Rollback()
	if e = deleteTx(ctx, tx2, plan, ""); e == nil || !strings.Contains(e.Error(), "아무것도 지우지 않았습니다") {
		t.Fatalf("changed device deleted: %v", e)
	}
}
