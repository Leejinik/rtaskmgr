package statsreg

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Registration patterns remember how the user named and configured each kind
// of key, so the next inspection (on any environment) starts from the same
// choices. A kind of key is its shape: every {…} placeholder replaced by {*},
// e.g. liz.stats.cachedb.{server01}.cpu -> liz.stats.cachedb.{*}.cpu.
//
// Names are stored as templates, resolved per device when applied:
//
//	{Host} hostname with its first letter upper-cased  server01 -> Server01
//	{host} hostname as is                              server01
//	{n}    number at the end of the hostname           server01 -> 1, server_3 -> 3
//	{t1}…  value of the key's 1st, 2nd … placeholder   liz.stats.server.{server01}.{eth0}… -> {t2}=eth0
//
// The file keeps a change history; the rules themselves are documented in
// docs/stats-name-patterns.md, whose change log must be updated with them.

type NamePattern struct {
	Name      string `json:"name"`
	Format    int    `json:"format,omitempty"`   // only for keys without a driver template
	Interval  int    `json:"interval,omitempty"` // ms
	Selected  *bool  `json:"selected,omitempty"` // nil: not learned
	Samples   int    `json:"samples"`            // devices the last learning agreed on
	UpdatedAt string `json:"updatedAt"`
	Source    string `json:"source"`
}
type PatternChange struct {
	Shape string `json:"shape"`
	Field string `json:"field"`
	From  string `json:"from"`
	To    string `json:"to"`
}
type PatternEvent struct {
	At      string          `json:"at"`
	Source  string          `json:"source"`
	Notes   []string        `json:"notes,omitempty"`
	Changes []PatternChange `json:"changes"`
}
type patternFile struct {
	Version  int                    `json:"version"`
	Patterns map[string]NamePattern `json:"patterns"`
	// Placements: first placeholder of a key -> hostname it was registered on.
	// Only used for keys the server lookup could not place (e.g. {lizlogdb01}),
	// and only when that hostname exists in the environment being inspected.
	Placements map[string]string `json:"placements,omitempty"`
	History    []PatternEvent    `json:"history"`
}

var placeholder = regexp.MustCompile(`\{([^{}]+)\}`)

func keyShape(key string) string { return placeholder.ReplaceAllString(key, "{*}") }

func keyTokens(key string) []string {
	out := []string{}
	for _, m := range placeholder.FindAllStringSubmatch(key, -1) {
		out = append(out, m[1])
	}
	return out
}

var trailingNumber = regexp.MustCompile(`(\d+)$`)

func hostOrdinal(host string) (int, bool) {
	m := trailingNumber.FindStringSubmatch(host)
	if m == nil {
		return 0, false
	}
	n, e := strconv.Atoi(m[1])
	return n, e == nil
}

