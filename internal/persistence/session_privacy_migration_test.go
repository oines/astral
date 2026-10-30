package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestSessionPrivacyMigrationAppendsDefaultWithoutRewritingHistory(t *testing.T) {
	ctx := context.Background()
	_, db := migrationFixture(t)
	const current = `{"id":"session","revision":7,"title":"Original","counter":9007199254740993123,"extra":{"kept":true}}`
	const historical = `{"id":"session","revision":3,"title":"Past"}`
	for _, statement := range []string{
		"INSERT INTO documents(kind,id,revision,data) VALUES('sessions','session',7,?)",
		"INSERT INTO versions(kind,id,revision,data) VALUES('sessions','session',7,?)",
	} {
		if _, err := db.Exec(statement, current); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("INSERT INTO versions(kind,id,revision,data) VALUES('sessions','session',3,?)", historical); err != nil {
		t.Fatal(err)
	}
	before := migrationRecords(t, db, "versions")
	plan := currentMigrationPlan()
	plan.target, plan.goMigrations = 5, plan.goMigrations[:2]
	if err := migrateDatabase(ctx, db, plan); err != nil {
		t.Fatal(err)
	}
	if err := migrateDatabase(ctx, db, plan); err != nil {
		t.Fatal(err)
	}
	versions := migrationRecords(t, db, "versions")
	for key, raw := range before {
		if !bytes.Equal(raw, versions[key]) {
			t.Fatalf("migration changed historical revision %s", key)
		}
	}
	if len(versions) != len(before)+1 {
		t.Fatalf("migration did not append exactly one revision: %d", len(versions))
	}
	var raw []byte
	if err := db.QueryRow("SELECT data FROM documents WHERE kind='sessions' AND id='session'").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, versions["sessions:session:8"]) {
		t.Fatal("current and appended business revision disagree")
	}
	var got, want map[string]json.RawMessage
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(current), &want); err != nil {
		t.Fatal(err)
	}
	if string(got["private"]) != "false" || string(got["revision"]) != "8" {
		t.Fatalf("unexpected initialized privacy: %s", raw)
	}
	delete(got, "private")
	delete(got, "updated_at")
	delete(got, "revision")
	delete(want, "revision")
	if !reflect.DeepEqual(got, want) {
		t.Fatal("privacy migration changed unrelated Session content")
	}
}
