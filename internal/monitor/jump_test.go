package monitor

import (
	"strings"
	"testing"

	"rtaskmgr/internal/host"
)

// resolverFor builds a HostResolver over a fixed set of hosts.
func resolverFor(hs ...host.Host) HostResolver {
	m := map[string]host.Host{}
	for _, h := range hs {
		m[h.ID] = h
	}
	return func(id string) (host.Host, bool) {
		h, ok := m[id]
		return h, ok
	}
}

// routes renders the enumerated paths as "a>b>c|a>d" for compact assertions.
func routes(paths [][]host.Host) string {
	out := make([]string, len(paths))
	for i, p := range paths {
		names := make([]string, len(p))
		for j, h := range p {
			names[j] = h.Name
		}
		out[i] = strings.Join(names, ">")
	}
	return strings.Join(out, "|")
}

func TestJumpPathsDirect(t *testing.T) {
	h := host.Host{ID: "a", Name: "target"}
	paths, err := jumpPaths(h, nil)
	if err != nil {
		t.Fatalf("direct host: %v", err)
	}
	if got := routes(paths); got != "target" {
		t.Errorf("routes = %q, want %q", got, "target")
	}
}

func TestJumpPathsSingleCandidate(t *testing.T) {
	bastion := host.Host{ID: "b", Name: "server_1"}
	target := host.Host{ID: "t", Name: "server_3", JumpHostIDs: []string{"b"}}
	paths, err := jumpPaths(target, resolverFor(bastion, target))
	if err != nil {
		t.Fatalf("single candidate: %v", err)
	}
	// The outermost hop is dialled FIRST, so it comes first in the path.
	if got := routes(paths); got != "server_1>server_3" {
		t.Errorf("routes = %q, want %q", got, "server_1>server_3")
	}
	if got := pathLabel(paths[0]); got != "server_1" {
		t.Errorf("pathLabel = %q", got)
	}
}

// The case from the field: server_3 is reachable from server_1 OR server_2, and
// which one works is not knowable in advance. Both routes must be offered, in the
// order the operator listed them.
func TestJumpPathsFallbackOrder(t *testing.T) {
	s1 := host.Host{ID: "1", Name: "server_1"}
	s2 := host.Host{ID: "2", Name: "server_2"}
	s3 := host.Host{ID: "3", Name: "server_3", JumpHostIDs: []string{"1", "2"}}
	paths, err := jumpPaths(s3, resolverFor(s1, s2, s3))
	if err != nil {
		t.Fatalf("two candidates: %v", err)
	}
	if got := routes(paths); got != "server_1>server_3|server_2>server_3" {
		t.Errorf("routes = %q, want server_1 first then server_2", got)
	}
	// Reversing the configured order must reverse the attempt order.
	s3.JumpHostIDs = []string{"2", "1"}
	paths, _ = jumpPaths(s3, resolverFor(s1, s2, s3))
	if got := routes(paths); got != "server_2>server_3|server_1>server_3" {
		t.Errorf("reversed routes = %q", got)
	}
}

// A candidate that is itself only reachable via a jump contributes a longer route,
// and the alternatives multiply — which is why the breadth cap exists.
func TestJumpPathsNestedCandidates(t *testing.T) {
	edge1 := host.Host{ID: "e1", Name: "edge1"}
	edge2 := host.Host{ID: "e2", Name: "edge2"}
	mid := host.Host{ID: "m", Name: "mid", JumpHostIDs: []string{"e1", "e2"}}
	target := host.Host{ID: "t", Name: "target", JumpHostIDs: []string{"m"}}
	paths, err := jumpPaths(target, resolverFor(edge1, edge2, mid, target))
	if err != nil {
		t.Fatalf("nested: %v", err)
	}
	if got := routes(paths); got != "edge1>mid>target|edge2>mid>target" {
		t.Errorf("routes = %q", got)
	}
}

func TestJumpPathsBreadthCap(t *testing.T) {
	// One target with many candidates, each reachable two ways → far more than the cap.
	all := []host.Host{}
	var midIDs []string
	for i := 0; i < 6; i++ {
		e1 := host.Host{ID: "e" + string(rune('a'+i)) + "1", Name: "e1"}
		e2 := host.Host{ID: "e" + string(rune('a'+i)) + "2", Name: "e2"}
		mid := host.Host{ID: "m" + string(rune('a'+i)), Name: "m", JumpHostIDs: []string{e1.ID, e2.ID}}
		all = append(all, e1, e2, mid)
		midIDs = append(midIDs, mid.ID)
	}
	target := host.Host{ID: "t", Name: "target", JumpHostIDs: midIDs}
	all = append(all, target)
	paths, err := jumpPaths(target, resolverFor(all...))
	if err != nil {
		t.Fatalf("breadth: %v", err)
	}
	if len(paths) > maxJumpPaths {
		t.Errorf("enumerated %d paths, cap is %d", len(paths), maxJumpPaths)
	}
	if len(paths) == 0 {
		t.Error("no paths enumerated")
	}
}

