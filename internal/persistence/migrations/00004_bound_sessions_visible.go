package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/pressly/goose/v3"
)

// BoundSessionsVisible repairs the format-3 state that allowed an active IM
// Session to be archived. New runtime code prohibits that state. This immutable
// migration owns its historical JSON shape and does not import current domain
// types. Prior revisions and Core history are never rewritten.
func BoundSessionsVisible() *goose.Migration {
	return goose.NewGoMigration(4, &goose.GoFunc{RunTx: unarchiveBoundSessions}, nil)
}

func unarchiveBoundSessions(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "SELECT id,revision,data FROM documents WHERE kind='sessions'")
	if err != nil {
		return err
	}
	type frozenSession struct {
		id       string
		revision int64
		data     []byte
	}
	var records []frozenSession
	for rows.Next() {
		var record frozenSession
		if err := rows.Scan(&record.id, &record.revision, &record.data); err != nil {
			_ = rows.Close()
			return err
		}
		records = append(records, record)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	updatedAt, _ := json.Marshal(time.Now().UTC())
	for _, record := range records {
		var previous struct {
			ID        string `json:"id"`
			Revision  int64  `json:"revision"`
			BindingID string `json:"binding_id"`
			Archived  bool   `json:"archived"`
		}
		if err := json.Unmarshal(record.data, &previous); err != nil {
			return fmt.Errorf("read format-3 Session %q: %w", record.id, err)
		}
		if previous.BindingID == "" || !previous.Archived {
			continue
		}
		if previous.ID != record.id || previous.Revision != record.revision || record.revision < 1 || record.revision == math.MaxInt64 {
			return fmt.Errorf("format-3 Session %q has inconsistent revision metadata", record.id)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(record.data, &fields); err != nil {
			return err
		}
		// Append a normal new business revision. Mutating only documents at the
		// previous revision would make its current and historical values disagree.
		revision := record.revision + 1
		fields["archived"] = json.RawMessage("false")
		fields["revision"], _ = json.Marshal(revision)
		fields["updated_at"] = updatedAt
		data, err := json.Marshal(fields)
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
			return fmt.Errorf("format-3 Session %q changed during migration", record.id)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO versions(kind,id,revision,data) VALUES('sessions',?,?,?)", record.id, revision, data); err != nil {
			return err
		}
	}
	return nil
}
