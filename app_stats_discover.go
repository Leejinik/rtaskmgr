package main

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"rtaskmgr/internal/host"
	"rtaskmgr/internal/statsreg"
)

// 「서버에서 불러오기」: read the MariaDB / Redis / Kafka connection from the
// cluster's own configuration. Read-only on the servers (statsreg.DiscoverScript
// only reads files and the process list); nothing is written, nothing restarted.
// The result carries passwords so the dialog can fill them — it is never logged.

// StatsDiscoverFromCluster reads a saved cluster's servers.
func (a *App) StatsDiscoverFromCluster(clusterID string) (statsreg.DiscoverResult, error) {
	if a.hosts == nil {
		return statsreg.DiscoverResult{}, fmt.Errorf("호스트 저장소를 열 수 없습니다")
	}
	all, err := a.hosts.List()
	if err != nil {
		return statsreg.DiscoverResult{}, err
	}
	var members []host.Host
	for _, h := range all {
		if h.ClusterID == clusterID {
			members = append(members, h)
		}
	}
	if len(members) == 0 {
		return statsreg.DiscoverResult{}, fmt.Errorf("클러스터에 서버가 없습니다")
	}
	return a.StatsDiscoverFromHosts(members)
}

// StatsDiscoverFromHosts reads the given servers as they are typed in the
// cluster dialog — saved or not, so a new cluster works before it is saved and
// an edit (changed password, added server) is used as is. A jump host id is
// looked up among these hosts first (new rows carry temporary ids), then in the
// saved host list.
func (a *App) StatsDiscoverFromHosts(hosts []host.Host) (statsreg.DiscoverResult, error) {
	if len(hosts) == 0 {
		return statsreg.DiscoverResult{}, fmt.Errorf("서버가 없습니다")
	}
	byID := map[string]host.Host{}
	for _, h := range hosts {
		if h.ID != "" {
			byID[h.ID] = h
		}
	}
	resolve := func(id string) (host.Host, bool) {
		if h, ok := byID[id]; ok {
			return h, true
		}
		if a.hosts == nil {
			return host.Host{}, false
		}
		h, ok, err := a.hosts.Get(id)
		return h, ok && err == nil
	}

	ctx, cancel := context.WithTimeout(a.ctx, 90*time.Second)
	defer cancel()
	dumps := make([]statsreg.ServerDump, len(hosts))
	servers := make([]statsreg.DiscoverServer, len(hosts))
	var wg sync.WaitGroup
	for i, h := range hosts {
		wg.Add(1)
		go func(i int, h host.Host) {
			defer wg.Done()
			d := statsreg.ServerDump{Name: h.Name, Addr: h.Addr}
			s := statsreg.DiscoverServer{Name: h.Name, Addr: h.Addr}
			defer func() { dumps[i], servers[i] = d, s }()
			o, err := a.mgr.OpenOneShot(ctx, h, resolve)
			if err != nil {
				d.Err, s.Error = err.Error(), err.Error()
				return
			}
			defer o.Close()
			s.Via, s.Elevated = o.Via, o.Elevated()
			out, err := o.Run(ctx, statsreg.DiscoverScript)
			statsreg.ParseDump(&d, out)
			if d.Hostname == "" {
				if err == nil {
					err = fmt.Errorf("응답을 해석할 수 없습니다")
				}
				d.Err, s.Error = err.Error(), err.Error()
				return
			}
			s.Hostname = d.Hostname
		}(i, h)
	}
	wg.Wait()

	res := statsreg.BuildDiscovery(dumps, statsreg.DiscoverEnv{Probe: probeTCP, LookupIP: lookupIP})
	res.Servers = servers
	return res, nil
}

// probeTCP tries every address from this PC at once (2 s each).
func probeTCP(addrs []string) map[string]bool {
	out := make(map[string]bool, len(addrs))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, a := range addrs {
		wg.Add(1)
		go func(a string) {
			defer wg.Done()
			c, err := net.DialTimeout("tcp", a, 2*time.Second)
			if err == nil {
				c.Close()
			}
			mu.Lock()
			out[a] = err == nil
			mu.Unlock()
		}(a)
	}
	wg.Wait()
	return out
}

func lookupIP(name string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, name)
	if err != nil {
		return ""
	}
	for _, ip := range ips {
		if v4 := ip.IP.To4(); v4 != nil {
			return v4.String()
		}
	}
	if len(ips) > 0 {
		return ips[0].IP.String()
	}
	return ""
}
