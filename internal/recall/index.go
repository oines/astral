package recall

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/messages"
	"github.com/oines/astral/internal/persistence"
	_ "modernc.org/sqlite"
)

type Service struct {
	store  *persistence.Store
	core   core.Store
	mu     sync.Mutex
	db     *sql.DB
	closed bool
}

func New(store *persistence.Store, history core.Store) *Service {
	return &Service{store: store, core: history}
}

func (s *Service) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

const schema = `
CREATE TABLE IF NOT EXISTS entries (
 rollout_id TEXT NOT NULL, id TEXT NOT NULL, parent_id TEXT NOT NULL,
 writer_id TEXT NOT NULL, sequence INTEGER NOT NULL, time INTEGER NOT NULL,
 activity_at INTEGER NOT NULL, PRIMARY KEY(rollout_id,id)
);
CREATE TABLE IF NOT EXISTS blocks (
 id INTEGER PRIMARY KEY, rollout_id TEXT NOT NULL, entry_id TEXT NOT NULL,
 part INTEGER NOT NULL, kind TEXT NOT NULL, role TEXT NOT NULL, tool_name TEXT NOT NULL,
 text TEXT NOT NULL, folded TEXT NOT NULL, UNIQUE(rollout_id,entry_id,part)
);
CREATE VIRTUAL TABLE IF NOT EXISTS blocks_fts USING fts5(folded, content='blocks', content_rowid='id', tokenize='trigram case_sensitive 1');
CREATE TABLE IF NOT EXISTS snapshots (
 id TEXT PRIMARY KEY, agent_id TEXT NOT NULL, kind TEXT NOT NULL,
 request BLOB NOT NULL, expires_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS snapshot_expiry ON snapshots(expires_at);
CREATE TABLE IF NOT EXISTS catalog_heads (
 rollout_id TEXT NOT NULL, head_id TEXT NOT NULL, activity_at INTEGER NOT NULL,
 PRIMARY KEY(rollout_id,head_id)
);
CREATE TABLE IF NOT EXISTS snapshot_sessions (
 snapshot_id TEXT NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
 session_id TEXT NOT NULL, rollout_id TEXT NOT NULL, head_id TEXT NOT NULL,
 activity_at INTEGER NOT NULL, data BLOB NOT NULL,
 PRIMARY KEY(snapshot_id,session_id)
);
PRAGMA user_version=1;
`

// Called under mu. Index schema versions are separate from Core's format.
func (s *Service) database(ctx context.Context) (*sql.DB, error) {
	if s.closed {
		return nil, core.ErrClosed
	}
	if s.db != nil {
		return s.db, nil
	}
	path := filepath.Join(s.store.Directory, "recall.db")
	uriPath := filepath.ToSlash(path)
	if runtime.GOOS == "windows" && len(uriPath) >= 2 && uriPath[1] == ':' {
		uriPath = "/" + uriPath
	}
	uri := url.URL{Scheme: "file", Path: uriPath}
	options := url.Values{}
	options.Set("_txlock", "immediate")
	for _, pragma := range []string{"journal_mode(WAL)", "synchronous(NORMAL)", "busy_timeout(5000)", "foreign_keys(ON)"} {
		options.Add("_pragma", pragma)
	}
	db, err := sql.Open("sqlite", uri.String()+"?"+options.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	fail := func(err error) (*sql.DB, error) { _ = db.Close(); return nil, err }
	var version int
	if err = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fail(err)
	}
	if version != 0 && version != 1 {
		return fail(fmt.Errorf("unsupported recall index %d; remove recall.db while the server is stopped to rebuild it", version))
	}
	if _, err = db.ExecContext(ctx, schema); err != nil {
		return fail(err)
	}
	if err = os.Chmod(path, 0600); err != nil {
		return fail(err)
	}
	s.db = db
	return db, nil
}

type block struct {
	part                   int
	kind, role, tool, text string
}

// extract defines version 1 of the stable text-part projection. It indexes
// committed content once, never frozen requests, reasoning or image payloads.
func extract(e core.Entry) []block {
	var result []block
	add := func(kind, role, tool, text string) {
		if text != "" {
			result = append(result, block{part: len(result), kind: kind, role: role, tool: tool, text: text})
		}
	}
	if e.Message != nil {
		m := e.Message
		if m.ContextKind == "astral.environment" || m.ContextKind == "im.trigger" || m.Role == core.RoleDeveloper || m.Failed() {
			return nil
		}
		kind := "message"
		if e.Kind == core.EntryContext {
			kind = "observation"
		}
		var metadata messages.Metadata
		_ = json.Unmarshal(m.Metadata, &metadata)
		if metadata.Source == "im.trigger" {
			return nil
		}
		display := metadata.Source == "web" || metadata.Source == "im"
		for i, part := range m.Content {
			switch part.Type {
			case "text":
				text := part.Text
				if display && i == 0 {
					text = metadata.DisplayText
				}
				add(kind, string(m.Role), "", text)
			case "tool_call":
				if part.ToolCall != nil {
					add("tool_call", string(m.Role), part.ToolCall.Name, string(part.ToolCall.Arguments))
				}
			}
		}
		if display {
			for _, file := range metadata.Files {
				add("attachment", string(m.Role), "", file.DisplayName+"\n"+file.MediaType+"\n"+file.Path)
			}
			for _, attachment := range metadata.Attachments {
				add("attachment", string(m.Role), "", attachment.Name+"\n"+attachment.MediaType+"\n"+attachment.Ref)
			}
		}
	}
	if e.Tool != nil && e.Tool.Result != nil {
		for _, part := range e.Tool.Result.Content {
			if part.Type == "text" {
				add("tool_result", "tool", e.Tool.Invocation.Call.Name, part.Text)
			}
		}
	}
	if e.Compaction != nil {
		add("summary", "assistant", "", e.Compaction.Summary)
	}
	return result
}

