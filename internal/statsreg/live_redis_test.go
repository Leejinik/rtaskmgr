package statsreg

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"
)

func TestRedisSeeds(t *testing.T) {
	got := redisSeeds(Config{RedisHost: " 10.0.0.1, 10.0.0.2:5001 ;10.0.0.3\n", RedisPort: 5000})
	want := []string{"10.0.0.1:5000", "10.0.0.2:5001", "10.0.0.3:5000"}
	if len(got) != len(want) {
		t.Fatal(got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatal(got)
		}
	}
}

// Read-only: SCAN liz.stats.* on a single node or every cluster master.
// RTM_STATS_REDIS_HOSTS="a,b,c" RTM_STATS_REDIS_PASSWORD=... [RTM_STATS_REDIS_PORT=5000]
func TestLiveRedisScan(t *testing.T) {
	hosts := os.Getenv("RTM_STATS_REDIS_HOSTS")
	if hosts == "" {
		t.Skip("set RTM_STATS_REDIS_HOSTS for the read-only Redis scan")
	}
	port, _ := strconv.Atoi(os.Getenv("RTM_STATS_REDIS_PORT"))
	if port == 0 {
		port = 5000
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	start := time.Now()
	r, e := openRedis(ctx, Config{RedisHost: hosts, RedisPort: port, RedisPassword: os.Getenv("RTM_STATS_REDIS_PASSWORD")})
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	keys, e := r.scanKeys(ctx, "liz.stats.*", 10000)
	if e != nil {
		t.Fatal(e)
	}
	// Values must be readable through the same client (cluster: routed by slot).
	for _, k := range keys {
		if e = r.Get(ctx, k).Err(); e != nil && e.Error() != "redis: nil" {
			t.Fatalf("GET %s: %v", k, e)
		}
	}
	t.Logf("%s\n%d keys, all readable, %.1fs", r.Info, len(keys), time.Since(start).Seconds())
}
