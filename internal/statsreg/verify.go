package statsreg

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// The collector keeps the latest value of every checkpoint in Redis as
// "checkpoint:<id>" = "id║display║timestampMs║raw". A checkpoint is collected
// when that timestamp keeps moving, which the registration itself (DB rows +
// Kafka ACK) cannot show: on 2026-09-21 everything was saved and notified while
// the collector's MK119 worker had already died.
//
// liz.checkvalue.topic was tried first and dropped: its format differs per
// environment (JSON on the test server, "ts, id, raw, display" CSV on AWS) and
// the AWS topic carries ~1.4M messages/s, far more than a PC can tail.
const checkpointValueKey = "checkpoint:"

// A checkpoint counts as collected after this many distinct timestamps, so a
// value left over from before (or written once before the worker died) is not
// taken for a working collection.
const collectNeed = 2

const pollEvery = 2 * time.Second

// Long enough for collectNeed rounds of the slowest selected interval.
func verifyWindow(points []Point, override time.Duration) time.Duration {
	if override > 0 {
		return override
	}
	longest := 10000
	for _, p := range points {
		if p.Interval > longest {
			longest = p.Interval
		}
	}
	w := time.Duration(collectNeed*longest)*time.Millisecond + 20*time.Second
	if w < 45*time.Second {
		w = 45 * time.Second
	}
	if w > 150*time.Second {
		w = 150 * time.Second
	}
	return w
}

// valueTime parses the timestamp field of a checkpoint value.
func valueTime(v string) (int64, bool) {
	parts := strings.Split(v, "║")
	if len(parts) < 3 {
		return 0, false
	}
	ts, e := strconv.ParseInt(strings.TrimSpace(parts[2]), 10, 64)
	return ts, e == nil && ts > 0
}

type collectWatch struct {
	r    *redisConn
	ids  []int64
	seen map[int64]map[int64]bool // checkpoint -> distinct value timestamps
	err  error                    // why Redis could not be watched
}

func (s *Service) watchCollection(ctx context.Context, c Config, ids []int64) *collectWatch {
	w := &collectWatch{ids: ids, seen: map[int64]map[int64]bool{}}
	for _, id := range ids {
		w.seen[id] = map[int64]bool{}
	}
	if w.r, w.err = openRedis(ctx, c); w.err != nil {
		w.err = fmt.Errorf("Redis 연결 실패: %w", w.err)
		return w
	}
	w.err = w.poll(ctx) // baseline: timestamps already there count once
	return w
}

func (w *collectWatch) poll(ctx context.Context) error {
	pipe := w.r.Pipeline()
	cmds := make([]*redis.StringCmd, len(w.ids))
	for i, id := range w.ids {
		cmds[i] = pipe.Get(ctx, checkpointValueKey+strconv.FormatInt(id, 10))
	}
	if _, e := pipe.Exec(ctx); e != nil && e != redis.Nil {
		// Per-key redis.Nil (not collected yet) is expected; anything else is not.
		for _, cmd := range cmds {
			if err := cmd.Err(); err != nil && err != redis.Nil {
				return err
			}
		}
	}
	for i, cmd := range cmds {
		if v, e := cmd.Result(); e == nil {
			w.observe(w.ids[i], v)
		}
	}
	return nil
}

func (w *collectWatch) observe(id int64, value string) {
	if ts, ok := valueTime(value); ok && w.seen[id] != nil {
		w.seen[id][ts] = true
	}
}

func (w *collectWatch) snapshot() (map[int64]int, int) {
	out := make(map[int64]int, len(w.seen))
	done := 0
	for id, set := range w.seen {
		out[id] = len(set)
		if len(set) >= collectNeed {
			done++
		}
	}
	return out, done
}

// wait polls until every checkpoint reached collectNeed timestamps or the window ends.
func (w *collectWatch) wait(ctx context.Context, window time.Duration, tick func(done, total int, left time.Duration)) map[int64]int {
	if w.err != nil {
		return nil
	}
	deadline := time.Now().Add(window)
	t := time.NewTicker(pollEvery)
	defer t.Stop()
	for {
		seen, done := w.snapshot()
		left := time.Until(deadline)
		if done == len(w.ids) || left <= 0 {
			return seen
		}
		tick(done, len(w.ids), left)
		select {
		case <-ctx.Done():
			seen, _ = w.snapshot()
			return seen
		case <-t.C:
		}
		if e := w.poll(ctx); e != nil && ctx.Err() == nil {
			w.err = e
			seen, _ = w.snapshot()
			return seen
		}
	}
}

func (w *collectWatch) close() {
	if w.r != nil {
		w.r.Close()
	}
}

func summarizeCollection(r *Result, seen map[int64]int, watchErr error) {
	total := len(r.CheckpointIDs)
	saved := fmt.Sprintf("장비 %d개, 체크포인트 %d개 등록", len(r.DeviceIDs), total)
	r.Missing = []int64{}
	if watchErr != nil && seen == nil {
		r.Verified, r.Collected = false, 0
		r.Message = saved + " 완료. 수집 확인은 못 했습니다(" + watchErr.Error() + ") — 「수집 다시 확인」 또는 웹에서 수집 상태를 확인하세요."
		return
	}
	partial := 0
	r.Collected = 0
	for _, id := range r.CheckpointIDs {
		switch n := seen[id]; {
		case n == 0:
			r.Missing = append(r.Missing, id)
		case n < collectNeed:
			partial++
			r.Collected++
		default:
			r.Collected++
		}
	}
	sort.Slice(r.Missing, func(i, j int) bool { return r.Missing[i] < r.Missing[j] })
	r.Verified = len(r.Missing) == 0 && partial == 0
	switch {
	case r.Verified:
		r.Message = saved + " 완료. 체크포인트 전부 Collector가 값을 갱신하고 있습니다."
	case len(r.Missing) == 0:
		r.Message = fmt.Sprintf("%s 완료. 모든 체크포인트에 값이 있지만 %d개는 확인 시간 안에 갱신이 안 됐습니다(원본 키가 멈췄거나 주기가 김) — 「수집 다시 확인」으로 한 번 더 보세요.", saved, partial)
	default:
		r.Message = fmt.Sprintf("%s 완료, 하지만 수집 확인 %d/%d개. 값이 없는 체크포인트: %s. 원본 Redis 키가 없거나 Collector가 수집을 멈췄을 수 있습니다 — Active Collector의 journal(journalctl -u lizcollector)에서 LizSystemStatusHandlerWorker 오류를 확인하세요. 재등록하지 마세요(중복 생성됩니다).",
			saved, r.Collected, total, idList(r.Missing, 20))
	}
	if watchErr != nil {
		r.Message += " (확인 도중 Redis 오류: " + watchErr.Error() + ")"
	}
}

func idList(ids []int64, max int) string {
	parts := []string{}
	for i, id := range ids {
		if i == max {
			parts = append(parts, fmt.Sprintf("외 %d개", len(ids)-max))
			break
		}
		parts = append(parts, fmt.Sprint(id))
	}
	return strings.Join(parts, ", ")
}
