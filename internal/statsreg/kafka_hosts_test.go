package statsreg

import (
	"context"
	"net"
	"testing"
)

func TestKafkaHostMappings(t *testing.T) {
	m, err := parseKafkaHosts("server_1 = 192.0.2.11\nserver_2 192.0.2.12:19093\nserver03 = 2001:db8::3 # IPv6\n")
	if err != nil {
		t.Fatal(err)
	}
	for in, want := range map[string]string{"SERVER_1.:9092": "192.0.2.11:9092", "server_2:9093": "192.0.2.12:19093", "server03:9094": "[2001:db8::3]:9094", "other:9092": "other:9092"} {
		if got := mappedKafkaAddress(in, m); got != want {
			t.Errorf("%s: got %s, want %s", in, got, want)
		}
	}
	for _, bad := range []string{"server_1", "server_1=not-an-ip", "server_1=192.0.2.1:0", "server_1=192.0.2.1:99999", "server_1=192.0.2.1\nSERVER_1.=192.0.2.2"} {
		if _, err := parseKafkaHosts(bad); err == nil {
			t.Errorf("accepted invalid mapping: %s", bad)
		}
	}
}

func TestKafkaDialerAndWriterBothBypassDNS(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			c, e := listener.Accept()
			if e != nil {
				return
			}
			c.Close()
		}
	}()
	c := Config{KafkaHosts: "server_3.invalid = " + listener.Addr().String(), KafkaSecurity: "SSL"}
	d, err := kafkaDialer(c)
	if err != nil {
		t.Fatal(err)
	}
	transport, err := kafkaTransport(c)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	for _, dial := range []func(context.Context, string, string) (net.Conn, error){d.DialFunc, transport.Dial} {
		conn, e := dial(context.Background(), "tcp", "server_3.invalid:9094")
		if e != nil {
			t.Fatal(e)
		}
		conn.Close()
	}
	if d.TLS == nil || transport.TLS == nil || d.TLS.InsecureSkipVerify || transport.TLS.InsecureSkipVerify {
		t.Fatal("TLS validation must remain enabled")
	}
}
