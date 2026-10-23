package statsreg

import (
	"encoding/base64"
	"strings"
	"testing"
)

// Fixtures are trimmed copies of trunk (2026-10-23): three servers, Redis on
// :5000 managed by Sentinel (master server_2), Kafka with INTERNAL (server_N)
// and EXTERNAL (IP) listeners on a different port per broker, MariaDB behind
// server_vip. server_3 has neither lizcollector nor lizadmin.

const fxHosts = `127.0.0.1   localhost localhost.localdomain
192.0.2.24	server_1
192.0.2.25	server_2
192.0.2.26	server_3
192.0.2.27	server_vip
192.0.2.24	liz_server_1
127.0.0.1    example.liz.local demo_kafka_1
192.0.2.94	server01
`

func fxRedis(replicaOf string) string {
	s := `bind 0.0.0.0
protected-mode no
port 5000
requirepass "s3cret?"
masterauth "s3cret?"
user default on sanitize-payload #57e4ffe4b875f551aaf6975a1b85006f2acbc8f7d377885a396c746c4da0f0f5 ~* &* +@all
`
	if replicaOf != "" {
		s += "replicaof " + replicaOf + " 5000\n"
	}
	return s
}

const fxSentinel = `port 26379
sentinel monitor mymaster server_2 5000 2
sentinel auth-pass mymaster s3cret?
sentinel known-replica mymaster server_1 5000
`

func fxKafka(id, port, ext, name, ip string) string {
	return `broker.id=` + id + `
listeners=INTERNAL://0.0.0.0:` + port + `,EXTERNAL://0.0.0.0:` + ext + `
listener.security.protocol.map=INTERNAL:PLAINTEXT,EXTERNAL:PLAINTEXT
advertised.listeners=INTERNAL://` + name + `:` + port + `,EXTERNAL://` + ip + `:` + ext + `
inter.broker.listener.name=INTERNAL
`
}

const fxCollector = `
repository.multiple.host=server_1:3306,server_vip:3306
repository.host=server_vip
repository.port=3306
repository.user.id=root
repository.user.password=dbpass?
repository.schema=liz
`

const fxAdmin = `server:
  port: 8443
  ssl:
    key-store-password: storepw
spring:
  profiles: local
  datasource:
    platform: mariadb
    driver-class-name: org.mariadb.jdbc.Driver
    url: jdbc:mariadb://server_vip:3306/liz
    username: root
    password: dbpass?
    hikari:
      maximumPoolSize: 50
  password:
    expiration:
      period: 30
`

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// dumpOut renders what DiscoverScript prints, so ParseDump is exercised too.
func dumpOut(host string, files map[string][2]string, procs ...string) string {
	var b strings.Builder
	b.WriteString("@@HOST " + host + "\n@@UID 0\n@@LIZ /OnionSoftware/liz\n")
	for _, p := range procs {
		b.WriteString("@@PROC " + p + "\n")
	}
	for kind, f := range files {
		switch f[1] {
		case "<missing>":
			b.WriteString("@@NOFILE " + kind + " " + f[0] + " missing\n")
		case "<denied>":
			b.WriteString("@@NOFILE " + kind + " " + f[0] + " denied\n")
		default:
			b.WriteString("@@FILE " + kind + " " + f[0] + " " + b64(f[1]) + "\n")
		}
	}
	return b.String()
}

