// Package statsreg registers existing MK119 Redis statistics as devices.
package statsreg

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type Config struct {
	DBHost        string `json:"dbHost"`
	DBPort        int    `json:"dbPort"`
	DBUser        string `json:"dbUser"`
	DBPassword    string `json:"dbPassword"`
	RedisHost     string `json:"redisHost"`
	RedisPort     int    `json:"redisPort"`
	RedisUser     string `json:"redisUser"`
	RedisPassword string `json:"redisPassword"`
	RedisDB       int    `json:"redisDB"`
	Brokers       string `json:"brokers"`
	KafkaSecurity string `json:"kafkaSecurity"`
	KafkaUser     string `json:"kafkaUser"`
	KafkaPassword string `json:"kafkaPassword"`
	KafkaHosts    string `json:"kafkaHosts"`
}
type Option struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}
type Server struct {
	Hostname string `json:"hostname"`
	Name     string `json:"name"`
	IP       string `json:"ip"`
}
type Point struct {
	Key        string `json:"key"`
	Name       string `json:"name"`
	Host       string `json:"host"`
	TemplateID int64  `json:"templateId"`
	Format     int    `json:"format"`
	Measure    string `json:"measure"`
	Interval   int    `json:"interval"`
	Warning    string `json:"warning"`
	Selected   bool   `json:"selected"`
	// Remembered: pre-filled from a registration pattern.
	Remembered bool `json:"remembered"`
	// Registered: already a live checkpoint of this driver (RegisteredOn = "group / device").
	Registered   bool   `json:"registered"`
	RegisteredOn string `json:"registeredOn"`
	// Moved: registered on a device of another host than the key is placed on now (report only).
	Moved bool `json:"moved"`
	// Deleted: a checkpoint with this key existed and was deleted (flag<>1); information only.
	Deleted bool `json:"deleted"`
}
type Plan struct {
	ID          string          `json:"id"`
	Servers     []Server        `json:"servers"`
	Points      []Point         `json:"points"`
	Explorers   []Option        `json:"explorers"`
	Clusters    []Option        `json:"clusters"`
	GroupName   string          `json:"groupName"`
	Warnings    []string        `json:"warnings"`
	RedisInfo   string          `json:"redisInfo"`
	PatternInfo string          `json:"patternInfo"`
	Existing    []ExistingGroup `json:"existing"`
}
type Selection struct {
	PlanID     string   `json:"planId"`
	GroupName  string   `json:"groupName"`
	ExplorerID int64    `json:"explorerId"`
	ClusterID  int64    `json:"clusterId"`
	Servers    []Server `json:"servers"`
	Points     []Point  `json:"points"`
	// UpdateGroupID > 0: add only unregistered keys to this existing group.
	UpdateGroupID int64 `json:"updateGroupId"`
}
type module struct {
	ID   int64
	Host string
}
type template struct {
	ID                     int64
	Name, Pattern, Measure string
	Format, Interval       int
}

var braces = regexp.MustCompile(`\{([^{}]+)\}`)

// Match literal punctuation and substitute only brace-delimited placeholders.
func matchTemplate(pattern, key string) (map[string]string, bool) {
	indices := braces.FindAllStringSubmatchIndex(pattern, -1)
	var expr strings.Builder
	expr.WriteString("^")
	last := 0
	for _, ix := range indices {
		expr.WriteString(regexp.QuoteMeta(pattern[last:ix[0]]))
		expr.WriteString(`\{([^{}]+)\}`)
		last = ix[1]
	}
	expr.WriteString(regexp.QuoteMeta(pattern[last:]))
	expr.WriteString("$")
	m := regexp.MustCompile(expr.String()).FindStringSubmatch(key)
	if m == nil {
		return nil, false
	}
	values := map[string]string{}
	for i, ix := range indices {
		name := pattern[ix[2]:ix[3]]
		if old, ok := values[name]; ok && old != m[i+1] {
			return nil, false
		}
		values[name] = m[i+1]
	}
	return values, true
}

