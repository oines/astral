package platform

import (
	"context"
	"github.com/oines/astral/internal/computers"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestContainerPreparationOnlyRewritesVerifiedSameHost(t *testing.T) {
	root := t.TempDir()
	s, err := New(Config{Listen: "0.0.0.0:0", DataDirectory: filepath.Join(root, "server"), ComputerDirectory: filepath.Join(root, "computers"), NoLocal: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	enrollment, err := s.Hub.Enroll(context.Background(), s.URL, true, false)
	if err != nil {
		t.Fatal(err)
	}
	hostname, _ := os.Hostname()
	for _, test := range []struct {
		name, remote, hostname, token string
		rewritten                     bool
		code                          int
	}{
		{"same host", "127.0.0.1:2345", hostname, enrollment.Token, true, 200},
		{"another hostname through loopback", "127.0.0.1:2345", "another-host", enrollment.Token, false, 200},
		{"remote with forged forwarding headers", "192.168.1.10:2345", hostname, enrollment.Token, false, 200},
		{"invalid enrollment", "127.0.0.1:2345", hostname, "wrong-token", false, 403},
	} {
		t.Run(test.name, func(t *testing.T) {
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				r.RemoteAddr = test.remote
				r.Header.Set("X-Forwarded-For", "127.0.0.1")
				r.Header.Set("X-Forwarded-Host", "localhost")
				s.Hub.Connect(w, r)
			}))
			defer proxy.Close()
			address, err := computers.PrepareRemoteContainer(context.Background(), proxy.URL, s.Hub.ServerPin(), enrollment.Enrollment.ID, test.token, test.hostname)
			if test.code != 200 {
				if err == nil {
					t.Fatal("invalid enrollment prepared container")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if (address != "") != test.rewritten {
				t.Fatalf("unexpected route: %q", address)
			}
			if test.rewritten && address != s.ContainerURL() {
				t.Fatalf("did not use main listener: %q", address)
			}
		})
	}
}
