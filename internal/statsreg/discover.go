package statsreg

import (
	"encoding/base64"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
)

// "서버에서 불러오기": the MariaDB / Redis / Kafka connection is read from the
// cluster's own configuration over SSH instead of being typed by hand (round-1
// testers spent ~13 minutes finding these values). Everything here is pure —
// the SSH side lives in the app and hands over one ServerDump per server — so
// the choices can be tested against fixture configs.
//
// Read-only on the servers: DiscoverScript only reads files and the process list.

// DiscoverScript runs on every server (elevated when the login allows it, since
// redis.conf and the module configs may be root-only). Each file is sent without
// its comment lines, base64 encoded, so its text cannot break the line protocol.
const DiscoverScript = `export LC_ALL=C
f() { if [ -r "$2" ]; then printf '@@FILE %s %s ' "$1" "$2"; grep -v '^[[:space:]]*#' "$2" 2>/dev/null | head -c 262144 | base64 | tr -d '\n'; echo; elif [ -e "$2" ]; then echo "@@NOFILE $1 $2 denied"; else echo "@@NOFILE $1 $2 missing"; fi; }
echo "@@HOST $(hostname 2>/dev/null)"
echo "@@UID $(id -u)"
L=; for d in /OnionSoftware/liz /usr/local/liz; do [ -d "$d" ] && { L=$d; break; }; done
echo "@@LIZ $L"
f hosts /etc/hosts
ps -eo args= 2>/dev/null | awk '$1 ~ /redis-server$/ {print "@@PROC redis " $2} /kafka\.Kafka/ {print "@@PROC kafka " $NF}'
[ -n "$L" ] || exit 0
for c in "$L"/redis/*.conf; do [ -e "$c" ] || continue; case "$c" in *sentinel*) f sentinel "$c";; *) f redis "$c";; esac; done
K=$(ps -eo args= 2>/dev/null | awk '/kafka\.Kafka/ && $NF ~ /\.properties$/ {print $NF; exit}')
[ -n "$K" ] || K="$L/kafka/config/server.properties"
f kafka "$K"
f collector "$L/lizcollector/config/collector.properties"
f admin "$L/lizadmin/config/application.yml"
`

// DumpFile is one config file read by DiscoverScript. State is "ok", "missing"
// or "denied" (exists but unreadable — no sudo).
type DumpFile struct {
	Kind  string
	Path  string
	State string
	Text  string
}

// ServerDump is what one server answered.
type ServerDump struct {
	Name     string // the host's name in rtaskmgr
	Addr     string // SSH address — reachable from this PC (unless via a jump host)
	Hostname string // `hostname` on the server
	Base     string // MK119 install directory found
	Root     bool   // the script ran as root
	Err      string // connection / run failure; nothing else is set
	Files    []DumpFile
	Redis    []string // redis-server listen addresses from the process list ("0.0.0.0:5000")
	Kafka    []string // config path of each running broker
}

// ParseDump reads DiscoverScript's output.
func ParseDump(d *ServerDump, out string) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		switch f[0] {
		case "@@HOST":
			if len(f) > 1 {
				d.Hostname = f[1]
			}
		case "@@UID":
			d.Root = len(f) > 1 && f[1] == "0"
		case "@@LIZ":
			if len(f) > 1 {
				d.Base = f[1]
			}
		case "@@PROC":
			if len(f) < 3 {
				continue
			}
			if f[1] == "redis" {
				d.Redis = append(d.Redis, f[2])
			} else if f[1] == "kafka" {
				d.Kafka = append(d.Kafka, f[2])
			}
		case "@@FILE":
			if len(f) < 3 {
				continue
			}
			text := ""
			if len(f) > 3 {
				b, err := base64.StdEncoding.DecodeString(f[3])
				if err != nil {
					d.Files = append(d.Files, DumpFile{Kind: f[1], Path: f[2], State: "denied"})
					continue
				}
				text = string(b)
			}
			d.Files = append(d.Files, DumpFile{Kind: f[1], Path: f[2], State: "ok", Text: text})
		case "@@NOFILE":
			if len(f) < 4 {
				continue
			}
			d.Files = append(d.Files, DumpFile{Kind: f[1], Path: f[2], State: f[3]})
		}
	}
}

func (d ServerDump) files(kind string) []DumpFile {
	var out []DumpFile
	for _, f := range d.Files {
		if f.Kind == kind {
			out = append(out, f)
		}
	}
	return out
}

func (d ServerDump) label() string {
	if d.Hostname != "" {
		return d.Hostname
	}
	if d.Name != "" {
		return d.Name
	}
	return d.Addr
}

// ---- result ----

// DiscoverField is one proposed value. Value is a string for every field; the
// UI converts the numeric ones. Secret values are never shown, only filled.
type DiscoverField struct {
	Key    string `json:"key"` // statsreg.Config json key
	Label  string `json:"label"`
	Value  string `json:"value"`
	Secret bool   `json:"secret"`
	Source string `json:"source"`
}

type DiscoverKafkaRow struct {
	Hostname  string `json:"hostname"`
	IP        string `json:"ip"`
	Port      int    `json:"port"`
	Source    string `json:"source"`
	Reachable bool   `json:"reachable"`
}

