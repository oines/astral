package persistence

import "context"

// IsDirectObservation verifies provenance without loading archived text or media.
func (s *Store) IsDirectObservation(ctx context.Context, id, conversation string) (bool, error) {
	var found bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM observations WHERE id=? AND conversation_id=? AND COALESCE(json_extract(payload,'$.self'),0)=0 AND json_extract(payload,'$.trigger') IN ('private','mention','reply','poke'))`, id, conversation).Scan(&found)
	return found, err
}
