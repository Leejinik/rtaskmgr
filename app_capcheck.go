// Wails bindings for the capacity check: Redis main-thread ceiling and Kafka broker
// threads/partitions, measured on every server of a cluster at the same time. The
// measuring itself is the field scripts — see internal/monitor/capcheck.go.
package main

import (
	"sync"

	"rtaskmgr/internal/monitor"
)

// CapacityHostReport is one server's row group in the view.
type CapacityHostReport struct {
	HostID   string                 `json:"hostId"`
	HostName string                 `json:"hostName"`
	Err      string                 `json:"err"`
	Result   monitor.CapacityResult `json:"result"`
}

// CapacityRun measures every given host concurrently. It blocks for about one
// measuring window (plus Kafka tool start-up), so the view shows a timer meanwhile.
func (a *App) CapacityRun(hostIDs []string, o monitor.CapacityOptions) []CapacityHostReport {
	if o.Seconds < 5 {
		o.Seconds = 5
	}
	if o.Seconds > 300 {
		o.Seconds = 300
	}
	ids := a.serverIdents(hostIDs)
	out := make([]CapacityHostReport, len(ids))
	var wg sync.WaitGroup
	for i, si := range ids {
		wg.Add(1)
		go func(i int, si monitor.ServerIdent) {
			defer wg.Done()
			hr := CapacityHostReport{HostID: si.HostID, HostName: si.DisplayName}
			r, err := a.mgr.RunCapacityCheck(si.HostID, o)
			if err != nil {
				hr.Err = err.Error()
			}
			hr.Result = r
			out[i] = hr
		}(i, si)
	}
	wg.Wait()

	a.capacityMu.Lock()
	if a.capacityLast == nil {
		a.capacityLast = map[string]CapacityHostReport{}
	}
	for _, hr := range out {
		a.capacityLast[hr.HostID] = hr
	}
	a.capacityMu.Unlock()
	return out
}

// CapacityLast returns the latest result of each host, if any (no server contact).
func (a *App) CapacityLast(hostIDs []string) []CapacityHostReport {
	a.capacityMu.Lock()
	defer a.capacityMu.Unlock()
	out := []CapacityHostReport{}
	for _, id := range hostIDs {
		if hr, ok := a.capacityLast[id]; ok {
			out = append(out, hr)
		}
	}
	return out
}
