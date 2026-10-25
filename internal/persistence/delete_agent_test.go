package persistence

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/oines/astral/internal/domain"
)

func TestPurgeAgentDataErasesHistoryReceiptsAndPrivateCredentials(t *testing.T) {
	ctx := context.Background()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var first, rotated, shared, privateWire string
	err = store.Update(ctx, func(tx *Tx) error {
		var err error
		for _, target := range []*string{&first, &rotated, &shared, &privateWire} {
			*target, err = tx.PutSecret(ctx, "test-private-token")
			if err != nil {
				return err
			}
		}
		account := domain.Connection{Meta: domain.Meta{ID: "deleted-account"}, AgentID: "deleted-bot", CredentialID: first}
		if err := tx.Save(ctx, "connections", &account.Meta, &account); err != nil {
			return err
		}
		account.CredentialID = rotated
		if err := tx.Save(ctx, "connections", &account.Meta, &account); err != nil {
			return err
		}
		// A previous ordinary account deletion retained historical revisions.
		if err := tx.Delete(ctx, "connections", account.ID, account.Revision); err != nil {
			return err
		}
		for _, record := range []struct {
			kind, id, agent, connection, conversation, binding, credential string
		}{
			{"connections", "owned-account", "deleted-bot", "", "", "", shared},
			{"conversations", "owned-chat", "", "deleted-account", "", "", ""},
			{"bindings", "owned-binding", "deleted-bot", "", "owned-chat", "", ""},
			{"outbox", "owned-outbox", "deleted-bot", "deleted-account", "owned-chat", "owned-binding", ""},
			{"channel_commands", "owned-command", "", "deleted-account", "owned-chat", "", ""},
			{"channel_cursors", "deleted-account", "", "", "", "", ""},
			{"inbox_cursors", "owned-binding", "", "", "", "", ""},
			{"probe_cursors", "owned-binding", "", "", "", "", ""},
			{"weixin_wire", "owned-wire", "", "deleted-account", "", "", privateWire},
			{"files", "owned-file", "deleted-bot", "", "", "", ""},
			{"connections", "other-account", "other-bot", "", "", "", shared},
			{"conversations", "other-chat", "", "other-account", "", "", ""},
			{"weixin_wire", "other-wire", "", "other-account", "", "", ""},
			{"files", "other-file", "other-bot", "", "", "", ""},
		} {
			meta := domain.Meta{ID: record.id}
			data := map[string]any{"agent_id": record.agent, "connection_id": record.connection, "conversation_id": record.conversation, "binding_id": record.binding, "credential_id": record.credential}
			if err := tx.Save(ctx, record.kind, &meta, data); err != nil {
				return err
			}
		}
		if _, err := tx.ClaimIntent(ctx, "owned-intent", "owned-outbox"); err != nil {
			return err
		}
		_, err = tx.Archive(ctx, domain.Observation{ID: "owned-event", ConnectionID: "deleted-account", ConversationID: "owned-chat", ExternalEventID: "one"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveDelivery(ctx, Delivery{EventID: "owned-event", SessionID: "already-removed-session", State: "routed"}); err != nil {
		t.Fatal(err)
	}
	if err = store.Update(ctx, func(tx *Tx) error { return tx.PurgeAgentData(ctx, "deleted-bot") }); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"connections", "conversations", "bindings", "outbox", "channel_commands", "channel_cursors", "inbox_cursors", "probe_cursors", "weixin_wire", "files"} {
		records, err := store.Retained(ctx, kind)
		if err != nil {
			t.Fatal(err)
		}
		for _, record := range records {
			if record.ID != "other-account" && record.ID != "other-chat" && record.ID != "other-wire" && record.ID != "other-file" {
				t.Fatalf("owned current/history retained: %s %s", kind, record.ID)
			}
		}
	}
	for _, id := range []string{first, rotated, privateWire} {
		if _, err = store.Secret(ctx, id); err == nil {
			t.Fatal("private credential retained:", id)
		}
	}
	if _, err = store.Secret(ctx, shared); err != nil {
		t.Fatal("shared credential still used by another Bot was erased:", err)
	}
	if _, err = store.Observation(ctx, "owned-event"); !domain.IsMissing(err) {
		t.Fatal("owned observation retained:", err)
	}
	if _, err = store.Delivery(ctx, "owned-event"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("owned receipt retained:", err)
	}
	if _, err = store.Intent(ctx, "owned-intent"); !domain.IsMissing(err) {
		t.Fatal("owned intent retained:", err)
	}
	if err = store.Update(ctx, func(tx *Tx) error { return tx.PurgeAgentData(ctx, "deleted-bot") }); err != nil {
		t.Fatal("cleanup retry:", err)
	}
}
