package recall

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
)

const snapshotLifetime = 24 * time.Hour

type cursor struct {
	Version  int    `json:"v"`
	Kind     string `json:"kind"`
	Snapshot string `json:"snapshot"`
	Offset   int    `json:"offset"`
	Time     int64  `json:"time,omitempty"`
	Block    int64  `json:"block,omitempty"`
}

func encode(value any) string {
	raw, _ := json.Marshal(value)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decode(raw string, target any) error {
	if len(raw) > 8192 {
		return domain.Invalid("invalid history cursor or reference")
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || json.Unmarshal(data, target) != nil {
		return domain.Invalid("invalid history cursor or reference")
	}
	return nil
}

func pageLimit(n, fallback int) (int, error) {
	if n == 0 {
		return fallback, nil
	}
	if n < 1 || n > 200 {
		return 0, domain.Invalid("limit must be between 1 and 200")
	}
	return n, nil
}

type scope struct {
	ids            []string
	source         string
	conversationID string
	archived       *bool
	title          string
}

// Capture records and their durable heads once. Future pages use the same
// heads and metadata, regardless of new writes, renaming or archiving.
func (s *Service) snapshot(ctx context.Context, agentID, kind string, request any, filter scope) (*sql.DB, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.database(ctx)
	if err != nil {
		return nil, "", err
	}
	if agentID == "" {
		return nil, "", domain.Forbidden("an Agent view is required")
	}
	records, err := s.catalog(ctx, agentID, filter.ids)
	if err != nil {
		return nil, "", err
	}
	type captured struct {
		view    Session
		rollout string
	}
	var sessions []captured
	for _, record := range records {
		if filter.source != "" && record.Source != filter.source || filter.conversationID != "" && record.ConversationID != filter.conversationID ||
			filter.archived != nil && record.Archived != *filter.archived || filter.title != "" && !strings.Contains(fold(record.Title), fold(filter.title)) {
			continue
		}
		state, readErr := s.core.LoadState(ctx, record.ID)
		// A Session may have been catalogued but never initialized. It has no
		// committed history yet; listing it must not create that history.
		if readErr != nil && !errors.Is(readErr, core.ErrNotFound) {
			return nil, "", readErr
		}
		if kind == "search" {
			if err = s.indexHead(ctx, db, state.RolloutID, state.Branch.HeadID); err != nil {
				return nil, "", err
			}
		}
		activity, err := s.lastActivity(ctx, db, state.RolloutID, state.Branch.HeadID)
		if err != nil {
			return nil, "", err
		}
		sessions = append(sessions, captured{summary(record, state.Branch.HeadID, activity), state.RolloutID})
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "DELETE FROM snapshots WHERE expires_at<=?", time.Now().Unix()); err != nil {
		return nil, "", err
	}
	id := domain.ID("recall")
	raw, err := json.Marshal(request)
	if err != nil {
		return nil, "", err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO snapshots VALUES(?,?,?,?,?)", id, agentID, kind, raw, time.Now().Add(snapshotLifetime).Unix()); err != nil {
		return nil, "", err
	}
	for _, item := range sessions {
		data, marshalErr := json.Marshal(item.view)
		if marshalErr != nil {
			return nil, "", marshalErr
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO snapshot_sessions VALUES(?,?,?,?,?,?)", id, item.view.ID, item.rollout, item.view.HeadID, item.view.LastActivityAt.UnixNano(), data); err != nil {
			return nil, "", err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, "", err
	}
	return db, id, nil
}

func (s *Service) resume(ctx context.Context, agentID, kind, token string, request any) (*sql.DB, cursor, error) {
	var position cursor
	if err := decode(token, &position); err != nil {
		return nil, position, err
	}
	if position.Version != 1 || position.Kind != kind || position.Snapshot == "" || position.Offset < 0 || position.Block < 0 {
		return nil, position, domain.Invalid("cursor does not belong to this query type")
	}
	s.mu.Lock()
	db, err := s.database(ctx)
	s.mu.Unlock()
	if err != nil {
		return nil, position, err
	}
	var raw []byte
	err = db.QueryRowContext(ctx, "SELECT request FROM snapshots WHERE id=? AND agent_id=? AND kind=? AND expires_at>?", position.Snapshot, agentID, kind, time.Now().Unix()).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, position, &domain.Error{Status: 410, Code: "cursor_expired", Message: "The cursor is expired, unavailable or outside this Agent view. Start a new query."}
	}
	if err != nil {
		return nil, position, err
	}
	if err = json.Unmarshal(raw, request); err != nil {
		return nil, position, err
	}
	rows, err := db.QueryContext(ctx, "SELECT session_id FROM snapshot_sessions WHERE snapshot_id=?", position.Snapshot)
	if err != nil {
		return nil, position, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, position, err
		}
		if _, err = s.authorize(ctx, agentID, id); err != nil {
			return nil, position, err
		}
	}
	return db, position, rows.Err()
}

func (s *Service) List(ctx context.Context, agentID string, request ListRequest) (SessionPage, error) {
	page := SessionPage{Items: []Session{}}
	limit, err := pageLimit(request.Limit, 50)
	if err != nil {
		return page, err
	}
	parameters := request
	parameters.Cursor, parameters.Limit = "", 0
	var db *sql.DB
	position := cursor{Version: 1, Kind: "list"}
	if request.Cursor == "" {
		db, position.Snapshot, err = s.snapshot(ctx, agentID, "list", parameters, scope{source: request.Source, conversationID: request.ConversationID, archived: request.Archived, title: request.Title})
	} else {
		var saved ListRequest
		db, position, err = s.resume(ctx, agentID, "list", request.Cursor, &saved)
		if err == nil && !reflect.DeepEqual(parameters, ListRequest{}) && !reflect.DeepEqual(parameters, saved) {
			err = domain.Invalid("cursor filters cannot change; pass cursor and optional limit to continue")
		}
	}
	if err != nil {
		return page, err
	}
	rows, err := db.QueryContext(ctx, "SELECT data FROM snapshot_sessions WHERE snapshot_id=? ORDER BY activity_at DESC,session_id LIMIT ? OFFSET ?", position.Snapshot, limit+1, position.Offset)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		if len(page.Items) == limit {
			position.Offset += limit
			page.NextCursor = encode(position)
			break
		}
		var raw []byte
		var item Session
		if err = rows.Scan(&raw); err != nil {
			return page, err
		}
		if err = json.Unmarshal(raw, &item); err != nil {
			return page, err
		}
		page.Items = append(page.Items, item)
	}
	return page, rows.Err()
}
