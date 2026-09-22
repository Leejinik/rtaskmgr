package main

import (
	"context"
	"rtaskmgr/internal/statsreg"
	"time"
)

func (a *App) StatsInspect(c statsreg.Config) (statsreg.Plan, error) {
	ctx, cancel := context.WithTimeout(a.ctx, 2*time.Minute)
	defer cancel()
	return a.stats.Inspect(ctx, c)
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

// StatsImportPatterns remembers the naming of an earlier, finished registration.
func (a *App) StatsImportPatterns(id string) (statsreg.PatternEvent, error) {
	return a.stats.ImportPatterns(id)
}

func (a *App) StatsRetry(c statsreg.Config, id string) (statsreg.Result, error) {
	ctx, cancel := context.WithTimeout(a.ctx, 6*time.Minute)
	defer cancel()
	return a.stats.Retry(ctx, c, id)
}
