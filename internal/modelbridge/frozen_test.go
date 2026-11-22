package modelbridge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral-core/models/provider"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func frozenSelection(t *testing.T, b *Bridge, profileID string) (core.ResolvedModel, core.ModelSnapshot) {
	t.Helper()
	resolved, err := b.ResolveSelection(context.Background(), core.ModelSelection{ID: profileID})
	if err != nil {
		t.Fatal(err)
	}
	return resolved, core.ModelSnapshot{Spec: resolved.Model.Spec(), Data: resolved.Snapshot}
}

func TestFrozenTargetPreservesHostHeadersAcrossProgramUpgrade(t *testing.T) {
	for _, headerName := range []string{"", "User-Agent", "user-agent"} {
		t.Run("custom-header="+headerName, func(t *testing.T) {
			ctx := context.Background()
			bridge := testBridge(t)
			bridge.Version = "0.1.7"
			requests := make(chan http.Header, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests <- r.Header.Clone()
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"done"}}]}`))
			}))
			defer server.Close()
			connection := domain.Provider{Name: "route", Protocol: "openai-completions", Dialect: "compatible", BaseURL: server.URL}
			key := "original-key"
			headers := map[string]string{"Custom-Credential": "original-header"}
			if headerName != "" {
				headers[headerName] = "custom-agent"
			}
			if err := bridge.SaveProvider(ctx, &connection, &key, &headers); err != nil {
				t.Fatal(err)
			}
			profile := reasoningIntegrationProfile(connection, "model", nil)
			if err := bridge.SaveProfile(ctx, &profile); err != nil {
				t.Fatal(err)
			}
			resolved, snapshot := frozenSelection(t, bridge, profile.ID)
			request := core.Request{Messages: []core.Message{core.User("same input")}, Options: resolved.Options}
			if _, err := provider.Call(ctx, resolved.Model, request, nil); err != nil {
				t.Fatal(err)
			}
			// A different Host process/version must restore the original request
			// identity rather than applying its new default or saved custom UA.
			next := &Bridge{Store: bridge.Store, HTTPClient: bridge.HTTPClient, Version: "0.2.0"}
			restored, err := next.RestoreModel(ctx, snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := provider.Call(ctx, restored, request, nil); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				got := <-requests
				if values := got.Values("User-Agent"); len(values) != 1 || values[0] != "Astral/0.1.7" {
					t.Fatalf("frozen Host User-Agent changed: %q", values)
				}
				if got.Get("Authorization") != "Bearer original-key" || got.Get("Custom-Credential") != "original-header" {
					t.Fatalf("provider credentials changed: %+v", got)
				}
			}
		})
	}
}

func TestFrozenTargetRestoresAfterResourceEditsAndDeletion(t *testing.T) {
	ctx := context.Background()
	bridge := testBridge(t)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/old/chat/completions" || r.Header.Get("Authorization") != "Bearer original-key" || r.Header.Get("Custom-Credential") != "original-header" {
			t.Errorf("frozen destination or credentials changed: %s %+v", r.URL, r.Header)
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if string(body["model"]) != `"original-model"` || string(body["reasoning_effort"]) != `"native-high"` {
			t.Errorf("frozen model/options changed: %s", raw(body))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"done"}}]}`))
	}))
	defer server.Close()
	connection := domain.Provider{Name: "old", Protocol: "openai-completions", Dialect: "compatible", BaseURL: server.URL + "/old"}
	key, headers := "original-key", map[string]string{"Custom-Credential": "original-header"}
	if err := bridge.SaveProvider(ctx, &connection, &key, &headers); err != nil {
		t.Fatal(err)
	}
	profile := reasoningIntegrationProfile(connection, "original-model", map[string]any{"efforts": []string{"native-high"}})
	profile.Overrides["defaults"] = raw(core.GenerationOptions{Reasoning: core.ReasoningOptions{Effort: "native-high"}})
	if err := bridge.SaveProfile(ctx, &profile); err != nil {
		t.Fatal(err)
	}
	resolved, snapshot := frozenSelection(t, bridge, profile.ID)
	for _, secret := range []string{key, headers["Custom-Credential"]} {
		if strings.Contains(string(snapshot.Data), secret) {
			t.Fatal("snapshot contains actual credentials")
		}
	}
	connection.BaseURL = server.URL + "/new"
	key = "new-key"
	headers["Custom-Credential"] = "new-header"
	if err := bridge.SaveProvider(ctx, &connection, &key, &headers); err != nil {
		t.Fatal(err)
	}
	if err := bridge.Store.Get(ctx, "models", profile.ID, &profile); err != nil {
		t.Fatal(err)
	}
	profile.ModelID = "new-model"
	profile.Recommendations = map[string]json.RawMessage{"context_window": raw(128000), "max_output_tokens": raw(8192)}
	profile.RecommendationTarget = nil // Explicit new manual configuration.
	profile.Overrides["defaults"] = raw(core.GenerationOptions{})
	if err := bridge.SaveProfile(ctx, &profile); err != nil {
		t.Fatal(err)
	}
	for _, deleted := range []bool{false, true} {
		if deleted {
			if err := bridge.DeleteProvider(ctx, connection.ID, connection.Revision); err != nil {
				t.Fatal(err)
			}
		}
		restored, err := bridge.RestoreModel(ctx, snapshot)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := provider.Call(ctx, restored, core.Request{Messages: []core.Message{core.User("retry")}, Options: resolved.Options}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatalf("frozen target was not independently executable: %d", calls)
	}
}
func TestFrozenOAuthRefreshUsesCapturedGrantAndPreservesConnectionIdentity(t *testing.T) {
	ctx := context.Background()
	bridge := testBridge(t)
	connection := saveAuthProvider(t, bridge, "codex")
	var originalSecretID string
	if err := bridge.Store.Update(ctx, func(tx *persistence.Tx) error {
		id, err := tx.PutSecret(ctx, string(raw(oauthTokens{AccessToken: "old-access", RefreshToken: "old-refresh", AccountID: "old-account", ExpiresAt: time.Now().Add(-time.Hour)})))
		if err != nil {
			return err
		}
		connection.CredentialID = id
		connection.ReplayRevision = connection.Revision + 1
		originalSecretID = id
		return tx.Save(ctx, "providers", &connection.Meta, connection)
	}); err != nil {
		t.Fatal(err)
	}
	refreshes, calls := 0, 0
	bridge.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/oauth/token" {
			refreshes++
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.Form.Get("refresh_token") != "old-refresh" {
				t.Fatal("frozen request refreshed live grant")
			}
			return authResponse(200, `{"access_token":"refreshed-old-access","expires_in":3600}`), nil
		}
		if r.URL.Path != "/backend-api/codex/responses" {
			t.Fatalf("unexpected request %s", r.URL)
		}
		calls++
		if r.Header.Get("Authorization") != "Bearer refreshed-old-access" || r.Header.Get("ChatGPT-Account-Id") != "old-account" {
			t.Fatal("frozen request changed authorization identity")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"done\"}]}]}}\n\n"))}, nil
	})}
	profile := reasoningIntegrationProfile(connection, "account-model", nil)
	profile.Recommendations["streaming"] = raw(true)
	if err := bridge.SaveProfile(ctx, &profile); err != nil {
		t.Fatal(err)
	}
	_, snapshot := frozenSelection(t, bridge, profile.ID)
	var frozenBefore frozenModel
	if err := json.Unmarshal(snapshot.Data, &frozenBefore); err != nil {
		t.Fatal(err)
	}
	// A later authorization creates a distinct grant. Normal refresh of the old
	// frozen credential must never write this current provider record or grant.
	if err := bridge.Store.Update(ctx, func(tx *persistence.Tx) error {
		id, err := tx.PutSecret(ctx, `{"access_token":"current-access"}`)
		if err != nil {
			return err
		}
		connection.CredentialID = id
		connection.ReplayRevision = connection.Revision + 1
		return tx.Save(ctx, "providers", &connection.Meta, connection)
	}); err != nil {
		t.Fatal(err)
	}
	liveRevision, liveScope, liveSecret := connection.Revision, connection.ReplayRevision, connection.CredentialID
	for range 2 {
		restored, err := bridge.RestoreModel(ctx, snapshot)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := provider.Call(ctx, restored, core.Request{Messages: []core.Message{core.User("retry")}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := bridge.Store.Get(ctx, "providers", connection.ID, &connection); err != nil {
		t.Fatal(err)
	}
	if connection.Revision != liveRevision || connection.ReplayRevision != liveScope || connection.CredentialID != liveSecret || refreshes != 1 || calls != 2 {
		t.Fatalf("refresh changed live connection or repeated: %+v refreshes=%d calls=%d", connection, refreshes, calls)
	}
	oldRaw, err := bridge.Store.Secret(ctx, originalSecretID)
	if err != nil || !strings.Contains(oldRaw, "refreshed-old-access") {
		t.Fatal("old grant was not rotated in place")
	}
	currentRaw, err := bridge.Store.Secret(ctx, liveSecret)
	if err != nil || !strings.Contains(currentRaw, "current-access") {
		t.Fatal("refresh overwrote live grant")
	}
	var frozenAfter frozenModel
	if err := json.Unmarshal(snapshot.Data, &frozenAfter); err != nil {
		t.Fatal(err)
	}
	if string(frozenBefore.Target.Data) != string(frozenAfter.Target.Data) {
		t.Fatal("credential refresh rewrote frozen replay identity")
	}
}