func trunkDumps() []ServerDump {
	type srv struct{ name, addr, out string }
	in := []srv{
		{"t-1", "192.0.2.24", dumpOut("server_1", map[string][2]string{
			"hosts":     {"/etc/hosts", fxHosts},
			"redis":     {"/OnionSoftware/liz/redis/redis.conf", fxRedis("server_2")},
			"sentinel":  {"/OnionSoftware/liz/redis/sentinel.conf", fxSentinel},
			"kafka":     {"/usr/local/liz/kafka/config/server.properties", fxKafka("1", "9092", "19092", "server_1", "192.0.2.24")},
			"collector": {"/OnionSoftware/liz/lizcollector/config/collector.properties", fxCollector},
			"admin":     {"/OnionSoftware/liz/lizadmin/config/application.yml", fxAdmin},
		}, "redis 0.0.0.0:5000", "kafka /usr/local/liz/kafka/config/server.properties")},
		{"t-2", "192.0.2.25", dumpOut("server_2", map[string][2]string{
			"hosts":     {"/etc/hosts", fxHosts},
			"redis":     {"/OnionSoftware/liz/redis/redis.conf", fxRedis("")},
			"kafka":     {"/usr/local/liz/kafka/config/server.properties", fxKafka("2", "9093", "19093", "server_2", "192.0.2.25")},
			"collector": {"/OnionSoftware/liz/lizcollector/config/collector.properties", fxCollector},
			"admin":     {"/OnionSoftware/liz/lizadmin/config/application.yml", fxAdmin},
		}, "redis 0.0.0.0:5000", "kafka /usr/local/liz/kafka/config/server.properties")},
		{"t-3", "192.0.2.26", dumpOut("server_3", map[string][2]string{
			"hosts":     {"/etc/hosts", fxHosts},
			"redis":     {"/OnionSoftware/liz/redis/redis.conf", fxRedis("server_2")},
			"kafka":     {"/usr/local/liz/kafka/config/server.properties", fxKafka("3", "9094", "19094", "server_3", "192.0.2.26")},
			"collector": {"/OnionSoftware/liz/lizcollector/config/collector.properties", "<missing>"},
			"admin":     {"/OnionSoftware/liz/lizadmin/config/application.yml", "<missing>"},
		}, "redis 0.0.0.0:5000", "kafka /usr/local/liz/kafka/config/server.properties")},
	}
	var out []ServerDump
	for _, s := range in {
		d := ServerDump{Name: s.name, Addr: s.addr}
		ParseDump(&d, s.out)
		out = append(out, d)
	}
	return out
}

func probeAll(ok bool, except ...string) func([]string) map[string]bool {
	return func(addrs []string) map[string]bool {
		m := map[string]bool{}
		for _, a := range addrs {
			m[a] = ok
			for _, e := range except {
				if strings.HasSuffix(a, e) {
					m[a] = !ok
				}
			}
		}
		return m
	}
}

func fieldMap(r DiscoverResult) map[string]DiscoverField {
	m := map[string]DiscoverField{}
	for _, f := range r.Fields {
		m[f.Key] = f
	}
	return m
}

func TestDiscoverTrunk(t *testing.T) {
	r := BuildDiscovery(trunkDumps(), DiscoverEnv{Probe: probeAll(true)})
	f := fieldMap(r)
	want := map[string]string{
		"dbHost": "192.0.2.27", "dbPort": "3306", "dbUser": "root", "dbPassword": "dbpass?",
		"redisHost": "192.0.2.24,192.0.2.25,192.0.2.26", "redisPort": "5000", "redisPassword": "s3cret?",
	}
	for k, v := range want {
		if f[k].Value != v {
			t.Errorf("%s = %q, want %q (source %q)", k, f[k].Value, v, f[k].Source)
		}
	}
	if !f["dbPassword"].Secret || !f["redisPassword"].Secret || f["dbHost"].Secret {
		t.Errorf("secret flags wrong: %+v", r.Fields)
	}
	if s := f["dbHost"].Source; !strings.Contains(s, "collector.properties repository.host=server_vip") || !strings.Contains(s, "/etc/hosts server_vip") {
		t.Errorf("dbHost source = %q", s)
	}
	if s := f["redisPassword"].Source; s != "server_1:/OnionSoftware/liz/redis/redis.conf requirepass" {
		t.Errorf("redisPassword source = %q", s)
	}
	if !strings.Contains(f["redisHost"].Source, "설정상 현재 마스터: server_2") {
		t.Errorf("redisHost source = %q", f["redisHost"].Source)
	}
	// Both listeners reachable: EXTERNAL (client-facing) wins.
	if r.KafkaChoice < 0 || r.Kafka[r.KafkaChoice].Listener != "EXTERNAL" {
		t.Fatalf("choice = %d %+v", r.KafkaChoice, r.Kafka)
	}
	ext := r.Kafka[r.KafkaChoice]
	if len(ext.Rows) != 3 || ext.Rows[1].Hostname != "192.0.2.25" || ext.Rows[1].Port != 19093 || ext.Security != "PLAINTEXT" {
		t.Errorf("EXTERNAL rows = %+v", ext.Rows)
	}
	for _, o := range r.Kafka {
		if o.Listener == "INTERNAL" {
			if !o.InterBroker || o.Rows[2].Hostname != "server_3" || o.Rows[2].IP != "192.0.2.26" || o.Rows[2].Port != 9094 {
				t.Errorf("INTERNAL = %+v", o)
			}
		}
	}
	if len(r.Warnings) != 0 {
		t.Errorf("unexpected warnings: %q", r.Warnings)
	}
	for _, w := range append(r.Warnings, r.KafkaReason) {
		if strings.Contains(w, "s3cret") || strings.Contains(w, "dbpass") {
			t.Errorf("secret in text: %q", w)
		}
	}
	for _, f := range r.Fields {
		if strings.Contains(f.Source, "s3cret") || strings.Contains(f.Source, "dbpass") {
			t.Errorf("secret in source: %q", f.Source)
		}
	}
}

