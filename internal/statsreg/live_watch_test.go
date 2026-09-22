package statsreg

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Read-only: watches checkpoint:<id> timestamps in Redis (single node or cluster).
// RTM_STATS_WATCH_REDIS="a,b,c" RTM_STATS_WATCH_PASSWORD=... RTM_STATS_WATCH_IDS="first-last"
// RTM_STATS_WATCH_SECONDS=140 (default 45)
func TestLiveCollectionWatch(t *testing.T) {
	hosts := os.Getenv("RTM_STATS_WATCH_REDIS")
	if hosts == "" {
		t.Skip("set RTM_STATS_WATCH_REDIS for the read-only collection watch")
	}
	c := Config{RedisHost: hosts, RedisPort: 5000, RedisPassword: os.Getenv("RTM_STATS_WATCH_PASSWORD")}
	r := strings.SplitN(os.Getenv("RTM_STATS_WATCH_IDS"), "-", 2)
	first, _ := strconv.ParseInt(r[0], 10, 64)
	last, _ := strconv.ParseInt(r[1], 10, 64)
	res := Result{CheckpointIDs: []int64{}}
	for id := first; id <= last; id++ {
		res.CheckpointIDs = append(res.CheckpointIDs, id)
	}
	window := 45 * time.Second
	if v, e := strconv.Atoi(os.Getenv("RTM_STATS_WATCH_SECONDS")); e == nil && v > 0 {
		window = time.Duration(v) * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), window+time.Minute)
	defer cancel()
	var s Service
	w := s.watchCollection(ctx, c, res.CheckpointIDs)
	defer w.close()
	start := time.Now()
	seen := w.wait(ctx, window, func(done, total int, left time.Duration) {})
	summarizeCollection(&res, seen, w.err)
	t.Logf("%.0fs verified=%v collected=%d/%d missing=%d\n%s", time.Since(start).Seconds(), res.Verified, res.Collected, len(res.CheckpointIDs), len(res.Missing), res.Message)
}
