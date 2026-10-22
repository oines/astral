package persistence

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/oines/astral/internal/domain"
	_ "modernc.org/sqlite"
)

type Store struct {
	db        *sql.DB
	cipher    cipher.AEAD
	Directory string
}

const schema = `
CREATE TABLE IF NOT EXISTS documents(kind TEXT NOT NULL,id TEXT NOT NULL,revision INTEGER NOT NULL,data BLOB NOT NULL,PRIMARY KEY(kind,id));
CREATE TABLE IF NOT EXISTS versions(kind TEXT NOT NULL,id TEXT NOT NULL,revision INTEGER NOT NULL,data BLOB NOT NULL,PRIMARY KEY(kind,id,revision));
CREATE TABLE IF NOT EXISTS secrets(id TEXT PRIMARY KEY,value BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS observations(sequence INTEGER PRIMARY KEY AUTOINCREMENT,id TEXT NOT NULL UNIQUE,connection_id TEXT NOT NULL,conversation_id TEXT NOT NULL,external_id TEXT NOT NULL,payload BLOB NOT NULL,UNIQUE(connection_id,external_id));
CREATE INDEX IF NOT EXISTS observation_conversation ON observations(conversation_id,sequence);
CREATE TABLE IF NOT EXISTS deliveries(event_id TEXT PRIMARY KEY,session_id TEXT NOT NULL,state TEXT NOT NULL,error TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS operations(id TEXT PRIMARY KEY,kind TEXT NOT NULL,state TEXT NOT NULL,data BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS outbox_keys(intent_key TEXT PRIMARY KEY,outbox_id TEXT NOT NULL);
PRAGMA user_version=3;
`

func Open(directory string) (*Store, error) {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	keyPath := filepath.Join(directory, "master.key")
	key, err := os.ReadFile(keyPath)
	if errors.Is(err, os.ErrNotExist) {
		key = make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return nil, err
		}
		file, createErr := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if createErr != nil {
			return nil, createErr
		}
		_, err = file.Write(key)
		closeErr := file.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
	} else if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("invalid master key: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(directory, "astral.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	st := &Store{db: db, cipher: aead, Directory: directory}
	fail := func(err error) (*Store, error) { _ = db.Close(); return nil, err }
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fail(err)
	}
	if version != 0 && version != 3 {
		return fail(fmt.Errorf("unsupported Astral database schema %d; initialize a new data directory", version))
	}
	if version == 0 {
		var tables int
		if err = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&tables); err != nil {
			return fail(err)
		}
		if tables != 0 {
			return fail(fmt.Errorf("unsupported unversioned Astral database; initialize a new data directory"))
		}
	}
	if _, err = db.Exec("PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON;"); err != nil {
		return fail(err)
	}
	if _, err = db.Exec(schema); err != nil {
		return fail(err)
	}
	if err = os.Chmod(path, 0600); err != nil {
		return fail(err)
	}
	return st, nil
}
func (s *Store) Close() error { return s.db.Close() }

type Record struct {
	ID       string          `json:"id"`
	Revision int64           `json:"revision"`
	Data     json.RawMessage `json:"data"`
}

