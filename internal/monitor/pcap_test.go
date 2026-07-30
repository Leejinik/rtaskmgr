package monitor

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// ---- port spec ----------------------------------------------------------

func TestParsePortSpecAccepts(t *testing.T) {
	cases := []struct {
		in   string
		want string // canonical form
		all  bool
	}{
		{"", "", true},                                       // empty = every port
		{"   \t\n ", "", true},                               // whitespace only = empty
		{"3306", "3306", false},                              // single
		{"80,443,3306", "80, 443, 3306", false},              // csv
		{"8080-8090", "8080-8090", false},                    // range
		{"80,8080-8090,3306", "80, 3306, 8080-8090", false},  // mixed + sorted
		{"80,81,82", "80-82", false},                         // adjacent singles merge
		{"1000-1010,1011-1020", "1000-1020", false},          // adjacent ranges merge
		{"80,80,80", "80", false},                            // duplicates collapse
		{"1-65535", "1-65535", false},                        // full range
		{"80 443\t3306\n9092", "80, 443, 3306, 9092", false}, // any whitespace separates
		{"443-443", "443", false},                            // width-1 range narrows to a port
		{"0080", "80", false},                                // leading zeros
		{"3306, 8080 , 9092", "3306, 8080, 9092", false},     // stray spaces around commas
		{"9000-9010,9005-9008", "9000-9010", false},          // contained range absorbed
	}
	for _, c := range cases {
		got, err := parsePortSpec(c.in)
		if err != nil {
			t.Errorf("parsePortSpec(%q) error = %v, want ok", c.in, err)
			continue
		}
		if got.All != c.all {
			t.Errorf("parsePortSpec(%q).All = %v, want %v", c.in, got.All, c.all)
		}
		if s := got.canonical(); s != c.want {
			t.Errorf("parsePortSpec(%q).canonical() = %q, want %q", c.in, s, c.want)
		}
	}
	// 64 non-adjacent ranges is the documented ceiling and must be accepted.
	var sb strings.Builder
	for i := 0; i < 64; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(strconv.Itoa(1 + i*2))
	}
	if _, err := parsePortSpec(sb.String()); err != nil {
		t.Errorf("64 ranges rejected: %v", err)
	}
}

func TestParsePortSpecRejects(t *testing.T) {
	// 65 non-adjacent singles (1,3,5,...,129) exceed maxPortRanges after merging.
	var many strings.Builder
	for i := 0; i < 65; i++ {
		if i > 0 {
			many.WriteByte(',')
		}
		many.WriteString(strconv.Itoa(1 + i*2))
	}
	bad := []string{
		"0",                       // port 0
		"65536",                   // above the 16-bit range
		"999999",                  // 6 digits: not even a token
		"-80",                     // no leading sign
		"80-",                     // dangling range
		"1237-1234",               // reversed: refused, never swapped
		"80;rm -rf /",             // ';' is not a separator, so this is one bad token
		"80`id`",                  // backticks
		"80$(id)",                 // command substitution
		"80|443",                  // pipe
		"80&443",                  // background
		"$(reboot)",               // pure injection
		"port 80",                 // bpf keyword typed by hand
		"0x50",                    // hex
		"８０",                      // full-width digits
		"80/tcp",                  // protocol suffix
		"80:443",                  // colon range
		"1-2-3",                   // triple range
		"1--2",                    // double dash
		";",                       // separator-only garbage
		"80\n443;id",              // newline separates, second token is bad
		"80, ,443, id",            // an identifier among valid ports
		many.String(),             // > 64 ranges
		strings.Repeat("1,", 513), // 1026 chars > 1024
	}
	for _, in := range bad {
		if got, err := parsePortSpec(in); err == nil {
			t.Errorf("parsePortSpec(%q) = %+v, want error", in, got)
		}
	}
}

