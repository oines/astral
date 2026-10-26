package modelbridge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func inventoryModel(id, name string, contextWindow int) map[string]any {
	return map[string]any{"id": id, "name": name, "context_window": contextWindow, "max_output_tokens": 4096}
}

func inventoryBody(models ...map[string]any) string {
	if models == nil {
		models = []map[string]any{}
	}
	return string(raw(map[string]any{"data": models}))
}

func inventoryProvider(t *testing.T, b *Bridge) domain.Provider {
	t.Helper()
	provider := domain.Provider{Name: "Inventory fixture", Protocol: "openai-completions", Dialect: "compatible", BaseURL: "https://fixture.example/v1"}
	if err := b.SaveProvider(context.Background(), &provider, nil, nil); err != nil {
		t.Fatal(err)
	}
	return provider
}

func currentInventory(t *testing.T, b *Bridge) []persistence.Record {
	t.Helper()
	records, err := b.Store.List(context.Background(), "models")
	if err != nil {
		t.Fatal(err)
	}
	return records
}

func inventoryProfiles(t *testing.T, b *Bridge) map[string]domain.ModelProfile {
	t.Helper()
	profiles := map[string]domain.ModelProfile{}
	for _, record := range currentInventory(t, b) {
		var profile domain.ModelProfile
		if err := json.Unmarshal(record.Data, &profile); err != nil {
			t.Fatal(err)
		}
		profiles[profile.ModelID] = profile
	}
	return profiles
}

func requireInventoryUnchanged(t *testing.T, b *Bridge, before []persistence.Record) {
	t.Helper()
	if got := currentInventory(t, b); !reflect.DeepEqual(before, got) {
		t.Fatalf("inventory changed unexpectedly\nbefore: %s\nafter:  %s", raw(before), raw(got))
	}
}

func TestProviderRefreshReconcilesCurrentInventory(t *testing.T) {
	ctx := context.Background()
	b := testBridge(t)
	provider := inventoryProvider(t, b)
	body := inventoryBody(
		inventoryModel("custom", "Original custom", 128000),
		inventoryModel("unchanged", "Old directory name", 128000),
		inventoryModel("removed", "Removed custom", 128000),
		inventoryModel("disabled", "Disabled", 128000),
		inventoryModel("restore", "Will be deleted", 128000),
	)
	b.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return authResponse(http.StatusOK, body), nil
	})}
	if result, err := b.RefreshProvider(ctx, provider.ID); err != nil || result.Added != 5 {
		t.Fatalf("initial refresh: %+v %v", result, err)
	}
	profiles := inventoryProfiles(t, b)
	custom := profiles["custom"]
	custom.Name, custom.Description = "My complete configuration", "Preserve every saved field"
	custom.Protocol, custom.Dialect = "openai-responses", "openai"
	custom.Overrides["max_output_tokens"] = raw(1024)
	if err := b.SaveProfile(ctx, &custom); err != nil {
		t.Fatal(err)
	}
	if !custom.UserModified {
		t.Fatal("saving a user edit did not mark the complete configuration as customized")
	}
	disabled := profiles["disabled"]
	enabled := false
	disabled.Enabled = &enabled
	if err := b.SaveProfile(ctx, &disabled); err != nil {
		t.Fatal(err)
	}
	removed := profiles["removed"]
	removed.Description = "A custom model still disappears when upstream removes it"
	if err := b.SaveProfile(ctx, &removed); err != nil {
		t.Fatal(err)
	}
	restore := profiles["restore"]
	if err := b.DeleteProfile(ctx, restore.ID, restore.Revision); err != nil {
		t.Fatal(err)
	}
	body = inventoryBody(
		inventoryModel("custom", "Upstream replacement name", 256000),
		inventoryModel("unchanged", "New directory name", 256000),
		inventoryModel("disabled", "Upstream enabled name", 256000),
		inventoryModel("restore", "Listed again", 256000),
		inventoryModel("added", "New model", 256000),
	)
	result, err := b.RefreshProvider(ctx, provider.ID)
	if err != nil || result != (RefreshResult{Added: 2, Updated: 1, Removed: 1, Unchanged: 2}) {
		t.Fatalf("reconcile: %+v %v", result, err)
	}
	current := inventoryProfiles(t, b)
	if got := current["custom"]; !reflect.DeepEqual(got, custom) {
		t.Fatalf("refresh overwrote a user-modified configuration\nbefore: %s\nafter:  %s", raw(custom), raw(got))
	}
	if got := current["disabled"]; !reflect.DeepEqual(got, disabled) || got.Enabled == nil || *got.Enabled {
		t.Fatal("refresh changed a disabled model")
	}
	updated := current["unchanged"]
	if updated.ID != profiles["unchanged"].ID || updated.Revision != profiles["unchanged"].Revision+1 || updated.Name != "New directory name" || string(updated.Recommendations["context_window"]) != "256000" || updated.UserModified {
		t.Fatalf("unmodified model did not follow the directory with a stable ID: %+v", updated)
	}
	if _, exists := current["removed"]; exists {
		t.Fatal("a customized model absent from the directory survived")
	}
	if got := current["restore"]; got.ID == "" || got.ID == restore.ID || got.Name != "Listed again" || got.UserModified {
		t.Fatalf("deleted model was not recreated from the current directory: %+v", got)
	}
	beforeNoop := currentInventory(t, b)
	result, err = b.RefreshProvider(ctx, provider.ID)
	if err != nil || result != (RefreshResult{Unchanged: 5}) {
		t.Fatalf("repeat refresh: %+v %v", result, err)
	}
	requireInventoryUnchanged(t, b, beforeNoop)

	body = inventoryBody()
	result, err = b.RefreshProvider(ctx, provider.ID)
	if err != nil || result != (RefreshResult{Removed: 5}) || len(currentInventory(t, b)) != 0 {
		t.Fatalf("successful empty directory did not clear inventory: %+v %v", result, err)
	}
}