// DiscoverKafkaOption is one listener (INTERNAL, EXTERNAL …) the brokers offer.
// The registration needs the host name exactly as that listener advertises it,
// bound to an address this PC can reach.
type DiscoverKafkaOption struct {
	Listener    string             `json:"listener"`
	Security    string             `json:"security"`
	InterBroker bool               `json:"interBroker"`
	Rows        []DiscoverKafkaRow `json:"rows"`
	Reachable   int                `json:"reachable"`
	Brokers     int                `json:"brokers"` // brokers found in total
	Note        string             `json:"note"`
}

type DiscoverServer struct {
	Name     string `json:"name"`
	Addr     string `json:"addr"`
	Hostname string `json:"hostname"`
	Via      string `json:"via"`
	Elevated bool   `json:"elevated"`
	Error    string `json:"error"`
}

type DiscoverResult struct {
	Fields      []DiscoverField       `json:"fields"`
	Kafka       []DiscoverKafkaOption `json:"kafka"`
	KafkaChoice int                   `json:"kafkaChoice"` // index into Kafka, -1 = none
	KafkaReason string                `json:"kafkaReason"`
	Warnings    []string              `json:"warnings"`
	Servers     []DiscoverServer      `json:"servers"`
	// Hosts: every server's /etc/hosts lines, for placing keys whose server part
	// is an alias such as {liz_log_db_1} (placement.go).
	Hosts []DiscoverHostsEntry `json:"hosts"`
}

// DiscoverHostsEntry is one name of one server's /etc/hosts.
type DiscoverHostsEntry struct {
	Server   string `json:"server"`   // which server's file
	Hostname string `json:"hostname"` // that server's `hostname`
	Name     string `json:"name"`     // lower case
	IP       string `json:"ip"`
}

// DiscoverEnv is what the pure builder needs from this PC.
type DiscoverEnv struct {
	// Probe reports which host:port addresses accept a TCP connection from this PC.
	Probe func(addrs []string) map[string]bool
	// LookupIP resolves a name with this PC's resolver ("" when it cannot).
	LookupIP func(name string) string
}

// ---- config parsing ----