// TestPortSpecBPFNeverLeaksInput is the load-bearing test of the capture path:
// whatever the user types, the filter handed to tcpdump is rebuilt from integers
// and fixed keywords only. Any accepted input must produce a filter made solely
// of digits, lowercase letters, spaces and hyphens.
func TestPortSpecBPFNeverLeaksInput(t *testing.T) {
	inputs := []string{
		"", "3306", "80,443", "8080-8090", "1-65535", "0080",
		"80;rm -rf /", "80`id`", "80$(id)", "80|443", "$(reboot)", "port 80",
		"80 443 3306", "80,81,82", "1237-1234", "0", "65536", "８０",
		"80\n443", "80\t443", strings.Repeat("80,", 300), "'; reboot; '",
		"80 or 443", "not port 22", "80 && id", "80 > /etc/passwd",
	}
	allowed := regexp.MustCompile(`^[0-9a-z \-]*$`)
	wordOK := regexp.MustCompile(`^(port|portrange|or|[0-9]{1,5}|[0-9]{1,5}-[0-9]{1,5})$`)
	for _, in := range inputs {
		spec, err := parsePortSpec(in)
		if err != nil {
			continue // rejected inputs never reach the filter at all
		}
		out, err := spec.bpf()
		if err != nil {
			t.Errorf("bpf(%q) error = %v", in, err)
			continue
		}
		if !allowed.MatchString(out) {
			t.Errorf("bpf(%q) = %q leaks disallowed characters", in, out)
		}
		for _, w := range strings.Fields(out) {
			if !wordOK.MatchString(w) {
				t.Errorf("bpf(%q) produced unexpected word %q", in, w)
			}
		}
		// The raw text is kept for display only — it must never appear in the
		// filter (a digits-only input legitimately can, so only check the rest).
		if strings.ContainsAny(in, ";|`$&'\"><\\") && strings.ContainsAny(out, ";|`$&'\"><\\") {
			t.Errorf("bpf(%q) = %q carried a metacharacter through", in, out)
		}
	}
}

func TestPortSpecIdempotent(t *testing.T) {
	for _, in := range []string{"80,81,82", "8080-8090,80", "3306", "1-65535", "443-443", ""} {
		first, err := parsePortSpec(in)
		if err != nil {
			t.Fatalf("parsePortSpec(%q): %v", in, err)
		}
		second, err := parsePortSpec(first.canonical())
		if err != nil {
			t.Fatalf("re-parse of %q: %v", first.canonical(), err)
		}
		if first.canonical() != second.canonical() {
			t.Errorf("not idempotent: %q -> %q -> %q", in, first.canonical(), second.canonical())
		}
		b1, _ := first.bpf()
		b2, _ := second.bpf()
		if b1 != b2 {
			t.Errorf("bpf not idempotent for %q: %q vs %q", in, b1, b2)
		}
	}
}

func TestPortSpecBPFShape(t *testing.T) {
	cases := map[string]string{
		"":             "",
		"3306":         "port 3306",
		"80,443":       "port 80 or port 443",
		"8080-8090":    "portrange 8080-8090",
		"80,8080-8090": "port 80 or portrange 8080-8090",
		"80,81":        "portrange 80-81",
		"443-443":      "port 443",
	}
	for in, want := range cases {
		spec, err := parsePortSpec(in)
		if err != nil {
			t.Fatalf("parsePortSpec(%q): %v", in, err)
		}
		got, err := spec.bpf()
		if err != nil {
			t.Fatalf("bpf(%q): %v", in, err)
		}
		if got != want {
			t.Errorf("bpf(%q) = %q, want %q", in, got, want)
		}
	}
}

// ---- host spec ----------------------------------------------------------

