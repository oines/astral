package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/modelbridge"
	"github.com/oines/astral/internal/testkit"
)

func decodeModelResponse[T any](t *testing.T, response *http.Response, wantStatus int) T {
	t.Helper()
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != wantStatus {
		t.Fatalf("model resource response: HTTP %d, want %d, body=%s, error=%v", response.StatusCode, wantStatus, raw, err)
	}
	var result T
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("invalid model resource response: %v", err)
	}
	return result
}

func modelInventoryViews(t *testing.T, p *testkit.Platform, server *httptest.Server) [][]domain.ModelProfile {
	t.Helper()
	list := decodeModelResponse[[]domain.ModelProfile](t, request(t, p.Context, server, http.MethodGet, "/api/v1/models", nil, true), http.StatusOK)
	bootstrap := decodeModelResponse[Bootstrap](t, request(t, p.Context, server, http.MethodGet, "/api/v1/bootstrap", nil, true), http.StatusOK)
	return [][]domain.ModelProfile{list, bootstrap.Models}
}

func TestDeleteProviderAPIAtomicallyRemovesItsCurrentModelInventory(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	_, server := apiFixture(t, p)
	var provider domain.Provider
	if err := p.Store.Get(p.Context, "providers", p.Profile.ProviderID, &provider); err != nil {
		t.Fatal(err)
	}
	if err := p.Store.Get(p.Context, "models", p.Profile.ID, &p.Profile); err != nil {
		t.Fatal(err)
	}
	child := p.Profile
	child.Meta, child.ModelID = domain.Meta{}, "second-child"
	if err := p.Harness.Models.SaveProfile(p.Context, &child); err != nil {
		t.Fatal(err)
	}
	unrelatedProvider := provider
	unrelatedProvider.Meta = domain.Meta{}
	unrelatedProvider.Name = "Unrelated provider"
	if err := p.Harness.Models.SaveProvider(p.Context, &unrelatedProvider, nil, nil); err != nil {
		t.Fatal(err)
	}
	unrelatedModel := p.Profile
	unrelatedModel.Meta, unrelatedModel.ProviderID = domain.Meta{}, unrelatedProvider.ID
	if err := p.Harness.Models.SaveProfile(p.Context, &unrelatedModel); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/providers/" + provider.ID + "?revision="
	decodeModelResponse[map[string]any](t, request(t, p.Context, server, http.MethodDelete, path+strconv.FormatInt(provider.Revision+1, 10), nil, true), http.StatusConflict)
	for _, profile := range []domain.ModelProfile{p.Profile, child} {
		var current domain.ModelProfile
		if err := p.Store.Get(p.Context, "models", profile.ID, &current); err != nil || current.Revision != profile.Revision {
			t.Fatal("rejected deletion partially removed or changed a child model")
		}
	}
	decodeModelResponse[map[string]any](t, request(t, p.Context, server, http.MethodDelete, path+strconv.FormatInt(provider.Revision, 10), nil, true), http.StatusOK)
	if err := p.Store.Get(p.Context, "providers", provider.ID, &domain.Provider{}); !domain.IsMissing(err) {
		t.Fatalf("deleted Provider remained current: %v", err)
	}
	for _, id := range []string{p.Profile.ID, child.ID} {
		if err := p.Store.Get(p.Context, "models", id, &domain.ModelProfile{}); !domain.IsMissing(err) {
			t.Fatalf("deleted Provider left a current child %s: %v", id, err)
		}
	}
	var currentProvider domain.Provider
	var currentModel domain.ModelProfile
	if err := p.Store.Get(p.Context, "providers", unrelatedProvider.ID, &currentProvider); err != nil || !reflect.DeepEqual(currentProvider, unrelatedProvider) {
		t.Fatal("Provider deletion changed an unrelated connection")
	}
	if err := p.Store.Get(p.Context, "models", unrelatedModel.ID, &currentModel); err != nil || !reflect.DeepEqual(currentModel, unrelatedModel) {
		t.Fatal("Provider deletion changed an unrelated model")
	}
	for _, profiles := range modelInventoryViews(t, p, server) {
		if len(profiles) != 1 || profiles[0].ID != unrelatedModel.ID || profiles[0].ProviderID != unrelatedProvider.ID {
			t.Fatalf("API inventory still presented deleted Provider models: %+v", profiles)
		}
	}
	if _, err := p.Harness.Models.Resolve(p.Context, p.Session.ModelRef, nil); err != nil {
		t.Fatalf("resource deletion lost historical request configuration: %v", err)
	}
	if p.Requests.Load() != 0 {
		t.Fatal("resource inventory operations started model generation")
	}
}

