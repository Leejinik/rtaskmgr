// Capacity check: the two field scripts from the AWS 2M load test, run on every
// server of a cluster at once.
//
// The scripts in fieldscripts/ are the same files handed to field engineers, embedded
// as they are so the thresholds (300만/s per partition, 94% main thread) live in one
// place. They read only: /proc, Redis INFO, Kafka offsets and topic metadata. The one
// thing that touches a running JVM is jstack, and that is off unless the operator
// turns it on: it stops the broker at a safepoint (three short ones per call — 2.4 ms
// at most on a 99-thread JDK 8 JVM in the 2026-10-15 test). The script also skips
// brokers started with -Xrs; the test found no harm there on JDK 8u504/11/17/21, but
// the option changes signal handling and jstack attaches by signal, so it stays out.
package monitor

import (
	_ "embed"
	"encoding/base64"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

//go:embed fieldscripts/kafka-thread-check.sh
var kafkaCheckScript string

//go:embed fieldscripts/redis-ceiling-check.sh
var redisCheckScript string

// CapacityOptions is what the operator chose for one run.
type CapacityOptions struct {
	Seconds int    `json:"seconds"`
	Topic   string `json:"topic"`
	Jstack  bool   `json:"jstack"`
	Kafka   bool   `json:"kafka"`
	Redis   bool   `json:"redis"`
}

type CapacityKafkaThread struct {
	Label   string  `json:"label"`
	Busy    int     `json:"busy"`
	Total   int     `json:"total"`
	Max     float64 `json:"max"`
	Avg     float64 `json:"avg"`
	Sum     float64 `json:"sum"`
	Verdict string  `json:"verdict"`
}

type CapacityKafkaPart struct {
	Part   string  `json:"part"`
	Rate   float64 `json:"rate"`
	Pct    float64 `json:"pct"`
	Leader string  `json:"leader"`
	ISR    string  `json:"isr"`
	ISROk  bool    `json:"isrOk"`
	// ISRKnown is false when the topic could not be described: the replica lists
	// are missing, so ISROk says nothing (the script used to call that healthy).
	ISRKnown bool   `json:"isrKnown"`
	Verdict  string `json:"verdict"`
}

type CapacityKafka struct {
	At         string                `json:"at"`
	PID        int                   `json:"pid"`
	NetThreads string                `json:"netThreads"`
	IOThreads  string                `json:"ioThreads"`
	Heap       string                `json:"heap"`
	Cores      int                   `json:"cores"`
	Mode       string                `json:"mode"` // full = jstack names, short = /proc comm
	JstackMs   int                   `json:"jstackMs"`
	Why        string                `json:"why"`
	Threads    []CapacityKafkaThread `json:"threads"`
	BrokerCPU  float64               `json:"brokerCpu"`
	GCPct      float64               `json:"gcPct"`
	HasGC      bool                  `json:"hasGc"`
	Parts      []CapacityKafkaPart   `json:"parts"`
	TopicTotal float64               `json:"topicTotal"`
	PartLimit  float64               `json:"partLimit"`
	Topic      string                `json:"topic"`
	Warnings   []string              `json:"warnings"`
	Err        string                `json:"err"`
}

type CapacityRedisSample struct {
	At      string  `json:"at"`
	Cmd     float64 `json:"cmd"`
	Set     float64 `json:"set"`
	MainPct float64 `json:"mainPct"`
	US      float64 `json:"us"`
	Cap     float64 `json:"cap"`
	FreePct float64 `json:"freePct"`
	Verdict string  `json:"verdict"`
}

type CapacityRedis struct {
	Port      int                   `json:"port"`
	Version   string                `json:"version"`
	Role      string                `json:"role"`
	Replicas  int                   `json:"replicas"`
	Backlog   string                `json:"backlog"`
	IOThreads string                `json:"ioThreads"`
	Samples   []CapacityRedisSample `json:"samples"`
	Warnings  []string              `json:"warnings"`
	Err       string                `json:"err"`
}

// CapacityResult is one host's answer. Kafka is nil when no broker runs there.
type CapacityResult struct {
	NowMs int64           `json:"nowMs"`
	Kafka *CapacityKafka  `json:"kafka"`
	Redis []CapacityRedis `json:"redis"`
}

// The check goes over in two steps. CapacityStageScript runs as the login user and
// writes both field scripts into a fresh private directory (mode 700, owned by that
// user); CapacityRunScript is the short script that runs them, elevated when the
// session can be. Sending the scripts inside the sudo command line instead would put
// ~30 KB of base64 into the server's secure log on every run.

var capDirRe = regexp.MustCompile(`^/tmp/\.rtaskmgr-cap\.[A-Za-z0-9]+$`)

// CapacityStageScript prints "DIR|<path>" once the scripts are in place.
func CapacityStageScript() string {
	var b strings.Builder
	b.WriteString(`umask 077
D=$(mktemp -d /tmp/.rtaskmgr-cap.XXXXXX) || { echo "ERR|임시 폴더를 만들 수 없습니다"; exit 1; }
`)
	fmt.Fprintf(&b, "printf %%s %s | base64 -d > \"$D/k.sh\" &&\n", shellQuote(base64.StdEncoding.EncodeToString([]byte(kafkaCheckScript))))
	fmt.Fprintf(&b, "printf %%s %s | base64 -d > \"$D/r.sh\" &&\n", shellQuote(base64.StdEncoding.EncodeToString([]byte(redisCheckScript))))
	b.WriteString(`echo "DIR|$D" || { rm -rf "$D"; echo "ERR|스크립트를 올리지 못했습니다"; }
`)
	return b.String()
}

// CapacityStagedDir picks the directory out of the stage step's output.
func CapacityStagedDir(out string) (string, error) {
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if d, ok := strings.CutPrefix(l, "DIR|"); ok && capDirRe.MatchString(d) {
			return d, nil
		}
		if e, ok := strings.CutPrefix(l, "ERR|"); ok {
			return "", fmt.Errorf("%s", e)
		}
	}
	return "", fmt.Errorf("스크립트를 올리지 못했습니다: %s", tailLines(out, 2))
}