func TestParseHostSpecAccepts(t *testing.T) {
	cases := []struct {
		in   string
		want string // canonical form
		all  bool
	}{
		{"", "", true},
		{"  \t ", "", true},
		{"10.0.0.5", "10.0.0.5", false},
		{"10.0.0.5,10.0.0.6", "10.0.0.5, 10.0.0.6", false},
		{"10.0.0.5 10.0.0.6", "10.0.0.5, 10.0.0.6", false},
		{"10.0.0.5,10.0.0.5", "10.0.0.5", false},                      // duplicates collapse
		{"10.0.0.0/24", "10.0.0.0/24", false},                         // network
		{"10.0.0.5/24", "10.0.0.0/24", false},                         // canonicalised to the network
		{"10.0.0.5, 172.16.0.0/12", "10.0.0.5, 172.16.0.0/12", false}, // mixed
		{"db1.example.com", "db1.example.com", false},
		{"DB1.Example.COM", "db1.example.com", false}, // lowercased
		{"db1", "db1", false},                         // short name from /etc/hosts
		{"fe80::1", "fe80::1", false},
		{"2001:db8::/32", "2001:db8::/32", false},
		{"trunk.logan.com, 5000.example.net", "trunk.logan.com, 5000.example.net", false},
	}
	for _, c := range cases {
		got, err := parseHostSpec(c.in)
		if err != nil {
			t.Errorf("parseHostSpec(%q) error = %v, want ok", c.in, err)
			continue
		}
		if got.All != c.all {
			t.Errorf("parseHostSpec(%q).All = %v, want %v", c.in, got.All, c.all)
		}
		if s := got.canonical(); s != c.want {
			t.Errorf("parseHostSpec(%q).canonical() = %q, want %q", c.in, s, c.want)
		}
	}
}

func TestParseHostSpecRejects(t *testing.T) {
	var many strings.Builder
	for i := 0; i < maxHostTerms+1; i++ {
		if i > 0 {
			many.WriteByte(',')
		}
		many.WriteString("10.0.0." + strconv.Itoa(i))
	}
	bad := []string{
		"10.0.0",                                 // a truncated IP is a typo, not a hostname
		"10.0.0.256",                             // out of range
		"1.2.3.4.5",                              // too many octets
		"10.0.0.0/33",                            // impossible prefix
		"10.0.0.0/",                              // dangling
		"10.0.0.5;id",                            // ';' is not a separator, so this is one bad token
		"10.0.0.5`id`",                           // backticks
		"$(reboot)",                              // injection
		"host 10.0.0.5",                          // bpf keyword typed by hand -> "host" is a bad token
		"-db1.example.com",                       // label starts with '-'
		"db1-.example.com",                       // label ends with '-'
		"db_1.example.com",                       // underscore is not a hostname character
		"db1..example.com",                       // empty label
		"10.0.0.5|10.0.0.6",                      // pipe
		"10.0.0.5/24/8",                          // double CIDR
		"‥example.com",                           // non-ASCII
		strings.Repeat("a", 64) + ".example.com", // label longer than 63
		strings.Repeat("a.", 200) + "com",        // name longer than 253
		many.String(),                            // too many terms
		strings.Repeat("1,", 700),                // over 1024 chars
	}
	for _, in := range bad {
		if got, err := parseHostSpec(in); err == nil {
			t.Errorf("parseHostSpec(%q) = %+v, want error", in, got)
		}
	}
}

