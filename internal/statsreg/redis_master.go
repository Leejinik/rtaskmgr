package statsreg

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/redis/go-redis/v9"
)

// openRedis takes the first node that answers, which is fine for reading but
// not for writing: with Sentinel the master moves (trunk: the list starts with
// .24 while .25 is master since a failover) and a DEL on a replica fails with
// READONLY. openRedisMaster probes the role of every address, follows what a
// replica or a sentinel says the master is, and returns only a node whose role
// is master. A Redis Cluster client already sends writes to the masters.
func openRedisMaster(ctx context.Context, c Config) (*redisConn, error) {
	seeds := redisSeeds(c)
	if len(seeds) == 0 {
		return nil, fmt.Errorf("Redis 주소를 입력하세요")
	}
	queue := append([]string{}, seeds...)
	tried := map[string]bool{}
	notes := []string{}
	for len(queue) > 0 && len(tried) < 24 {
		addr := queue[0]
		queue = queue[1:]
		if tried[addr] {
			continue
		}
		tried[addr] = true
		n := redisNode(c, addr)
		role, next, e := nodeRole(ctx, n)
		if e != nil && strings.Contains(strings.ToLower(e.Error()), "auth") && c.RedisPassword != "" {
			// A sentinel usually has no password of its own.
			n.Close()
			bare := c
			bare.RedisUser, bare.RedisPassword = "", ""
			n = redisNode(bare, addr)
			role, next, e = nodeRole(ctx, n)
		}
		switch {
		case e != nil:
			notes = append(notes, fmt.Sprintf("%s: %v", addr, e))
		case role == "master":
			if info, err := n.Info(ctx, "cluster").Result(); err == nil && strings.Contains(info, "cluster_enabled:1") {
				n.Close()
				r, err := openRedis(ctx, c) // cluster: the cluster client routes writes
				if r != nil {
					r.Master = "cluster"
				}
				return r, err
			}
			return &redisConn{UniversalClient: n, Info: "Redis 마스터 " + addr, Master: addr}, nil
		default:
			notes = append(notes, fmt.Sprintf("%s: %s%s", addr, role, followNote(next)))
			queue = append(queue, next...)
		}
		n.Close()
	}
	return nil, fmt.Errorf("쓸 수 있는 Redis 마스터를 찾지 못했습니다(%s) — Redis 주소 칸에 마스터가 될 수 있는 서버를 모두 쉼표로 넣으세요", strings.Join(notes, " / "))
}

func followNote(next []string) string {
	if len(next) == 0 {
		return ""
	}
	return " → 마스터 " + strings.Join(next, ", ")
}

// nodeRole returns the node's role and, for a replica or a sentinel, the
// address(es) it names as master. ROLE first, INFO replication if ROLE is not allowed.
func nodeRole(ctx context.Context, n *redis.Client) (string, []string, error) {
	v, e := n.Do(ctx, "ROLE").Slice()
	if e == nil && len(v) > 0 {
		role := fmt.Sprint(v[0])
		switch role {
		case "slave", "replica":
			if len(v) >= 3 {
				return "복제본", []string{net.JoinHostPort(fmt.Sprint(v[1]), fmt.Sprint(v[2]))}, nil
			}
			return "복제본", nil, nil
		case "sentinel":
			out := []string{}
			if len(v) > 1 {
				if names, ok := v[1].([]any); ok {
					for _, name := range names {
						if a, err := n.Do(ctx, "SENTINEL", "get-master-addr-by-name", fmt.Sprint(name)).StringSlice(); err == nil && len(a) == 2 {
							out = append(out, net.JoinHostPort(a[0], a[1]))
						}
					}
				}
			}
			return "센티널", out, nil
		}
		return role, nil, nil
	}
	info, err := n.Info(ctx, "replication").Result()
	if err != nil {
		if e != nil {
			return "", nil, e
		}
		return "", nil, err
	}
	fields := map[string]string{}
	for _, line := range strings.Split(info, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), ":"); ok {
			fields[k] = v
		}
	}
	switch fields["role"] {
	case "master":
		return "master", nil, nil
	case "slave":
		if h, p := fields["master_host"], fields["master_port"]; h != "" {
			if _, err := strconv.Atoi(p); err == nil {
				return "복제본", []string{net.JoinHostPort(h, p)}, nil
			}
		}
		return "복제본", nil, nil
	}
	return "", nil, fmt.Errorf("역할을 알 수 없습니다")
}

// redisSummary is what the screen says about Redis: how many addresses were
// given and which one ROLE names master (2nd round: 「Redis 단일 노드 .24」 was
// shown while .25 was master). The second value is the master ("" = none).
func redisSummary(ctx context.Context, c Config, servers []Server, read *redisConn) (string, string) {
	n := len(redisSeeds(c))
	if read != nil && read.cluster != nil {
		return fmt.Sprintf("Redis Cluster(서버 %d대 입력) · %s", n, read.Info), "cluster"
	}
	m, e := openRedisMaster(ctx, c)
	if e != nil {
		return fmt.Sprintf("Redis %d대 입력 · 마스터 없음 — 장비·그룹 삭제는 할 수 없습니다(Redis 에 남은 최근값을 지울 수 없음). 등록과 이름·주기 수정은 됩니다.\n자세히: %v", n, e), ""
	}
	defer m.Close()
	return fmt.Sprintf("Redis %d대 입력 · 마스터 %s", n, serverOfAddr(m.Master, servers)), m.Master
}

// serverOfAddr names the server of host:port by its IP ("server_2 (192.0.2.12:5000)").
func serverOfAddr(addr string, servers []Server) string {
	h, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	for _, s := range servers {
		if s.Hostname != "common" && (s.IP == h || strings.EqualFold(s.Hostname, h)) {
			return s.Hostname + " (" + addr + ")"
		}
	}
	return addr
}
