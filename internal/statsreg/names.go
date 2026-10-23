package statsreg

import "fmt"

// Name placeholders in a registration (3rd round, F8). The manage screen fills
// {서버} {서버대문자} {서버번호} {키N} (and the older {host} {Host} {n} {tN}) per
// server; the registration screen stored them as typed — trunk got
// 「Zookeeper CPU (%) - {서버}」 on three checkpoints. A registration now fills
// them the same way, with the row's own server, before checking and saving; a
// name that cannot be filled for its row is refused.

// ResolvedName is a name whose placeholders were filled for its row.
type ResolvedName struct {
	Key  string `json:"key"`
	Host string `json:"host"`
	From string `json:"from"`
	To   string `json:"to"`
}

// resolveSelectionNames fills the placeholders of the selected points' names.
// The caller's points are not changed.
func resolveSelectionNames(sel Selection) (Selection, []ResolvedName, error) {
	points := make([]Point, len(sel.Points))
	copy(points, sel.Points)
	out := []ResolvedName{}
	for i := range points {
		p := &points[i]
		if !p.Selected || p.Host == "" || !namePlaceholder.MatchString(screenToPattern(p.Name)) {
			continue
		}
		name, ok := resolveName(p.Name, p.Key, p.Host)
		if !ok {
			where := "서버 " + p.Host
			if p.Host == "common" {
				where = "공통 항목"
			}
			return sel, nil, fmt.Errorf("체크포인트 이름 %q 의 자리를 이 줄(%s, %s)에서 채울 수 없습니다 — {서버}·{서버대문자}는 서버가 정해진 줄에서만, {서버번호}는 서버 이름이 숫자로 끝날 때만, {키N}은 키에 { }가 N개 이상일 때만 쓸 수 있습니다", p.Name, p.Key, where)
		}
		out = append(out, ResolvedName{Key: p.Key, Host: p.Host, From: p.Name, To: name})
		p.Name = name
	}
	sel.Points = points
	return sel, out, nil
}