// TestCaptureFilterNeverLeaksInput is the host-side twin of the port test: no
// matter what is typed, the filter handed to tcpdump is rebuilt from canonical
// values and fixed keywords.
func TestCaptureFilterNeverLeaksInput(t *testing.T) {
	hostInputs := []string{
		"", "10.0.0.5", "10.0.0.0/24", "db1.example.com", "fe80::1", "2001:db8::/32",
		"10.0.0.5;rm -rf /", "10.0.0.5`id`", "$(reboot)", "10.0.0.5|10.0.0.6",
		"host 10.0.0.5", "10.0.0.5 and port 80", "'; reboot; '", "10.0.0.5 > /etc/passwd",
		"10.0.0.5,10.0.0.6,10.0.0.7", "10.0.0", "*.example.com", "db1$(id).example.com",
	}
	portInputs := []string{"", "3306", "80,443", "8080-8090", "80;id"}
	allowed := regexp.MustCompile(`^[0-9A-Za-z .:/()\- ]*$`)
	wordOK := regexp.MustCompile(`^(host|net|port|portrange|or|and|\(|\)|[0-9]{1,5}|[0-9]{1,5}-[0-9]{1,5}|[0-9A-Za-z.:/\-]+)$`)
	for _, hi := range hostInputs {
		hs, herr := parseHostSpec(hi)
		if herr != nil {
			continue // rejected input never reaches the filter
		}
		for _, pi := range portInputs {
			ps, perr := parsePortSpec(pi)
			if perr != nil {
				continue
			}
			words, display, err := captureFilter(hs, ps)
			if err != nil {
				t.Errorf("captureFilter(%q, %q) error = %v", hi, pi, err)
				continue
			}
			if !allowed.MatchString(display) {
				t.Errorf("captureFilter(%q, %q) = %q leaks disallowed characters", hi, pi, display)
			}
			for _, w := range words {
				if !wordOK.MatchString(w) {
					t.Errorf("captureFilter(%q, %q) produced unexpected word %q", hi, pi, w)
				}
			}
			if strings.ContainsAny(display, ";|`$&'\"><\\*?") {
				t.Errorf("captureFilter(%q, %q) = %q carried a metacharacter through", hi, pi, display)
			}
		}
	}
}

// TestCaptureFilterShape is the whole point of the feature: it must produce what
// an operator would type by hand.
func TestCaptureFilterShape(t *testing.T) {
	cases := []struct{ hosts, ports, want string }{
		{"", "", ""},
		{"", "3306", "port 3306"},
		{"10.0.0.5", "", "host 10.0.0.5"},
		{"10.0.0.5", "3306", "host 10.0.0.5 and port 3306"},
		{"10.0.0.0/24", "5000", "net 10.0.0.0/24 and port 5000"},
		{"db1.example.com", "8080-8090", "host db1.example.com and portrange 8080-8090"},
		// Multiple terms on either side MUST be parenthesised: `and` binds tighter
		// than `or`, so "host A or host B and port 80" would mean
		// "host A or (host B and port 80)" and capture everything from host A.
		{"10.0.0.5,10.0.0.6", "3306", "( host 10.0.0.5 or host 10.0.0.6 ) and port 3306"},
		{"10.0.0.5", "80,443", "host 10.0.0.5 and ( port 80 or port 443 )"},
		{"10.0.0.5,10.0.0.6", "80,443",
			"( host 10.0.0.5 or host 10.0.0.6 ) and ( port 80 or port 443 )"},
		{"10.0.0.5,10.0.0.0/24", "", "host 10.0.0.5 or net 10.0.0.0/24"},
	}
	for _, c := range cases {
		hs, err := parseHostSpec(c.hosts)
		if err != nil {
			t.Fatalf("parseHostSpec(%q): %v", c.hosts, err)
		}
		ps, err := parsePortSpec(c.ports)
		if err != nil {
			t.Fatalf("parsePortSpec(%q): %v", c.ports, err)
		}
		_, got, err := captureFilter(hs, ps)
		if err != nil {
			t.Fatalf("captureFilter(%q, %q): %v", c.hosts, c.ports, err)
		}
		if got != c.want {
			t.Errorf("captureFilter(%q, %q) = %q, want %q", c.hosts, c.ports, got, c.want)
		}
	}
}

func TestHostSpecIdempotent(t *testing.T) {
	for _, in := range []string{"10.0.0.5", "10.0.0.5,10.0.0.6", "10.0.0.5/24", "db1.example.com", "fe80::1", ""} {
		first, err := parseHostSpec(in)
		if err != nil {
			t.Fatalf("parseHostSpec(%q): %v", in, err)
		}
		second, err := parseHostSpec(first.canonical())
		if err != nil {
			t.Fatalf("re-parse of %q: %v", first.canonical(), err)
		}
		if first.canonical() != second.canonical() {
			t.Errorf("not idempotent: %q -> %q -> %q", in, first.canonical(), second.canonical())
		}
	}
}

