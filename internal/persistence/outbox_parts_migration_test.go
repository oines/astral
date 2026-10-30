package persistence

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/oines/astral/internal/domain"
)

func migrationFixtureAt(t *testing.T, version int64) (string, *sql.DB) {
	t.Helper()
	dir, db := migrationFixture(t)
	plan := currentMigrationPlan()
	plan.target, plan.goMigrations = version, plan.goMigrations[:version-BaselineVersion]
	if err := migrateDatabase(context.Background(), db, plan); err != nil {
		t.Fatal(err)
	}
	return dir, db
}

func insertMigrationDocument(t *testing.T, db *sql.DB, kind, id string, revision int, raw string) {
	t.Helper()
	for _, table := range []string{"documents", "versions"} {
		if _, err := db.Exec("INSERT INTO "+table+"(kind,id,revision,data) VALUES(?,?,?,?)", kind, id, revision, raw); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOutboxPartsMigrationFrom4And5PreservesReceiptsAndHistorySemantics(t *testing.T) {
	for _, version := range []int64{4, 5} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			dir, db := migrationFixtureAt(t, version)
			for id, adapter := range map[string]string{"tg": "telegram.bot", "feishu": "feishu.bot", "qq": "qq.napcat", "qqbot": "qq.bot", "weixin": "weixin.ilink"} {
				insertMigrationDocument(t, db, "connections", id, 1, fmt.Sprintf(`{"id":%q,"revision":1,"adapter":%q}`, id, adapter))
			}
			for id, mime := range map[string]string{"png": "image/png", "gif": "image/gif", "pdf": "application/pdf", "mp4": "video/mp4", "mp3": "audio/mpeg"} {
				insertMigrationDocument(t, db, "files", id, 1, fmt.Sprintf(`{"id":%q,"revision":1,"media_type":%q}`, id, mime))
			}
			cases := []struct {
				id, content string
				parts       []domain.OutboxPart
			}{
				{"text", `"text":"hello\nworld"`, []domain.OutboxPart{{Type: "text", Text: "hello\nworld"}}},
				{"photo", `"connection_id":"tg","file_ref_id":"png"`, []domain.OutboxPart{{Type: "image", FileRefID: "png"}}},
				{"gif-document", `"connection_id":"tg","file_ref_id":"gif"`, []domain.OutboxPart{{Type: "file", FileRefID: "gif"}}},
				{"feishu-document", `"connection_id":"feishu","file_ref_id":"png"`, []domain.OutboxPart{{Type: "file", FileRefID: "png"}}},
				{"napcat-photo", `"connection_id":"qq","file_ref_id":"gif"`, []domain.OutboxPart{{Type: "image", FileRefID: "gif"}}},
				{"qq-video", `"connection_id":"qqbot","file_ref_id":"mp4"`, []domain.OutboxPart{{Type: "file", FileRefID: "mp4", MediaKind: "video"}}},
				{"qq-audio", `"connection_id":"qqbot","file_ref_id":"mp3"`, []domain.OutboxPart{{Type: "file", FileRefID: "mp3", MediaKind: "audio"}}},
				{"weixin-video", `"connection_id":"weixin","file_ref_id":"mp4"`, []domain.OutboxPart{{Type: "file", FileRefID: "mp4", MediaKind: "video"}}},
				{"weixin-audio-document", `"connection_id":"weixin","file_ref_id":"mp3"`, []domain.OutboxPart{{Type: "file", FileRefID: "mp3"}}},
				{"telegram-video-document", `"connection_id":"tg","file_ref_id":"mp4"`, []domain.OutboxPart{{Type: "file", FileRefID: "mp4"}}},
				{"feishu-video-document", `"connection_id":"feishu","file_ref_id":"mp4"`, []domain.OutboxPart{{Type: "file", FileRefID: "mp4"}}},
				{"napcat-video-document", `"connection_id":"qq","file_ref_id":"mp4"`, []domain.OutboxPart{{Type: "file", FileRefID: "mp4"}}},
				{"explicit-video-document", `"connection_id":"qqbot","file_ref_id":"mp4","parts":[{"type":"file","file_ref_id":"mp4"}]`, []domain.OutboxPart{{Type: "file", FileRefID: "mp4"}}},
				{"frozen-video", `"parts":[{"type":"file","file_ref_id":"mp4","media_kind":"video"}]`, []domain.OutboxPart{{Type: "file", FileRefID: "mp4", MediaKind: "video"}}},
				{"caption-document", `"text":"caption","connection_id":"tg","file_ref_id":"pdf"`, []domain.OutboxPart{{Type: "text", Text: "caption"}, {Type: "file", FileRefID: "pdf"}}},
				{"as-document", `"as_document":true,"connection_id":"tg","file_ref_id":"png"`, []domain.OutboxPart{{Type: "image", FileRefID: "png"}}},
				{"duplicate", `"file_ref_id":"png","parts":[{"type":"file","file_ref_id":"png","path":"/old/path","file_uri":"astral-file://old/reference"}]`, []domain.OutboxPart{{Type: "file", FileRefID: "png"}}},
				{"mixed", `"parts":[{"type":"mention","user_id":"123","name":"Alice"},{"type":"text","text":"\nLook:"},{"type":"image","file_ref_id":"png"}]`, []domain.OutboxPart{{Type: "mention", UserID: "123", Name: "Alice"}, {Type: "text", Text: "\nLook:"}, {Type: "image", FileRefID: "png"}}},
				{"template", `"format":"markdown_template","markdown_template":{"custom_template_id":"t","params":[]}`, nil},
			}
			for _, item := range cases {
				current := fmt.Sprintf(`{"id":%q,"revision":2,"status":"accepted","platform_message_ids":["platform-1"],"group_id":"group","intent_key":"intent:0","request_digest":"original-digest","updated_at":"2026-01-02T00:00:00Z","counter":9007199254740993123,%s}`, item.id, item.content)
				insertMigrationDocument(t, db, "outbox", item.id, 2, current)
				historical := strings.Replace(strings.Replace(current, `"revision":2`, `"revision":1`, 1), `"status":"accepted"`, `"status":"pending"`, 1)
				if _, err := db.Exec("INSERT INTO versions(kind,id,revision,data) VALUES('outbox',?,1,?)", item.id, historical); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.Exec("INSERT INTO outbox_keys(intent_key,outbox_id) VALUES('intent','text')"); err != nil {
				t.Fatal(err)
			}
			const observation = `{"id":"unchanged","text":"Original own message"}`
			if _, err := db.Exec("INSERT INTO observations(id,connection_id,conversation_id,external_id,payload) VALUES('event','tg','chat','event',?)", observation); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := Migrate(context.Background(), dir); err != nil {
					t.Fatal(err)
				}
			}
			store, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			for _, item := range cases {
				var current, historical domain.Outbox
				if err := store.Get(context.Background(), "outbox", item.id, &current); err != nil {
					t.Fatal(err)
				}
				if err := store.Version(context.Background(), "outbox", item.id, 1, &historical); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(current.Parts, item.parts) && !(len(current.Parts) == 0 && len(item.parts) == 0) {
					t.Fatalf("%s current parts: %#v", item.id, current.Parts)
				}
				if !reflect.DeepEqual(historical.Parts, current.Parts) {
					t.Fatalf("%s history lost content: %#v", item.id, historical.Parts)
				}
				if current.Revision != 2 || historical.Revision != 1 || current.Status != "accepted" || historical.Status != "pending" || current.RequestDigest != "original-digest" || current.GroupID != "group" || current.IntentKey != "intent:0" || !reflect.DeepEqual(current.PlatformMessageIDs, []string{"platform-1"}) {
					t.Fatalf("%s changed delivery metadata: %+v / %+v", item.id, current, historical)
				}
				var raw []byte
				if err := db.QueryRow("SELECT data FROM documents WHERE kind='outbox' AND id=?", item.id).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(raw, &fields); err != nil {
					t.Fatal(err)
				}
				if fields["text"] != nil || fields["file_ref_id"] != nil || bytes.Contains(raw, []byte(`"path"`)) || bytes.Contains(raw, []byte(`"file_uri"`)) || string(fields["counter"]) != "9007199254740993123" || string(fields["updated_at"]) != `"2026-01-02T00:00:00Z"` {
					t.Fatalf("%s changed unrelated values or retained obsolete fields: %s", item.id, raw)
				}
				if item.id == "as-document" && !current.AsDocument {
					t.Fatal("lost as_document")
				}
				if item.id == "template" && (current.MarkdownTemplate == nil || current.MarkdownTemplate.ID != "t") {
					t.Fatal("lost Markdown template")
				}
			}
			if id, err := store.Intent(context.Background(), "intent"); err != nil || id != "text" {
				t.Fatalf("changed idempotency key: %q %v", id, err)
			}
			var retained string
			if err := db.QueryRow("SELECT payload FROM observations WHERE id='event'").Scan(&retained); err != nil || retained != observation {
				t.Fatalf("rewrote IM observation: %s %v", retained, err)
			}
		})
	}
}