func TestProviderRefreshFailurePreservesWholeInventory(t *testing.T) {
	for _, scenario := range []string{"network", "http", "json", "missing-list", "null-list", "missing-model-id", "pagination-http", "pagination-cursor"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			b := testBridge(t)
			provider := inventoryProvider(t, b)
			failure := false
			b.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if !failure {
					return authResponse(200, inventoryBody(inventoryModel("saved", "Saved", 128000))), nil
				}
				switch scenario {
				case "network":
					return nil, errors.New("fixture network failure")
				case "http":
					return authResponse(503, `{}`), nil
				case "json":
					return authResponse(200, `{"data":`), nil
				case "missing-list":
					return authResponse(200, `{}`), nil
				case "null-list":
					return authResponse(200, `{"data":null}`), nil
				case "missing-model-id":
					return authResponse(200, `{"data":[{"name":"Broken entry"}]}`), nil
				case "pagination-http":
					if r.URL.Query().Get("after") != "" {
						return authResponse(503, `{}`), nil
					}
					return authResponse(200, `{"data":[{"id":"partial-new"}],"has_more":true,"last_id":"next"}`), nil
				default:
					return authResponse(200, `{"data":[{"id":"partial-new"}],"has_more":true}`), nil
				}
			})}
			if _, err := b.RefreshProvider(ctx, provider.ID); err != nil {
				t.Fatal(err)
			}
			before := currentInventory(t, b)
			failure = true
			result, err := b.RefreshProvider(ctx, provider.ID)
			if err == nil || result != (RefreshResult{}) {
				t.Fatalf("failed refresh did not reject the complete transaction: %+v %v", result, err)
			}
			requireInventoryUnchanged(t, b, before)
		})
	}
}

func TestGoogleRefreshRejectsMalformedEntriesBeforeFiltering(t *testing.T) {
	ctx := context.Background()
	b := testBridge(t)
	provider := domain.Provider{Name: "Google inventory", Protocol: "google-generative-ai", Dialect: "google", BaseURL: "https://fixture.example/v1beta"}
	if err := b.SaveProvider(ctx, &provider, nil, nil); err != nil {
		t.Fatal(err)
	}
	body := `{"models":[{"name":"models/saved","displayName":"Saved","inputTokenLimit":128000,"outputTokenLimit":4096,"supportedGenerationMethods":["generateContent"]}]}`
	b.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return authResponse(http.StatusOK, body), nil
	})}
	if result, err := b.RefreshProvider(ctx, provider.ID); err != nil || result.Added != 1 {
		t.Fatalf("initial Google refresh: %+v %v", result, err)
	}
	before := currentInventory(t, b)
	for _, malformed := range []string{`{"models":[{}]}`, `{"models":[null]}`} {
		body = malformed
		result, err := b.RefreshProvider(ctx, provider.ID)
		if err == nil || result != (RefreshResult{}) {
			t.Fatalf("malformed Google entry was filtered into an empty directory: %s %+v %v", malformed, result, err)
		}
		requireInventoryUnchanged(t, b, before)
	}
	body = `{"models":[{"name":"models/embedding","supportedGenerationMethods":["embedContent"]}]}`
	result, err := b.RefreshProvider(ctx, provider.ID)
	if err != nil || result != (RefreshResult{Removed: 1}) || len(currentInventory(t, b)) != 0 {
		t.Fatalf("valid embedding entry was not filtered from the Agent model directory: %+v %v", result, err)
	}
}

