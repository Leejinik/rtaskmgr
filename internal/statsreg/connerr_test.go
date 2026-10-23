package statsreg

import (
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/segmentio/kafka-go"
)

func TestFieldErrorText(t *testing.T) {
	e := &fieldError{"kafka.1.port", "포트를 확인하세요", errors.New("dial tcp: refused")}
	if e.Error() != "포트를 확인하세요 ⟦kafka.1.port⟧\n자세히: dial tcp: refused" {
		t.Fatalf("%q", e.Error())
	}
	n := noteErr(e, " — 아무것도 바꾸지 않았습니다")
	if !strings.HasPrefix(n.Error(), "포트를 확인하세요 — 아무것도 바꾸지 않았습니다 ⟦kafka.1.port⟧") {
		t.Fatalf("%q", n.Error())
	}
}

func TestConnectionErrorsNameTheInput(t *testing.T) {
	c := Config{DBHost: "192.0.2.10", DBPort: 3306}
	cases := []struct {
		err   error
		field string
	}{
		{&mysql.MySQLError{Number: 1045, Message: "Access denied"}, "db.password"},
		{&net.OpError{Op: "dial", Err: errors.New("connectex: No connection could be made because the target machine actively refused it.")}, "db.port"},
		{&net.DNSError{Err: "no such host", Name: "nowhere"}, "db.host"},
	}
	for _, k := range cases {
		if fe := dbError(c, k.err).(*fieldError); fe.field != k.field {
			t.Errorf("%v → %s, want %s", k.err, fe.field, k.field)
		}
	}
	if fe := redisError("192.0.2.10:5000", errors.New("WRONGPASS invalid username-password pair")).(*fieldError); fe.field != "redis.password" {
		t.Fatalf("redis: %s", fe.field)
	}
}

// The 2nd-round case: the row says port 19099, Kafka advertises 19092.
func TestKafkaLeaderErrorPointsAtThePort(t *testing.T) {
	rows := []string{"server_1:19092", "server_2:19099"}
	bindings, _ := parseKafkaHosts("server_1 = 192.0.2.11:19092\nserver_2 = 192.0.2.12:19099")
	fe := kafkaLeaderError(rows, bindings, kafka.Broker{Host: "server_2", Port: 19092}, errors.New("i/o timeout")).(*fieldError)
	if fe.field != "kafka.1.port" || !strings.Contains(fe.summary, "2번째") || !strings.Contains(fe.summary, "19092 로 고쳐") {
		t.Fatalf("%+v", fe)
	}
	fe = kafkaLeaderError(rows, bindings, kafka.Broker{Host: "server_9", Port: 19092}, errors.New("x")).(*fieldError)
	if fe.field != "kafka" || !strings.Contains(fe.summary, "server_9") {
		t.Fatalf("%+v", fe)
	}
	fe = kafkaRowError(0, rows[0], bindings, errors.New("dial tcp 192.0.2.11:19092: i/o timeout")).(*fieldError)
	if fe.field != "kafka.0" || !strings.Contains(fe.summary, "1번째 서버(server_1 → 192.0.2.11:19092)") || !strings.Contains(fe.summary, "시간 초과") {
		t.Fatalf("%+v", fe)
	}
}

func TestServerOfAddr(t *testing.T) {
	servers := []Server{{Hostname: "server_2", IP: "192.0.2.12"}}
	if got := serverOfAddr("192.0.2.12:5000", servers); got != "server_2 (192.0.2.12:5000)" {
		t.Fatal(got)
	}
	if got := serverOfAddr("192.0.2.99:5000", servers); got != "192.0.2.99:5000" {
		t.Fatal(got)
	}
}
