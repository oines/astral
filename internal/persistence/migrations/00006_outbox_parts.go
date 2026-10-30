package migrations

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"

	"github.com/pressly/goose/v3"
)

// OutboxParts freezes the format-5 sending shape into one ordered content
// representation. It does not import current domain or adapter implementations.
func OutboxParts() *goose.Migration {
	return goose.NewGoMigration(6, &goose.GoFunc{RunTx: migrateOutboxParts}, nil)
}

type outbox6Record struct {
	id       string
	revision int64
	data     []byte
}

func outbox6Records(ctx context.Context, tx *sql.Tx, table, kind string) ([]outbox6Record, error) {
	rows, err := tx.QueryContext(ctx, "SELECT id,revision,data FROM "+table+" WHERE kind=?", kind)
	if err != nil {
		return nil, err
	}
	var records []outbox6Record
	for rows.Next() {
		var record outbox6Record
		if err := rows.Scan(&record.id, &record.revision, &record.data); err != nil {
			_ = rows.Close()
			return nil, err
		}
		records = append(records, record)
	}
	return records, errors.Join(rows.Err(), rows.Close())
}

func migrateOutboxParts(ctx context.Context, tx *sql.Tx) error {
	for _, table := range []string{"documents", "versions"} {
		records, err := outbox6Records(ctx, tx, table, "outbox")
		if err != nil {
			return err
		}
		for _, record := range records {
			data, err := convertOutbox6(ctx, tx, record.data)
			if err != nil {
				return fmt.Errorf("format-5 Outbox %q revision %d: %w", record.id, record.revision, err)
			}
			if _, err := tx.ExecContext(ctx, "UPDATE "+table+" SET data=? WHERE kind='outbox' AND id=? AND revision=?", data, record.id, record.revision); err != nil {
				return err
			}
		}
	}
	return restorePrivacy6Timestamps(ctx, tx)
}

func convertOutbox6(ctx context.Context, tx *sql.Tx, raw []byte) ([]byte, error) {
	var fields map[string]json.RawMessage
	var prior struct {
		Text         string                       `json:"text"`
		FileRefID    string                       `json:"file_ref_id"`
		Parts        []map[string]json.RawMessage `json:"parts"`
		ConnectionID string                       `json:"connection_id"`
		Format       string                       `json:"format"`
		Template     json.RawMessage              `json:"markdown_template"`
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		return nil, errors.New("expected an Outbox object")
	}
	if err := json.Unmarshal(raw, &prior); err != nil {
		return nil, err
	}
	parts := prior.Parts
	if len(parts) > 0 {
		var text strings.Builder
		var files []string
		for i, part := range parts {
			var value struct {
				Type      string `json:"type"`
				Text      string `json:"text"`
				UserID    string `json:"user_id"`
				Name      string `json:"name"`
				FileRefID string `json:"file_ref_id"`
				MediaKind string `json:"media_kind"`
			}
			encoded, err := json.Marshal(part)
			if err != nil {
				return nil, err
			}
			if err := json.Unmarshal(encoded, &value); err != nil {
				return nil, err
			}
			if value.MediaKind != "" && (value.Type != "file" || value.MediaKind != "video" && value.MediaKind != "audio") {
				return nil, fmt.Errorf("part %d has invalid frozen media kind", i)
			}
			switch value.Type {
			case "text":
				if value.UserID != "" || value.Name != "" || value.FileRefID != "" {
					return nil, fmt.Errorf("part %d has conflicting text fields", i)
				}
				text.WriteString(value.Text)
			case "mention":
				if value.UserID == "" || value.Text != "" || value.FileRefID != "" {
					return nil, fmt.Errorf("part %d has invalid mention fields", i)
				}
			case "image", "file":
				if value.FileRefID == "" || value.Text != "" || value.UserID != "" || value.Name != "" {
					return nil, fmt.Errorf("part %d has no frozen media reference or conflicting fields", i)
				}
				files = append(files, value.FileRefID)
			default:
				return nil, fmt.Errorf("part %d has unsupported type %q", i, value.Type)
			}
			// Preparation already fixed this media to FileRefID. Public source
			// arguments are not durable delivery data and must not survive.
			delete(part, "path")
			delete(part, "file_uri")
		}
		if prior.Text != "" && prior.Text != text.String() {
			return nil, errors.New("top-level text conflicts with ordered parts")
		}
		if prior.FileRefID != "" && (len(files) != 1 || files[0] != prior.FileRefID) {
			return nil, errors.New("top-level file reference conflicts with ordered parts")
		}
	} else {
		parts = []map[string]json.RawMessage{}
		if prior.Text != "" {
			text, _ := json.Marshal(prior.Text)
			parts = append(parts, map[string]json.RawMessage{"type": json.RawMessage(`"text"`), "text": text})
		}
		if prior.FileRefID != "" {
			kind, mediaKind, err := legacyOutbox6MediaKind(ctx, tx, prior.FileRefID, prior.ConnectionID)
			if err != nil {
				return nil, err
			}
			kindJSON, _ := json.Marshal(kind)
			file, _ := json.Marshal(prior.FileRefID)
			part := map[string]json.RawMessage{"type": kindJSON, "file_ref_id": file}
			if mediaKind != "" {
				part["media_kind"], _ = json.Marshal(mediaKind)
			}
			parts = append(parts, part)
		}
		if len(parts) == 0 && (prior.Format != "markdown_template" || len(prior.Template) == 0 || bytes.Equal(prior.Template, []byte("null"))) {
			return nil, errors.New("message has neither content nor a Markdown template")
		}
	}
	fields["parts"], _ = json.Marshal(parts)
	delete(fields, "text")
	delete(fields, "file_ref_id")
	return json.Marshal(fields)
}