// ---- identifier / path validation ---------------------------------------

func TestValidCapID(t *testing.T) {
	ok := []string{
		"pcap-1753800000000-a1b2c3d4",
		"pcap-1234567890-00000000",
		"pcap-17538000000001234-ffffffff",
	}
	for _, id := range ok {
		if !validCapID(id) {
			t.Errorf("validCapID(%q) = false, want true", id)
		}
	}
	bad := []string{
		"", "pcap", "pcap-1-a1b2c3d4", "pcap-x-y", "pcap-1753800000000-A1B2C3D4",
		"pcap-1753800000000-a1b2c3d", "pcap-1753800000000-a1b2c3d45",
		"pcap-1753800000000-a1b2c3d4 ", " pcap-1753800000000-a1b2c3d4",
		"pcap-1753800000000-a1b2c3d4;id", "pcap-1 $(id)", "../../etc/passwd",
		"pcap-1753800000000-a1b2c3d4\n", "rec-1753800000000",
		"pcap-123456789012345678-a1b2c3d4", // 18-digit stamp exceeds the pattern
	}
	for _, id := range bad {
		if validCapID(id) {
			t.Errorf("validCapID(%q) = true, want false", id)
		}
	}
	// Everything newCapID mints must pass, or a capture can never be listed.
	for i := 0; i < 50; i++ {
		if id := newCapID(); !validCapID(id) {
			t.Fatalf("newCapID() produced %q which fails validCapID", id)
		}
	}
}

func TestValidIfName(t *testing.T) {
	ok := []string{"eth0", "ens192", "bond0.100", "br-ex", "any", "lo", "enp0s31f6", "vlan@if3", "team0:1"}
	for _, n := range ok {
		if !validIfName(n) {
			t.Errorf("validIfName(%q) = false, want true", n)
		}
	}
	bad := []string{
		"", "eth 0", "eth0;id", "eth0$(id)", "eth0`id`", "../x", "eth0\n",
		"abcdefghijklmnop", // 16 chars > IFNAMSIZ-1
		"eth0/../x", "eth|0", "eth0>f",
	}
	for _, n := range bad {
		if validIfName(n) {
			t.Errorf("validIfName(%q) = true, want false", n)
		}
	}
}

func TestValidUnixUser(t *testing.T) {
	ok := []string{"liz", "root", "_svc", "logan.lee", "app-user", "u123"}
	for _, u := range ok {
		if !validUnixUser(u) {
			t.Errorf("validUnixUser(%q) = false, want true", u)
		}
	}
	bad := []string{"", "1abc", "-abc", "Root", "li z", "liz;id", "liz$(id)", "liz\n",
		strings.Repeat("a", 33)}
	for _, u := range bad {
		if validUnixUser(u) {
			t.Errorf("validUnixUser(%q) = true, want false", u)
		}
	}
}

func TestValidAbsPath(t *testing.T) {
	ok := []string{"/home/liz", "/var/tmp", "/data", "/run/user/1000", "/home/liz/.rtaskmgr-pcap",
		"/opt/app-1.2/dir_name"}
	for _, p := range ok {
		if !validAbsPath(p) {
			t.Errorf("validAbsPath(%q) = false, want true", p)
		}
	}
	bad := []string{
		"", "/", "relative/path", "/home/liz/", "//home", "/home//liz",
		"/home/../etc", "/home/./liz", "/home/li z", "/home/liz;id", "/home/$(id)",
		"/home/liz\n", "/home/liz'", "/tmp/*", "/home/liz|x",
	}
	for _, p := range bad {
		if validAbsPath(p) {
			t.Errorf("validAbsPath(%q) = true, want false", p)
		}
	}
}

