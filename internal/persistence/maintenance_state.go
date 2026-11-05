package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/oines/astral/internal/domain"
)

// Maintenance stores replaceable operation progress without retaining a new
// configuration version for every page or poll. The existing operations table
// is Host-owned and its identity is namespaced by kind.
func (s *Store) SaveMaintenance(ctx context.Context, kind, id, state string, value any) error {
	return saveMaintenance(ctx, changeWriter{exec: s.execKey, key: ChangeKey{Kind: kind, ID: id}}, kind, id, state, value)
}

func (tx *Tx) SaveMaintenance(ctx context.Context, kind, id, state string, value any) error {
	return saveMaintenance(ctx, changeWriter{exec: tx.execKey, key: ChangeKey{Kind: kind, ID: id}}, kind, id, state, value)
}

func saveMaintenance(ctx context.Context, writer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, kind, id, state string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = writer.ExecContext(ctx, `INSERT INTO operations(id,kind,state,data) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET state=excluded.state,data=excluded.data WHERE operations.kind=excluded.kind AND (operations.state IS NOT excluded.state OR operations.data IS NOT excluded.data)`, kind+":"+id, kind, state, raw)
	return err
}
func (s *Store) Maintenance(ctx context.Context, kind, id string, target any) error {
	return maintenance(ctx, s.db, kind, id, target)
}

func (tx *Tx) Maintenance(ctx context.Context, kind, id string, target any) error {
	return maintenance(ctx, tx.tx, kind, id, target)
}

func maintenance(ctx context.Context, reader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, kind, id string, target any) error {
	var raw []byte
	err := reader.QueryRowContext(ctx, "SELECT data FROM operations WHERE kind=? AND id=?", kind, kind+":"+id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Missing(kind, id)
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
}

func (s *Store) PendingMaintenance(ctx context.Context, kind string) ([]json.RawMessage, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT data FROM operations WHERE kind=? AND state<>'complete'", kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []json.RawMessage{}
	for rows.Next() {
		var raw json.RawMessage
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		result = append(result, raw)
	}
	return result, rows.Err()
}

// DeleteMaintenance participates in the same transaction as resource removal.
func (tx *Tx) DeleteMaintenance(ctx context.Context, kind, id string) error {
	_, err := tx.execKey(ctx, ChangeKey{Kind: kind, ID: id}, "DELETE FROM operations WHERE kind=? AND id=?", kind, kind+":"+id)
	return err
}

func (s *Store) DeleteMaintenance(ctx context.Context, kind, id string) error {
	return s.Update(ctx, func(tx *Tx) error { return tx.DeleteMaintenance(ctx, kind, id) })
}