func TestOutboxPartsMigrationRejectsConflictsAtomically(t *testing.T) {
	for _, bad := range []string{
		`"text":"old","parts":[{"type":"text","text":"different"}]`,
		`"file_ref_id":"old","parts":[{"type":"file","file_ref_id":"different"}]`,
		`"parts":[{"type":"image","path":"/not/frozen"}]`,
		`"parts":[{"type":"image","file_ref_id":"image","media_kind":"video"}]`,
		`"parts":[{"type":"file","file_ref_id":"file","media_kind":"unsupported"}]`,
	} {
		t.Run(bad, func(t *testing.T) {
			dir, db := migrationFixtureAt(t, 5)
			insertMigrationDocument(t, db, "outbox", "a-valid", 1, `{"id":"a-valid","revision":1,"text":"retain"}`)
			insertMigrationDocument(t, db, "outbox", "z-invalid", 1, `{"id":"z-invalid","revision":1,`+bad+`}`)
			beforeDocs, beforeVersions := migrationRecords(t, db, "documents"), migrationRecords(t, db, "versions")
			if err := Migrate(context.Background(), dir); err == nil {
				t.Fatal("migration accepted contradictory or unfrozen content")
			}
			if !reflect.DeepEqual(beforeDocs, migrationRecords(t, db, "documents")) || !reflect.DeepEqual(beforeVersions, migrationRecords(t, db, "versions")) {
				t.Fatal("failed migration partially rewrote Outbox")
			}
			status, err := InspectMigration(context.Background(), dir)
			if err != nil || status.Current != 5 || status.Target != FormatVersion || !status.NeedsMigration {
				t.Fatalf("failed migration advanced version: %+v %v", status, err)
			}
		})
	}
}

