package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
			body, _ := json.Marshal(map[string]string{"hostname": test.hostname})
			request := httptest.NewRequest(http.MethodPost, "/internal/enrollments/"+enrollment.Enrollment.ID+"/prepare", strings.NewReader(string(body)))
			request.SetPathValue("id", enrollment.Enrollment.ID)
			request.RemoteAddr = test.remote
			request.Header.Set("Authorization", "Bearer "+test.token)
			request.Header.Set("X-Forwarded-For", "127.0.0.1")
			request.Header.Set("X-Forwarded-Host", "localhost")
			recorder := httptest.NewRecorder()
			s.prepareContainer(recorder, request)
			if recorder.Code != test.code {
				t.Fatalf("unexpected status %d: %s", recorder.Code, recorder.Body.String())
			}
			if test.code != 200 {
				return
			}
			var response struct {
				LocalContainerURL string `json:"local_container_url"`
			}
			if err = json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if (response.LocalContainerURL != "") != test.rewritten {
				t.Fatalf("unexpected route: %q", response.LocalContainerURL)
			}
			if test.rewritten && response.LocalContainerURL != s.ContainerURL() {
				t.Fatalf("did not use ordinary main listener: %q", response.LocalContainerURL)
			}
		})
	}
}
