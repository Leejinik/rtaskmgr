package monitor

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// This file implements managed-account (liz/root) password expiry reporting and
// rotation over the live SSH session. Design notes:
//
//   - All changes run as root (via the session's existing sudo path) so PAM
//     history/quality checks are bypassed and either account can be set freely.
//   - We set passwords with `passwd --stdin <user>` (RHEL). The new password is
//     base64-embedded in the command and decoded into passwd's OWN stdin pipe,
//     so it never collides with the sudo password fed on the session stdin and
//     survives any special characters.
//   - The LOGIN account is rotated LAST, and after each of its steps we advance
//     s.password to the new value — otherwise the next `sudo -S` would try the
//     stale password (matters when the sudo timestamp isn't cached).
//   - Every step is journaled through a PwRecorder BEFORE and AFTER it runs, so a
//     mid-rotation crash leaves a durable record of which password is live.

// PwStatus reports password-expiry for the two managed accounts. Expiry is a
// Unix day number (days since 1970-01-01): -1 means "never expires", 0 means
// unknown/not-present. TodayDays is the server's current day for exact
// days-left math at the moment of the read.
type PwStatus struct {
	HasLiz      bool  `json:"hasLiz"`
	HasRoot     bool  `json:"hasRoot"`
	LizExpDays  int64 `json:"lizExpDays"`
	RootExpDays int64 `json:"rootExpDays"`
	TodayDays   int64 `json:"todayDays"`
}

// PwRecorder journals rotation progress. app.go implements it over the pwledger
// store and also emits a UI progress event on each call.
type PwRecorder interface {
	// Begin records a step about to run (status "pending") and returns a handle.
	Begin(account, op, step, password string) string
	// Done marks a previously-begun step: status "ok" (errMsg == "") or "fail".
	Done(id, status, errMsg string)
}

type pwStep struct {
	name string // "to-temp" | "to-current" | "to-new"
	pw   string
}

type acctPlan struct {
	account string
	steps   []pwStep
}

// managedAccounts returns the two accounts to rotate, ordered so the login
// account (whose credential also drives sudo) is processed LAST.
func managedAccounts(loginUser string) []string {
	if loginUser == "root" {
		return []string{"liz", "root"}
	}
	return []string{"root", "liz"}
}

// PasswordStatus reads /etc/shadow (as root) and computes expiry for liz/root.
func (m *Manager) PasswordStatus(hostID string) (PwStatus, error) {
	s := m.get(hostID)
	if s == nil {
		return PwStatus{}, fmt.Errorf("연결되어 있지 않습니다")
	}
	inner := `printf 'NOW:%s\n' "$(date +%s)"; awk -F: '$1=="root"||$1=="liz"{printf "%s:%s:%s\n",$1,$3,$5}' /etc/shadow`
	out, err := m.sudoRun(s, inner)
	if err != nil {
		return PwStatus{}, fmt.Errorf("만료일 조회 실패: %v (%s)", err, strings.TrimSpace(out))
	}
	return parseShadow(out)
}

// parseShadow turns the NOW/root/liz lines into a PwStatus.
func parseShadow(out string) (PwStatus, error) {
	var st PwStatus
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, ":")
		switch {
		case parts[0] == "NOW" && len(parts) >= 2:
			sec, _ := strconv.ParseInt(parts[1], 10, 64)
			st.TodayDays = sec / 86400
		case (parts[0] == "root" || parts[0] == "liz") && len(parts) >= 3:
			exp := expiryDay(parts[1], parts[2]) // lastchange, max
			if parts[0] == "root" {
				st.HasRoot = true
				st.RootExpDays = exp
			} else {
				st.HasLiz = true
				st.LizExpDays = exp
			}
		}
	}
	if !st.HasLiz && !st.HasRoot {
		return st, fmt.Errorf("shadow에서 liz/root 계정을 찾지 못했습니다 (sudo 권한 확인)")
	}
	return st, nil
}

