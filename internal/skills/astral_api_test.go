package skills

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAstralAPIClientPublishesCompleteResponses(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python 3 is required to run the bundled CLI")
	}
	const token = "future-format-token"
	t.Setenv("ASTRAL_API_TOKEN", token)
	body := `"` + strings.Repeat("x", 64<<10) + `"`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("missing command credential")
		}
		if r.Method == "POST" {
			raw, err := io.ReadAll(r.Body)
			if err != nil || string(raw) != body {
				t.Errorf("invalid uploaded body, length %d, error %v", len(raw), err)
			}
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		if r.URL.Path == "/v1/short" {
			w.Header().Set("Content-Length", "100")
		}
		_, _ = io.WriteString(w, "download")
	}))
	defer server.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "output")
	if err := os.WriteFile(target, []byte("previous"), 0600); err != nil {
		t.Fatal(err)
	}
	call := func(wantOK bool, args ...string) {
		t.Helper()
		script := filepath.Join("builtin", "astral-api", "scripts", "astral_api.py")
		cmd := exec.Command(python, append([]string{script, "--base-url", server.URL, "request"}, args...)...)
		cmd.Stdin = strings.NewReader(body)
		out, err := cmd.CombinedOutput()
		if (err == nil) != wantOK || strings.Contains(string(out), token) {
			t.Fatalf("CLI result %v, output %s", err, out)
		}
	}
	call(false, "GET", "/v1/ok", "--output", target)
	call(false, "GET", "/v1/short", "--force", "--output", target)
	if data, err := os.ReadFile(target); err != nil || string(data) != "previous" {
		t.Fatalf("failed download changed existing file: %q, %v", data, err)
	}
	call(true, "POST", "/v1/ok", "--body-file", "-", "--force", "--output", target)
	if data, err := os.ReadFile(target); err != nil || string(data) != "download" {
		t.Fatalf("complete download did not replace output: %q, %v", data, err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 1 {
		t.Fatalf("temporary files left in output directory: %v, %v", entries, err)
	}
	t.Setenv("ASTRAL_API_TOKEN", token+"\ninvalid")
	call(false, "GET", "/v1/ok")
}