func makePoints(values map[string]string, servers []Server, modules []module, templates []template) []Point {
	hosts := map[string]string{}
	for _, s := range servers {
		hosts[strings.ToLower(s.Hostname)] = s.Hostname
		if s.IP != "" {
			hosts[strings.ToLower(s.IP)] = s.Hostname
		}
	}
	mods := map[string]string{}
	for _, m := range modules {
		mods[strconv.FormatInt(m.ID, 10)] = m.Host
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		if strings.HasPrefix(k, "liz.stats.") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	points := make([]Point, 0, len(keys))
	for _, key := range keys {
		p := Point{Key: key, Name: strings.TrimPrefix(key, "liz.stats."), Format: 4, Interval: 10000, Selected: true}
		if _, err := strconv.ParseFloat(values[key], 64); err == nil {
			p.Format = 3
		}
		var captures map[string]string
		matches := 0
		for _, t := range templates {
			if c, ok := matchTemplate(t.Pattern, key); ok {
				matches++
				captures = c
				p.TemplateID = t.ID
				p.Name = t.Name
				p.Format = t.Format
				p.Measure = t.Measure
				p.Interval = t.Interval
			}
		}
		if matches > 1 {
			p.Warning = "대응하는 드라이버 템플릿이 여러 개입니다"
			p.Selected = false
			p.TemplateID = 0
		}
		if matches == 0 {
			p.Warning = "드라이버 템플릿 없음: 직접 키 등록 (자료형 확인)"
		}
		if p.Interval <= 0 {
			p.Interval = 10000
		}
		tokens := braces.FindAllStringSubmatch(key, -1)
		if len(tokens) == 0 {
			p.Host = "common"
		} else {
			if id := captures["module_id"]; id != "" {
				p.Host = mods[id]
			} else if h := captures["hostname"]; h != "" {
				p.Host = hosts[strings.ToLower(h)]
			} else {
				first := tokens[0][1]
				p.Host = hosts[strings.ToLower(first)]
				if p.Host == "" {
					p.Host = mods[first]
				}
			}
			if p.Host == "" {
				p.Warning = strings.TrimSpace(p.Warning + " · 서버를 찾을 수 없음: 배치 선택 또는 제외")
				p.Selected = false
			}
		}
		if len([]rune(p.Name)) > 100 {
			p.Name = string([]rune(p.Name)[:100])
		}
		points = append(points, p)
	}
	return points
}

func validateSelection(plan Plan, s Selection) error {
	validName := func(n string) bool {
		return strings.TrimSpace(n) != "" && n == strings.TrimSpace(n) && len([]rune(n)) <= 100
	}
	upd, e := updateTarget(plan, s)
	if e != nil {
		return e
	}
	if upd == nil && !validName(s.GroupName) {
		return fmt.Errorf("그룹 이름은 앞뒤 공백 없이 1~100자로 입력하세요: %q", s.GroupName)
	}
	known := map[string]bool{"common": true}
	for _, v := range plan.Servers {
		known[v.Hostname] = true
	}
	hosts := map[string]bool{}
	used := map[string]bool{}
	for _, p := range s.Points {
		if p.Selected {
			used[p.Host] = true
		}
	}
	for _, v := range s.Servers {
		if !known[v.Hostname] || hosts[v.Hostname] {
			return fmt.Errorf("잘못된 서버 배치(없거나 중복된 호스트): %s", v.Hostname)
		}
		hosts[v.Hostname] = true
		// Hosts with nothing selected get no device, so their name is irrelevant.
		if !used[v.Hostname] {
			continue
		}
		if upd != nil && upd.deviceFor(v.Hostname) != nil {
			continue // checkpoints go to the group's existing device for this host
		}
		if !validName(v.Name) {
			return fmt.Errorf("장비 이름은 앞뒤 공백 없이 1~100자로 입력하세요 — 호스트 %s: %q", v.Hostname, v.Name)
		}
	}
	keys := map[string]Point{}
	for _, p := range plan.Points {
		keys[p.Key] = p
	}
	seen := map[string]bool{}
	count := 0
	for _, p := range s.Points {
		if !p.Selected {
			continue
		}
		original, ok := keys[p.Key]
		if !ok || seen[p.Key] {
			return fmt.Errorf("분석에 없거나 중복된 Redis 키: %s", p.Key)
		}
		seen[p.Key] = true
		if original.Registered {
			return fmt.Errorf("이미 등록된 키입니다(중복 등록 불가): %s (%s)", p.Key, original.RegisteredOn)
		}
		if !hosts[p.Host] {
			return fmt.Errorf("서버 배치를 선택하세요: %s", p.Key)
		}
		if !validName(p.Name) {
			return fmt.Errorf("체크포인트 이름은 앞뒤 공백 없이 1~100자로 입력하세요 — %s: %q", p.Key, p.Name)
		}
		if original.TemplateID != p.TemplateID || p.Interval < 1000 || p.Interval > 86400000 || p.Format < 0 || p.Format > 4 {
			return fmt.Errorf("잘못된 체크포인트 설정: %s", p.Key)
		}
		if (p.TemplateID != 0 && (p.Format != original.Format || p.Measure != original.Measure)) || (p.TemplateID == 0 && p.Format != 3 && p.Format != 4) {
			return fmt.Errorf("템플릿 자료형을 유지하세요. 직접 키는 숫자 또는 문자열만 지원합니다: %s", p.Key)
		}
		if strings.Contains(p.Key, "{") && p.Host == "common" {
			return fmt.Errorf("서버 식별자가 있는 키는 실제 서버에 배치하세요: %s", p.Key)
		}
		if len(braces.FindAllString(p.Key, -1)) == 0 && p.Host != "common" {
			return fmt.Errorf("공통 키는 common에 배치해야 합니다")
		}
		count++
	}
	if count == 0 {
		return fmt.Errorf("등록할 체크포인트를 선택하세요")
	}
	return nil
}