func TestShArgs(t *testing.T) {
	// A value must stay ONE argv word no matter what it contains.
	got := shArgs("-o", "/home/li z/a'b.pcap", "-i", "eth0")
	if !strings.Contains(got, `'/home/li z/a'\''b.pcap'`) {
		t.Errorf("shArgs did not quote the awkward path: %s", got)
	}
	if n := strings.Count(got, "'"); n%2 == 0 && n == 0 {
		t.Errorf("shArgs produced no quoting: %s", got)
	}
	if shArgs() != "" {
		t.Errorf("shArgs() = %q, want empty", shArgs())
	}
	// Newlines and semicolons stay inside the quotes.
	q := shArgs("a\nb; reboot")
	if !strings.HasPrefix(q, "'") || !strings.HasSuffix(q, "'") {
		t.Errorf("shArgs(%q) = %s, want fully quoted", "a\nb; reboot", q)
	}
}

// ---- NIC / log parsing --------------------------------------------------

func TestParseNICLine(t *testing.T) {
	n, ok := parseNICLine("N|eth0|up|0x1003|00:11:22:33:44:55|1500|1000|0||0|10.0.0.5/24")
	if !ok {
		t.Fatal("parseNICLine rejected a well-formed row")
	}
	if n.Name != "eth0" || !n.Up || n.Loopback || n.SpeedMb != 1000 || n.MTU != 1500 ||
		n.MAC != "00:11:22:33:44:55" || n.IPv4 != "10.0.0.5/24" || n.Virtual || n.IsMaster || n.Master != "" {
		t.Errorf("parseNICLine gave %+v", n)
	}

	// Loopback: IFF_LOOPBACK (0x8) set, "unknown" operstate, no speed.
	lo, ok := parseNICLine("N|lo|unknown|0x9|00:00:00:00:00:00|65536|-1|1||0|127.0.0.1/8")
	if !ok || !lo.Loopback || !lo.Up || lo.SpeedMb != -1 || !lo.Virtual {
		t.Errorf("loopback row gave %+v (ok=%v)", lo, ok)
	}

	// A bond slave reports its master; the master reports isMaster.
	sl, ok := parseNICLine("N|ens192|up|0x1103|00:11:22:33:44:66|1500|10000|0|bond0|0|")
	if !ok || sl.Master != "bond0" || sl.IsMaster {
		t.Errorf("slave row gave %+v (ok=%v)", sl, ok)
	}
	ma, ok := parseNICLine("N|bond0|up|0x1103|00:11:22:33:44:66|1500|20000|1||1|10.0.0.9/24")
	if !ok || !ma.IsMaster || ma.Master != "" {
		t.Errorf("master row gave %+v (ok=%v)", ma, ok)
	}

	// A down interface.
	dn, ok := parseNICLine("N|eth9|down|0x1002|00:11:22:33:44:77|1500|-1|0||0|")
	if !ok || dn.Up {
		t.Errorf("down row gave %+v (ok=%v)", dn, ok)
	}

	// Rows we must drop rather than offer to the user.
	bad := []string{
		"", "eth0|up", "N|eth0|up|0x1003", // truncated
		"N|eth 0|up|0x1003|m|1500|1000|0||0|",      // invalid name
		"N|eth0;id|up|0x1003|m|1500|1000|0||0|",    // injection in the name
		"N|abcdefghijklmnop|up|0x1|m|1500|1|0||0|", // name too long
		"X|eth0|up|0x1003|m|1500|1000|0||0|",       // wrong prefix
	}
	for _, line := range bad {
		if _, ok := parseNICLine(line); ok {
			t.Errorf("parseNICLine(%q) accepted a bad row", line)
		}
	}
	// A "|" inside the trailing ipv4 field must not shift the columns.
	odd, ok := parseNICLine("N|eth1|up|0x1003|00:11:22:33:44:88|1500|1000|0||0|10.0.0.6/24|junk")
	if !ok || odd.Name != "eth1" || odd.MTU != 1500 {
		t.Errorf("trailing-pipe row gave %+v (ok=%v)", odd, ok)
	}
	// A master name that would not survive validIfName is dropped, not carried.
	inj, ok := parseNICLine("N|eth2|up|0x1003|m|1500|1000|0|bond0;id|0|")
	if !ok || inj.Master != "" {
		t.Errorf("injected master gave %+v (ok=%v)", inj, ok)
	}
}

