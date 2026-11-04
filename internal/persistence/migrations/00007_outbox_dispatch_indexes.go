package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

// OutboxDispatchIndexes changes only scheduler access paths. Existing receipts,
// identities and retained message bodies are untouched.
func OutboxDispatchIndexes() *goose.Migration {
	return goose.NewGoMigration(7, &goose.GoFunc{RunTx: func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
CREATE INDEX IF NOT EXISTS outbox_dispatch_status ON documents(json_extract(data,'$.status'),json_extract(data,'$.created_at'),id) WHERE kind='outbox';
CREATE INDEX IF NOT EXISTS outbox_dispatch_group ON documents(json_extract(data,'$.group_id'),json_extract(data,'$.status'),json_extract(data,'$.fragment')) WHERE kind='outbox';
CREATE INDEX IF NOT EXISTS outbox_dispatch_bucket ON documents(json_extract(data,'$.binding_id'),json_extract(data,'$.conversation_id'),json_extract(data,'$.status')) WHERE kind='outbox';`)
		return err
	}}, nil)
}