// parseProperties reads a Java .properties text (key=value / key: value / key value).
func parseProperties(text string) map[string]string {
	out := map[string]string{}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		for strings.HasSuffix(line, `\`) && i+1 < len(lines) {
			i++
			line = strings.TrimSuffix(line, `\`) + strings.TrimSpace(lines[i])
		}
		if line == "" || line[0] == '#' || line[0] == '!' {
			continue
		}
		k, v := line, ""
		if j := strings.IndexAny(line, "=: \t"); j >= 0 {
			k = line[:j]
			v = strings.TrimLeft(line[j:], " \t")
			if v != "" && (v[0] == '=' || v[0] == ':') {
				v = strings.TrimLeft(v[1:], " \t")
			}
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out
}

// redisArgs splits a redis.conf line the way Redis does: whitespace separated,
// "double quoted" (with \ escapes) and 'single quoted' arguments.
func redisArgs(line string) []string {
	var out []string
	var cur strings.Builder
	in := false
	quote := byte(0)
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote == '"':
			if c == '\\' && i+1 < len(line) {
				i++
				switch line[i] {
				case 'n':
					cur.WriteByte('\n')
				case 't':
					cur.WriteByte('\t')
				default:
					cur.WriteByte(line[i])
				}
			} else if c == '"' {
				quote = 0
			} else {
				cur.WriteByte(c)
			}
		case quote == '\'':
			if c == '\\' && i+1 < len(line) && line[i+1] == '\'' {
				i++
				cur.WriteByte('\'')
			} else if c == '\'' {
				quote = 0
			} else {
				cur.WriteByte(c)
			}
		case c == '"' || c == '\'':
			quote = c
			in = true
		case c == ' ' || c == '\t':
			if in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteByte(c)
			in = true
		}
	}
	if in {
		out = append(out, cur.String())
	}
	return out
}

// redisConf keeps the last value of each directive (Redis applies them in
// order), and every "user" line.
type redisConf struct {
	last  map[string][]string
	users [][]string
}

func parseRedisConf(text string) redisConf {
	c := redisConf{last: map[string][]string{}}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if line == "" || line[0] == '#' {
			continue
		}
		a := redisArgs(line)
		if len(a) == 0 {
			continue
		}
		k := strings.ToLower(a[0])
		if k == "user" {
			c.users = append(c.users, a[1:])
			continue
		}
		c.last[k] = a[1:]
	}
	return c
}

func (c redisConf) get(k string) (string, bool) {
	v, ok := c.last[k]
	if !ok || len(v) == 0 {
		return "", false
	}
	return v[0], true
}

// parseHostsFile maps each name in an /etc/hosts text to its address. The first
// line naming a host wins, as for the resolver.
func parseHostsFile(text string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		if _, err := netip.ParseAddr(f[0]); err != nil {
			continue
		}
		for _, n := range f[1:] {
			n = strings.ToLower(n)
			if _, ok := out[n]; !ok {
				out[n] = f[0]
			}
		}
	}
	return out
}

type jdbcURL struct {
	Host string
	Port int
	DB   string
}

// parseJDBC reads jdbc:mariadb://host:port/db?… (also mysql, and the first host
// of a multi-host URL).
func parseJDBC(u string) (jdbcURL, bool) {
	i := strings.Index(u, "://")
	if !strings.HasPrefix(strings.ToLower(u), "jdbc:") || i < 0 {
		return jdbcURL{}, false
	}
	rest := u[i+3:]
	hosts, db := rest, ""
	if j := strings.IndexByte(rest, '/'); j >= 0 {
		hosts, db = rest[:j], rest[j+1:]
	}
	if j := strings.IndexAny(db, "?;"); j >= 0 {
		db = db[:j]
	}
	first := strings.Split(hosts, ",")[0]
	r := jdbcURL{Host: first, Port: 3306, DB: db}
	if h, p, err := net.SplitHostPort(first); err == nil {
		r.Host = h
		if n, err := strconv.Atoi(p); err == nil {
			r.Port = n
		}
	}
	return r, r.Host != ""
}

// yamlDatasource finds "url: jdbc:…" in a Spring application.yml and the
// username/password beside it (same indentation, same block). It is not a YAML
// parser; it only has to read the datasource block MK119 modules ship.
type yamlDS struct {
	URL, User, Password string
	HasUser, HasPass    bool
}

func yamlDatasources(text string) []yamlDS {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	indent := func(s string) int { return len(s) - len(strings.TrimLeft(s, " ")) }
	kv := func(s string) (string, string, bool) {
		t := strings.TrimSpace(s)
		j := strings.Index(t, ":")
		if j <= 0 {
			return "", "", false
		}
		v := strings.TrimSpace(t[j+1:])
		if k := strings.Index(v, " #"); k >= 0 {
			v = strings.TrimSpace(v[:k])
		}
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		return strings.TrimSpace(t[:j]), v, true
	}
	var out []yamlDS
	for i, line := range lines {
		k, v, ok := kv(line)
		if !ok || k != "url" && k != "jdbc-url" || !strings.HasPrefix(strings.ToLower(v), "jdbc:") {
			continue
		}
		in := indent(line)
		ds := yamlDS{URL: v}
		look := func(j int) bool {
			s := lines[j]
			if strings.TrimSpace(s) == "" || strings.HasPrefix(strings.TrimSpace(s), "#") {
				return true
			}
			if indent(s) < in || strings.HasPrefix(strings.TrimSpace(s), "---") {
				return false
			}
			if indent(s) == in {
				if k, v, ok := kv(s); ok {
					switch k {
					case "username", "user":
						ds.User, ds.HasUser = v, true
					case "password":
						ds.Password, ds.HasPass = v, true
					}
				}
			}
			return true
		}
		for j := i - 1; j >= 0 && look(j); j-- {
		}
		for j := i + 1; j < len(lines) && look(j); j++ {
		}
		out = append(out, ds)
	}
	return out
}

// looksEncrypted: Jasypt ENC(…), or a long base64 blob — a value the module
// decrypts itself and that cannot be used as is.
func looksEncrypted(v string) bool {
	if strings.HasPrefix(v, "ENC(") || strings.HasPrefix(v, "{cipher}") {
		return true
	}
	if len(v) < 20 || !strings.HasSuffix(v, "=") {
		return false
	}
	_, err := base64.StdEncoding.DecodeString(v)
	return err == nil
}

type listener struct {
	Name string
	Host string
	Port int
}

// parseListeners reads "INTERNAL://server_1:9092,EXTERNAL://1.2.3.4:19092".
func parseListeners(v string) []listener {
	var out []listener
	for _, p := range strings.Split(v, ",") {
		p = strings.TrimSpace(p)
		i := strings.Index(p, "://")
		if i <= 0 {
			continue
		}
		h, ps, err := net.SplitHostPort(p[i+3:])
		if err != nil {
			continue
		}
		n, err := strconv.Atoi(ps)
		if err != nil {
			continue
		}
		out = append(out, listener{Name: strings.ToUpper(p[:i]), Host: h, Port: n})
	}
	return out
}

func protocolMap(v string) map[string]string {
	out := map[string]string{}
	for _, p := range strings.Split(v, ",") {
		kv := strings.SplitN(strings.TrimSpace(p), ":", 2)
		if len(kv) == 2 {
			out[strings.ToUpper(strings.TrimSpace(kv[0]))] = strings.ToUpper(strings.TrimSpace(kv[1]))
		}
	}
	return out
}

var kafkaProtocols = map[string]bool{"PLAINTEXT": true, "SSL": true, "SASL_PLAINTEXT": true, "SASL_SSL": true}

func isLoopback(ip string) bool {
	a, err := netip.ParseAddr(ip)
	return err == nil && (a.IsLoopback() || a.IsUnspecified())
}

func isIP(s string) bool {
	_, err := netip.ParseAddr(s)
	return err == nil
}

// ---- building the proposal ----

type builder struct {
	dumps []ServerDump
	env   DiscoverEnv
	hosts []map[string]string // per dump: /etc/hosts
	res   DiscoverResult
}

func (b *builder) warn(format string, a ...any) {
	b.res.Warnings = append(b.res.Warnings, fmt.Sprintf(format, a...))
}

func (b *builder) field(key, label, value string, secret bool, source string) {
	b.res.Fields = append(b.res.Fields, DiscoverField{Key: key, Label: label, Value: value, Secret: secret, Source: source})
}

// sshIP is the server's SSH address as an IP (resolved on this PC if it is a name).
func (b *builder) sshIP(d ServerDump) string {
	if isIP(d.Addr) || b.env.LookupIP == nil {
		return d.Addr
	}
	if ip := b.env.LookupIP(d.Addr); ip != "" {
		return ip
	}
	return d.Addr
}

// resolve turns a name a server config uses (server_vip, server_1, liz_server_2)
// into an address for this PC: the server's own /etc/hosts first, then any
// server's, then the server itself when the name is its hostname, then this PC's
// resolver. The returned source says which.
func (b *builder) resolve(name string, from int) (string, string) {
	if isIP(name) {
		return name, ""
	}
	n := strings.ToLower(strings.TrimSuffix(name, "."))
	order := []int{from}
	for i := range b.dumps {
		if i != from {
			order = append(order, i)
		}
	}
	for _, i := range order {
		if i < 0 || i >= len(b.hosts) {
			continue
		}
		if ip, ok := b.hosts[i][n]; ok && !isLoopback(ip) {
			return ip, fmt.Sprintf("%s:/etc/hosts %s", b.dumps[i].label(), name)
		}
	}
	for _, d := range b.dumps {
		if d.Err == "" && (strings.EqualFold(d.Hostname, n) || strings.EqualFold(strings.SplitN(d.Hostname, ".", 2)[0], n)) {
			return b.sshIP(d), fmt.Sprintf("%s 의 SSH 주소(hostname %s)", d.label(), d.Hostname)
		}
	}
	if b.env.LookupIP != nil {
		if ip := b.env.LookupIP(name); ip != "" && !isLoopback(ip) {
			return ip, "이 PC의 DNS " + name
		}
	}
	return "", ""
}

// BuildDiscovery turns the servers' answers into proposed connection values.
func BuildDiscovery(dumps []ServerDump, env DiscoverEnv) DiscoverResult {
	b := &builder{dumps: dumps, env: env}
	b.res.KafkaChoice = -1
	for _, d := range dumps {
		h := map[string]string{}
		for _, f := range d.files("hosts") {
			if f.State == "ok" {
				h = parseHostsFile(f.Text)
			}
		}
		b.hosts = append(b.hosts, h)
		names := make([]string, 0, len(h))
		for n := range h {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			b.res.Hosts = append(b.res.Hosts, DiscoverHostsEntry{Server: d.label(), Hostname: d.Hostname, Name: n, IP: h[n]})
		}
	}
	ok := 0
	for _, d := range dumps {
		if d.Err != "" {
			b.warn("%s: 접속하지 못했습니다 — %s", d.label(), d.Err)
			continue
		}
		ok++
		if d.Base == "" {
			b.warn("%s: MK119 설치 폴더(/OnionSoftware/liz, /usr/local/liz)가 없습니다", d.label())
		}
		for _, f := range d.Files {
			if f.State == "denied" {
				b.warn("%s: %s 를 읽을 권한이 없습니다 (sudo 불가)", d.label(), f.Path)
			}
		}
	}
	if ok == 0 {
		return b.res
	}

	// What this PC can reach is checked in one go, before choosing.
	var addrs []string
	db := b.mariadb()
	if db != "" {
		addrs = append(addrs, db)
	}
	redisAddrs := b.redis()
	addrs = append(addrs, redisAddrs...)
	brokers := b.kafkaBrokers()
	for _, br := range brokers {
		for _, l := range br.listeners {
			addrs = append(addrs, l.candidates...)
		}
	}
	reach := map[string]bool{}
	if env.Probe != nil && len(addrs) > 0 {
		reach = env.Probe(uniq(addrs))
	}
	if db != "" && !reach[db] {
		b.warn("MariaDB %s: 이 PC에서 접속되지 않습니다 (방화벽·경유 접속 환경이면 등록이 실패할 수 있습니다)", db)
	}
	var dead []string
	for _, a := range redisAddrs {
		if !reach[a] {
			dead = append(dead, a)
		}
	}
	if len(dead) > 0 && len(dead) == len(redisAddrs) {
		b.warn("Redis: 이 PC에서 어느 서버에도 접속되지 않습니다 (%s)", strings.Join(dead, ", "))
	} else if len(dead) > 0 {
		b.warn("Redis: 이 PC에서 일부 서버에 접속되지 않습니다 (%s) — 마스터가 그쪽으로 옮겨 가면 등록이 실패합니다", strings.Join(dead, ", "))
	}
	b.kafka(brokers, reach)
	return b.res
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// ---- MariaDB ----

type dbCandidate struct {
	server            int
	src               string // "server_1:/…/collector.properties"
	host              string
	port              int
	user, pass        string
	hasUser, hasPass  bool
	hostKey, userKey  string
	passKey, portKey  string
	schema, schemaKey string
}

// mariadb proposes dbHost/dbPort/dbUser/dbPassword and returns host:port for the
// reachability check ("" when nothing was found).
//
// Sources, in order: lizcollector collector.properties (repository.*), then
// lizadmin application.yml (spring datasource url/username/password), on each
// server in turn. The first one wins; differences between them are warned about.
func (b *builder) mariadb() string {
	var cands []dbCandidate
	for i, d := range b.dumps {
		if d.Err != "" {
			continue
		}
		for _, f := range d.files("collector") {
			if f.State != "ok" {
				continue
			}
			p := parseProperties(f.Text)
			c := dbCandidate{server: i, src: d.label() + ":" + f.Path, port: 3306,
				hostKey: "repository.host", portKey: "repository.port", userKey: "repository.user.id", passKey: "repository.user.password", schemaKey: "repository.schema"}
			c.host = p["repository.host"]
			if c.host == "" {
				if m := p["repository.multiple.host"]; m != "" {
					first := strings.Split(m, ",")[0]
					if h, ps, err := net.SplitHostPort(strings.TrimSpace(first)); err == nil {
						c.host, c.hostKey = h, "repository.multiple.host"
						if n, err := strconv.Atoi(ps); err == nil {
							c.port, c.portKey = n, "repository.multiple.host"
						}
					}
				}
			}
			if n, err := strconv.Atoi(p["repository.port"]); err == nil {
				c.port = n
			}
			c.user, c.hasUser = p["repository.user.id"]
			c.pass, c.hasPass = p["repository.user.password"]
			c.schema = p["repository.schema"]
			if c.host != "" || c.hasUser || c.hasPass {
				cands = append(cands, c)
			}
		}
		for _, f := range d.files("admin") {
			if f.State != "ok" {
				continue
			}
			for _, ds := range yamlDatasources(f.Text) {
				u, ok := parseJDBC(ds.URL)
				if !ok {
					continue
				}
				cands = append(cands, dbCandidate{server: i, src: d.label() + ":" + f.Path, host: u.Host, port: u.Port,
					user: ds.User, hasUser: ds.HasUser, pass: ds.Password, hasPass: ds.HasPass,
					hostKey: "datasource url", portKey: "datasource url", userKey: "datasource username", passKey: "datasource password",
					schema: u.DB, schemaKey: "datasource url"})
			}
		}
	}
	if len(cands) == 0 {
		b.warn("MariaDB: lizcollector/config/collector.properties, lizadmin/config/application.yml 을 어느 서버에서도 찾지 못했습니다 — 직접 입력하세요")
		return ""
	}

	var host, hostSrc, user, userSrc, pass, passSrc string
	port, portSrc := 0, ""
	hosts := map[string][]string{}
	users := map[string][]string{}
	passes := map[string][]string{}
	for _, c := range cands {
		if c.host != "" {
			hp := net.JoinHostPort(c.host, strconv.Itoa(c.port))
			hosts[hp] = append(hosts[hp], c.src)
			if host == "" {
				ip, how := b.resolve(c.host, c.server)
				hostSrc = fmt.Sprintf("%s %s=%s", c.src, c.hostKey, c.host)
				if ip == "" {
					ip = c.host
					b.warn("MariaDB: %s 의 주소를 /etc/hosts 에서 찾지 못해 이름 그대로 넣었습니다 — 이 PC에서 풀리지 않으면 IP로 고치세요", c.host)
				} else if ip != c.host {
					hostSrc += " → " + how + " " + ip
				}
				host = ip
				port, portSrc = c.port, fmt.Sprintf("%s %s", c.src, c.portKey)
			}
		}
		if c.hasUser && c.user != "" {
			if looksEncrypted(c.user) {
				b.warn("MariaDB: %s 의 %s 는 암호화돼 있어 쓰지 않았습니다", c.src, c.userKey)
			} else {
				users[c.user] = append(users[c.user], c.src)
				if user == "" {
					user, userSrc = c.user, c.src+" "+c.userKey
				}
			}
		}
		if c.hasPass && c.pass != "" {
			if looksEncrypted(c.pass) {
				b.warn("MariaDB: %s 의 %s 는 암호화돼 있어 쓰지 않았습니다", c.src, c.passKey)
			} else {
				passes[c.pass] = append(passes[c.pass], c.src)
				if pass == "" {
					pass, passSrc = c.pass, c.src+" "+c.passKey
				}
			}
		}
		if c.schema != "" && c.schema != "liz" {
			b.warn("MariaDB: %s 의 DB 이름이 liz 가 아니라 %s 입니다 — 등록은 liz 를 씁니다", c.src, c.schema)
		}
	}
	if len(hosts) > 1 {
		b.warn("MariaDB 주소가 설정마다 다릅니다: %s — 첫 값(%s)을 넣었습니다", describeSet(hosts), hostSrc)
	}
	if len(users) > 1 {
		b.warn("MariaDB 계정이 설정마다 다릅니다: %s", describeSet(users))
	}
	if len(passes) > 1 {
		b.warn("MariaDB 비밀번호가 설정마다 다릅니다 (%d가지) — %s 의 값을 넣었습니다", len(passes), passSrc)
	}
	if host != "" {
		b.field("dbHost", "MariaDB 서버 주소", host, false, hostSrc)
		b.field("dbPort", "MariaDB 포트", strconv.Itoa(port), false, portSrc)
	}
	if user != "" {
		b.field("dbUser", "MariaDB 계정", user, false, userSrc)
	}
	if pass != "" {
		b.field("dbPassword", "MariaDB 비밀번호", pass, true, passSrc)
	} else {
		b.warn("MariaDB 비밀번호를 설정 파일에서 찾지 못했습니다 — 직접 입력하세요")
	}
	if host == "" {
		return ""
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// describeSet renders value → sources without values that are secrets (callers
// only pass non-secret sets).
func describeSet(m map[string][]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s (%s)", k, strings.Join(m[k], ", ")))
	}
	return strings.Join(parts, " / ")
}

// ---- Redis ----

// redis proposes redisHost/redisPort/redisPassword and returns the addresses for
// the reachability check.
//
// Every server running redis-server is listed, not just today's master: Redis is
// Sentinel-managed on MK119 and the master moves (on trunk it moved from .24 to
// .25), and the registration finds the master among the listed servers itself.
// Password, in order: redis.conf requirepass, masterauth, then sentinel.conf
// "sentinel auth-pass".
func (b *builder) redis() []string {
	type node struct {
		server int
		addr   string
		port   int
	}
	var nodes []node
	var pass, passSrc string
	passes := map[string][]string{}
	var portSrc string
	hashOnly := ""
	var masters, replicas []string
	for i, d := range b.dumps {
		if d.Err != "" {
			continue
		}
		confs := d.files("redis")
		ports := map[int]bool{}
		for _, a := range d.Redis {
			a = strings.TrimPrefix(a, "*")
			if j := strings.LastIndexByte(a, ':'); j >= 0 {
				if n, err := strconv.Atoi(a[j+1:]); err == nil && n > 0 {
					ports[n] = true
				}
			}
		}
		var confPort int
		role := ""
		for _, f := range confs {
			if f.State != "ok" {
				continue
			}
			c := parseRedisConf(f.Text)
			if v, ok := c.get("port"); ok {
				if n, err := strconv.Atoi(v); err == nil && n > 0 {
					if confPort == 0 {
						confPort = n
						if portSrc == "" {
							portSrc = d.label() + ":" + f.Path + " port"
						}
					}
					if len(d.Redis) == 0 {
						ports[n] = true
					}
				}
			}
			if _, ok := c.get("replicaof"); ok {
				role = "replica"
			} else if _, ok := c.get("slaveof"); ok {
				role = "replica"
			} else if role == "" {
				role = "master"
			}
			src := d.label() + ":" + f.Path
			p, key := "", ""
			if v, ok := c.get("requirepass"); ok && v != "" {
				p, key = v, "requirepass"
			} else if v, ok := c.get("masterauth"); ok && v != "" {
				p, key = v, "masterauth"
			}
			if p != "" {
				passes[p] = append(passes[p], src)
				if pass == "" {
					pass, passSrc = p, src+" "+key
				}
			} else {
				for _, u := range c.users {
					if len(u) > 0 && u[0] == "default" {
						for _, r := range u[1:] {
							if strings.HasPrefix(r, "#") && hashOnly == "" {
								hashOnly = src
							}
						}
					}
				}
			}
		}
		if len(ports) == 0 {
			continue
		}
		pl := make([]int, 0, len(ports))
		for p := range ports {
			pl = append(pl, p)
		}
		sort.Ints(pl)
		for _, p := range pl {
			nodes = append(nodes, node{server: i, addr: d.Addr, port: p})
		}
		if len(d.Redis) == 0 {
			b.warn("%s: redis.conf 는 있지만 redis-server 가 실행 중이 아닙니다", d.label())
		}
		switch role {
		case "master":
			masters = append(masters, d.label())
		case "replica":
			replicas = append(replicas, d.label())
		}
	}
	if pass == "" {
		for _, d := range b.dumps {
			for _, f := range d.files("sentinel") {
				if f.State != "ok" {
					continue
				}
				// "sentinel auth-pass <master> <password>"
				for _, line := range strings.Split(f.Text, "\n") {
					a := redisArgs(strings.TrimSpace(line))
					if len(a) >= 4 && strings.EqualFold(a[0], "sentinel") && strings.EqualFold(a[1], "auth-pass") {
						pass, passSrc = a[3], d.label()+":"+f.Path+" sentinel auth-pass"
						break
					}
				}
				if pass != "" {
					break
				}
			}
			if pass != "" {
				break
			}
		}
	}
	if len(nodes) == 0 {
		b.warn("Redis: 어느 서버에서도 redis-server 나 redis/*.conf 를 찾지 못했습니다 — 직접 입력하세요")
		return nil
	}
	samePort := true
	for _, n := range nodes {
		if n.port != nodes[0].port {
			samePort = false
		}
	}
	var hostList, addrs, servers []string
	seenSrv := map[int]bool{}
	for _, n := range nodes {
		a := net.JoinHostPort(b.sshIP(b.dumps[n.server]), strconv.Itoa(n.port))
		addrs = append(addrs, a)
		if samePort {
			hostList = append(hostList, b.dumps[n.server].Addr)
		} else {
			hostList = append(hostList, net.JoinHostPort(b.dumps[n.server].Addr, strconv.Itoa(n.port)))
		}
		if !seenSrv[n.server] {
			seenSrv[n.server] = true
			servers = append(servers, b.dumps[n.server].label())
		}
	}
	src := fmt.Sprintf("redis-server 가 도는 서버 전부(%s)의 SSH 주소 — Sentinel 이 마스터를 옮겨도 등록이 마스터를 찾습니다", strings.Join(servers, ", "))
	if len(masters) > 0 {
		src += fmt.Sprintf(" · 설정상 현재 마스터: %s", strings.Join(masters, ", "))
	}
	b.field("redisHost", "Redis 서버 주소", strings.Join(hostList, ","), false, src)
	if samePort {
		ps := portSrc
		if ps == "" {
			ps = "redis-server 실행 주소"
		}
		b.field("redisPort", "Redis 포트", strconv.Itoa(nodes[0].port), false, ps)
	} else {
		b.warn("Redis 포트가 서버마다 달라 주소에 포트를 붙여 넣었습니다")
	}
	if pass != "" {
		b.field("redisPassword", "Redis 비밀번호", pass, true, passSrc)
		if len(passes) > 1 {
			b.warn("Redis 비밀번호가 서버마다 다릅니다 (%d가지) — %s 의 값을 넣었습니다", len(passes), passSrc)
		}
	} else if hashOnly != "" {
		b.warn("Redis 비밀번호: %s 에는 해시(user default … #…)만 있어 원래 비밀번호를 알 수 없습니다 — 직접 입력하세요", hashOnly)
	} else {
		b.warn("Redis 비밀번호를 찾지 못했습니다 (requirepass·masterauth·sentinel auth-pass 없음) — 비밀번호가 없는 Redis 이면 비워 두세요")
	}
	return uniq(addrs)
}

// ---- Kafka ----

type brokerListener struct {
	listener
	security   string
	ip         string
	ipSrc      string
	candidates []string // host:port to try from this PC, best first
}

type broker struct {
	server    int
	id        string
	src       string
	inter     string
	listeners []brokerListener
}

// kafkaBrokers reads every broker's server.properties: advertised.listeners
// (listeners when absent), the protocol map and the inter-broker listener.
func (b *builder) kafkaBrokers() []broker {
	var out []broker
	seen := map[string]bool{}
	for i, d := range b.dumps {
		if d.Err != "" {
			continue
		}
		for _, f := range d.files("kafka") {
			if f.State != "ok" {
				if f.State == "missing" && len(d.Kafka) > 0 {
					b.warn("%s: Kafka 가 실행 중인데 %s 를 찾지 못했습니다", d.label(), f.Path)
				}
				continue
			}
			p := parseProperties(f.Text)
			br := broker{server: i, id: p["broker.id"], src: d.label() + ":" + f.Path}
			key := "advertised.listeners"
			ls := parseListeners(p["advertised.listeners"])
			if len(ls) == 0 {
				key = "listeners"
				ls = parseListeners(p["listeners"])
			}
			if len(ls) == 0 {
				b.warn("%s: listeners / advertised.listeners 가 없습니다", br.src)
				continue
			}
			pm := protocolMap(p["listener.security.protocol.map"])
			br.inter = strings.ToUpper(p["inter.broker.listener.name"])
			if br.inter == "" {
				br.inter = strings.ToUpper(p["security.inter.broker.protocol"])
				if br.inter == "" {
					br.inter = "PLAINTEXT"
				}
			}
			for _, l := range ls {
				bl := brokerListener{listener: l, security: pm[l.Name]}
				if bl.security == "" && kafkaProtocols[l.Name] {
					bl.security = l.Name
				}
				if l.Host == "" || l.Host == "0.0.0.0" || l.Host == "::" {
					// Kafka advertises the machine's own name for an empty host.
					bl.Host = d.Hostname
				}
				ip, how := b.resolve(bl.Host, i)
				if ip == "" {
					ip, how = b.sshIP(d), fmt.Sprintf("%s 를 풀지 못해 %s 의 SSH 주소", bl.Host, d.label())
				}
				bl.ip = ip
				bl.ipSrc = fmt.Sprintf("%s %s %s://%s:%d", br.src, key, l.Name, bl.Host, l.Port)
				if how != "" {
					bl.ipSrc += " → " + how + " " + ip
				}
				// The broker binds every address on the machine as a rule, so the SSH
				// address is a fallback when the advertised one is not reachable.
				bl.candidates = uniq([]string{
					net.JoinHostPort(ip, strconv.Itoa(l.Port)),
					net.JoinHostPort(b.sshIP(d), strconv.Itoa(l.Port)),
				})
				br.listeners = append(br.listeners, bl)
			}
			k := br.id
			if k == "" {
				k = br.src
			}
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, br)
		}
	}
	return out
}

// kafka builds one option per listener name and picks one:
//  1. reachable from this PC on every broker, then on more brokers;
//  2. offered by every broker;
//  3. no SASL credentials needed (they cannot be read from the broker config);
//  4. a client-facing listener (not inter.broker.listener.name, typically
//     EXTERNAL) — it is the one meant for clients outside the cluster.
func (b *builder) kafka(brokers []broker, reach map[string]bool) {
	if len(brokers) == 0 {
		b.warn("Kafka: 어느 서버에서도 server.properties 를 찾지 못했습니다 — 직접 입력하세요")
		return
	}
	var names []string
	byName := map[string]*DiscoverKafkaOption{}
	for _, br := range brokers {
		for _, l := range br.listeners {
			o := byName[l.Name]
			if o == nil {
				o = &DiscoverKafkaOption{Listener: l.Name, Security: l.security, Brokers: len(brokers)}
				byName[l.Name] = o
				names = append(names, l.Name)
			}
			if br.inter == l.Name {
				o.InterBroker = true
			}
			if o.Security != l.security {
				b.warn("Kafka 리스너 %s 의 보안 방식이 브로커마다 다릅니다 (%s / %s)", l.Name, o.Security, l.security)
			}
			row := DiscoverKafkaRow{Hostname: l.Host, IP: l.ip, Port: l.Port, Source: l.ipSrc}
			for _, c := range l.candidates {
				if reach[c] {
					h, _, _ := net.SplitHostPort(c)
					if h != l.ip {
						row.Source += fmt.Sprintf(" — %s 은 이 PC에서 접속되지 않아 SSH 주소 %s 로 바꿈", l.ip, h)
					}
					row.IP, row.Reachable = h, true
					break
				}
			}
			if row.Reachable {
				o.Reachable++
			}
			o.Rows = append(o.Rows, row)
		}
	}
	var hostnames []string
	for _, n := range names {
		o := byName[n]
		for _, r := range o.Rows {
			if !brokerHostname.MatchString(r.Hostname) {
				hostnames = append(hostnames, r.Hostname)
			}
		}
		var notes []string
		notes = append(notes, fmt.Sprintf("이 PC에서 %d/%d 브로커 접속 확인", o.Reachable, len(o.Rows)))
		if len(o.Rows) < o.Brokers {
			notes = append(notes, fmt.Sprintf("브로커 %d개 중 %d개에만 있음", o.Brokers, len(o.Rows)))
		}
		if o.InterBroker {
			notes = append(notes, "브로커끼리 쓰는 리스너(inter.broker.listener.name)")
		} else {
			notes = append(notes, "외부 클라이언트용 리스너")
		}
		if o.Security == "" {
			notes = append(notes, "보안 방식을 알 수 없음(listener.security.protocol.map 없음)")
		} else if strings.HasPrefix(o.Security, "SASL") {
			notes = append(notes, "SASL 계정은 직접 입력해야 함")
		}
		o.Note = strings.Join(notes, " · ")
		b.res.Kafka = append(b.res.Kafka, *o)
	}
	if len(hostnames) > 0 {
		b.warn("Kafka: 등록이 받지 않는 호스트 이름이 있습니다 (%s)", strings.Join(hostnames, ", "))
	}
	score := func(o DiscoverKafkaOption) []int {
		all := 0
		if o.Reachable == len(o.Rows) && o.Reachable > 0 {
			all = 1
		}
		complete := 0
		if len(o.Rows) == o.Brokers {
			complete = 1
		}
		noCred := 0
		if kafkaProtocols[o.Security] && !strings.HasPrefix(o.Security, "SASL") {
			noCred = 1
		}
		ext := 0
		if !o.InterBroker {
			ext = 1
		}
		return []int{all, o.Reachable, complete, noCred, ext}
	}
	best := -1
	for i, o := range b.res.Kafka {
		if best < 0 || less(score(b.res.Kafka[best]), score(o)) {
			best = i
		}
	}
	b.res.KafkaChoice = best
	o := b.res.Kafka[best]
	why := []string{}
	switch {
	case o.Reachable == len(o.Rows):
		why = append(why, "이 PC에서 모든 브로커에 접속됩니다")
	case o.Reachable > 0:
		why = append(why, fmt.Sprintf("이 PC에서 %d/%d 브로커만 접속됩니다", o.Reachable, len(o.Rows)))
	default:
		why = append(why, "이 PC에서 어느 리스너로도 브로커에 접속되지 않습니다 (경유 접속 환경이면 등록의 Kafka 단계가 실패합니다)")
	}
	if len(b.res.Kafka) > 1 {
		if o.InterBroker {
			why = append(why, "외부용 리스너보다 접속이 잘 돼 브로커 간 리스너를 골랐습니다")
		} else {
			why = append(why, "외부 클라이언트용 리스너를 우선합니다")
		}
	}
	b.res.KafkaReason = fmt.Sprintf("%s 리스너: %s. 호스트 이름은 이 리스너가 알려 주는 이름 그대로입니다.", o.Listener, strings.Join(why, ", "))
	if o.Reachable < len(o.Rows) {
		b.warn("Kafka %s: 이 PC에서 접속되지 않는 브로커가 있습니다", o.Listener)
	}
}

func less(a, b []int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}
