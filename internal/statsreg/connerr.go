package statsreg

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/segmentio/kafka-go"
)

// A connection failure is told in screen words, names the input it concerns
// and keeps the raw error for 「자세히」 (2nd-round testers met
// "failed to open connection to …:19092: Kafka …:19092 → …:19099 연결 실패:
// i/o timeout" — English, two ports, no input marked). The text is
//
//	<summary> ⟦<field>⟧
//	자세히: <raw error>
//
// field: db.host|db.port|db.password|db.user, redis.host|redis.port|redis.password,
// kafka (the whole block) or kafka.<row>[.host|.ip|.port], rows counted from 0.
// The screen marks that input and folds the raw part away.
type fieldError struct {
	field, summary string
	err            error
}

func (e *fieldError) Error() string {
	s := e.summary + " ⟦" + e.field + "⟧"
	if e.err != nil {
		s += "\n자세히: " + e.err.Error()
	}
	return s
}

func (e *fieldError) Unwrap() error { return e.err }

// netKind sorts a dial error into what the user can fix.
func netKind(err error) string {
	var ne net.Error
	var dns *net.DNSError
	msg := strings.ToLower(err.Error())
	switch {
	case errors.As(err, &dns) || strings.Contains(msg, "no such host"):
		return "name"
	case strings.Contains(msg, "refused") || strings.Contains(msg, "actively refused"):
		return "refused"
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) || strings.Contains(msg, "timeout"):
		return "timeout"
	}
	return ""
}

func dbError(c Config, err error) error {
	addr := net.JoinHostPort(c.DBHost, strconv.Itoa(c.DBPort))
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		switch me.Number {
		case 1045:
			return &fieldError{"db.password", fmt.Sprintf("MariaDB(%s) 계정 또는 비밀번호가 틀립니다", addr), err}
		case 1049:
			return &fieldError{"db.host", fmt.Sprintf("MariaDB(%s)에 liz 데이터베이스가 없습니다 — MK119 DB 서버 주소인지 확인하세요", addr), err}
		}
	}
	switch netKind(err) {
	case "name":
		return &fieldError{"db.host", fmt.Sprintf("MariaDB 주소 %s 를 찾을 수 없습니다 — 주소를 확인하세요", c.DBHost), err}
	case "refused":
		return &fieldError{"db.port", fmt.Sprintf("MariaDB(%s)가 연결을 거부했습니다 — 포트를 확인하세요", addr), err}
	case "timeout":
		return &fieldError{"db.host", fmt.Sprintf("MariaDB(%s)에서 응답이 없습니다(시간 초과) — 주소·포트·방화벽을 확인하세요", addr), err}
	}
	return &fieldError{"db.host", fmt.Sprintf("MariaDB(%s)에 연결하지 못했습니다", addr), err}
}

func redisError(addrs string, err error) error {
	msg := strings.ToUpper(err.Error())
	switch {
	case strings.Contains(msg, "WRONGPASS") || strings.Contains(msg, "NOAUTH") || strings.Contains(msg, "INVALID PASSWORD") || strings.Contains(msg, "AUTH"):
		return &fieldError{"redis.password", "Redis 비밀번호(또는 ACL 계정)가 틀렸거나 비어 있습니다", err}
	}
	switch netKind(err) {
	case "name":
		return &fieldError{"redis.host", fmt.Sprintf("Redis 주소(%s)를 찾을 수 없습니다", addrs), err}
	case "refused":
		return &fieldError{"redis.port", fmt.Sprintf("Redis(%s)가 연결을 거부했습니다 — 포트를 확인하세요", addrs), err}
	case "timeout":
		return &fieldError{"redis.host", fmt.Sprintf("Redis(%s)에서 응답이 없습니다(시간 초과) — 주소·포트·방화벽을 확인하세요", addrs), err}
	}
	return &fieldError{"redis.host", fmt.Sprintf("Redis(%s)에 연결하지 못했습니다", addrs), err}
}

// kafkaRow describes the i-th Kafka input row: name typed, where it is dialed.
func kafkaRow(i int, addr string, bindings map[string]string) (host, port, target string) {
	host, port, _ = net.SplitHostPort(addr)
	return host, port, mappedKafkaAddress(addr, bindings)
}

