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

// SessionPrivacy explicitly initializes existing Sessions as public. Historical
// revisions and Core entries retain their original bytes.
func SessionPrivacy() *goose.Migration {
	return goose.NewGoMigration(5, &goose.GoFunc{RunTx: initializeSessionPrivacy}, nil)
}

func initializeSessionPrivacy(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "SELECT id,revision,data FROM documents WHERE kind='sessions'")
	if err != nil {
		return err
	}
	type frozen struct {
		id       string
		revision int64
		data     []byte
	}
	var records []frozen
	for rows.Next() {
		var item frozen
		if err := rows.Scan(&item.id, &item.revision, &item.data); err != nil {
			_ = rows.Close()
			return err
		}
		records = append(records, item)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	updatedAt, _ := json.Marshal(time.Now().UTC())
	for _, item := range records {
		var prior struct {
			ID       string `json:"id"`
			Revision int64  `json:"revision"`
		}
		if err := json.Unmarshal(item.data, &prior); err != nil {
			return err
		}
		if prior.ID != item.id || prior.Revision != item.revision || item.revision < 1 || item.revision == math.MaxInt64 {
			return fmt.Errorf("format-4 Session %q has inconsistent revision metadata", item.id)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(item.data, &fields); err != nil {
			return err
		}
		revision := item.revision + 1
		fields["private"] = json.RawMessage("false")
		fields["revision"], _ = json.Marshal(revision)
		fields["updated_at"] = updatedAt
		data, err := json.Marshal(fields)
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, "UPDATE documents SET revision=?,data=? WHERE kind='sessions' AND id=? AND revision=?", revision, data, item.id, item.revision)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("format-4 Session %q changed during migration", item.id)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO versions(kind,id,revision,data) VALUES('sessions',?,?,?)", item.id, revision, data); err != nil {
			return err
		}
	}
	return nil
}