// CapacityRunScript finds what runs on the host, starts the checks side by side and
// prints each one's output between markers, then removes the staged directory.
// Running them in parallel keeps the whole check at roughly one measuring window
// instead of one per Redis port plus Kafka.
//
// Redis ports come from the process title Redis sets for itself ("redis-server
// server_1:5000"); sentinels are skipped. The scripts are run with `bash file`, so a
// noexec /tmp does not matter.
func CapacityRunScript(dir string, o CapacityOptions) string {
	sec := o.Seconds
	if sec <= 0 {
		sec = 20
	}
	topic := o.Topic
	if topic == "" {
		topic = "liz.checkvalue.topic"
	}
	kopt := "--tsv"
	if o.Jstack {
		kopt += " --jstack"
	}
	var b strings.Builder
	b.WriteString("export LC_ALL=C\n")
	fmt.Fprintf(&b, "D=%s\n", shellQuote(dir))
	b.WriteString(`[ -f "$D/k.sh" ] && [ -f "$D/r.sh" ] || { echo "ERR|올린 스크립트가 없습니다"; exit 1; }
trap 'rm -rf "$D"' EXIT
echo "NOW|$(date +%s)"
`)
	if o.Kafka {
		// Judge by grep's output, not its status: a process that exits while /proc is
		// scanned makes grep return 2 even when the broker matched (seen on server_2,
		// where keepalived forks a check script every few seconds — 3 runs in 10 lost
		// their Kafka section that way).
		fmt.Fprintf(&b, `if [ -n "$(grep -alzx 'kafka\.Kafka' /proc/[0-9]*/cmdline 2>/dev/null | head -1)" ]; then
  bash "$D/k.sh" %s %d %s > "$D/k.out" 2>&1 &
else : > "$D/k.none"; fi
`, kopt, sec, shellQuote(topic))
	}
	if o.Redis {
		fmt.Fprintf(&b, `PORTS=""
for p in $(pgrep -x redis-server 2>/dev/null); do
  t=$(tr '\0' ' ' < /proc/$p/cmdline 2>/dev/null)
  case "$t" in *sentinel*) continue ;; esac
  port=$(printf '%%s' "$t" | grep -oE ':[0-9]+' | head -1 | tr -d :)
  [ -n "$port" ] || continue
  case " $PORTS " in *" $port "*) continue ;; esac
  PORTS="$PORTS $port"
  bash "$D/r.sh" --tsv "$port" %d 1 > "$D/r.$port.out" 2>&1 &
done
`, sec)
	}
	b.WriteString(`wait
if [ -f "$D/k.none" ]; then echo "K|NONE"; elif [ -f "$D/k.out" ]; then echo "K|BEGIN"; cat "$D/k.out"; echo "K|END"; fi
for f in "$D"/r.*.out; do
  [ -f "$f" ] || continue
  port=${f##*/r.}; port=${port%.out}
  echo "R|$port|BEGIN"; cat "$f"; echo "R|END"
done
`)
	return b.String()
}

// RunCapacityCheck runs the check on one host. Elevated when the session can be: the
// Kafka script needs the broker's /proc/<pid>/exe (root or the broker's account).
func (m *Manager) RunCapacityCheck(hostID string, o CapacityOptions) (CapacityResult, error) {
	s := m.get(hostID)
	if s == nil {
		return CapacityResult{}, fmt.Errorf("호스트가 연결되어 있지 않습니다")
	}
	staged, err := m.plainRun(s, CapacityStageScript())
	dir, derr := CapacityStagedDir(staged)
	if derr != nil {
		if err != nil {
			derr = fmt.Errorf("%v (%v)", derr, err)
		}
		return CapacityResult{}, fmt.Errorf("용량 점검 실패: %v", derr)
	}
	var out string
	if s.elevated {
		out, err = m.sudoRun(s, CapacityRunScript(dir, o))
	} else {
		out, err = m.plainRun(s, CapacityRunScript(dir, o))
	}
	// The run removes the directory itself; this covers a run that never started
	// (sudo refused, session dropped). The directory is the login user's, so no sudo.
	m.plainRun(s, "rm -rf "+shellQuote(dir))
	r := ParseCapacityOutput(out)
	if r.NowMs == 0 {
		if err == nil {
			err = fmt.Errorf("응답을 해석할 수 없습니다")
		}
		return r, fmt.Errorf("용량 점검 실패: %v: %s", err, tailLines(out, 2))
	}
	return r, nil
}

