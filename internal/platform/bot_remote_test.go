//go:build !daemon

package platform

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oines/astral/internal/auth"
	"github.com/oines/astral/internal/botaccess"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/httpapi"
)

// Test traffic uses real HTTP sockets. Substitute the TCP peer as net/http
// builds the request so the results are portable even on loopback-only CI.
type botRemoteListener struct{ net.Listener }

func (l botRemoteListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return botRemoteConn{conn}, nil
}

type botRemoteConn struct{ net.Conn }

func (botRemoteConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(192, 0, 2, 70), Port: 43210}
}

func TestRemoteBotAPIRespectsBotACLAndHostnamePolicyWithOwnerManagementOff(t *testing.T) {
	const allowedHost = "astral.example"
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	directory := t.TempDir()
	server, err := newAuthenticatedTestServer(Config{
		Listen: "127.0.0.1:0", DataDirectory: filepath.Join(directory, "server"), ComputerDirectory: filepath.Join(directory, "computers"), NoLocal: true,
		PublicURLs: []string{"http://" + allowedHost},
		NetworkCLI: NetworkValues{AllowRemoteManagement: pointer(false), RestrictHostnames: pointer(true), AllowedHostnames: pointer([]string{allowedHost})},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		shutdown, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		if err := server.Close(shutdown); err != nil {
			t.Error(err)
		}
	})
	server.Listener = botRemoteListener{server.Listener}
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	if server.allowsRemoteManagement() {
		t.Fatal("fixture requires remote Owner management to be disabled")
	}

	const owningBot, anotherBot = "bot-home", "bot-other"
	for _, id := range []string{owningBot, anotherBot} {
		bot := domain.Agent{Meta: domain.Meta{ID: id}, Name: id}
		if err := server.Store.Save(ctx, "agents", &bot.Meta, bot); err != nil {
			t.Fatal(err)
		}
	}
	for _, session := range []domain.Session{
		{Meta: domain.Meta{ID: "owned-session"}, AgentID: owningBot, Title: "Shared"},
		{Meta: domain.Meta{ID: "private-session"}, AgentID: owningBot, Title: "Private", Private: true},
		{Meta: domain.Meta{ID: "foreign-session"}, AgentID: anotherBot, Title: "Other Bot"},
	} {
		if err := server.Store.Save(ctx, "sessions", &session.Meta, session); err != nil {
			t.Fatal(err)
		}
	}
	const tokenPath = "/api/v1/agents/" + owningBot + "/bot-api-tokens"
	local := httptest.NewRequest(http.MethodPost, "http://"+allowedHost+tokenPath, strings.NewReader(`{"name":"Remote Bot client"}`)).WithContext(ctx)
	local.RemoteAddr = "127.0.0.1:43210"
	local.Header.Set("Authorization", "Desktop "+testDesktopToken)
	ownerResult := httptest.NewRecorder()
	server.HTTP.Handler.ServeHTTP(ownerResult, local)
	var token botaccess.CreatedToken
	if ownerResult.Code != http.StatusOK || json.Unmarshal(ownerResult.Body.Bytes(), &token) != nil || token.Token == "" {
		t.Fatalf("local Owner could not create a Bot credential: status %d", ownerResult.Code)
	}
	if err := server.Auth.Setup(ctx, "test-owner", "strong-test-password"); err != nil {
		t.Fatal(err)
	}
	_, ownerSession, err := server.Auth.Login(ctx, "test-owner", "strong-test-password", "loopback-test", "test-browser")
	if err != nil {
		t.Fatal(err)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	remote := func(t *testing.T, method, host, path, credential, body string) *http.Response {
		t.Helper()
		request, err := http.NewRequestWithContext(ctx, method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Host = host
		request.Header.Set("Authorization", credential)
		request.Header.Set("X-Forwarded-For", "127.0.0.1")
		request.Header.Set("X-Forwarded-Host", allowedHost)
		request.AddCookie(&http.Cookie{Name: auth.SessionCookieName(false), Value: ownerSession})
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = response.Body.Close() })
		return response
	}
	const self = httpapi.BotAPIPrefix + "/self"
	const sessions = httpapi.BotAPIPrefix + "/sessions"
	credentials := "Bearer " + token.Token
	response := remote(t, http.MethodGet, allowedHost, self, credentials, "")
	var bot httpapi.BotSelf
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&bot) != nil || bot.ID != owningBot {
		t.Fatalf("remote Bot credential did not resolve the owning Bot: status=%d, id=%q", response.StatusCode, bot.ID)
	}
	_ = response.Body.Close()
	response = remote(t, http.MethodGet, allowedHost, sessions, credentials, "")
	var found []domain.Session
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&found) != nil || len(found) != 1 || found[0].ID != "owned-session" {
		t.Fatalf("remote Bot session list escaped its access scope: status=%d, records=%+v", response.StatusCode, found)
	}
	_ = response.Body.Close()
	for _, test := range []struct {
		name, method, host, path, credential, body string
		want                                       int
	}{
		{"Owner cookie alone", http.MethodGet, allowedHost, self, "", "", http.StatusUnauthorized},
		{"desktop credential", http.MethodGet, allowedHost, self, "Desktop " + testDesktopToken, "", http.StatusUnauthorized},
		{"foreign session", http.MethodGet, allowedHost, sessions + "/foreign-session", credentials, "", http.StatusNotFound},
		{"private session", http.MethodGet, allowedHost, sessions + "/private-session", credentials, "", http.StatusNotFound},
		{"foreign session creation", http.MethodPost, allowedHost, sessions, credentials, `{"agent_id":"` + anotherBot + `","title":"Foreign"}`, http.StatusForbidden},
		{"disallowed Host", http.MethodGet, "blocked.example", self, credentials, "", http.StatusForbidden},
		{"Owner bootstrap", http.MethodGet, allowedHost, "/api/v1/bootstrap", "", "", http.StatusNotFound},
		{"Owner token management", http.MethodGet, allowedHost, tokenPath, "", "", http.StatusNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := remote(t, test.method, test.host, test.path, test.credential, test.body)
			defer response.Body.Close()
			if response.StatusCode != test.want {
				t.Fatalf("%s %s returned %d, want %d", test.method, test.path, response.StatusCode, test.want)
			}
		})
	}
}