func TestParseCapLog(t *testing.T) {
	// RHEL 9 / tcpdump 4.99 — full counter set.
	rhel9 := "tcpdump: listening on eth0, link-type EN10MB (Ethernet), snapshot length 262144 bytes\n" +
		"1234 packets captured\n1240 packets received by filter\n0 packets dropped by kernel\n" +
		"6 packets dropped by interface\n"
	c := parseCapLog(rhel9)
	if c.Captured != 1234 || c.Received != 1240 || c.DroppedKern != 0 || c.DroppedIf != 6 {
		t.Errorf("rhel9 counters = %+v", c)
	}
	if c.LinkType != "EN10MB" || c.Err != "" {
		t.Errorf("rhel9 linktype/err = %q / %q", c.LinkType, c.Err)
	}

	// RHEL 8 / tcpdump 4.9 — no "dropped by interface" line at all: it must stay
	// -1 (N/A) instead of becoming a misleading 0.
	rhel8 := "tcpdump: listening on ens192, link-type EN10MB (Ethernet), capture size 262144 bytes\n" +
		"55 packets captured\n55 packets received by filter\n0 packets dropped by kernel\n"
	c = parseCapLog(rhel8)
	if c.DroppedIf != -1 {
		t.Errorf("rhel8 DroppedIf = %d, want -1", c.DroppedIf)
	}
	if c.Captured != 55 || c.DroppedKern != 0 {
		t.Errorf("rhel8 counters = %+v", c)
	}

	// -i any records Linux cooked capture; phase 2 branches on this.
	if c := parseCapLog("tcpdump: listening on any, link-type LINUX_SLL2 (Linux cooked v2), snapshot length 262144 bytes\n"); c.LinkType != "LINUX_SLL2" {
		t.Errorf("any linktype = %q", c.LinkType)
	}

	// Error-only logs: no counters, one error surfaced.
	for _, in := range []string{
		"tcpdump: eth99: No such device exists\n",
		"tcpdump: syntax error in filter expression\n",
		"rtm-pcapd: tcpdump: not found (PATH, /usr/sbin, /sbin, /usr/bin)\n",
	} {
		c := parseCapLog(in)
		if c.Err == "" {
			t.Errorf("parseCapLog(%q).Err is empty", in)
		}
		if c.Captured != -1 {
			t.Errorf("parseCapLog(%q).Captured = %d, want -1", in, c.Captured)
		}
	}

	// Empty log: everything unknown, nothing invented.
	if c := parseCapLog(""); c.Captured != -1 || c.Received != -1 || c.DroppedKern != -1 ||
		c.DroppedIf != -1 || c.Err != "" || c.LinkType != "" {
		t.Errorf("empty log gave %+v", c)
	}

	// The normal chatter must never be reported as an error.
	noise := "tcpdump: verbose output suppressed, use -v[v]... for full protocol decode\n" +
		"tcpdump: listening on eth0, link-type EN10MB (Ethernet), snapshot length 96 bytes\n" +
		"Warning: assuming Ethernet\n"
	if c := parseCapLog(noise); c.Err != "" {
		t.Errorf("noise reported as error: %q", c.Err)
	}

	// CRLF must not defeat the counter parse.
	if c := parseCapLog("12 packets captured\r\n13 packets received by filter\r\n"); c.Captured != 12 || c.Received != 13 {
		t.Errorf("CRLF log gave %+v", c)
	}
}

// ---- status mapping -----------------------------------------------------