// expiryDay computes the expiry Unix-day from shadow fields 3 (last change, in
// days) and 5 (max days). Returns -1 for "never", 0 for "unknown".
func expiryDay(lastChange, maxDays string) int64 {
	lc, lcErr := strconv.ParseInt(strings.TrimSpace(lastChange), 10, 64)
	md, mdErr := strconv.ParseInt(strings.TrimSpace(maxDays), 10, 64)
	if maxDays == "" || mdErr != nil || md < 0 || md >= 99999 {
		return -1 // never expires
	}
	if lastChange == "" || lcErr != nil || lc <= 0 {
		return 0 // unknown (or forced-change); can't project a date
	}
	return lc + md
}

// setPassword sets one account's password to pw, as root, non-interactively.
func (m *Manager) setPassword(s *session, account, pw string) error {
	b64 := base64.StdEncoding.EncodeToString([]byte(pw))
	// The decoded password is piped straight into passwd's stdin, separate from
	// the sudo password on the session stdin.
	inner := fmt.Sprintf(`out=$(echo %s | base64 -d | passwd --stdin %s 2>&1) && echo RTM_OK || echo "RTM_FAIL:$out"`, b64, account)
	out, err := m.sudoRun(s, inner)
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(out))
	}
	if !strings.Contains(out, "RTM_OK") {
		msg := strings.TrimSpace(out)
		if i := strings.Index(msg, "RTM_FAIL:"); i >= 0 {
			msg = strings.TrimSpace(msg[i+len("RTM_FAIL:"):])
		}
		if msg == "" {
			msg = "passwd 실패 (원인 불명)"
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

// rotate runs each account's steps in order, journaling every step. It advances
// s.password whenever the login account's password changes so sudo keeps working.
func (m *Manager) rotate(s *session, op string, plan []acctPlan, rec PwRecorder) error {
	for _, ap := range plan {
		for _, stp := range ap.steps {
			id := rec.Begin(ap.account, op, stp.name, stp.pw)
			if err := m.setPassword(s, ap.account, stp.pw); err != nil {
				rec.Done(id, "fail", err.Error())
				return fmt.Errorf("%s %s: %w", ap.account, stp.name, err)
			}
			rec.Done(id, "ok", "")
			if ap.account == s.user {
				s.password = stp.pw
			}
		}
	}
	return nil
}

// RenewPasswords refreshes the expiry date on liz+root WITHOUT changing the
// effective password: each account cycles current -> tempPw -> current (two real
// changes so the shadow last-change date advances). The login password is
// unchanged at the end.
func (m *Manager) RenewPasswords(hostID, tempPw string, rec PwRecorder) error {
	s := m.get(hostID)
	if s == nil {
		return fmt.Errorf("연결되어 있지 않습니다")
	}
	if tempPw == "" {
		return fmt.Errorf("임시 패스워드가 비어 있습니다")
	}
	current := s.password
	if current == "" {
		return fmt.Errorf("현재 패스워드를 알 수 없습니다 (비밀번호 인증 호스트에서만 지원)")
	}
	if current == tempPw {
		return fmt.Errorf("임시 패스워드가 현재 패스워드와 같습니다")
	}
	var plan []acctPlan
	for _, acct := range managedAccounts(s.user) {
		plan = append(plan, acctPlan{account: acct, steps: []pwStep{
			{"to-temp", tempPw},
			{"to-current", current},
		}})
	}
	return m.rotate(s, "renew", plan, rec)
}

// ChangePasswords sets a brand-new password on BOTH liz and root (current ->
// new). On success the login password has become newPw; the caller persists it
// to the host store.
func (m *Manager) ChangePasswords(hostID, newPw string, rec PwRecorder) error {
	s := m.get(hostID)
	if s == nil {
		return fmt.Errorf("연결되어 있지 않습니다")
	}
	if newPw == "" {
		return fmt.Errorf("변경할 패스워드가 비어 있습니다")
	}
	var plan []acctPlan
	for _, acct := range managedAccounts(s.user) {
		plan = append(plan, acctPlan{account: acct, steps: []pwStep{
			{"to-new", newPw},
		}})
	}
	return m.rotate(s, "change", plan, rec)
}