// connLost: the connection broke or never answered — not a wrong setting.
// 3rd round: a cut network ("forcibly closed") read as 「MK119 의 Kafka 인지 확인하세요」.
func connLost(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, s := range []string{"forcibly closed", "connection reset", "broken pipe", "connection aborted", "use of closed network connection", "eof"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return netKind(err) != ""
}

// kafkaReadError: the i-th server took the connection but reading the topic failed.
func kafkaReadError(i int, err error) error {
	if connLost(err) {
		return &fieldError{fmt.Sprintf("kafka.%d", i), fmt.Sprintf("Kafka %d번째 서버와 연결이 끊겼습니다 — 네트워크·서버 상태를 확인한 뒤 다시 하세요", i+1), err}
	}
	return &fieldError{fmt.Sprintf("kafka.%d", i), fmt.Sprintf("Kafka %d번째 서버에서 liz.message.pipeline 토픽을 읽지 못했습니다 — MK119 의 Kafka 인지 확인하세요", i+1), err}
}

func kafkaRowError(i int, addr string, bindings map[string]string, err error) error {
	host, _, target := kafkaRow(i, addr, bindings)
	where := fmt.Sprintf("Kafka %d번째 서버(%s → %s)", i+1, host, target)
	switch netKind(err) {
	case "name":
		return &fieldError{fmt.Sprintf("kafka.%d.ip", i), where + ": 주소를 찾을 수 없습니다 — IP 칸을 확인하세요", err}
	case "refused":
		return &fieldError{fmt.Sprintf("kafka.%d.port", i), where + ": 연결을 거부했습니다(그 포트에 Kafka 가 없음) — 포트를 확인하세요", err}
	case "timeout":
		return &fieldError{fmt.Sprintf("kafka.%d", i), where + ": 응답이 없습니다(시간 초과) — IP·포트·방화벽을 확인하세요", err}
	}
	if connLost(err) {
		return &fieldError{fmt.Sprintf("kafka.%d", i), where + ": 연결이 끊겼습니다 — 네트워크·서버 상태를 확인한 뒤 다시 하세요", err}
	}
	return &fieldError{fmt.Sprintf("kafka.%d", i), where + ": 연결하지 못했습니다", err}
}

// kafkaLeaderError: the first server answered, but the broker it names for the
// topic (by its advertised host:port) could not be reached through the rows.
func kafkaLeaderError(rows []string, bindings map[string]string, leader kafka.Broker, err error) error {
	adv := net.JoinHostPort(leader.Host, strconv.Itoa(leader.Port))
	for j, addr := range rows {
		host, port, target := kafkaRow(j, addr, bindings)
		if !strings.EqualFold(strings.TrimSuffix(host, "."), strings.TrimSuffix(leader.Host, ".")) {
			continue
		}
		if port != strconv.Itoa(leader.Port) {
			return &fieldError{fmt.Sprintf("kafka.%d.port", j), fmt.Sprintf("Kafka %d번째 서버(%s): 입력한 포트 %s 로는 연결되지 않습니다. Kafka 가 스스로 알려 준 주소는 %s 입니다 — 포트를 %d 로 고쳐 보세요", j+1, host, port, adv, leader.Port), err}
		}
		e := kafkaRowError(j, net.JoinHostPort(host, port), bindings, err).(*fieldError)
		e.summary = strings.Replace(e.summary, target, target+" (Kafka 가 알려 준 주소 "+adv+")", 1)
		return e
	}
	return &fieldError{"kafka", fmt.Sprintf("Kafka 가 알려 준 서버 이름 %s 가 입력한 행에 없습니다 — 「호스트 이름」 칸에 %s 를 그대로 넣고 그 서버의 IP·포트를 적으세요", leader.Host, leader.Host), err}
}

func checkKafka(ctx context.Context, c Config) error {
	d, err := kafkaDialer(c)
	if err != nil {
		return &fieldError{"kafka", "Kafka 입력을 확인하세요", err}
	}
	bindings, _ := parseKafkaHosts(c.KafkaHosts)
	rows := brokers(c)
	if len(rows) == 0 {
		return &fieldError{"kafka.0.host", "Kafka 서버를 한 줄 이상 입력하세요", nil}
	}
	var first error
	keep := func(e error) {
		if first == nil {
			first = e
		}
	}
	for i, addr := range rows {
		conn, e := d.DialContext(ctx, "tcp", addr)
		if e != nil {
			keep(kafkaRowError(i, addr, bindings, e))
			continue
		}
		conn.SetDeadline(time.Now().Add(10 * time.Second))
		parts, e := conn.ReadPartitions("liz.message.pipeline")
		conn.Close()
		if e != nil {
			keep(kafkaReadError(i, e))
			continue
		}
		if len(parts) == 0 {
			keep(&fieldError{"kafka", "Kafka 에 liz.message.pipeline 토픽이 없습니다 — MK119 의 Kafka 인지 확인하세요", nil})
			continue
		}
		leader, e := d.DialLeader(ctx, "tcp", addr, "liz.message.pipeline", parts[0].ID)
		if e != nil {
			keep(kafkaLeaderError(rows, bindings, parts[0].Leader, e))
			continue
		}
		leader.Close()
		return nil
	}
	return first
}

// noteErr adds what was (not) done to an error, keeping a fieldError's input mark.
func noteErr(err error, note string) error {
	var fe *fieldError
	if errors.As(err, &fe) {
		return &fieldError{fe.field, fe.summary + note, fe.err}
	}
	return fmt.Errorf("%w%s", err, note)
}
