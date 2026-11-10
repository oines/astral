package platform

import (
	"net/http"
	"testing"
)

func TestComputerConnectionIPUsesTrustedForwardingChain(t *testing.T) {
	for _, test := range []struct {
		name      string
		remote    string
		proxies   []string
		forwarded []string
		want      string
	}{
		{name: "direct", remote: "203.0.113.1:1234", want: "203.0.113.1"},
		{name: "spoofed direct", remote: "203.0.113.1:1234", forwarded: []string{"192.0.2.1"}, want: "203.0.113.1"},
		{name: "loopback proxy", remote: "[::1]:1234", forwarded: []string{"192.0.2.1, 203.0.113.1"}, want: "203.0.113.1"},
		{name: "absent XFF", remote: "127.0.0.1:1234", want: "127.0.0.1"},
		{name: "explicit trust replaces default", remote: "127.0.0.1:1234", proxies: []string{"172.20.0.5"}, forwarded: []string{"192.0.2.1"}, want: "127.0.0.1"},
		{name: "explicitly empty trust", remote: "127.0.0.1:1234", proxies: []string{}, forwarded: []string{"192.0.2.1"}, want: "127.0.0.1"},
		{name: "multiple header lines", remote: "172.20.0.6:1234", proxies: []string{"172.20.0.0/24"}, forwarded: []string{"forged, 2001:db8::1", "::ffff:172.20.0.5"}, want: "2001:db8::1"},
		{name: "mapped peer and client", remote: "[::ffff:127.0.0.1]:1234", forwarded: []string{"::ffff:192.0.2.1"}, want: "192.0.2.1"},
		{name: "malformed relevant hop", remote: "127.0.0.1:1234", forwarded: []string{"192.0.2.1, invalid, 127.0.0.2"}, want: "127.0.0.1"},
		{name: "only trusted addresses", remote: "127.0.0.1:1234", forwarded: []string{"::1, 127.0.0.2"}, want: "127.0.0.1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := &Server{Config: Config{TrustedProxies: test.proxies}}
			r := &http.Request{RemoteAddr: test.remote, Header: make(http.Header)}
			r.Header.Set("CF-Connecting-IP", "198.51.100.99")
			for _, value := range test.forwarded {
				r.Header.Add("X-Forwarded-For", value)
			}
			if got := s.computerConnectionIP(r); got != test.want {
				t.Fatalf("connection IP = %q, want %q", got, test.want)
			}
			if r.RemoteAddr != test.remote {
				t.Fatal("diagnostic projection altered the authentication peer")
			}
		})
	}
}
