package main

import (
	"context"
	"rtaskmgr/internal/statsreg"
	"sync"
	"time"
)

// The running 「연결 확인」, so the screen can cancel the automatic one it starts
// when the dialog opens with a saved connection.
var statsInspect struct {
	sync.Mutex
	cancel context.CancelFunc
}

func (a *App) StatsInspect(c statsreg.Config) (statsreg.Plan, error) {
	ctx, cancel := context.WithTimeout(a.ctx, 2*time.Minute)
	statsInspect.Lock()
	statsInspect.cancel = cancel
	statsInspect.Unlock()
	defer cancel()
	return a.stats.Inspect(ctx, c)
}

// StatsCancelInspect stops the running 「연결 확인」 (it is read-only, nothing to undo).
func (a *App) StatsCancelInspect() {
	statsInspect.Lock()
	defer statsInspect.Unlock()
	if statsInspect.cancel != nil {
		statsInspect.cancel()
	}
}
func (a *App) StatsValidate(s statsreg.Selection) error {
	ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
	defer cancel()
	return a.stats.Validate(ctx, s)
}

// Register and Retry include the collector settle wait and the collection check
// (up to ~3 minutes together), see statsreg.register.
func (a *App) StatsRegister(s statsreg.Selection) (statsreg.Result, error) {
	ctx, cancel := context.WithTimeout(a.ctx, 6*time.Minute)
	defer cancel()
	return a.stats.Register(ctx, s)
}

// StatsConnections lists saved connections, most recent first.
func (a *App) StatsConnections() ([]statsreg.SavedConnection, error) {
	return a.stats.Connections()
}

// StatsSaveConnection remembers a connection that inspected successfully.
func (a *App) StatsSaveConnection(c statsreg.SavedConnection) error {
	return a.stats.SaveConnection(c)
}

// StatsClusterConnections returns the connection each cluster was set up with
// ("MK119 리소스 수집 등록" ticked in the cluster dialog), by cluster id.
func (a *App) StatsClusterConnections() (map[string]statsreg.SavedConnection, error) {
	return a.stats.ClusterConnections()
}

// StatsSetClusterConnection stores the cluster's connection.
func (a *App) StatsSetClusterConnection(clusterID string, c statsreg.SavedConnection) error {
	return a.stats.SetClusterConnection(clusterID, &c)
}

// StatsDeleteClusterConnection forgets the cluster's connection (unticked, or
// the cluster was deleted). No-op when there is none.
func (a *App) StatsDeleteClusterConnection(clusterID string) error {
	return a.stats.SetClusterConnection(clusterID, nil)
}

// StatsImportPatterns remembers the naming of an earlier, finished registration.
func (a *App) StatsImportPatterns(id string) (statsreg.PatternEvent, error) {
	return a.stats.ImportPatterns(id)
}

// StatsTesterPrefix is the tester name of a tester-only build ("" in a product
// build), see statsreg.TesterPrefix.
func (a *App) StatsTesterPrefix() string {
	return statsreg.TesterPrefix
}

func (a *App) StatsRetry(c statsreg.Config, id string) (statsreg.Result, error) {
	ctx, cancel := context.WithTimeout(a.ctx, 6*time.Minute)
	defer cancel()
	return a.stats.Retry(ctx, c, id)
}