func TestOutboxPartsMigrationUsesRetainedFileAndAccountVersions(t *testing.T) {
	dir, db := migrationFixtureAt(t, 5)
	if _, err := db.Exec(`INSERT INTO versions(kind,id,revision,data) VALUES
		('files','old-image',1,'{"id":"old-image","revision":1,"media_type":"image/png"}'),
		('connections','old-account',1,'{"id":"old-account","revision":1,"adapter":"feishu.bot"}')`); err != nil {
		t.Fatal(err)
	}
	insertMigrationDocument(t, db, "outbox", "receipt", 1, `{"id":"receipt","revision":1,"status":"accepted","file_ref_id":"old-image","connection_id":"old-account"}`)
	if err := Migrate(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := db.QueryRow("SELECT data FROM documents WHERE kind='outbox' AND id='receipt'").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var receipt domain.Outbox
	if err := json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(receipt.Parts, []domain.OutboxPart{{Type: "file", FileRefID: "old-image"}}) {
		t.Fatalf("lost old account's file semantics: %s", raw)
	}
}

func TestMigration6RestoresOnlyUntouchedPrivacyInitializationTimestamp(t *testing.T) {
	for _, source := range []int64{4, 5} {
		t.Run(fmt.Sprint(source), func(t *testing.T) {
			dir, db := migrationFixtureAt(t, 4)
			for _, item := range []struct{ id, extra string }{{"dated", `,"updated_at":"2026-01-02T00:00:00Z"`}, {"undated", ""}, {"edited", `,"updated_at":"2026-01-03T00:00:00Z"`}} {
				insertMigrationDocument(t, db, "sessions", item.id, 7, fmt.Sprintf(`{"id":%q,"revision":7,"title":"Original","counter":9007199254740993123%s}`, item.id, item.extra))
			}
			if source == 5 {
				plan := currentMigrationPlan()
				plan.target, plan.goMigrations = 5, plan.goMigrations[:2]
				if err := migrateDatabase(context.Background(), db, plan); err != nil {
					t.Fatal(err)
				}
				const edited = `{"id":"edited","revision":9,"title":"Edited later","private":false,"updated_at":"2026-02-01T00:00:00Z"}`
				if _, err := db.Exec("UPDATE documents SET revision=9,data=? WHERE kind='sessions' AND id='edited'", edited); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec("INSERT INTO versions(kind,id,revision,data) VALUES('sessions','edited',9,?)", edited); err != nil {
					t.Fatal(err)
				}
			}
			before := migrationRecords(t, db, "versions")
			if err := Migrate(context.Background(), dir); err != nil {
				t.Fatal(err)
			}
			for key, raw := range before {
				if !bytes.Equal(raw, migrationRecords(t, db, "versions")[key]) {
					t.Fatalf("rewrote existing Session history %s", key)
				}
			}
			for id, want := range map[string]string{"dated": `"2026-01-02T00:00:00Z"`, "undated": "", "edited": `"2026-01-03T00:00:00Z"`} {
				if source == 5 && id == "edited" {
					want = `"2026-02-01T00:00:00Z"`
				}
				var raw []byte
				if err := db.QueryRow("SELECT data FROM documents WHERE kind='sessions' AND id=?", id).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(raw, &fields); err != nil {
					t.Fatal(err)
				}
				if string(fields["updated_at"]) != want || string(fields["private"]) != "false" || string(fields["revision"]) != "9" {
					t.Fatalf("incorrect restored metadata: %s", raw)
				}
				var historical []byte
				if err := db.QueryRow("SELECT data FROM versions WHERE kind='sessions' AND id=? AND revision=9", id).Scan(&historical); err != nil || !bytes.Equal(raw, historical) {
					t.Fatalf("current/history diverged: %s / %s %v", raw, historical, err)
				}
			}
		})
	}
}
