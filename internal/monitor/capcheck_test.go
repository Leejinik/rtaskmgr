package monitor

import (
	"os/exec"
	"strings"
	"testing"
)

// Real --tsv output captured from server_2 (Kafka 2.7.0, JDK 8) on 2026-10-15, plus a
// Redis section and a broken one.
const capSample = "NOW|1760492479\n" +
	"K|BEGIN\n" +
	"H\tserver_2\t2026-10-15 10:41:35\t10\t311332\t3\t8\t-Xmx1G\t8\tshort\t0\tjstack 을 쓰지 않았습니다(기본). 스레드를 이름으로 나누려면 --jstack\n" +
	"T\t네트워크+요청 처리 스레드\t0\t16\t0.3\t0.0\t0\t여유 있음\n" +
	"T\t복제 스레드\t0\t1\t0.2\t0.0\t0\t-\n" +
	"C\t2\n" +
	"G\t0.0\n" +
	"P\t0\t30\t0.0\t3\t3,2\t0\t파티션 늘림\n" +
	"S\t30\t3000000\tliz.checkvalue.topic\n" +
	"K|END\n" +
	"R|5000|BEGIN\n" +
	"H\tserver_2\t5000\t7.0.15\tmaster\t0\t0\t1\n" +
	"S\t10:41:40\t1200\t800\t3.5\t1.200\t32228\t96\t여유 있음\n" +
	"R|END\n" +
	"R|5001|BEGIN\n" +
	"bash: line 3: redis-cli: command not found\n" +
	"R|END\n"

func TestParseCapOutput(t *testing.T) {
	r := ParseCapacityOutput(capSample)
	if r.NowMs != 1760492479000 {
		t.Fatalf("now %d", r.NowMs)
	}
	k := r.Kafka
	if k == nil || k.PID != 311332 || k.Mode != "short" || k.Cores != 8 || k.Heap != "-Xmx1G" {
		t.Fatalf("kafka head %+v", k)
	}
	if len(k.Threads) != 2 || k.Threads[0].Total != 16 || !k.HasGC || k.BrokerCPU != 2 {
		t.Fatalf("kafka threads %+v", k)
	}
	if len(k.Parts) != 1 || k.Parts[0].ISROk || k.Parts[0].Verdict != "파티션 늘림" || k.PartLimit != 3e6 {
		t.Fatalf("kafka parts %+v", k.Parts)
	}
	if len(r.Redis) != 2 {
		t.Fatalf("redis %+v", r.Redis)
	}
	if r.Redis[0].Role != "master" || r.Redis[0].Samples[0].MainPct != 3.5 {
		t.Fatalf("redis0 %+v", r.Redis[0])
	}
	if !strings.Contains(r.Redis[1].Err, "command not found") {
		t.Fatalf("redis1 should carry the raw error: %+v", r.Redis[1])
	}
}

func TestParseCapKafkaError(t *testing.T) {
	r := ParseCapacityOutput("NOW|1\nK|BEGIN\nE\tKafka 브로커 프로세스를 찾지 못했습니다.\nK|END\n")
	if r.Kafka == nil || r.Kafka.Err != "Kafka 브로커 프로세스를 찾지 못했습니다." {
		t.Fatalf("%+v", r.Kafka)
	}
}

// The wrapper and both embedded scripts must at least parse as bash, with and
// without jstack.
func TestCapScriptsParse(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	for name, src := range map[string]string{
		"kafka":   kafkaCheckScript,
		"redis":   redisCheckScript,
		"stage":   CapacityStageScript(),
		"wrapper": CapacityRunScript("/tmp/.rtaskmgr-cap.abc123", CapacityOptions{Seconds: 5, Kafka: true, Redis: true}),
		"jstack":  CapacityRunScript("/tmp/.rtaskmgr-cap.abc123", CapacityOptions{Seconds: 5, Kafka: true, Redis: true, Jstack: true, Topic: "a'b"}),
	} {
		if strings.Contains(src, "\r") {
			t.Errorf("%s: CR in script", name)
		}
		cmd := exec.Command("bash", "-n")
		cmd.Stdin = strings.NewReader(src)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s: %v %s", name, err, out)
		}
	}
}

// jstack must never be the default, and the script must never use the forced mode.
func TestCapJstackOptIn(t *testing.T) {
	if strings.Contains(CapacityRunScript("/tmp/.rtaskmgr-cap.x", CapacityOptions{Kafka: true}), "--jstack") {
		t.Fatal("jstack passed without being asked for")
	}
	if !strings.Contains(CapacityRunScript("/tmp/.rtaskmgr-cap.x", CapacityOptions{Kafka: true, Jstack: true}), "--jstack") {
		t.Fatal("jstack option dropped")
	}
	for _, l := range strings.Split(kafkaCheckScript, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "#") {
			continue
		}
		if strings.Contains(l, "jstack\" -F") || strings.Contains(l, "jstack -F") {
			t.Fatal("forced jstack in script")
		}
	}
}

// Only a directory the stage step made may be run from — never a path the remote
// side could steer elsewhere.
func TestCapacityStagedDir(t *testing.T) {
	if d, err := CapacityStagedDir("noise\nDIR|/tmp/.rtaskmgr-cap.Ab12cD\n"); err != nil || d != "/tmp/.rtaskmgr-cap.Ab12cD" {
		t.Fatal(d, err)
	}
	for _, bad := range []string{"DIR|/tmp/../etc", "DIR|/tmp/.rtaskmgr-cap.a b", "DIR|/home/x", "ERR|임시 폴더를 만들 수 없습니다", ""} {
		if _, err := CapacityStagedDir(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	// The secure log must not get the scripts: the elevated step is short.
	if n := len(CapacityRunScript("/tmp/.rtaskmgr-cap.x", CapacityOptions{Kafka: true, Redis: true, Jstack: true})); n > 2000 {
		t.Fatalf("run script is %d bytes", n)
	}
}

// awk print rounds to 6 significant digits; the Redis snapshot must keep the
// main-thread CPU to the microsecond (it was cut to whole seconds on a long-running
// Redis and the 20 s main%% came out as 0 or 5 only).
func TestRedisSnapKeepsCPUPrecision(t *testing.T) {
	if !strings.Contains(redisCheckScript, `printf "%s %.0f %.0f %.6f %d\n", t, tc, sc, u+s, has`) {
		t.Fatal("redis snap lost its printf")
	}
}

// grep exits 2 when a /proc entry vanishes mid-scan, even after a match; the broker
// check must go by grep's output or a busy host loses its Kafka section at random.
func TestCapacityKafkaDetectByOutput(t *testing.T) {
	s := CapacityRunScript("/tmp/.rtaskmgr-cap.x", CapacityOptions{Kafka: true})
	if strings.Contains(s, "if grep -alzx") || !strings.Contains(s, `if [ -n "$(grep -alzx`) {
		t.Fatal("kafka detection relies on grep's exit status")
	}
}