func TestProviderRefreshRejectsEditsDuringDirectoryFetch(t *testing.T) {
	for _, scenario := range []string{"provider-edit", "model-edit", "model-create", "model-delete"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			b := testBridge(t)
			provider := inventoryProvider(t, b)
			var duringFetch func()
			b.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if duringFetch == nil {
					return authResponse(200, inventoryBody(inventoryModel("saved", "Saved", 128000))), nil
				}
				duringFetch()
				return authResponse(200, inventoryBody(inventoryModel("replacement", "New directory", 256000))), nil
			})}
			if _, err := b.RefreshProvider(ctx, provider.ID); err != nil {
				t.Fatal(err)
			}
			profile := inventoryProfiles(t, b)["saved"]
			var afterEdit []persistence.Record
			duringFetch = func() {
				var err error
				switch scenario {
				case "provider-edit":
					provider.Name = "Renamed during fetch"
					err = b.SaveProvider(ctx, &provider, nil, nil)
				case "model-edit":
					profile.Description = "Edited during fetch"
					err = b.SaveProfile(ctx, &profile)
				case "model-create":
					manual := domain.ModelProfile{ProviderID: provider.ID, ModelID: "manual", Name: "Created during fetch", Recommendations: map[string]json.RawMessage{"context_window": raw(128000), "max_output_tokens": raw(4096)}}
					err = b.SaveProfile(ctx, &manual)
				case "model-delete":
					err = b.DeleteProfile(ctx, profile.ID, profile.Revision)
				}
				if err != nil {
					t.Fatal(err)
				}
				afterEdit = currentInventory(t, b)
			}
			result, err := b.RefreshProvider(ctx, provider.ID)
			var problem *domain.Error
			if !errors.As(err, &problem) || problem.Status != http.StatusConflict || result != (RefreshResult{}) {
				t.Fatalf("refresh accepted a concurrent edit: %+v %v", result, err)
			}
			requireInventoryUnchanged(t, b, afterEdit)
		})
	}
}

func TestDeleteProviderRemovesChildrenAndPreservesFrozenVersions(t *testing.T) {
	ctx := context.Background()
	b := testBridge(t)
	provider, otherProvider := inventoryProvider(t, b), inventoryProvider(t, b)
	key := "frozen-provider-key"
	if err := b.SaveProvider(ctx, &provider, &key, nil); err != nil {
		t.Fatal(err)
	}
	profiles := []domain.ModelProfile{
		{ProviderID: provider.ID, ModelID: "child-one", Name: "Child one"},
		{ProviderID: provider.ID, ModelID: "child-two", Name: "Child two"},
		{ProviderID: otherProvider.ID, ModelID: "unrelated", Name: "Unrelated"},
	}
	for i := range profiles {
		profiles[i].Recommendations = map[string]json.RawMessage{"context_window": raw(128000), "max_output_tokens": raw(4096)}
		if err := b.SaveProfile(ctx, &profiles[i]); err != nil {
			t.Fatal(err)
		}
	}
	before := currentInventory(t, b)
	if err := b.DeleteProvider(ctx, provider.ID, provider.Revision-1); err == nil {
		t.Fatal("stale delete revision was accepted")
	}
	requireInventoryUnchanged(t, b, before)
	if err := b.DeleteProvider(ctx, provider.ID, provider.Revision); err != nil {
		t.Fatal(err)
	}
	var deleted domain.Provider
	if err := b.Store.Get(ctx, "providers", provider.ID, &deleted); !domain.IsMissing(err) {
		t.Fatalf("provider remains in current inventory: %v", err)
	}
	if got := inventoryProfiles(t, b); len(got) != 1 || got["unrelated"].ID != profiles[2].ID {
		t.Fatalf("deletion did not respect provider ownership: %+v", got)
	}
	var frozenProvider domain.Provider
	if err := b.Store.Version(ctx, "providers", provider.ID, provider.Revision, &frozenProvider); err != nil || !reflect.DeepEqual(frozenProvider, provider) {
		t.Fatalf("provider version lost: %+v %v", frozenProvider, err)
	}
	if got, err := b.Store.Secret(ctx, frozenProvider.CredentialID); err != nil || got != key {
		t.Fatalf("frozen provider credential lost: %q %v", got, err)
	}
	for _, profile := range profiles[:2] {
		var frozen domain.ModelProfile
		if err := b.Store.Version(ctx, "models", profile.ID, profile.Revision, &frozen); err != nil || !reflect.DeepEqual(frozen, profile) {
			t.Fatalf("child version lost: %+v %v", frozen, err)
		}
	}
}

