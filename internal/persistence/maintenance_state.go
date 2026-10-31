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
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO operations(id,kind,state,data) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET state=excluded.state,data=excluded.data WHERE operations.kind=excluded.kind`, kind+":"+id, kind, state, raw)
	return err
}
func (s *Store) Maintenance(ctx context.Context, kind, id string, target any) error {
	var raw []byte
	err := s.db.QueryRowContext(ctx, "SELECT data FROM operations WHERE kind=? AND id=?", kind, kind+":"+id).Scan(&raw)
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