func TestProviderRefreshAPIReconcilesInventoryAndReplacesPerModelRoute(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	api, server := apiFixture(t, p)
	var body atomic.Value
	body.Store(`{"data":[{"id":"retained","name":"Retained","context_window":128000,"max_output_tokens":4096},{"id":"removed","context_window":128000,"max_output_tokens":4096}]}`)
	var catalogCalls atomic.Int64
	catalog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/models" {
			t.Errorf("unexpected catalog request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		catalogCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body.Load().(string))
	}))
	t.Cleanup(catalog.Close)
	provider := domain.Provider{Name: "Refreshed provider", Protocol: "openai-completions", Dialect: "compatible", BaseURL: catalog.URL}
	if err := p.Harness.Models.SaveProvider(p.Context, &provider, nil, nil); err != nil {
		t.Fatal(err)
	}
	refresh := func() modelbridge.RefreshResult {
		t.Helper()
		return decodeModelResponse[modelbridge.RefreshResult](t, request(t, p.Context, server, http.MethodPost, "/api/v1/providers/"+provider.ID+"/refresh", nil, true), http.StatusOK)
	}
	if result := refresh(); result.Added != 2 || result.Removed != 0 {
		t.Fatalf("Provider refresh failed to add inventory: %+v", result)
	}
	var retained domain.ModelProfile
	for _, profile := range modelInventoryViews(t, p, server)[0] {
		if profile.ProviderID == provider.ID && profile.ModelID == "retained" {
			retained = profile
		}
	}
	if retained.ID == "" {
		t.Fatal("refresh did not expose the retained model")
	}
	body.Store(`{"data":[{"id":"retained","name":"Updated name","context_window":256000,"max_output_tokens":4096},{"id":"added","context_window":128000,"max_output_tokens":4096}]}`)
	if result := refresh(); result != (modelbridge.RefreshResult{Added: 1, Updated: 1, Removed: 1}) {
		t.Fatalf("refresh route did not synchronize the full Provider inventory: %+v", result)
	}
	for _, profiles := range modelInventoryViews(t, p, server) {
		foundRetained, foundAdded := false, false
		for _, profile := range profiles {
			if profile.ProviderID != provider.ID {
				continue
			}
			switch profile.ModelID {
			case "retained":
				foundRetained = profile.ID == retained.ID && profile.Name == "Updated name" && profile.Revision > retained.Revision
			case "added":
				foundAdded = true
			default:
				t.Fatalf("refresh left a model absent from /models: %+v", profile)
			}
		}
		if !foundRetained || !foundAdded {
			t.Fatal("refresh lost stable identity, metadata updates or newly added inventory")
		}
	}
	response := request(t, p.Context, server, http.MethodPost, "/api/v1/models/"+retained.ID+"/refresh", nil, true)
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("per-model refresh route still exists: HTTP %d", response.StatusCode)
	}
	if _, exists := api.Contract.Paths["/api/v1/models/{id}/refresh"]; exists {
		t.Fatal("removed per-model refresh route remained in the API contract")
	}
	if _, exists := api.Contract.Paths["/api/v1/providers/{id}/refresh"]; !exists || catalogCalls.Load() != 2 {
		t.Fatal("Provider refresh route was missing or fetched an unexpected catalog")
	}
}

func TestModelInventoryAPIsExposeProviderAvailabilityConsistently(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	_, server := apiFixture(t, p)
	var provider domain.Provider
	if err := p.Store.Get(p.Context, "providers", p.Profile.ProviderID, &provider); err != nil {
		t.Fatal(err)
	}
	check := func(wantAvailable bool, reason string) {
		t.Helper()
		for _, profiles := range modelInventoryViews(t, p, server) {
			if len(profiles) != 1 {
				t.Fatalf("unexpected inventory: %+v", profiles)
			}
			profile := profiles[0]
			if profile.ID != p.Profile.ID || profile.Available == nil || *profile.Available != wantAvailable || profile.UnavailableReason != reason || profile.ProviderName != provider.Name {
				t.Fatalf("model availability differs from connection state: %+v", profile)
			}
		}
	}
	check(true, "")
	enabled := false
	provider.Enabled = &enabled
	provider = decodeModelResponse[domain.Provider](t, request(t, p.Context, server, http.MethodPost, "/api/v1/providers", bytes.NewReader(testkit.JSON(ProviderInput{Provider: provider})), true), http.StatusOK)
	check(false, "provider_disabled")
	response := request(t, p.Context, server, http.MethodPost, "/api/v1/sessions/"+p.Session.ID+"/model", strings.NewReader(`{"profile_id":"`+p.Profile.ID+`"}`), true)
	decodeModelResponse[map[string]any](t, response, http.StatusConflict)
	enabled = true
	provider.Enabled = &enabled
	provider = decodeModelResponse[domain.Provider](t, request(t, p.Context, server, http.MethodPost, "/api/v1/providers", bytes.NewReader(testkit.JSON(ProviderInput{Provider: provider})), true), http.StatusOK)
	check(true, "")
	if err := p.Store.Get(p.Context, "models", p.Profile.ID, &p.Profile); err != nil {
		t.Fatal(err)
	}
	modelEnabled := false
	p.Profile.Enabled = &modelEnabled
	decodeModelResponse[domain.ModelProfile](t, request(t, p.Context, server, http.MethodPost, "/api/v1/models", bytes.NewReader(testkit.JSON(p.Profile)), true), http.StatusOK)
	check(false, "model_disabled")
}