type reader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func get(ctx context.Context, r reader, kind, id string, revision int64, target any) error {
	var data []byte
	query, args := "SELECT data FROM documents WHERE kind=? AND id=?", []any{kind, id}
	if revision > 0 {
		query = "SELECT data FROM versions WHERE kind=? AND id=? AND revision=?"
		args = append(args, revision)
	}
	err := r.QueryRowContext(ctx, query, args...).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Missing(kind, id)
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}
func (s *Store) Get(ctx context.Context, kind, id string, target any) error {
	return get(ctx, s.db, kind, id, 0, target)
}
func (s *Store) Version(ctx context.Context, kind, id string, revision int64, target any) error {
	return get(ctx, s.db, kind, id, revision, target)
}
func list(ctx context.Context, r reader, kind string) ([]Record, error) {
	rows, err := r.QueryContext(ctx, "SELECT id,revision,data FROM documents WHERE kind=? ORDER BY id", kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Record{}
	for rows.Next() {
		var value Record
		if err = rows.Scan(&value.ID, &value.Revision, &value.Data); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}
func (s *Store) List(ctx context.Context, kind string) ([]Record, error) {
	return list(ctx, s.db, kind)
}

type Tx struct {
	tx    *sql.Tx
	store *Store
	undo  []func()
}

func (s *Store) Update(ctx context.Context, fn func(*Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	unit := &Tx{tx: tx, store: s}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
			for i := len(unit.undo) - 1; i >= 0; i-- {
				unit.undo[i]()
			}
		}
	}()
	if err = fn(unit); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}
func (tx *Tx) Get(ctx context.Context, kind, id string, target any) error {
	return get(ctx, tx.tx, kind, id, 0, target)
}
func (tx *Tx) List(ctx context.Context, kind string) ([]Record, error) { return list(ctx, tx.tx, kind) }
func (tx *Tx) Save(ctx context.Context, kind string, meta *domain.Meta, value any) error {
	if meta.ID == "" {
		return domain.Invalid("record ID is required")
	}
	previous := *meta
	now := time.Now().UTC()
	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = now
	}
	meta.UpdatedAt = now
	meta.Revision++
	data, err := json.Marshal(value)
	if err != nil {
		*meta = previous
		return err
	}
	// The metadata argument is authoritative even when value was passed by
	// value and therefore still contains its previous revision.
	var object map[string]json.RawMessage
	if err = json.Unmarshal(data, &object); err != nil || object == nil {
		*meta = previous
		return domain.Invalid("record must be a JSON object")
	}
	metaJSON, _ := json.Marshal(meta)
	var metaFields map[string]json.RawMessage
	_ = json.Unmarshal(metaJSON, &metaFields)
	for key, value := range metaFields {
		object[key] = value
	}
	data, err = json.Marshal(object)
	if err != nil {
		*meta = previous
		return err
	}
	var result sql.Result
	if previous.Revision == 0 {
		result, err = tx.tx.ExecContext(ctx, `INSERT OR IGNORE INTO documents(kind,id,revision,data) VALUES(?,?,?,?)`, kind, meta.ID, meta.Revision, data)
	} else {
		result, err = tx.tx.ExecContext(ctx, `UPDATE documents SET revision=?,data=? WHERE kind=? AND id=? AND revision=?`, meta.Revision, data, kind, meta.ID, previous.Revision)
	}
	if err != nil {
		*meta = previous
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		*meta = previous
		return err
	}
	if count != 1 {
		*meta = previous
		return domain.Conflict("this record changed; reload it before saving")
	}
	if _, err = tx.tx.ExecContext(ctx, "INSERT INTO versions(kind,id,revision,data) VALUES(?,?,?,?)", kind, meta.ID, meta.Revision, data); err != nil {
		*meta = previous
		return err
	}
	tx.undo = append(tx.undo, func() { *meta = previous })
	return nil
}
func (s *Store) Save(ctx context.Context, kind string, meta *domain.Meta, value any) error {
	previous := *meta
	err := s.Update(ctx, func(tx *Tx) error { return tx.Save(ctx, kind, meta, value) })
	if err != nil {
		*meta = previous
	}
	return err
}
func (tx *Tx) Delete(ctx context.Context, kind, id string, revision int64) error {
	result, err := tx.tx.ExecContext(ctx, "DELETE FROM documents WHERE kind=? AND id=? AND revision=?", kind, id, revision)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return domain.Conflict("record is missing or changed")
	}
	return nil
}
func (s *Store) Secret(ctx context.Context, id string) (string, error) {
	if id == "" {
		return "", nil
	}
	var sealed []byte
	if err := s.db.QueryRowContext(ctx, "SELECT value FROM secrets WHERE id=?", id).Scan(&sealed); err != nil {
		return "", err
	}
	if len(sealed) < s.cipher.NonceSize() {
		return "", errors.New("corrupt credential")
	}
	plain, err := s.cipher.Open(nil, sealed[:s.cipher.NonceSize()], sealed[s.cipher.NonceSize():], []byte(id))
	return string(plain), err
}
func (tx *Tx) PutSecret(ctx context.Context, value string) (string, error) {
	id := domain.ID("credential")
	nonce := make([]byte, tx.store.cipher.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := tx.store.cipher.Seal(nonce, nonce, []byte(value), []byte(id))
	_, err := tx.tx.ExecContext(ctx, "INSERT INTO secrets(id,value) VALUES(?,?)", id, sealed)
	return id, err
}

// RotateSecret refreshes an OAuth token without changing the identity of its
// authorization grant. Normal API-key edits always use PutSecret and a new
// provider revision instead.
func (s *Store) RotateSecret(ctx context.Context, id, value string) error {
	nonce := make([]byte, s.cipher.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	sealed := s.cipher.Seal(nonce, nonce, []byte(value), []byte(id))
	result, err := s.db.ExecContext(ctx, "UPDATE secrets SET value=? WHERE id=?", sealed, id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return domain.Missing("credential", id)
	}
	return nil
}

type Delivery struct {
	EventID   string `json:"event_id"`
	SessionID string `json:"session_id"`
	State     string `json:"state"`
	Error     string `json:"error,omitempty"`
}
type StoredObservation struct {
	Sequence    int64              `json:"sequence"`
	Observation domain.Observation `json:"observation"`
}

func (s *Store) Archive(ctx context.Context, event domain.Observation) (bool, error) {
	return archive(ctx, s.db, event)
}
func (tx *Tx) Archive(ctx context.Context, event domain.Observation) (bool, error) {
	return archive(ctx, tx.tx, event)
}
func archive(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, event domain.Observation) (bool, error) {
	raw, err := json.Marshal(event)
	if err != nil {
		return false, err
	}
	result, err := db.ExecContext(ctx, "INSERT OR IGNORE INTO observations(id,connection_id,conversation_id,external_id,payload) VALUES(?,?,?,?,?)", event.ID, event.ConnectionID, event.ConversationID, event.ExternalEventID, raw)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}
func (s *Store) Observations(ctx context.Context, conversation string, after int64, limit int) ([]StoredObservation, error) {
	return s.SearchObservations(ctx, conversation, after, limit, "")
}

func (s *Store) SearchObservations(ctx context.Context, conversation string, after int64, limit int, query string) ([]StoredObservation, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, "SELECT sequence,payload FROM observations WHERE conversation_id=? AND sequence>? AND (?='' OR instr(lower(json_extract(payload,'$.text')),lower(?))>0) ORDER BY sequence LIMIT ?", conversation, after, query, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []StoredObservation{}
	for rows.Next() {
		var item StoredObservation
		var raw []byte
		if err = rows.Scan(&item.Sequence, &raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &item.Observation); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
func (s *Store) SaveDelivery(ctx context.Context, d Delivery) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO deliveries(event_id,session_id,state,error) VALUES(?,?,?,?)
ON CONFLICT(event_id) DO UPDATE SET state=excluded.state,error=excluded.error WHERE deliveries.session_id=excluded.session_id`,
		d.EventID, d.SessionID, d.State, d.Error)
	return err
}
func (s *Store) Delivery(ctx context.Context, eventID string) (Delivery, error) {
	var d Delivery
	err := s.db.QueryRowContext(ctx, "SELECT event_id,session_id,state,error FROM deliveries WHERE event_id=?", eventID).Scan(&d.EventID, &d.SessionID, &d.State, &d.Error)
	return d, err
}
func (tx *Tx) ClaimIntent(ctx context.Context, key, id string) (string, error) {
	_, err := tx.tx.ExecContext(ctx, "INSERT OR IGNORE INTO outbox_keys(intent_key,outbox_id) VALUES(?,?)", key, id)
	if err != nil {
		return "", err
	}
	var existing string
	err = tx.tx.QueryRowContext(ctx, "SELECT outbox_id FROM outbox_keys WHERE intent_key=?", key).Scan(&existing)
	return existing, err
}

func (s *Store) Intent(ctx context.Context, key string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, "SELECT outbox_id FROM outbox_keys WHERE intent_key=?", key).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", domain.Missing("send intent", key)
	}
	return id, err
}
func (s *Store) ObservationHead(ctx context.Context, conversation string) (int64, error) {
	var sequence int64
	err := s.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence),0) FROM observations WHERE conversation_id=?", conversation).Scan(&sequence)
	return sequence, err
}

func (s *Store) ObservationSequence(ctx context.Context, id string) (int64, error) {
	var sequence int64
	err := s.db.QueryRowContext(ctx, "SELECT sequence FROM observations WHERE id=?", id).Scan(&sequence)
	return sequence, err
}

func (s *Store) Message(ctx context.Context, conversation, messageID string) (domain.Observation, error) {
	var raw []byte
	var event domain.Observation
	err := s.db.QueryRowContext(ctx, "SELECT payload FROM observations WHERE conversation_id=? AND json_extract(payload,'$.message_id')=? ORDER BY sequence DESC LIMIT 1", conversation, messageID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return event, domain.Missing("message", messageID)
	}
	if err == nil {
		err = json.Unmarshal(raw, &event)
	}
	return event, err
}

func (s *Store) MessageAttachment(ctx context.Context, conversation, messageID, ref string) (domain.Attachment, error) {
	var raw []byte
	var attachment domain.Attachment
	err := s.db.QueryRowContext(ctx, `SELECT a.value FROM observations o, json_each(o.payload,'$.attachments') a WHERE o.conversation_id=? AND json_extract(o.payload,'$.message_id')=? AND json_extract(a.value,'$.ref')=? ORDER BY o.sequence DESC LIMIT 1`, conversation, messageID, ref).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return attachment, domain.Missing("attachment", ref)
	}
	if err == nil {
		err = json.Unmarshal(raw, &attachment)
	}
	return attachment, err
}
func (s *Store) Observation(ctx context.Context, id string) (domain.Observation, error) {
	var raw []byte
	var event domain.Observation
	err := s.db.QueryRowContext(ctx, "SELECT payload FROM observations WHERE id=?", id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return event, domain.Missing("observation", id)
	}
	if err == nil {
		err = json.Unmarshal(raw, &event)
	}
	return event, err
}
