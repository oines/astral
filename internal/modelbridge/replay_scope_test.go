package modelbridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral-core/models/provider/openai/responses"
	"github.com/oines/astral/internal/domain"
)

func TestMetadataChangesPreserveReplayScopeAndCredentialChangesDropPrivateReplay(t *testing.T) {
	ctx := context.Background()
	b := testBridge(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"reply","status":"completed","output":[{"id":"reason","type":"reasoning","encrypted_content":"opaque-state","summary":[]},{"id":"message","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ready","annotations":[]}]}]}`))
	}))
	defer server.Close()
	// Replay identity is owned by connection changes, not mutable display metadata.
	provider := domain.Provider{Meta: domain.Meta{ID: domain.ID("provider")}, Name: "Before", Protocol: "openai-responses", Dialect: "openai", BaseURL: server.URL}
	if err := b.SaveProvider(ctx, &provider, nil, nil); err != nil {
		t.Fatal(err)
	}
	profile := domain.ModelProfile{ProviderID: provider.ID, ModelID: "remote", Name: "Fixture", Recommendations: map[string]json.RawMessage{"context_window": raw(128000), "max_output_tokens": raw(8192), "streaming": raw(false)}}
	if err := b.SaveProfile(ctx, &profile); err != nil {
		t.Fatal(err)
	}
	original, err := b.construct(ctx, profile, provider)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := original.Stream(ctx, core.Request{ModelID: original.Spec().ID, MaxOutputTokens: 1000, Messages: []core.Message{core.User("begin")}}, func(core.ModelEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{false, true} {
		provider.Name = "After"
		provider.Enabled = &enabled
		provider.ReplayRevision = 999 // A client cannot forge the scope.
		if err := b.SaveProvider(ctx, &provider, nil, nil); err != nil {
			t.Fatal(err)
		}
		current, err := b.construct(ctx, profile, provider)
		if err != nil {
			t.Fatal(err)
		}
		req := core.Request{ModelID: current.Spec().ID, MaxOutputTokens: 1000, Messages: []core.Message{core.User("begin"), reply.Message, core.User("continue")}}
		if _, err := current.(*responses.Model).RequestBody(req); err != nil {
			t.Fatalf("metadata edit broke signed history: %v", err)
		}
	}
	key := "another-credential"
	if err := b.SaveProvider(ctx, &provider, &key, nil); err != nil {
		t.Fatal(err)
	}
	changed, err := b.construct(ctx, profile, provider)
	if err != nil {
		t.Fatal(err)
	}
	req := core.Request{ModelID: changed.Spec().ID, MaxOutputTokens: 1000, Messages: []core.Message{core.User("begin"), reply.Message, core.User("continue")}}
	prepared, err := core.PrepareRequest(ctx, changed, req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := changed.(*responses.Model).RequestBody(prepared)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(body)
	if strings.Contains(string(encoded), "opaque-state") || !strings.Contains(string(encoded), "ready") {
		t.Fatalf("private replay leaked or ordinary text was discarded: %s", encoded)
	}
}
