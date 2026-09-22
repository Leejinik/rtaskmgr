package statsreg

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/segmentio/kafka-go"
)

var brokerHostname = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)

// Bindings apply only to this connection; neither the OS hosts file nor Kafka
// metadata is modified. Rewriting at TCP dial time preserves TLS ServerName.
func parseKafkaHosts(text string) (map[string]string, error) {
	bindings := map[string]string{}
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
		if line == "" {
			continue
		}
		fields := strings.Fields(strings.ReplaceAll(line, "=", " "))
		if len(fields) != 2 {
			return nil, fmt.Errorf("Kafka 호스트 매핑 %d행: hostname = IP 또는 IP:port 형식으로 입력하세요", i+1)
		}
		host := strings.ToLower(strings.TrimSuffix(fields[0], "."))
		if !brokerHostname.MatchString(host) {
			return nil, fmt.Errorf("Kafka 호스트 매핑 %d행: 호스트 이름이 잘못됐습니다", i+1)
		}
		target := fields[1]
		if _, err := netip.ParseAddr(target); err != nil {
			ip, port, err := net.SplitHostPort(target)
			if err != nil {
				return nil, fmt.Errorf("Kafka 호스트 매핑 %d행: 올바른 IP 또는 IP:port를 입력하세요", i+1)
			}
			if _, err = netip.ParseAddr(ip); err != nil {
				return nil, fmt.Errorf("Kafka 호스트 매핑 %d행: 대상은 IP 주소여야 합니다", i+1)
			}
			p, err := strconv.Atoi(port)
			if err != nil || p < 1 || p > 65535 {
				return nil, fmt.Errorf("Kafka 호스트 매핑 %d행: 포트 범위는 1~65535입니다", i+1)
			}
		}
		if _, exists := bindings[host]; exists {
			return nil, fmt.Errorf("Kafka 호스트 매핑 중복: %s", host)
		}
		bindings[host] = target
	}
	return bindings, nil
}

func mappedKafkaAddress(address string, bindings map[string]string) string {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return address
	}
	target, ok := bindings[strings.ToLower(strings.TrimSuffix(host, "."))]
	if !ok {
		return address
	}
	if ip, err := netip.ParseAddr(target); err == nil {
		return net.JoinHostPort(ip.String(), port)
	}
	return target
}

func kafkaTCPDial(c Config) (func(context.Context, string, string) (net.Conn, error), error) {
	bindings, err := parseKafkaHosts(c.KafkaHosts)
	if err != nil {
		return nil, err
	}
	dialer := net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		target := mappedKafkaAddress(address, bindings)
		conn, err := dialer.DialContext(ctx, network, target)
		if err != nil && target != address {
			return nil, fmt.Errorf("Kafka %s → %s 연결 실패: %w", address, target, err)
		}
		return conn, err
	}, nil
}

func kafkaTransport(c Config) (*kafka.Transport, error) {
	d, err := kafkaDialer(c)
	if err != nil {
		return nil, err
	}
	return &kafka.Transport{TLS: d.TLS, SASL: d.SASLMechanism, Dial: d.DialFunc, DialTimeout: 8 * time.Second}, nil
}
