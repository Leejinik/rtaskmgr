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
	// Deleted: a checkpoint with this key existed and was deleted (flag<>1). Offered selected.
	Deleted bool `json:"deleted"`
	// For a Moved key with exactly one live checkpoint in a group: that checkpoint,
	// the interface it is on and its group. Move (update of that group only) moves
	// the checkpoint to the device of Host, keeping its id and history.
	CheckpointID    int64 `json:"checkpointId"`
	FromInterfaceID int64 `json:"fromInterfaceId"`
	MoveGroupID     int64 `json:"moveGroupId"`
	Move            bool  `json:"move"`
	// BadValue: a numeric point whose current value is not a number. The collector
	// (v10.2 LizSystemStatusMonitorService) logs a NumberFormatException for it every
	// check interval, never collects it and sleeps 100 ms per failure, slowing every
	// MK119 point. Offered unselected whatever a pattern or deleted-key rule says.
	BadValue bool `json:"badValue"`
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
			p.Warning = "제품 정의(드라이버 템플릿) 여러 개와 맞아 어느 것인지 몰라 기본 제외"
			p.Selected = false
			p.TemplateID = 0
		}
		if matches == 0 {
			p.Warning = "제품 정의(드라이버 템플릿) 없는 키: 이름을 넣지 않으면 키 그대로·단위 없이 등록됩니다 — 이름과 자료형(숫자/문자열)을 확인하세요"
		}
		if p.Interval <= 0 {
			p.Interval = 10000
		}
		if p.Format != formatString && !javaNumber(values[key]) {
			p.BadValue = true
			p.Selected = false
			p.Warning = strings.TrimSpace(p.Warning + fmt.Sprintf(" · 지금 값 %q 이(가) 숫자가 아니라 기본 제외: 숫자로 등록하면 Collector가 매 주기 이 키에서 오류를 내며 값을 하나도 못 모으고, 오류마다 0.1초씩 쉬어 다른 MK119 수집도 늦어집니다. 값이 숫자가 되면 다음 업데이트에서 추가하세요", values[key]))
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
				p.Warning = strings.TrimSpace(p.Warning + " " + unplacedWarning)
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

// unplacedWarning marks a key whose server is unknown (removed again when a
// remembered placement places it).
const unplacedWarning = "· 어느 서버의 키인지 모름: 서버를 고르거나 빼세요"

// Data format code of a string point (0 Boolean, 1 Integer, 2 Long, 3 Double, 4 String).
const formatString = 4

// javaNumber reports whether Java's Double.parseDouble would accept v, which the
// collector applies to every non-string point: surrounding whitespace is allowed,
// so are NaN and Infinity.
func javaNumber(v string) bool {
	v = strings.TrimSpace(v)
	switch strings.TrimLeft(v, "+-") {
	case "NaN", "Infinity":
		return true
	case "":
		return false
	}
	if l := strings.ToLower(v); strings.Contains(l, "inf") || strings.Contains(l, "nan") {
		return false // Go also takes inf/infinity/nan in any case; Java does not
	}
	_, err := strconv.ParseFloat(strings.TrimRight(v, "dDfF"), 64)
	return err == nil
}

// keepBadValuesOff runs after patterns and the deleted-key rule, which may select.
func keepBadValuesOff(p *Plan) {
	for i := range p.Points {
		if p.Points[i].BadValue {
			p.Points[i].Selected = false
		}
	}
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
		if p.Selected || p.Move {
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
		if p.Move {
			original, ok := keys[p.Key]
			if !ok || seen[p.Key] || p.Selected || !original.Moved || original.CheckpointID == 0 || original.CheckpointID != p.CheckpointID || original.FromInterfaceID != p.FromInterfaceID || original.Host != p.Host || !hosts[p.Host] {
				return fmt.Errorf("옮길 수 없는 체크포인트입니다 — 다시 조회하세요: %s", p.Key)
			}
			if upd == nil || original.MoveGroupID != upd.GroupID {
				return fmt.Errorf("옮기기는 그 체크포인트가 있는 그룹을 업데이트할 때만 됩니다: %s", p.Key)
			}
			seen[p.Key] = true
			count++
			continue
		}
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