func outbox6Metadata(ctx context.Context, tx *sql.Tx, kind, id string, into any) error {
	var raw []byte
	err := tx.QueryRowContext(ctx, "SELECT data FROM documents WHERE kind=? AND id=?", kind, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, "SELECT data FROM versions WHERE kind=? AND id=? ORDER BY revision DESC LIMIT 1", kind, id).Scan(&raw)
	}
	if err != nil {
		return fmt.Errorf("read %s %q for frozen media: %w", kind, id, err)
	}
	return json.Unmarshal(raw, into)
}

func legacyOutbox6MediaKind(ctx context.Context, tx *sql.Tx, fileID, connectionID string) (string, string, error) {
	var file struct {
		MediaType string `json:"media_type"`
	}
	if err := outbox6Metadata(ctx, tx, "files", fileID, &file); err != nil {
		return "", "", err
	}
	image, video, audio := strings.HasPrefix(file.MediaType, "image/"), strings.HasPrefix(file.MediaType, "video/"), strings.HasPrefix(file.MediaType, "audio/")
	if !image && !video && !audio {
		return "file", "", nil
	}
	var connection struct {
		Platform string `json:"platform"`
		Adapter  string `json:"adapter"`
	}
	if err := outbox6Metadata(ctx, tx, "connections", connectionID, &connection); err != nil {
		return "", "", err
	}
	// QQ's old convenience file path selected video/audio natively. Weixin
	// selected video, but audio uploads remained generic files. Keep these
	// choices frozen instead of guessing from MIME at every future dispatch.
	if connection.Adapter == "qq.bot" {
		if video {
			return "file", "video", nil
		}
		if audio {
			return "file", "audio", nil
		}
	}
	if connection.Adapter == "weixin.ilink" && video {
		return "file", "video", nil
	}
	if !image {
		return "file", "", nil
	}
	// The old Feishu single-file path always uploaded a document. Telegram
	// only treated these three MIME types as photos; other images were files.
	if connection.Platform == "feishu" || connection.Adapter == "feishu.bot" {
		return "file", "", nil
	}
	if (connection.Platform == "telegram" || connection.Adapter == "telegram.bot") && file.MediaType != "image/png" && file.MediaType != "image/jpeg" && file.MediaType != "image/webp" {
		return "file", "", nil
	}
	return "image", "", nil
}

// Step 5 is left frozen. Only its untouched synthetic revision is corrected:
// any actual edit after initialization keeps its own current timestamp.
func restorePrivacy6Timestamps(ctx context.Context, tx *sql.Tx) error {
	records, err := outbox6Records(ctx, tx, "documents", "sessions")
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.revision < 2 {
			continue
		}
		var current map[string]json.RawMessage
		if err := json.Unmarshal(record.data, &current); err != nil {
			return err
		}
		if !bytes.Equal(current["private"], []byte("false")) {
			continue
		}
		var previousRaw []byte
		err := tx.QueryRowContext(ctx, "SELECT data FROM versions WHERE kind='sessions' AND id=? AND revision=?", record.id, record.revision-1).Scan(&previousRaw)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		var previous map[string]json.RawMessage
		if err := json.Unmarshal(previousRaw, &previous); err != nil {
			return err
		}
		if _, exists := previous["private"]; exists || bytes.Equal(current["updated_at"], previous["updated_at"]) {
			continue
		}
		// Keep the complete maps for writing, comparing only fields step 5 did
		// not touch. RawMessage retains unknown fields and large integers.
		comparison := make(map[string]json.RawMessage, len(current))
		priorComparison := make(map[string]json.RawMessage, len(previous))
		for key, value := range current {
			if key != "private" && key != "revision" && key != "updated_at" {
				comparison[key] = value
			}
		}
		for key, value := range previous {
			if key != "revision" && key != "updated_at" {
				priorComparison[key] = value
			}
		}
		if !reflect.DeepEqual(comparison, priorComparison) {
			continue
		}
		var currentMeta, previousMeta struct {
			ID       string `json:"id"`
			Revision int64  `json:"revision"`
		}
		if err := json.Unmarshal(record.data, &currentMeta); err != nil {
			return err
		}
		if err := json.Unmarshal(previousRaw, &previousMeta); err != nil {
			return err
		}
		if currentMeta.ID != record.id || previousMeta.ID != record.id || currentMeta.Revision != record.revision || previousMeta.Revision != record.revision-1 || record.revision == math.MaxInt64 {
			return fmt.Errorf("format-5 Session %q has inconsistent revision metadata", record.id)
		}
		if timestamp, exists := previous["updated_at"]; exists {
			current["updated_at"] = timestamp
		} else {
			delete(current, "updated_at")
		}
		revision := record.revision + 1
		current["revision"], _ = json.Marshal(revision)
		data, err := json.Marshal(current)
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, "UPDATE documents SET revision=?,data=? WHERE kind='sessions' AND id=? AND revision=?", revision, data, record.id, record.revision)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return fmt.Errorf("format-5 Session %q changed during migration", record.id)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO versions(kind,id,revision,data) VALUES('sessions',?,?,?)", record.id, revision, data); err != nil {
			return err
		}
	}
	return nil
}