// Unicode simple lowercasing preserves the number of code points, so matching
// on folded text never changes source offsets (unlike byte-based slicing).
func fold(text string) string { return strings.Map(unicode.ToLower, text) }

// indexHead walks only the unindexed prefix of an immutable parent chain.
// One transaction makes existence imply that ALL ancestors are indexed, even
// after a crash or cancellation. Shared Fork ancestors are indexed once.
func (s *Service) indexHead(ctx context.Context, db *sql.DB, rollout, head string) error {
	if head == "" {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var inserted []string
	for id := head; id != ""; {
		var parent string
		err = tx.QueryRowContext(ctx, "SELECT parent_id FROM entries WHERE rollout_id=? AND id=?", rollout, id).Scan(&parent)
		if err == nil {
			break
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		record, readErr := s.core.Get(ctx, rollout, id)
		if readErr != nil {
			return readErr
		}
		e := record.Entry
		if record.Projection == nil || e.ID != id || e.ParentID == id {
			return core.ErrCorrupt
		}
		parts := extract(e)
		activity := activityTime(e)
		if _, err = tx.ExecContext(ctx, "INSERT INTO entries VALUES(?,?,?,?,?,?,?)", rollout, e.ID, e.ParentID, e.SessionID, e.Sequence, e.Time.UnixNano(), activity); err != nil {
			return err
		}
		for _, part := range parts {
			result, insertErr := tx.ExecContext(ctx, "INSERT INTO blocks(rollout_id,entry_id,part,kind,role,tool_name,text,folded) VALUES(?,?,?,?,?,?,?,?)", rollout, e.ID, part.part, part.kind, part.role, part.tool, part.text, fold(part.text))
			if insertErr != nil {
				return insertErr
			}
			row, rowErr := result.LastInsertId()
			if rowErr != nil {
				return rowErr
			}
			if _, err = tx.ExecContext(ctx, "INSERT INTO blocks_fts(rowid,folded) VALUES(?,?)", row, fold(part.text)); err != nil {
				return err
			}
		}
		inserted = append(inserted, id)
		id = e.ParentID
	}
	for i := len(inserted) - 1; i >= 0; i-- {
		_, err = tx.ExecContext(ctx, `UPDATE entries SET activity_at=coalesce((SELECT p.activity_at FROM entries p WHERE p.rollout_id=entries.rollout_id AND p.id=entries.parent_id),0) WHERE rollout_id=? AND id=? AND activity_at=0`, rollout, inserted[i])
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Service) activeAgent(ctx context.Context, id string) error {
	if id == "" {
		return domain.Forbidden("an Agent view is required")
	}
	var agent domain.Agent
	if err := s.store.Get(ctx, "agents", id, &agent); err != nil {
		return err
	}
	if agent.Deletion {
		return domain.Missing("Bot", id)
	}
	return nil
}

func (s *Service) authorize(ctx context.Context, agentID, id string) (domain.Session, error) {
	if err := s.activeAgent(ctx, agentID); err != nil {
		return domain.Session{}, err
	}
	var session domain.Session
	if err := s.store.Get(ctx, "sessions", id, &session); err != nil {
		return session, err
	}
	if session.Deletion != nil {
		return domain.Session{}, domain.Missing("Session", id)
	}
	if agentID == "" || session.AgentID != agentID {
		return domain.Session{}, domain.Forbidden("Session is outside the current Agent view")
	}
	if err := persistence.CheckSessionAccess(ctx, session); err != nil {
		return domain.Session{}, err
	}
	return session, nil
}

func summary(record domain.Session, head string, activity int64) Session {
	last := record.CreatedAt
	if at := time.Unix(0, activity).UTC(); activity != 0 && at.After(last) {
		last = at
	}
	return Session{ID: record.ID, Title: record.Title, Source: record.Source, ConversationID: record.ConversationID,
		ChannelPlatform: record.ChannelPlatform, ParentSessionID: record.ParentSessionID, Archived: record.Archived,
		CreatedAt: record.CreatedAt, LastActivityAt: last, HeadID: head}
}
