package monitor

import "testing"

func TestSSHEndpoint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		addr string
		port int
		want string
	}{
		{name: "IPv4", addr: "192.168.0.10", port: 22, want: "192.168.0.10:22"},
		{name: "hostname", addr: "server.example.com", port: 2222, want: "server.example.com:2222"},
		{name: "IPv6 literal", addr: "2001:db8::10", port: 22, want: "[2001:db8::10]:22"},
		{name: "bracketed IPv6", addr: "[2001:db8::10]", port: 22, want: "[2001:db8::10]:22"},
		{name: "IPv6 zone", addr: " fe80::1%eth0 ", port: 22, want: "[fe80::1%eth0]:22"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := sshEndpoint(tt.addr, tt.port)
			if err != nil {
				t.Fatalf("sshEndpoint(%q, %d) error: %v", tt.addr, tt.port, err)
			}
			if got != tt.want {
				t.Fatalf("sshEndpoint(%q, %d) = %q, want %q", tt.addr, tt.port, got, tt.want)
			}
		})
	}
}

func TestSSHEndpointRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		addr string
		port int
	}{
		{name: "empty host", addr: "  ", port: 22},
		{name: "zero port", addr: "server.example.com", port: 0},
		{name: "negative port", addr: "server.example.com", port: -1},
		{name: "port too large", addr: "server.example.com", port: 65536},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got, err := sshEndpoint(tt.addr, tt.port); err == nil {
				t.Fatalf("sshEndpoint(%q, %d) = %q, want error", tt.addr, tt.port, got)
			}
		})
	}
}