// EXTERNAL closed to this PC: INTERNAL is chosen, still with the advertised names.
func TestDiscoverKafkaExternalUnreachable(t *testing.T) {
	r := BuildDiscovery(trunkDumps(), DiscoverEnv{Probe: probeAll(true, ":19092", ":19093", ":19094")})
	o := r.Kafka[r.KafkaChoice]
	if o.Listener != "INTERNAL" || o.Reachable != 3 {
		t.Fatalf("choice = %+v", o)
	}
	if o.Rows[0].Hostname != "server_1" || o.Rows[0].IP != "192.0.2.24" {
		t.Errorf("rows = %+v", o.Rows)
	}
	if !strings.Contains(r.KafkaReason, "브로커 간 리스너") {
		t.Errorf("reason = %q", r.KafkaReason)
	}
}

// Nothing reachable (cluster behind a jump host): a choice is still made, and
// the user is told.
func TestDiscoverNothingReachable(t *testing.T) {
	r := BuildDiscovery(trunkDumps(), DiscoverEnv{Probe: probeAll(false)})
	if r.KafkaChoice < 0 || r.Kafka[r.KafkaChoice].Listener != "EXTERNAL" {
		t.Fatalf("choice = %d", r.KafkaChoice)
	}
	joined := strings.Join(r.Warnings, "\n")
	for _, w := range []string{"MariaDB 192.0.2.27:3306", "Redis: 이 PC에서 어느 서버에도", "Kafka EXTERNAL"} {
		if !strings.Contains(joined, w) {
			t.Errorf("missing warning %q in %q", w, joined)
		}
	}
}

func TestDiscoverVariants(t *testing.T) {
	// One server only: no collector, admin with an encrypted password and a
	// multi-host URL, Redis with only an ACL hash, Kafka with plain "listeners"
	// and an empty host, redis.conf readable but kafka denied on another server.
	admin := `spring:
  datasource:
    url: "jdbc:mariadb://db_1:3307,db_2:3307/liz?useSSL=false"
    username: liz
    password: ENC(abcdefABCDEF0123456789==)
`
	redisHashOnly := "port 6379\nuser default on #abc123 ~* +@all\n"
	kafka := "broker.id=7\nlisteners=PLAINTEXT://:9092\n"
	hosts := "10.0.0.5 db_1 node-a\n"
	d1 := ServerDump{Name: "a", Addr: "10.0.0.5"}
	ParseDump(&d1, dumpOut("node-a", map[string][2]string{
		"hosts":     {"/etc/hosts", hosts},
		"redis":     {"/usr/local/liz/redis/redis.conf", redisHashOnly},
		"kafka":     {"/usr/local/liz/kafka/config/server.properties", kafka},
		"collector": {"/usr/local/liz/lizcollector/config/collector.properties", "<missing>"},
		"admin":     {"/usr/local/liz/lizadmin/config/application.yml", admin},
	}, "redis *:6379", "kafka /usr/local/liz/kafka/config/server.properties"))
	d2 := ServerDump{Name: "b", Addr: "10.0.0.6"}
	ParseDump(&d2, dumpOut("node-b", map[string][2]string{
		"kafka": {"/usr/local/liz/kafka/config/server.properties", "<denied>"},
	}))
	d3 := ServerDump{Name: "c", Addr: "10.0.0.7", Err: "ssh: handshake failed"}

	r := BuildDiscovery([]ServerDump{d1, d2, d3}, DiscoverEnv{Probe: probeAll(true)})
	f := fieldMap(r)
	if f["dbHost"].Value != "10.0.0.5" || f["dbPort"].Value != "3307" || f["dbUser"].Value != "liz" {
		t.Errorf("db fields = %+v", r.Fields)
	}
	if _, ok := f["dbPassword"]; ok {
		t.Errorf("encrypted password must not be proposed")
	}
	if f["redisHost"].Value != "10.0.0.5" || f["redisPort"].Value != "6379" {
		t.Errorf("redis fields = %+v", r.Fields)
	}
	if _, ok := f["redisPassword"]; ok {
		t.Errorf("hash must not be proposed as password")
	}
	if r.KafkaChoice != 0 || r.Kafka[0].Rows[0].Hostname != "node-a" || r.Kafka[0].Rows[0].IP != "10.0.0.5" || r.Kafka[0].Security != "PLAINTEXT" {
		t.Errorf("kafka = %+v", r.Kafka)
	}
	joined := strings.Join(r.Warnings, "\n")
	for _, w := range []string{"c: 접속하지 못했습니다", "node-b: /usr/local/liz/kafka/config/server.properties 를 읽을 권한이 없습니다",
		"application.yml 의 datasource password 는 암호화", "해시(user default", "MariaDB 비밀번호를 설정 파일에서 찾지 못했습니다"} {
		if !strings.Contains(joined, w) {
			t.Errorf("missing warning %q in\n%s", w, joined)
		}
	}
}

