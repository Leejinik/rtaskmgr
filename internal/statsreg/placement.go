package statsreg

import (
	"fmt"
	"net"
	"sort"
	"strings"
)

// Keys whose server part is an alias (e.g. {liz_log_db_1}) cannot be placed on
// a server by name. logan (2026-10-23): place them by the servers' /etc/hosts.
// When the dialog was opened for a cluster, its servers' /etc/hosts (read by
// 「서버에서 불러오기」) recommend the server; the screen pre-selects it, marked
// 「hosts 로 추천」, and the user can change it. Nothing found: the key stays out.

type PlacementHint struct {
	Token  string `json:"token"`
	Host   string `json:"host"`   // recommended server ("" = none)
	Source string `json:"source"` // e.g. "server_1:/etc/hosts 192.0.2.21"
	Note   string `json:"note"`   // why there is no recommendation
}

// unplacedTokens: first placeholder values of keys with no server.
func unplacedTokens(points []Point) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, p := range points {
		if p.Host != "" {
			continue
		}
		if t := keyTokens(p.Key); len(t) > 0 && !seen[t[0]] {
			seen[t[0]] = true
			out = append(out, t[0])
		}
	}
	sort.Strings(out)
	return out
}

func placementHints(points []Point, servers []Server, res DiscoverResult) []PlacementHint {
	known := map[string]string{} // lower hostname -> hostname
	byIP := map[string]string{}
	for _, s := range servers {
		if s.Hostname == "common" || s.Hostname == "" {
			continue
		}
		known[strings.ToLower(s.Hostname)] = s.Hostname
		if s.IP != "" {
			byIP[s.IP] = s.Hostname
		}
	}
	// Another name of the same address that is a server of this environment.
	serverAt := func(ip string) string {
		if h := byIP[ip]; h != "" {
			return h
		}
		for _, e := range res.Hosts {
			if e.IP == ip && known[e.Name] != "" {
				return known[e.Name]
			}
		}
		for _, s := range res.Servers {
			if h, _, err := net.SplitHostPort(s.Addr); (err == nil && h == ip) || s.Addr == ip {
				if k := known[strings.ToLower(s.Hostname)]; k != "" {
					return k
				}
			}
		}
		return ""
	}
	out := []PlacementHint{}
	for _, token := range unplacedTokens(points) {
		hint := PlacementHint{Token: token}
		cands := map[string]string{}
		seen := []string{}
		for _, e := range res.Hosts {
			if e.Name != strings.ToLower(token) {
				continue
			}
			seen = append(seen, fmt.Sprintf("%s → %s", e.Server, e.IP))
			h := ""
			if isLoopback(e.IP) {
				h = known[strings.ToLower(e.Hostname)] // the server names itself
			} else {
				h = serverAt(e.IP)
			}
			if h != "" {
				if _, ok := cands[h]; !ok {
					cands[h] = fmt.Sprintf("%s:/etc/hosts %s", e.Server, e.IP)
				}
			}
		}
		switch {
		case len(cands) == 1:
			for h, src := range cands {
				hint.Host, hint.Source = h, src
			}
		case len(cands) > 1:
			names := []string{}
			for h := range cands {
				names = append(names, h)
			}
			sort.Strings(names)
			hint.Note = fmt.Sprintf("서버마다 /etc/hosts 가 다른 서버를 가리킵니다(%s) — 직접 고르세요", strings.Join(names, ", "))
		case len(seen) > 0:
			hint.Note = fmt.Sprintf("/etc/hosts 에 있지만(%s) 그 주소의 서버를 이 환경의 서버 목록에서 찾지 못했습니다", strings.Join(uniq(seen), ", "))
		default:
			hint.Note = "클러스터 서버들의 /etc/hosts 에 이 이름이 없습니다"
		}
		out = append(out, hint)
	}
	return out
}

// PlacementHints recommends servers for the analysis' unplaced keys from a
// cluster's /etc/hosts (res comes from 「서버에서 불러오기」 of that cluster).
func (s *Service) PlacementHints(planID string, res DiscoverResult) ([]PlacementHint, error) {
	s.mu.Lock()
	v, e := s.session(planID)
	s.mu.Unlock()
	if e != nil {
		return nil, e
	}
	return placementHints(v.Plan.Points, v.Plan.Servers, res), nil
}
