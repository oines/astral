package modelbridge

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func TestAccountCatalogUsesOnlyAccountInventoryAndAdvertisedRoutes(t *testing.T) {
	tests := []struct {
		preset, body string
		want         []string
		protocols    []string
	}{
		{"codex", `[{"slug":"listed","display_name":"Listed","visibility":"list","context_window":200000},{"slug":"hidden","visibility":"hide"},{"slug":"internal"}]`, []string{"listed"}, []string{"openai-responses"}},
		{"github-copilot", `[{"id":"responses","model_picker_enabled":true,"supported_endpoints":["/chat/completions","/responses"],"capabilities":{"type":"chat","limits":{"max_context_window_tokens":128000},"supports":{"tool_calls":true}}},{"id":"chat","model_picker_enabled":true,"supported_endpoints":["/chat/completions"]},{"id":"messages","model_picker_enabled":true,"supported_endpoints":["/messages"]},{"id":"disabled","model_picker_enabled":true,"policy":{"state":"disabled"}},{"id":"not-picker"},{"id":"embed","model_picker_enabled":true,"capabilities":{"type":"embedding"}},{"id":"unknown-route","model_picker_enabled":true,"supported_endpoints":["/embeddings"]}]`, []string{"responses", "chat", "messages"}, []string{"openai-responses", "openai-completions", "anthropic-messages"}},
		{"github-copilot", `[{"id":"legacy","model_picker_enabled":false},{"id":"disabled","policy":{"state":"disabled"}}]`, []string{"legacy"}, []string{"openai-completions"}},
	}
	for _, tt := range tests {
		t.Run(tt.preset+tt.want[0], func(t *testing.T) {
			var items []json.RawMessage
			if err := json.Unmarshal([]byte(tt.body), &items); err != nil {
				t.Fatal(err)
			}
			got, err := accountDiscoveries(domain.Provider{PresetID: tt.preset}, "account", items)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("unexpected account inventory: %+v", got)
			}
			for i := range got {
				if got[i].ID != tt.want[i] || got[i].Protocol != tt.protocols[i] {
					t.Fatalf("wrong route/inventory: %+v", got)
				}
			}
		})
	}
}

func TestAccountCatalogDistinguishesAuthorizationAndPolicyFailures(t *testing.T) {
	for _, status := range []int{401, 403} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			b := testBridge(t)
			ctx := context.Background()
			p := saveAuthProvider(t, b, "codex")
			token, _ := json.Marshal(oauthTokens{AccessToken: "test", ExpiresAt: time.Now().Add(time.Hour)})
			err := b.Store.Update(ctx, func(tx *persistence.Tx) error {
				id, e := tx.PutSecret(ctx, string(token))
				p.CredentialID = id
				return e
			})
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			b.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Query().Get("client_version") != "1.0.0" {
					t.Fatal("missing Codex catalog client version")
				}
				return authResponse(status, `{"error":"private upstream detail"}`), nil
			})}
			_, err = b.accountCatalog(ctx, p)
			if err == nil || calls != 1 || strings.Contains(err.Error(), "private upstream detail") {
				t.Fatalf("unsafe error/retry behavior: %v calls=%d", err, calls)
			}
			if status == 401 && !strings.Contains(err.Error(), "expired") || status == 403 && !strings.Contains(err.Error(), "subscription") {
				t.Fatalf("wrong failure semantics: %v", err)
			}
		})
	}
}

func TestAccountImportCannotExpandAuthenticatedInventory(t *testing.T) {
	for _, preset := range []string{"codex", "github-copilot"} {
		for _, scenario := range []string{"not-listed", "catalog-failed", "listed"} {
			t.Run(preset+"/"+scenario, func(t *testing.T) {
				b := testBridge(t)
				ctx := context.Background()
				p := saveAuthProvider(t, b, preset)
				if err := b.Store.Update(ctx, func(tx *persistence.Tx) error {
					id, err := tx.PutSecret(ctx, string(raw(oauthTokens{AccessToken: "fixture", ExpiresAt: time.Now().Add(time.Hour)})))
					if err != nil {
						return err
					}
					p.CredentialID = id
					return tx.Save(ctx, "providers", &p.Meta, p)
				}); err != nil {
					t.Fatal(err)
				}
				snapshot, err := parseModelCatalog([]byte(catalogFixture), time.Now())
				if err != nil {
					t.Fatal(err)
				}
				b.ModelCatalog = &ModelCatalog{loaded: true, snapshot: snapshot, now: time.Now}
				b.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/models") {
						t.Fatalf("unexpected upstream call %s %s", r.Method, r.URL)
					}
					if scenario == "catalog-failed" {
						return authResponse(503, `{}`), nil
					}
					id := "other-model"
					if scenario == "listed" {
						id = "gpt-fixture"
					}
					if preset == "codex" {
						return authResponse(200, `{"models":[{"slug":"`+id+`","visibility":"list"}]}`), nil
					}
					return authResponse(200, `{"data":[{"id":"`+id+`","model_picker_enabled":true,"supported_endpoints":["/responses"]}]}`), nil
				})}
				// Public recommendation remains a read-only description, even for a model
				// absent from the account. It is not an account discovery/import result.
				recommended, err := b.ModelRecommendation(ctx, p.ID, "gpt-fixture")
				if err != nil || len(recommended.Recommendations) == 0 {
					t.Fatalf("public metadata unavailable: %+v %v", recommended, err)
				}
				before, err := b.Store.List(ctx, "models")
				if err != nil || len(before) != 0 {
					t.Fatal("recommendation created model inventory")
				}
				result, err := b.ImportModels(ctx, p.ID, ImportModelsInput{IDs: []string{"gpt-fixture"}})
				records, listErr := b.Store.List(ctx, "models")
				if listErr != nil {
					t.Fatal(listErr)
				}
				if scenario == "listed" {
					if err != nil || len(result.Imported) != 1 || len(records) != 1 {
						t.Fatalf("listed model not imported %+v %v", result, err)
					}
				} else if err == nil || len(result.Imported) != 0 || len(records) != 0 {
					t.Fatalf("account inventory expanded from public metadata: %+v %v records=%d", result, err, len(records))
				}
			})
		}
	}
}
