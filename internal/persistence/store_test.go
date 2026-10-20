package persistence_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func TestVersionedConfigurationAndCredentialReopen(t *testing.T) {
	directory := t.TempDir()
	ctx := context.Background()
	store, err := persistence.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	provider := domain.Provider{Meta: domain.Meta{ID: "provider"}, Name: "before", Protocol: "openai-responses"}
	const secret = "test-credential-not-a-real-key"
	err = store.Update(ctx, func(tx *persistence.Tx) error {
		var err error
		provider.CredentialID, err = tx.PutSecret(ctx, secret)
		if err != nil {
			return err
		}
		return tx.Save(ctx, "provider", &provider.Meta, provider)
	})
	if err != nil {
		t.Fatal(err)
	}
	// Stored metadata is authoritative even when the value was copied.
	var first domain.Provider
	if err = store.Get(ctx, "provider", provider.ID, &first); err != nil {
		t.Fatal(err)
	}
	provider.Name = "after"
	if err = store.Save(ctx, "provider", &provider.Meta, &provider); err != nil {
		t.Fatal(err)
	}
	if err = store.Save(ctx, "provider", &first.Meta, &first); err == nil {
		t.Fatal("stale write overwrote current configuration")
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = persistence.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var previous domain.Provider
	if err = store.Version(ctx, "provider", "provider", 1, &previous); err != nil || previous.Name != "before" {
		t.Fatalf("previous revision missing: %+v %v", previous, err)
	}
	key, err := store.Secret(ctx, provider.CredentialID)
	if err != nil || key != secret {
		t.Fatal("credential did not reopen")
	}
	for _, name := range []string{"astral.db", "astral.db-wal"} {
		raw, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), secret) {
			t.Fatal("credential stored in plaintext")
		}
	}
}

func TestRecommendationsPreserveExplicitOverrideAndRestore(t *testing.T) {
	profile := domain.ModelProfile{Recommendations: map[string]json.RawMessage{"context_window": json.RawMessage(`128000`), "max_output_tokens": json.RawMessage(`8192`)},
		Overrides: map[string]json.RawMessage{"max_output_tokens": json.RawMessage(`2048`), "defaults": json.RawMessage(`{"reasoning":{"enabled":false}}`)}}
	profile.Recommendations["max_output_tokens"] = json.RawMessage(`16384`)
	values, sources, err := profile.Effective()
	if err != nil || values.MaxOutputTokens != 2048 || sources["max_output_tokens"] != "override" || values.Defaults.Reasoning.Enabled == nil || *values.Defaults.Reasoning.Enabled {
		t.Fatalf("override lost: %+v %v", values, err)
	}
	delete(profile.Overrides, "max_output_tokens")
	values, sources, err = profile.Effective()
	if err != nil || values.MaxOutputTokens != 16384 || sources["max_output_tokens"] != "recommended" {
		t.Fatal("restoring recommendations failed")
	}
}

func TestObservationIdentityKeepsEditsAndIsScopedToConnection(t *testing.T) {
	store, err := persistence.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	for i, event := range []domain.Observation{
		{ID: "1", ConnectionID: "a", ConversationID: "c", ExternalEventID: "10", MessageID: "50", Kind: "message.created"},
		{ID: "duplicate", ConnectionID: "a", ConversationID: "c", ExternalEventID: "10", MessageID: "50", Kind: "message.created"},
		{ID: "2", ConnectionID: "a", ConversationID: "c", ExternalEventID: "11", MessageID: "50", Kind: "message.edited"},
		{ID: "3", ConnectionID: "b", ConversationID: "d", ExternalEventID: "10", MessageID: "50", Kind: "message.created"},
	} {
		added, err := store.Archive(ctx, event)
		if err != nil {
			t.Fatal(err)
		}
		if added != (i != 1) {
			t.Fatalf("incorrect deduplication for event %d", i)
		}
	}
	events, err := store.Observations(ctx, "c", 0, 100)
	if err != nil || len(events) != 2 || events[1].Observation.Kind != "message.edited" {
		t.Fatal("legitimate edit disappeared")
	}
}
