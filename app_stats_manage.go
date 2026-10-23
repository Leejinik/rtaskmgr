package main

import (
	"context"
	"rtaskmgr/internal/statsreg"
	"time"
)

// 「기존 등록 관리」 of the MK119 resource registration, see statsreg/manage.go.

func (a *App) StatsManageLoad(planID string, groupID int64) (statsreg.ManagedGroup, error) {
	ctx, cancel := context.WithTimeout(a.ctx, time.Minute)
	defer cancel()
	return a.stats.ManageLoad(ctx, planID, groupID)
}

func (a *App) StatsManagePreview(edit statsreg.ManageEdit) (statsreg.ManagePreview, error) {
	ctx, cancel := context.WithTimeout(a.ctx, time.Minute)
	defer cancel()
	return a.stats.ManagePreview(ctx, edit)
}

// StatsManageApply includes the notices and the collection check (up to ~3 minutes).
func (a *App) StatsManageApply(previewID string) (statsreg.ManageResult, error) {
	ctx, cancel := context.WithTimeout(a.ctx, 6*time.Minute)
	defer cancel()
	return a.stats.ManageApply(ctx, previewID)
}

// StatsManageResend finishes a rename or delete whose notices were not all
// sent (also after a restart); planID is the current connection (연결 확인).
func (a *App) StatsManageResend(planID, id string) (statsreg.ManageOutcome, error) {
	ctx, cancel := context.WithTimeout(a.ctx, 6*time.Minute)
	defer cancel()
	return a.stats.ManageResendOutcome(ctx, planID, id)
}

// StatsManagePending lists unfinished renames and deletes (kept on disk).
func (a *App) StatsManagePending() ([]statsreg.PendingManage, error) {
	return a.stats.ManagePending()
}

// StatsManageRedisRetry deletes the Redis keys of a finished delete again.
func (a *App) StatsManageRedisRetry(planID, id string) (statsreg.DeleteResult, error) {
	ctx, cancel := context.WithTimeout(a.ctx, 3*time.Minute)
	defer cancel()
	return a.stats.ManageRedisRetry(ctx, planID, id)
}

func (a *App) StatsManageDeletePreview(req statsreg.DeleteRequest) (statsreg.DeletePreview, error) {
	ctx, cancel := context.WithTimeout(a.ctx, time.Minute)
	defer cancel()
	return a.stats.ManageDeletePreview(ctx, req)
}

// StatsManageDelete includes the collector wait and the Redis key check (up to ~2 minutes).
func (a *App) StatsManageDelete(previewID, confirm string) (statsreg.DeleteResult, error) {
	ctx, cancel := context.WithTimeout(a.ctx, 6*time.Minute)
	defer cancel()
	return a.stats.ManageDelete(ctx, previewID, confirm)
}
