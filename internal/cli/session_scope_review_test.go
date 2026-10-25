package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/oines/astral/internal/platform"
)

func TestServerOverrideDoesNotReuseAnotherServersSession(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("HOME", directory)
	t.Setenv("XDG_CONFIG_HOME", directory)
	t.Setenv("AppData", directory)
	root, err := platform.DefaultRoot()
	if err != nil {
		t.Fatal(err)
	}
	sessionFile := filepath.Join(root, "client.session")
	if err = platform.WritePrivate(sessionFile, []byte("private-session-for-original-server\n")); err != nil {
		t.Fatal(err)
	}
	if err = platform.WriteJSON(filepath.Join(root, "client.json"), ownerConfig{Server: "https://original.example", SessionFile: sessionFile}); err != nil {
		t.Fatal(err)
	}
	var leaked atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cookie, err := r.Cookie("astral_session"); err == nil && cookie.Value == "private-session-for-original-server" {
			leaked.Store(true)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer target.Close()
	err = ownerRequest(context.Background(), []string{"GET", "/api/v1/auth", "--server", target.URL}, io.Discard, io.Discard)
	if leaked.Load() {
		t.Fatal("--server sent the original Server's browser session to a different target")
	}
	if err == nil {
		t.Fatal("changing Server without explicitly selecting credentials must fail")
	}
}

func TestManagementRequestPersistsRotatedWebSession(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("HOME", directory)
	t.Setenv("XDG_CONFIG_HOME", directory)
	t.Setenv("AppData", directory)
	root, err := platform.DefaultRoot()
	if err != nil {
		t.Fatal(err)
	}
	sessionFile := filepath.Join(root, "client.session")
	if err = platform.WritePrivate(sessionFile, []byte("original-session\n")); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("astral_session")
		want := "original-session"
		if requests.Add(1) > 1 {
			want = "rotated-session"
		}
		if err != nil || cookie.Value != want {
			t.Errorf("request did not use its current Web session")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "astral_session", Value: "rotated-session", HttpOnly: true, Path: "/"})
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"authenticated":true}`)
	}))
	defer server.Close()
	if err = platform.WriteJSON(filepath.Join(root, "client.json"), ownerConfig{Server: server.URL, SessionFile: sessionFile}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = ownerRequest(context.Background(), []string{"POST", "/api/v1/auth/reauth", "--body", `{"password":"fixture-password"}`}, io.Discard, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	saved, err := os.ReadFile(sessionFile)
	if err != nil || strings.TrimSpace(string(saved)) != "rotated-session" {
		t.Fatal("rotated Web login was not stored privately")
	}
}
