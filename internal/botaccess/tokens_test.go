package botaccess

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func tokenStore(t *testing.T) *persistence.Store {
	t.Helper()
	store, err := persistence.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func saveBot(t *testing.T, store *persistence.Store, id string) domain.Agent {
	t.Helper()
	bot := domain.Agent{Meta: domain.Meta{ID: id}, Name: id}
	if err := store.Save(t.Context(), "agents", &bot.Meta, bot); err != nil {
		t.Fatal(err)
	}
	return bot
}

func token(t *testing.T, s *Service, botID, name string) CreatedToken {
	t.Helper()
	created, err := s.CreateToken(t.Context(), botID, name)
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func authorize(t *testing.T, s *Service, botID, bearer string) context.Context {
	t.Helper()
	id, ctx, release, err := s.Authorize(t.Context(), bearer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	if id != botID {
		t.Fatalf("credential resolved to Bot %q, want %q", id, botID)
	}
	return ctx
}

func assertTokenCancelled(t *testing.T, ctx context.Context) {
	t.Helper()
	deadline, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	select {
	case <-ctx.Done():
	case <-deadline.Done():
		t.Fatal("authorized request remained active")
	}
}

func TestTokenOwnershipHashAndScopedRevocation(t *testing.T) {
	store := tokenStore(t)
	first := saveBot(t, store, "first")
	second := saveBot(t, store, "second")
	s := New(store)
	a := token(t, s, first.ID, "  Editor  ")
	b := token(t, s, first.ID, "Other client")
	c := token(t, s, second.ID, "DevOps")
	if a.Name != "Editor" || !strings.HasPrefix(a.Token, "bapi_") {
		t.Fatal("unexpected new credential metadata")
	}
	rows, err := store.Retained(t.Context(), tokenKind)
	if err != nil || len(rows) == 0 {
		t.Fatalf("stored tokens: %d, %v", len(rows), err)
	}
	for _, row := range rows {
		if bytes.Contains(row.Data, []byte(a.Token)) || !bytes.Contains(row.Data, []byte(`"hash":`)) {
			t.Fatal("credential persisted without its hashed representation")
		}
	}
	firstRequest := authorize(t, s, first.ID, "Bearer "+a.Token)
	secondRequest := authorize(t, s, first.ID, "bearer  "+a.Token)
	otherClient := authorize(t, s, first.ID, "Bearer "+b.Token)
	otherBot := authorize(t, s, second.ID, "Bearer "+c.Token)
	if err := s.RevokeToken(t.Context(), second.ID, a.ID); !domain.IsMissing(err) || firstRequest.Err() != nil {
		t.Fatalf("foreign Bot revoked credential: %v, %v", err, firstRequest.Err())
	}
	list, err := s.ListTokens(t.Context(), first.ID)
	if err != nil || len(list) != 2 {
		t.Fatalf("Bot token directory: %+v, %v", list, err)
	}
	for _, item := range list {
		if item.ID == c.ID || item.LastUsedAt == nil {
			t.Fatalf("incorrect Bot token metadata: %+v", item)
		}
	}
	encoded, err := json.Marshal(list)
	if err != nil || bytes.Contains(encoded, []byte(a.Token)) || bytes.Contains(encoded, []byte(`"hash"`)) {
		t.Fatal("token directory returned private credential material")
	}
	if _, _, _, err := s.Authorize(t.Context(), "Bearer "+a.ID+"."+strings.Repeat("a", 43)); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("incorrect secret accepted: %v", err)
	}
	if err := s.RevokeToken(t.Context(), first.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	if firstRequest.Err() == nil || secondRequest.Err() == nil || otherClient.Err() != nil || otherBot.Err() != nil {
		t.Fatal("revocation canceled the wrong request scope")
	}
	if _, _, _, err := New(store).Authorize(t.Context(), "Bearer "+a.Token); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked token survived persisted reload: %v", err)
	}
	rows, err = store.Retained(t.Context(), tokenKind)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ID == a.ID {
			t.Fatal("revoked credential was retained in a storage revision")
		}
	}
}

func TestTokenWitnessesRevocationAndBotDeletion(t *testing.T) {
	store := tokenStore(t)
	bot := saveBot(t, store, "first")
	manager, gateway := New(store), New(store)
	issued := token(t, manager, bot.ID, "First")
	working := authorize(t, gateway, bot.ID, "Bearer "+issued.Token)
	if err := manager.RevokeToken(t.Context(), bot.ID, issued.ID); err != nil {
		t.Fatal(err)
	}
	assertTokenCancelled(t, working)

	issued = token(t, manager, bot.ID, "Second")
	working = authorize(t, gateway, bot.ID, "Bearer "+issued.Token)
	bot.Deletion = true
	if err := store.Save(t.Context(), "agents", &bot.Meta, bot); err != nil {
		t.Fatal(err)
	}
	assertTokenCancelled(t, working)
	if _, _, _, err := gateway.Authorize(t.Context(), "Bearer "+issued.Token); err == nil {
		t.Fatal("deleting Bot still accepts token authentication")
	}
	if err := store.Update(t.Context(), func(tx *persistence.Tx) error { return tx.PurgeAgentData(t.Context(), bot.ID) }); err != nil {
		t.Fatal(err)
	}
	rows, err := store.Retained(t.Context(), tokenKind)
	if err != nil || len(rows) != 0 {
		t.Fatalf("Bot deletion retained credentials: %d, %v", len(rows), err)
	}
}
