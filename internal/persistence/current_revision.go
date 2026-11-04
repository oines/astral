package persistence

import (
	"context"
	"database/sql"
	"errors"

	"github.com/oines/astral/internal/domain"
)

// CurrentRevision reads only the existing document revision, without copying
// or decoding its body. Normal Save transactions own revision changes.
func (s *Store) CurrentRevision(ctx context.Context, kind, id string) (int64, error) {
	var revision int64
	err := s.db.QueryRowContext(ctx, "SELECT revision FROM documents WHERE kind=? AND id=?", kind, id).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, domain.Missing(kind, id)
	}
	return revision, err
}