func TestDiscoverConflictsAndSentinelFallback(t *testing.T) {
	ds := trunkDumps()
	// server_2's collector points elsewhere with another password; no
	// requirepass anywhere, only the sentinel auth-pass.
	for i := range ds {
		for j := range ds[i].Files {
			f := &ds[i].Files[j]
			if f.Kind == "redis" {
				f.Text = "port 5000\n"
			}
			if i == 1 && f.Kind == "collector" {
				f.Text = strings.ReplaceAll(strings.ReplaceAll(f.Text, "server_vip", "server_2"), "dbpass?", "other")
			}
		}
	}
	r := BuildDiscovery(ds, DiscoverEnv{Probe: probeAll(true)})
	f := fieldMap(r)
	if f["redisPassword"].Value != "s3cret?" || !strings.Contains(f["redisPassword"].Source, "sentinel auth-pass") {
		t.Errorf("redisPassword = %+v", f["redisPassword"])
	}
	if f["dbHost"].Value != "192.0.2.27" {
		t.Errorf("dbHost = %+v", f["dbHost"])
	}
	joined := strings.Join(r.Warnings, "\n")
	if !strings.Contains(joined, "MariaDB 주소가 설정마다 다릅니다") || !strings.Contains(joined, "MariaDB 비밀번호가 설정마다 다릅니다 (2가지)") {
		t.Errorf("warnings = %s", joined)
	}
	if strings.Contains(joined, "other") || strings.Contains(joined, "dbpass") {
		t.Errorf("secret leaked: %s", joined)
	}
}

func TestDiscoverNothingInstalled(t *testing.T) {
	d := ServerDump{Name: "x", Addr: "10.1.1.1"}
	ParseDump(&d, "@@HOST x\n@@UID 1000\n@@LIZ \n@@FILE hosts /etc/hosts "+b64("10.1.1.1 x\n")+"\n")
	r := BuildDiscovery([]ServerDump{d}, DiscoverEnv{})
	if len(r.Fields) != 0 || r.KafkaChoice != -1 {
		t.Errorf("result = %+v", r)
	}
	if len(r.Warnings) < 4 {
		t.Errorf("warnings = %q", r.Warnings)
	}
}

func TestRedisArgsAndParsers(t *testing.T) {
	a := redisArgs(`requirepass "a b\"c" 'd\'e'`)
	if len(a) != 3 || a[1] != `a b"c` || a[2] != "d'e" {
		t.Errorf("redisArgs = %q", a)
	}
	if u, ok := parseJDBC("jdbc:mysql://h:3310/liz?x=1"); !ok || u.Host != "h" || u.Port != 3310 || u.DB != "liz" {
		t.Errorf("parseJDBC = %+v", u)
	}
	p := parseProperties("a = 1\nb:2\n# c=3\nd 4\ne=x\\\n  y\n")
	if p["a"] != "1" || p["b"] != "2" || p["d"] != "4" || p["e"] != "xy" || p["c"] != "" {
		t.Errorf("parseProperties = %q", p)
	}
	if !looksEncrypted("L3mF2sI4F4t4S69F6Y4Lux6yilpbHl6ZZ0kUZjYEkW2HEKb2o3a+6OY/i/kBC2jTgsItCA==") || looksEncrypted("plainPass12?") {
		t.Errorf("looksEncrypted")
	}
}