func TestAccountProvidersShareRefreshInventoryRules(t *testing.T) {
	for _, preset := range []string{"codex", "github-copilot"} {
		t.Run(preset, func(t *testing.T) {
			ctx := context.Background()
			b := testBridge(t)
			provider := saveAuthProvider(t, b, preset)
			if err := b.Store.Update(ctx, func(tx *persistence.Tx) error {
				credentialID, err := tx.PutSecret(ctx, string(raw(oauthTokens{AccessToken: "fixture-token", ExpiresAt: time.Now().Add(time.Hour)})))
				if err != nil {
					return err
				}
				provider.CredentialID = credentialID
				return tx.Save(ctx, "providers", &provider.Meta, provider)
			}); err != nil {
				t.Fatal(err)
			}
			accountBody := func(ids ...string) string {
				items := []map[string]any{}
				for _, id := range ids {
					item := inventoryModel(id, "Directory "+id, 128000)
					if preset == "codex" {
						item["slug"], item["display_name"], item["visibility"] = id, "Directory "+id, "list"
					} else {
						item["model_picker_enabled"], item["supported_endpoints"] = true, []string{"/responses"}
					}
					items = append(items, item)
				}
				field := "data"
				if preset == "codex" {
					field = "models"
				}
				return string(raw(map[string]any{field: items}))
			}
			body := accountBody("custom", "removed", "restore")
			b.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if !strings.HasSuffix(r.URL.Path, "/models") || r.Header.Get("Authorization") != "Bearer fixture-token" {
					t.Fatalf("unexpected account catalog request: %s", r.URL)
				}
				return authResponse(200, body), nil
			})}
			if result, err := b.RefreshProvider(ctx, provider.ID); err != nil || result.Added != 3 {
				t.Fatalf("initial account refresh: %+v %v", result, err)
			}
			profiles := inventoryProfiles(t, b)
			custom := profiles["custom"]
			custom.Name, custom.Description = "My account model", "Preserve the whole account configuration"
			custom.Overrides["max_output_tokens"] = raw(1024)
			if err := b.SaveProfile(ctx, &custom); err != nil {
				t.Fatal(err)
			}
			removed := profiles["restore"]
			if err := b.DeleteProfile(ctx, removed.ID, removed.Revision); err != nil {
				t.Fatal(err)
			}
			body = accountBody("custom", "restore", "added")
			result, err := b.RefreshProvider(ctx, provider.ID)
			if err != nil || result != (RefreshResult{Added: 2, Removed: 1, Unchanged: 1}) {
				t.Fatalf("account reconcile: %+v %v", result, err)
			}
			current := inventoryProfiles(t, b)
			if !reflect.DeepEqual(current["custom"], custom) || current["restore"].ID == removed.ID {
				t.Fatal("account provider did not preserve edits and restore listed deleted models")
			}
			before := currentInventory(t, b)
			partialBody := strings.TrimSuffix(accountBody("partial"), "}")
			for _, invalid := range []string{
				`{}`, `null`, `{"data":null,"models":null}`, `{"data":`, accountBody(""),
				partialBody + `,"has_more":true}`, partialBody + `,"nextPageToken":"next"}`,
			} {
				body = invalid
				if result, err := b.RefreshProvider(ctx, provider.ID); err == nil || result != (RefreshResult{}) {
					t.Fatalf("invalid account directory accepted: %q %+v %v", invalid, result, err)
				}
				requireInventoryUnchanged(t, b, before)
			}
			body = accountBody()
			if result, err := b.RefreshProvider(ctx, provider.ID); err != nil || result.Removed != 3 || len(currentInventory(t, b)) != 0 {
				t.Fatalf("account empty directory did not clear inventory: %+v %v", result, err)
			}
		})
	}
}