func ParseCapacityOutput(out string) CapacityResult {
	var r CapacityResult
	var k *CapacityKafka
	var rd *CapacityRedis
	var raw []string // lines of the current section that are not records (bash errors etc.)
	flushRaw := func(dst *string) {
		if *dst == "" && len(raw) > 0 {
			*dst = strings.Join(lastN(raw, 3), " | ")
		}
		raw = nil
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "NOW|"):
			sec, _ := strconv.ParseInt(strings.TrimSpace(line[4:]), 10, 64)
			r.NowMs = sec * 1000
			continue
		case line == "K|NONE":
			continue
		case line == "K|BEGIN":
			k = &CapacityKafka{}
			raw = nil
			continue
		case line == "K|END":
			if k != nil {
				if k.PID == 0 && k.Err == "" {
					flushRaw(&k.Err)
					if k.Err == "" {
						k.Err = "점검 스크립트가 결과를 내지 않았습니다"
					}
				}
				r.Kafka = k
			}
			k, raw = nil, nil
			continue
		case strings.HasPrefix(line, "R|") && strings.HasSuffix(line, "|BEGIN"):
			port, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(line, "R|"), "|BEGIN"))
			rd = &CapacityRedis{Port: port}
			raw = nil
			continue
		case line == "R|END":
			if rd != nil {
				if rd.Role == "" && rd.Err == "" {
					flushRaw(&rd.Err)
					if rd.Err == "" {
						rd.Err = "점검 스크립트가 결과를 내지 않았습니다"
					}
				}
				r.Redis = append(r.Redis, *rd)
			}
			rd, raw = nil, nil
			continue
		}
		f := strings.Split(line, "\t")
		switch {
		case k != nil:
			if !parseKafkaRec(k, f) && strings.TrimSpace(line) != "" {
				raw = append(raw, strings.TrimSpace(line))
			}
		case rd != nil:
			if !parseRedisRec(rd, f) && strings.TrimSpace(line) != "" {
				raw = append(raw, strings.TrimSpace(line))
			}
		}
	}
	return r
}

func parseKafkaRec(k *CapacityKafka, f []string) bool {
	if len(f) < 2 {
		return false
	}
	switch f[0] {
	case "H":
		if len(f) < 12 {
			return false
		}
		k.At = f[2]
		k.PID = capInt(f[4])
		k.NetThreads, k.IOThreads, k.Heap = f[5], f[6], f[7]
		k.Cores = capInt(f[8])
		k.Mode = f[9]
		k.JstackMs = capInt(f[10])
		k.Why = f[11]
	case "T":
		if len(f) < 8 {
			return false
		}
		k.Threads = append(k.Threads, CapacityKafkaThread{Label: f[1], Busy: capInt(f[2]), Total: capInt(f[3]),
			Max: capFloat(f[4]), Avg: capFloat(f[5]), Sum: capFloat(f[6]), Verdict: f[7]})
	case "C":
		k.BrokerCPU = capFloat(f[1])
	case "G":
		k.GCPct, k.HasGC = capFloat(f[1]), true
	case "P":
		if len(f) < 8 {
			return false
		}
		k.Parts = append(k.Parts, CapacityKafkaPart{Part: f[1], Rate: capFloat(f[2]), Pct: capFloat(f[3]),
			Leader: f[4], ISR: f[5], ISROk: f[6] == "1", ISRKnown: f[6] != "?", Verdict: f[7]})
	case "S":
		if len(f) < 4 {
			return false
		}
		k.TopicTotal, k.PartLimit, k.Topic = capFloat(f[1]), capFloat(f[2]), f[3]
	case "W":
		k.Warnings = append(k.Warnings, f[1])
	case "E":
		k.Err = f[1]
	default:
		return false
	}
	return true
}

func parseRedisRec(r *CapacityRedis, f []string) bool {
	if len(f) < 2 {
		return false
	}
	switch f[0] {
	case "H":
		if len(f) < 8 {
			return false
		}
		r.Version, r.Role = f[3], f[4]
		r.Replicas = capInt(f[5])
		r.Backlog, r.IOThreads = f[6], f[7]
	case "S":
		if len(f) < 9 {
			return false
		}
		r.Samples = append(r.Samples, CapacityRedisSample{At: f[1], Cmd: capFloat(f[2]), Set: capFloat(f[3]), MainPct: capFloat(f[4]),
			US: capFloat(f[5]), Cap: capFloat(f[6]), FreePct: capFloat(f[7]), Verdict: f[8]})
	case "W":
		r.Warnings = append(r.Warnings, f[1])
	case "E":
		r.Err = f[1]
	default:
		return false
	}
	return true
}

func capInt(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func capFloat(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return v
}

func lastN(s []string, n int) []string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}
