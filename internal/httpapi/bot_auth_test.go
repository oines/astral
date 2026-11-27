package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/oines/astral/internal/botaccess"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
	"github.com/oines/astral/internal/testkit"
)

func bearerRequest(t *testing.T, ctx context.Context, server *httptest.Server, method, path, token string) *http.Response {
	t.Helper()
	r, err := http.NewRequestWithContext(ctx, method, server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+token)
	response, err := server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestOwnerManagesBotAPICredentialsAndRevocationClosesStream(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("fixture") })
	a, server := apiFixture(t, p)
	const streamPath = BotAPIPrefix + "/_test-token-stream"
	ended := make(chan struct{})
	a.botMux.HandleFunc("GET "+streamPath, func(w http.ResponseWriter, r *http.Request) {
		defer close(ended)
		scope, ok := persistence.CurrentSessionScope(r.Context())
		if !ok || scope.AgentID != p.Agent.ID || scope.SessionID != "" || botID(r) != p.Agent.ID {
			t.Errorf("authenticated scope = %+v, Bot = %q", scope, botID(r))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: ready\ndata: {}\n\n")
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Error(err)
			return
		}
		<-r.Context().Done()
	})

	path := "/api/v1/agents/" + p.Agent.ID + "/bot-api-tokens"
	response := request(t, p.Context, server, http.MethodPost, path, strings.NewReader(`{"name":"Operator"}`), false)
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous token creation status %d", response.StatusCode)
	}
	response = request(t, p.Context, server, http.MethodPost, path, strings.NewReader(`{"name":"Operator"}`), true)
	var issued botaccess.CreatedToken
	err := json.NewDecoder(response.Body).Decode(&issued)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || issued.ID == "" || issued.Token == "" || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("Owner token creation failed: %d, %v", response.StatusCode, err)
	}
	response = request(t, p.Context, server, http.MethodGet, path, nil, true)
	raw, err := io.ReadAll(response.Body)
	response.Body.Close()
	var directory BotAPITokens
	if err != nil || response.StatusCode != http.StatusOK || json.Unmarshal(raw, &directory) != nil || strings.Contains(string(raw), issued.Token) || strings.Contains(string(raw), `"hash"`) {
		t.Fatalf("Owner token directory failed or exposed private material: %d, %v", response.StatusCode, err)
	}
	if directory.Endpoint != server.URL+BotAPIPrefix || len(directory.Tokens) != 1 || directory.Tokens[0].ID != issued.ID {
		t.Fatalf("Owner token directory metadata = %+v", directory)
	}

	acp, err := a.Config.ACP.CreateToken(p.Context, p.Agent.ID, "ACP only")
	if err != nil {
		t.Fatal(err)
	}
	response = bearerRequest(t, p.Context, server, http.MethodGet, streamPath, acp.Token)
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized || response.Header.Get("WWW-Authenticate") != "Bearer" {
		t.Fatalf("Bot API ACP credential status %d", response.StatusCode)
	}
	response = bearerRequest(t, p.Context, server, http.MethodGet, path, issued.Token)
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("Owner API Bot credential status %d", response.StatusCode)
	}
	response = request(t, p.Context, server, http.MethodGet, streamPath, nil, true)
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("Bot API Desktop credential status %d", response.StatusCode)
	}
	other := domain.Agent{Meta: domain.Meta{ID: "other-bot"}, Name: "Other"}
	if err := p.Store.Save(p.Context, "agents", &other.Meta, other); err != nil {
		t.Fatal(err)
	}
	response = request(t, p.Context, server, http.MethodDelete, "/api/v1/agents/"+other.ID+"/bot-api-tokens/"+issued.ID, nil, true)
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("foreign Bot token deletion status %d", response.StatusCode)
	}

	streamCtx, stopStream := context.WithTimeout(p.Context, 10*time.Second)
	defer stopStream()
	stream := bearerRequest(t, streamCtx, server, http.MethodGet, streamPath, issued.Token)
	defer stream.Body.Close()
	reader := bufio.NewReader(stream.Body)
	if first, err := reader.ReadString('\n'); stream.StatusCode != http.StatusOK || err != nil || first != "event: ready\n" {
		t.Fatalf("Bot stream open: status=%d, first=%q, err=%v", stream.StatusCode, first, err)
	}
	response = request(t, p.Context, server, http.MethodDelete, path+"/"+issued.ID, nil, true)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("Owner token deletion status %d", response.StatusCode)
	}
	select {
	case <-ended:
	case <-streamCtx.Done():
		t.Fatal("revocation did not cancel the existing Bot stream")
	}
	if rest, err := io.ReadAll(reader); err != nil || string(rest) != "data: {}\n\n" {
		t.Fatalf("Bot stream did not end cleanly: %q, %v", rest, err)
	}
	response = bearerRequest(t, p.Context, server, http.MethodGet, streamPath, issued.Token)
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked Bot credential status %d", response.StatusCode)
	}
}