// A circular jump configuration is trivially easy to create in the UI. Without the
// cycle check this would recurse until it exhausted sockets, and each attempt
// would open real SSH connections on the way.
func TestJumpPathsRejectsCycles(t *testing.T) {
	a := host.Host{ID: "a", Name: "A", JumpHostIDs: []string{"b"}}
	b := host.Host{ID: "b", Name: "B", JumpHostIDs: []string{"a"}}
	if _, err := jumpPaths(a, resolverFor(a, b)); err == nil {
		t.Error("A→B→A cycle was accepted")
	}
	s := host.Host{ID: "s", Name: "S", JumpHostIDs: []string{"s"}}
	if _, err := jumpPaths(s, resolverFor(s)); err == nil {
		t.Error("self-referencing jump was accepted")
	}
	x := host.Host{ID: "x", Name: "X", JumpHostIDs: []string{"y"}}
	y := host.Host{ID: "y", Name: "Y", JumpHostIDs: []string{"z"}}
	z := host.Host{ID: "z", Name: "Z", JumpHostIDs: []string{"y"}}
	if _, err := jumpPaths(x, resolverFor(x, y, z)); err == nil {
		t.Error("Y↔Z loop reached from X was accepted")
	}
}

// One bad candidate must not poison a good one: server_1 deleted should still
// leave server_2 usable.
func TestJumpPathsSkipsBadCandidates(t *testing.T) {
	s2 := host.Host{ID: "2", Name: "server_2"}
	s3 := host.Host{ID: "3", Name: "server_3", JumpHostIDs: []string{"gone", "2"}}
	paths, err := jumpPaths(s3, resolverFor(s2, s3))
	if err != nil {
		t.Fatalf("one deleted candidate: %v", err)
	}
	if got := routes(paths); got != "server_2>server_3" {
		t.Errorf("routes = %q, want the surviving candidate only", got)
	}
}

func TestJumpPathsRejectsTooManyHops(t *testing.T) {
	var hs []host.Host
	n := maxJumpHops + 3
	for i := 0; i < n; i++ {
		h := host.Host{ID: string(rune('a' + i)), Name: string(rune('A' + i))}
		if i > 0 {
			h.JumpHostIDs = []string{string(rune('a' + i - 1))}
		}
		hs = append(hs, h)
	}
	if _, err := jumpPaths(hs[n-1], resolverFor(hs...)); err == nil {
		t.Errorf("a chain of %d hops was accepted (max %d)", n, maxJumpHops)
	}
}

// A host marked as jump-only must fail loudly when no candidate resolves. Falling
// back to a direct dial would either hang or, on a network where the address
// happens to resolve, land somewhere the operator did not intend — and in the
// no-firewall test setup it would silently bypass the very path being tested.
func TestJumpPathsNeverFallsBackToDirect(t *testing.T) {
	target := host.Host{ID: "t", Name: "target", JumpHostIDs: []string{"gone"}}
	if paths, err := jumpPaths(target, resolverFor(target)); err == nil {
		t.Errorf("a deleted jump host silently connected directly: %v", routes(paths))
	}
	if _, err := jumpPaths(target, nil); err == nil {
		t.Error("a nil resolver silently connected directly")
	}
	bad := func(string) (host.Host, bool) { return host.Host{}, true }
	if _, err := jumpPaths(target, bad); err == nil {
		t.Error("an empty jump host record was accepted")
	}
}

// The single-value form from an older hosts.json must keep working.
func TestJumpCandidatesMigration(t *testing.T) {
	old := host.Host{ID: "t", JumpHostID: "b"}
	if got := old.JumpCandidates(); len(got) != 1 || got[0] != "b" {
		t.Errorf("legacy jumpHostId = %v", got)
	}
	// The list wins when both are present.
	both := host.Host{ID: "t", JumpHostID: "old", JumpHostIDs: []string{"new1", "new2"}}
	if got := both.JumpCandidates(); strings.Join(got, ",") != "new1,new2" {
		t.Errorf("list should win: %v", got)
	}
	// Empty entries are dropped rather than becoming an unresolvable candidate.
	blanks := host.Host{ID: "t", JumpHostIDs: []string{"", "x", ""}}
	if got := blanks.JumpCandidates(); strings.Join(got, ",") != "x" {
		t.Errorf("blank ids should be dropped: %v", got)
	}
	if got := (host.Host{ID: "t"}).JumpCandidates(); len(got) != 0 {
		t.Errorf("no jump = no candidates, got %v", got)
	}
}

func TestHostPort(t *testing.T) {
	if got := hostPort(host.Host{Addr: "10.0.0.5"}); got != "10.0.0.5:22" {
		t.Errorf("default port: %q", got)
	}
	if got := hostPort(host.Host{Addr: "10.0.0.5", Port: 2222}); got != "10.0.0.5:2222" {
		t.Errorf("explicit port: %q", got)
	}
}

func TestClientConfigRequiresCredentials(t *testing.T) {
	if _, err := clientConfig(host.Host{Addr: "10.0.0.5"}); err == nil {
		t.Error("a host with no user was accepted")
	}
	if _, err := clientConfig(host.Host{Addr: "10.0.0.5", User: "liz"}); err == nil {
		t.Error("a host with neither password nor key was accepted")
	}
	cfg, err := clientConfig(host.Host{Addr: "10.0.0.5", User: "liz", Password: "x"})
	if err != nil {
		t.Fatalf("password auth: %v", err)
	}
	if cfg.User != "liz" || len(cfg.Auth) != 1 {
		t.Errorf("config = %+v", cfg)
	}
}
