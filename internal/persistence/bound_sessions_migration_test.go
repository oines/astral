package persistence

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral-core/store/sqlite"
)

type migrationHistoryModel struct{}

func (migrationHistoryModel) Spec() core.ModelSpec {
	return core.ModelSpec{ID: "history-only", ContextWindow: 32000, MaxOutputTokens: 1024}
}
func (migrationHistoryModel) Stream(context.Context, core.Request, func(core.ModelEvent)) (core.Reply, error) {
	return core.Reply{}, errors.New("history fixture must not activate a model")
}

func migrationRecords(t *testing.T, db *sql.DB, table string) map[string][]byte {
	t.Helper()
	rows, err := db.Query("SELECT kind,id,revision,data FROM " + table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	result := map[string][]byte{}
	for rows.Next() {
		var kind, id string
		var revision int64
		var data []byte
		if err := rows.Scan(&kind, &id, &revision, &data); err != nil {
			t.Fatal(err)
		}
		result[fmt.Sprintf("%s:%s:%d", kind, id, revision)] = data
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestMigration4RestoresOnlyCurrentBoundArchivedSessionsAndRetainsHistory(t *testing.T) {
	ctx := context.Background()
	directory, db := migrationFixture(t)
	const repair = `{"id":"bound-archived","revision":3,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z","binding_id":"active-binding","archived":true,"title":"Platform title","counter":9007199254740993123,"extra":{"retain":"original"}}`
	const ordinary = `{"id":"ordinary-archived","revision":7,"binding_id":"","archived":true,"title":"Owner archive"}`
	const unbound = `{"id":"former-im","revision":2,"source":"im","archived":true,"title":"Unbound history"}`
	const active = `{"id":"bound-active","revision":2,"binding_id":"other-binding","archived":false}`
	for _, record := range []struct {
		id       string
		revision int
		data     string
	}{{"bound-archived", 3, repair}, {"ordinary-archived", 7, ordinary}, {"former-im", 2, unbound}, {"bound-active", 2, active}} {
		for _, table := range []string{"documents", "versions"} {
			if _, err := db.Exec("INSERT INTO "+table+"(kind,id,revision,data) VALUES('sessions',?,?,?)", record.id, record.revision, record.data); err != nil {
				t.Fatal(err)
			}
		}
	}
	const historical = `{"id":"bound-archived","revision":1,"binding_id":"","archived":true,"title":"Earlier title"}`
	if _, err := db.Exec("INSERT INTO versions(kind,id,revision,data) VALUES('sessions','bound-archived',1,?)", historical); err != nil {
		t.Fatal(err)
	}
	const observation = `{"id":"event-original","text":"Keep this immutable IM history."}`
	if _, err := db.Exec("INSERT INTO observations(id,connection_id,conversation_id,external_id,payload) VALUES('event-original','account','chat','event-original',?)", observation); err != nil {
		t.Fatal(err)
	}
	// Seed nonempty Core history only through its public API. The Host migration
	// never opens, rewrites, projects or imports this separate database.
	corePath := filepath.Join(directory, "core.db")
	coreStore, err := sqlite.Open(corePath)
	if err != nil {
		t.Fatal(err)
	}
	session, err := core.NewSession(ctx, core.Config{Store: coreStore, Model: migrationHistoryModel{},
		SystemPrompt: "Retained original instructions", EstimateTokens: func(context.Context, core.Model, core.Request) (int, error) { return 1, nil }}, core.SessionOptions{ID: "bound-archived"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.InjectContext(ctx, core.Input{ID: "old-observation", Contexts: []core.Message{{Role: core.RoleUser, ContextKind: "im.observation", Content: core.Text("Original context before migration.")}}}); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := coreStore.Close(); err != nil {
		t.Fatal(err)
	}
	coreBefore, err := os.ReadFile(corePath)
	if err != nil {
		t.Fatal(err)
	}
	documentsBefore, versionsBefore := migrationRecords(t, db, "documents"), migrationRecords(t, db, "versions")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Migrate(ctx, directory); err != nil {
			t.Fatal(err)
		}
	}
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	documentsAfter, versionsAfter := migrationRecords(t, store.db, "documents"), migrationRecords(t, store.db, "versions")
	for key, original := range documentsBefore {
		if key != "sessions:bound-archived:3" && !bytes.Equal(original, documentsAfter[key]) {
			t.Fatalf("migration altered unrelated current document %q", key)
		}
	}
	for key, original := range versionsBefore {
		if !bytes.Equal(original, versionsAfter[key]) {
			t.Fatalf("migration rewrote prior revision %q", key)
		}
	}
	if len(documentsAfter) != len(documentsBefore) || len(versionsAfter) != len(versionsBefore)+1 {
		t.Fatal("migration did not append exactly one corrected business revision")
	}
	updated := documentsAfter["sessions:bound-archived:4"]
	if !bytes.Equal(updated, versionsAfter["sessions:bound-archived:4"]) {
		t.Fatal("current document and newest historical revision disagree")
	}
	var oldFields, newFields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(repair), &oldFields); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(updated, &newFields); err != nil {
		t.Fatal(err)
	}
	if string(newFields["archived"]) != "false" || string(newFields["revision"]) != "4" || bytes.Equal(oldFields["updated_at"], newFields["updated_at"]) {
		t.Fatal("migration did not restore visibility with a new revision and update time")
	}
	for _, fields := range []map[string]json.RawMessage{oldFields, newFields} {
		delete(fields, "archived")
		delete(fields, "revision")
		delete(fields, "updated_at")
	}
	if !reflect.DeepEqual(oldFields, newFields) {
		t.Fatal("migration changed creation metadata, unknown fields or large integer precision")
	}
	var retained string
	if err := store.db.QueryRow("SELECT payload FROM observations WHERE id='event-original'").Scan(&retained); err != nil || retained != observation {
		t.Fatal("migration changed archived IM history")
	}
	coreAfter, err := os.ReadFile(corePath)
	if err != nil || !bytes.Equal(coreBefore, coreAfter) {
		t.Fatal("Host migration rewrote the Core history database")
	}
}

func TestMigration4RollsBackPresentationChangesOnInconsistentMetadata(t *testing.T) {
	directory, db := migrationFixture(t)
	for _, item := range []struct{ id, data string }{
		{"first-valid", `{"id":"first-valid","revision":1,"binding_id":"binding","archived":true}`},
		{"second-invalid", `{"id":"second-invalid","revision":9,"binding_id":"binding","archived":true}`},
	} {
		for _, table := range []string{"documents", "versions"} {
			if _, err := db.Exec("INSERT INTO "+table+"(kind,id,revision,data) VALUES('sessions',?,1,?)", item.id, item.data); err != nil {
				t.Fatal(err)
			}
		}
	}
	documentsBefore, versionsBefore := migrationRecords(t, db, "documents"), migrationRecords(t, db, "versions")
	if err := Migrate(context.Background(), directory); err == nil {
		t.Fatal("migration accepted inconsistent Session revision metadata")
	}
	if !reflect.DeepEqual(documentsBefore, migrationRecords(t, db, "documents")) || !reflect.DeepEqual(versionsBefore, migrationRecords(t, db, "versions")) {
		t.Fatal("failed migration partially changed current or historical records")
	}
	status, err := InspectMigration(context.Background(), directory)
	if err != nil || status.Current != 3 || status.Target != 4 || !status.NeedsMigration {
		t.Fatalf("failed step advanced its format marker or ledger: %+v %v", status, err)
	}
}