func titleHost(host string) string {
	r := []rune(host)
	if len(r) == 0 {
		return host
	}
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// generalize turns a concrete name into a template for this key and device.
func generalize(name, key, host string) string {
	out := name
	if host != "" && host != "common" {
		out = strings.ReplaceAll(out, titleHost(host), "{Host}")
		out = strings.ReplaceAll(out, host, "{host}")
	}
	for i, t := range keyTokens(key) {
		if t != host && len(t) > 1 && !isDigits(t) {
			out = strings.ReplaceAll(out, t, fmt.Sprintf("{t%d}", i+1))
		}
	}
	if n, ok := hostOrdinal(host); ok && host != "common" {
		num := regexp.MustCompile(`(^|[^0-9{])` + strconv.Itoa(n) + `($|[^0-9}])`)
		if loc := num.FindStringSubmatchIndex(out); loc != nil {
			out = out[:loc[3]] + "{n}" + out[loc[4]:]
		}
	}
	return out
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// render resolves a template for this key and device; ok=false if a
// placeholder cannot be filled (then the default name is kept).
func render(tmpl, key, host string) (string, bool) {
	out := tmpl
	if strings.Contains(out, "{Host}") || strings.Contains(out, "{host}") {
		if host == "" || host == "common" {
			return "", false
		}
		out = strings.ReplaceAll(out, "{Host}", titleHost(host))
		out = strings.ReplaceAll(out, "{host}", host)
	}
	if strings.Contains(out, "{n}") {
		n, ok := hostOrdinal(host)
		if !ok {
			return "", false
		}
		out = strings.ReplaceAll(out, "{n}", strconv.Itoa(n))
	}
	for i, t := range keyTokens(key) {
		out = strings.ReplaceAll(out, fmt.Sprintf("{t%d}", i+1), t)
	}
	if regexp.MustCompile(`\{(Host|host|n|t\d+)\}`).MatchString(out) {
		return "", false
	}
	return out, true
}

func (s *Service) patternPath() (string, error) {
	if s.PatternFile != "" {
		return s.PatternFile, nil
	}
	home, e := os.UserHomeDir()
	if e != nil {
		return "", e
	}
	return filepath.Join(home, ".rtaskmgr", "stats-patterns.json"), nil
}

func (s *Service) loadPatterns() (patternFile, error) {
	f := patternFile{Version: 1, Patterns: map[string]NamePattern{}, Placements: map[string]string{}, History: []PatternEvent{}}
	path, e := s.patternPath()
	if e != nil {
		return f, e
	}
	data, e := os.ReadFile(path)
	if errors.Is(e, os.ErrNotExist) {
		return f, nil
	}
	if e != nil {
		return f, e
	}
	if e = json.Unmarshal(data, &f); e != nil {
		return f, fmt.Errorf("패턴 파일을 읽을 수 없습니다(%s): %w", path, e)
	}
	if f.Patterns == nil {
		f.Patterns = map[string]NamePattern{}
	}
	if f.Placements == nil {
		f.Placements = map[string]string{}
	}
	return f, nil
}

func (s *Service) savePatterns(f patternFile) error {
	path, e := s.patternPath()
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	data, e := json.MarshalIndent(f, "", "  ")
	if e != nil {
		return e
	}
	tmp, e := os.CreateTemp(filepath.Dir(path), ".patterns-*")
	if e != nil {
		return e
	}
	defer os.Remove(tmp.Name())
	if _, e = tmp.Write(data); e != nil {
		tmp.Close()
		return e
	}
	if e = tmp.Close(); e != nil {
		return e
	}
	return os.Rename(tmp.Name(), path)
}

// applyPatterns pre-fills points from remembered patterns. Only points placed
// on a device are touched; a template's data format is never overridden.
func (s *Service) applyPatterns(p *Plan) {
	f, e := s.loadPatterns()
	if e != nil {
		p.Warnings = append(p.Warnings, e.Error())
		return
	}
	if len(f.Patterns) == 0 && len(f.Placements) == 0 {
		return
	}
	hosts := map[string]bool{}
	for _, v := range p.Servers {
		hosts[v.Hostname] = true
	}
	applied, placed := 0, 0
	for i := range p.Points {
		pt := &p.Points[i]
		if toks := keyTokens(pt.Key); pt.Host == "" && len(toks) > 0 {
			if h := f.Placements[toks[0]]; h != "" && hosts[h] {
				pt.Host = h
				pt.Warning = strings.TrimSpace(strings.Replace(pt.Warning, "· 서버를 찾을 수 없음: 배치 선택 또는 제외", "", 1) + " · 기억된 배치: " + h)
				placed++
			}
		}
		np, ok := f.Patterns[keyShape(pt.Key)]
		if !ok || pt.Host == "" {
			continue
		}
		if name, ok := render(np.Name, pt.Key, pt.Host); ok && name != "" && len([]rune(name)) <= 100 {
			pt.Name = name
		}
		if pt.TemplateID == 0 && (np.Format == 3 || np.Format == 4) {
			pt.Format = np.Format
		}
		if np.Interval >= 1000 {
			pt.Interval = np.Interval
		}
		if np.Selected != nil {
			pt.Selected = *np.Selected
		}
		pt.Remembered = true
		applied++
	}
	if applied > 0 || placed > 0 {
		p.PatternInfo = fmt.Sprintf("기억된 등록 패턴 %d개 적용(키 모양 %d종 저장됨), 기억된 서버 배치 %d개 적용. 바꾼 값은 등록 성공 시 다시 기억합니다.", applied, len(f.Patterns), placed)
	}
}

// learnPatterns records the choices of a successful registration. points may
// include unselected ones (then Selected=false is learned for placed keys).
// Devices of one shape that disagree are resolved by majority and noted.
func (s *Service) learnPatterns(points []Point, source string) (PatternEvent, error) {
	f, e := s.loadPatterns()
	if e != nil {
		return PatternEvent{}, e
	}
	type obs struct {
		name, host string
		format     int
		interval   int
		selected   bool
		template   bool
	}
	byShape := map[string][]obs{}
	now := time.Now().Format(time.RFC3339)
	ev := PatternEvent{At: now, Source: source, Changes: []PatternChange{}}
	for _, p := range points {
		if p.Host == "" {
			continue
		}
		if toks := keyTokens(p.Key); p.Selected && p.Host != "common" && len(toks) > 0 && toks[0] != p.Host && !isDigits(toks[0]) {
			if old := f.Placements[toks[0]]; old != p.Host {
				ev.Changes = append(ev.Changes, PatternChange{"{" + toks[0] + "}", "placement", old, p.Host})
				f.Placements[toks[0]] = p.Host
			}
		}
		byShape[keyShape(p.Key)] = append(byShape[keyShape(p.Key)], obs{generalize(p.Name, p.Key, p.Host), p.Host, p.Format, p.Interval, p.Selected, p.TemplateID != 0})
	}
	shapes := make([]string, 0, len(byShape))
	for k := range byShape {
		shapes = append(shapes, k)
	}
	sort.Strings(shapes)
	for _, shape := range shapes {
		list := byShape[shape]
		selected := []obs{}
		for _, o := range list {
			if o.selected {
				selected = append(selected, o)
			}
		}
		old, had := f.Patterns[shape]
		np := old
		np.UpdatedAt, np.Source = now, source
		sel := len(selected) > 0
		np.Selected = &sel
		if sel {
			// Name: majority template over the selected devices.
			count := map[string]int{}
			for _, o := range selected {
				count[o.name]++
			}
			best := ""
			for n, c := range count {
				if c > count[best] || (c == count[best] && n < best) {
					best = n
				}
			}
			np.Name, np.Samples = best, count[best]
			if len(count) > 1 {
				odd := []string{}
				for _, o := range selected {
					if o.name != best {
						odd = append(odd, fmt.Sprintf("%s=%q", o.host, o.name))
					}
				}
				ev.Notes = append(ev.Notes, fmt.Sprintf("%s: 장비마다 이름이 달라 다수(%d/%d) %q 채택, 다른 이름 %s", shape, count[best], len(selected), best, strings.Join(odd, ", ")))
			}
			np.Interval = selected[0].interval
			if !selected[0].template {
				np.Format = selected[0].format
			}
		}
		change := func(field, from, to string) {
			if from != to {
				ev.Changes = append(ev.Changes, PatternChange{shape, field, from, to})
			}
		}
		if !had {
			to := np.Name
			if !sel {
				to = "(선택 안 함)"
			}
			change("new", "", to)
		} else {
			change("name", old.Name, np.Name)
			change("format", strconv.Itoa(old.Format), strconv.Itoa(np.Format))
			change("interval", strconv.Itoa(old.Interval), strconv.Itoa(np.Interval))
			change("selected", boolText(old.Selected), boolText(np.Selected))
		}
		f.Patterns[shape] = np
	}
	if len(ev.Changes) == 0 && len(ev.Notes) == 0 {
		return ev, nil
	}
	f.History = append(f.History, ev)
	return ev, s.savePatterns(f)
}

func boolText(b *bool) string {
	if b == nil {
		return ""
	}
	return strconv.FormatBool(*b)
}

// ImportPatterns learns from an earlier registration's receipt (selected points only).
func (s *Service) ImportPatterns(id string) (PatternEvent, error) {
	path, e := s.receiptPath(id)
	if e != nil {
		return PatternEvent{}, e
	}
	data, e := os.ReadFile(path)
	if e != nil {
		return PatternEvent{}, e
	}
	var rec receipt
	if e = json.Unmarshal(data, &rec); e != nil {
		return PatternEvent{}, e
	}
	if !rec.Result.Committed || len(rec.Points) == 0 {
		return PatternEvent{}, fmt.Errorf("완료된 2단계 등록 기록이 아닙니다")
	}
	return s.learnPatterns(rec.Points, "receipt "+id)
}