func TestCapStatusFrom(t *testing.T) {
	cases := []struct {
		alive     int
		haveDone  bool
		haveStop  bool
		reason    string
		want      string
		uncertain bool
	}{
		{1, false, false, "", "running", false},
		{2, false, false, "", "running", true}, // hidepid: alive but unverifiable
		{1, false, true, "", "stopping", false},
		{2, false, true, "", "stopping", true},
		{0, false, false, "", "interrupted", false}, // reboot / OOM / SIGKILL
		{0, false, true, "", "interrupted", false},  // asked to stop, wrapper died first
		{0, true, false, "signal", "stopped", false},
		{0, true, false, "signal-forced", "stopped", false},
		{0, true, false, "deadline", "deadline", false},
		{0, true, false, "maxsize", "maxsize", false},
		{0, true, false, "maxpackets", "maxpackets", false},
		{0, true, false, "low-disk", "low-disk", false},
		{1, true, false, "orphan", "orphan", false},
		{0, true, false, "error", "error", false},
		{0, true, false, "no-tcpdump", "error", false},
		{0, true, false, "done", "done", false},
		{0, true, false, "something-new", "done", false},
	}
	for _, c := range cases {
		got, unc := capStatusFrom(c.alive, c.haveDone, c.haveStop, c.reason)
		if got != c.want || unc != c.uncertain {
			t.Errorf("capStatusFrom(%d,%v,%v,%q) = (%q,%v), want (%q,%v)",
				c.alive, c.haveDone, c.haveStop, c.reason, got, unc, c.want, c.uncertain)
		}
	}
}

// ---- request clamping ---------------------------------------------------

func TestClampCapRequest(t *testing.T) {
	// Zero/negative fall back to defaults; a 0 packet cap stays 0 (unlimited).
	r := clampCapRequest(CapRequest{})
	if r.MaxSec != DefaultCaptureSeconds || r.MaxMB != DefaultCaptureMB ||
		r.MinFreeMB != DefaultMinFreeMB || r.MaxPackets != 0 || r.SnapLen != 0 {
		t.Errorf("zero request clamped to %+v", r)
	}
	r = clampCapRequest(CapRequest{MaxSec: -5, MaxMB: -1, MinFreeMB: -1, MaxPackets: -9, SnapLen: -3})
	if r.MaxSec != DefaultCaptureSeconds || r.MaxMB != DefaultCaptureMB ||
		r.MinFreeMB != DefaultMinFreeMB || r.MaxPackets != 0 || r.SnapLen != 0 {
		t.Errorf("negative request clamped to %+v", r)
	}
	// Over the ceiling in every dimension.
	r = clampCapRequest(CapRequest{
		MaxSec: 999999, MaxMB: 999999, MinFreeMB: 10, MaxPackets: 1 << 30, SnapLen: 999999,
	})
	if r.MaxSec != MaxCaptureSeconds || r.MaxMB != MaxCaptureMB ||
		r.MinFreeMB != MinMinFreeMB || r.MaxPackets != MaxCapturePackets || r.SnapLen != 262144 {
		t.Errorf("over-cap request clamped to %+v", r)
	}
	// A snaplen below the floor is raised, presets pass through untouched.
	if got := clampCapRequest(CapRequest{SnapLen: 1}).SnapLen; got != 64 {
		t.Errorf("SnapLen 1 -> %d, want 64", got)
	}
	for _, s := range []int{96, 256, 65535} {
		if got := clampCapRequest(CapRequest{SnapLen: s}).SnapLen; got != s {
			t.Errorf("SnapLen %d -> %d", s, got)
		}
	}
	// Iface / TargetDir are trimmed so " eth0 " cannot fail validIfName later.
	r = clampCapRequest(CapRequest{Iface: "  eth0 ", TargetDir: " /data "})
	if r.Iface != "eth0" || r.TargetDir != "/data" {
		t.Errorf("trim gave %q / %q", r.Iface, r.TargetDir)
	}
}
