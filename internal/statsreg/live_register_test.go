package statsreg

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

// WRITES to the target: a real two-phase registration that stays in the DB and
// sends the Kafka notifications. Needs RTM_STATS_REG_CONFIRM=yes.
// RTM_STATS_REG_HOST=<db/redis host> RTM_STATS_REG_PASSWORD=... RTM_STATS_REG_BROKERS=server_1:9092
// RTM_STATS_REG_KAFKA_HOSTS="server_1=192.0.2.1:9092"
func TestLiveTwoPhaseRegistration(t *testing.T) {
	host := os.Getenv("RTM_STATS_REG_HOST")
	if host == "" || os.Getenv("RTM_STATS_REG_CONFIRM") != "yes" {
		t.Skip("set RTM_STATS_REG_HOST and RTM_STATS_REG_CONFIRM=yes for a real registration")
	}
	pw := os.Getenv("RTM_STATS_REG_PASSWORD")
	c := Config{DBHost: host, DBPort: 3306, DBUser: "root", DBPassword: pw, RedisHost: host, RedisPort: 5000, RedisPassword: pw,
		Brokers: os.Getenv("RTM_STATS_REG_BROKERS"), KafkaSecurity: "PLAINTEXT", KafkaHosts: os.Getenv("RTM_STATS_REG_KAFKA_HOSTS")}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	start := time.Now()
	s := Service{Progress: func(m string) { t.Logf("[%5.1fs] %s", time.Since(start).Seconds(), m) }}
	last := ""
	s.Progress = func(m string) {
		if len(m) > 12 && len(last) > 12 && m[:12] == last[:12] { // one line per step
			return
		}
		last = m
		t.Logf("[%5.1fs] %s", time.Since(start).Seconds(), m)
	}
	p, e := s.Inspect(ctx, c)
	if e != nil {
		t.Fatal(e)
	}
	name := "RTM_2phase_" + time.Now().Format("0102_1504") + "_" + uuid.NewString()[:4]
	sel := Selection{PlanID: p.ID, GroupName: name, Servers: p.Servers, Points: p.Points, ExplorerID: p.Explorers[0].ID, ClusterID: p.Clusters[0].ID}
	for i := range sel.Servers {
		sel.Servers[i].Name = name + "_" + sel.Servers[i].Hostname
	}
	n := 0
	for i := range sel.Points {
		sel.Points[i].Selected = sel.Points[i].Host != ""
		if sel.Points[i].Selected {
			n++
		}
	}
	t.Logf("group %s: %d keys found, %d selected", name, len(p.Points), n)
	if e = s.Validate(ctx, sel); e != nil {
		t.Fatal(e)
	}
	res, e := s.Register(ctx, sel)
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("[%5.1fs] receipt=%s group=%d devices=%v interfaces=%v checkpoints=%d(%d..%d) stage=%s notified=%v verified=%v collected=%d missing=%v\n%s",
		time.Since(start).Seconds(), res.ID, res.GroupID, res.DeviceIDs, res.InterfaceIDs, len(res.CheckpointIDs), first(res.CheckpointIDs), lastID(res.CheckpointIDs), res.Stage, res.Notified, res.Verified, res.Collected, res.Missing, res.Message)
}

func first(v []int64) int64 {
	if len(v) == 0 {
		return 0
	}
	return v[0]
}
func lastID(v []int64) int64 {
	if len(v) == 0 {
		return 0
	}
	return v[len(v)-1]
}
