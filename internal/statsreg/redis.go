package statsreg

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Redis may be a single node or a Redis Cluster (MK119 on AWS: 3 masters + 3
// replicas, each announcing its private 172.31.x address). A single-node
// client on a cluster silently sees only that node's shard, so cluster mode
// is detected and every master is scanned.

// redisSeeds splits the address field: "a, b c" -> host:port for each.
func redisSeeds(c Config) []string {
	out := []string{}
	for _, h := range strings.FieldsFunc(c.RedisHost, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == ';' }) {
		if _, _, e := net.SplitHostPort(h); e == nil {
			out = append(out, h)
		} else {
			out = append(out, net.JoinHostPort(strings.Trim(h, "[]"), strconv.Itoa(c.RedisPort)))
		}
	}
	return out
}

func redisNode(c Config, addr string) *redis.Client {
	return redis.NewClient(&redis.Options{Addr: addr, Username: c.RedisUser, Password: c.RedisPassword, DB: c.RedisDB, DialTimeout: 8 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second})
}

// Private node address -> the host the user typed for that machine. Ports are
// kept, so "172.31.57.82:5001" reaches "<public>:5001".
func clusterHostMap(ctx context.Context, c Config, seeds []string) (map[string]string, error) {
	m := map[string]string{}
	var last error
	for _, seed := range seeds {
		n := redisNode(c, seed)
		nodes, e := n.ClusterNodes(ctx).Result()
		n.Close()
		if e != nil {
			last = e
			continue
		}
		public, _, _ := net.SplitHostPort(seed)
		for _, line := range strings.Split(nodes, "\n") {
			f := strings.Fields(line)
			if len(f) < 3 || !strings.Contains(f[2], "myself") {
				continue
			}
			addr := f[1]
			if i := strings.IndexByte(addr, '@'); i >= 0 {
				addr = addr[:i]
			}
			if private, _, e := net.SplitHostPort(addr); e == nil && private != "" && private != public {
				m[private] = public
			}
		}
	}
	if len(m) == 0 && last != nil {
		return nil, last
	}
	return m, nil
}

type redisConn struct {
	redis.UniversalClient
	cluster *redis.ClusterClient
	Info    string // shown to the user: which topology was read
}

func openRedis(ctx context.Context, c Config) (*redisConn, error) {
	seeds := redisSeeds(c)
	if len(seeds) == 0 {
		return nil, fmt.Errorf("Redis 주소를 입력하세요")
	}
	var probe *redis.Client
	var last error
	for _, s := range seeds {
		n := redisNode(c, s)
		if last = n.Ping(ctx).Err(); last == nil {
			probe = n
			break
		}
		n.Close()
	}
	if probe == nil {
		return nil, fmt.Errorf("Redis 연결 실패: %w", last)
	}
	info, e := probe.Info(ctx, "cluster").Result()
	if e != nil || !strings.Contains(info, "cluster_enabled:1") {
		// Standalone (or INFO not permitted): the node that answered.
		return &redisConn{UniversalClient: probe, Info: "Redis 단일 노드 " + probe.Options().Addr}, nil
	}
	probe.Close()
	if c.RedisDB != 0 {
		return nil, fmt.Errorf("Redis Cluster는 DB 0만 사용합니다 (DB 번호 %d)", c.RedisDB)
	}
	hosts, e := clusterHostMap(ctx, c, seeds)
	if e != nil {
		return nil, fmt.Errorf("Redis Cluster 노드 확인 실패: %w", e)
	}
	d := &net.Dialer{Timeout: 8 * time.Second}
	cl := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs: seeds, Username: c.RedisUser, Password: c.RedisPassword,
		DialTimeout: 8 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second,
		Dialer: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if h, p, e := net.SplitHostPort(addr); e == nil {
				if public, ok := hosts[h]; ok {
					addr = net.JoinHostPort(public, p)
				}
			}
			return d.DialContext(ctx, network, addr)
		},
	})
	masters := []string{}
	var mu sync.Mutex
	e = cl.ForEachMaster(ctx, func(ctx context.Context, n *redis.Client) error {
		if e := n.Ping(ctx).Err(); e != nil {
			return fmt.Errorf("%s: %w", n.Options().Addr, e)
		}
		mu.Lock()
		masters = append(masters, n.Options().Addr)
		mu.Unlock()
		return nil
	})
	if e != nil {
		cl.Close()
		return nil, fmt.Errorf("Redis Cluster 마스터에 연결할 수 없습니다 — 클러스터 서버 주소를 모두 쉼표로 입력하세요: %w", e)
	}
	sort.Strings(masters)
	mapped := []string{}
	for private, public := range hosts {
		mapped = append(mapped, private+"→"+public)
	}
	sort.Strings(mapped)
	return &redisConn{UniversalClient: cl, cluster: cl, Info: fmt.Sprintf("Redis Cluster 마스터 %d개 (%s) · 주소 매핑 %s", len(masters), strings.Join(masters, ", "), strings.Join(mapped, ", "))}, nil
}

// scanKeys returns every key matching pattern; on a cluster, from every master.
func (r *redisConn) scanKeys(ctx context.Context, pattern string, limit int) ([]string, error) {
	seen := map[string]bool{}
	var mu sync.Mutex
	scan := func(ctx context.Context, n redis.Cmdable) error {
		var cursor uint64
		for {
			keys, next, e := n.Scan(ctx, cursor, pattern, 1000).Result()
			if e != nil {
				return e
			}
			mu.Lock()
			for _, k := range keys {
				seen[k] = true
			}
			over := len(seen) > limit
			mu.Unlock()
			if over {
				return fmt.Errorf("stat 키가 %d개를 초과합니다", limit)
			}
			if cursor = next; cursor == 0 {
				return nil
			}
		}
	}
	var e error
	if r.cluster != nil {
		e = r.cluster.ForEachMaster(ctx, func(ctx context.Context, n *redis.Client) error { return scan(ctx, n) })
	} else {
		e = scan(ctx, r.UniversalClient)
	}
	if e != nil {
		return nil, e
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}
