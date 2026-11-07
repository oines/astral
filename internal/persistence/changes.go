package persistence

import (
	"context"
	"database/sql"
	"sync"
)

type changeSignal struct {
	mu       sync.Mutex
	current  chan struct{}
	watchers map[*Watcher]struct{}
}

// ChangeKey identifies a Host record. An empty ID watches the whole kind.
// Observation changes use the Conversation ID; maintenance uses its kind/ID.
type ChangeKey struct {
	Kind string
	ID   string
}

// Watcher owns a bounded set of record selectors, never record contents.
// Close it when its worker exits so deleted chats do not retain subscriptions.
type Watcher struct {
	store   *Store
	keys    []ChangeKey
	current chan struct{}
}

func (s *Store) Watch(keys ...ChangeKey) *Watcher {
	w := &Watcher{store: s, keys: append([]ChangeKey(nil), keys...), current: make(chan struct{})}
	s.changes.mu.Lock()
	if s.changes.watchers == nil {
		s.changes.watchers = map[*Watcher]struct{}{}
	}
	s.changes.watchers[w] = struct{}{}
	s.changes.mu.Unlock()
	return w
}

// Changes must be obtained before reading state, as with Store.Changes.
// It returns nil after Close.
func (w *Watcher) Changes() <-chan struct{} {
	w.store.changes.mu.Lock()
	defer w.store.changes.mu.Unlock()
	return w.current
}

func (w *Watcher) Close() {
	w.store.changes.mu.Lock()
	defer w.store.changes.mu.Unlock()
	if w.current != nil {
		delete(w.store.changes.watchers, w)
		close(w.current)
		w.current, w.keys = nil, nil
	}
}

func (w *Watcher) matches(keys []ChangeKey) bool {
	if len(w.keys) == 0 || len(keys) == 0 {
		return true
	}
	for _, changed := range keys {
		for _, watched := range w.keys {
			if watched.Kind == changed.Kind && (watched.ID == "" || watched.ID == changed.ID) {
				return true
			}
		}
	}
	return false
}

// Changes returns a coalesced invalidation signal, not a stream of records.
// Obtain it before reading state, then wait on it after the read. A committed
// write between the read and the wait therefore cannot be missed.
func (s *Store) Changes() <-chan struct{} {
	s.changes.mu.Lock()
	defer s.changes.mu.Unlock()
	if s.changes.current == nil {
		s.changes.current = make(chan struct{})
	}
	return s.changes.current
}

func (s *Store) notifyChanges(keys ...ChangeKey) {
	s.changes.mu.Lock()
	defer s.changes.mu.Unlock()
	if s.changes.current != nil {
		close(s.changes.current)
		s.changes.current = make(chan struct{})
	}
	for watcher := range s.changes.watchers {
		if watcher.matches(keys) {
			close(watcher.current)
			watcher.current = make(chan struct{})
		}
	}
}

func changedRows(result sql.Result, err error) bool {
	if err != nil || result == nil {
		return false
	}
	n, err := result.RowsAffected()
	return err != nil || n > 0
}

func (s *Store) execKey(ctx context.Context, key ChangeKey, query string, args ...any) (sql.Result, error) {
	result, err := s.db.ExecContext(ctx, query, args...)
	if changedRows(result, err) {
		if key.Kind == "" {
			s.notifyChanges()
		} else {
			s.notifyChanges(key)
		}
	}
	return result, err
}

func (tx *Tx) exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return tx.execKey(ctx, ChangeKey{}, query, args...)
}

func (tx *Tx) execKey(ctx context.Context, key ChangeKey, query string, args ...any) (sql.Result, error) {
	result, err := tx.tx.ExecContext(ctx, query, args...)
	if changedRows(result, err) {
		tx.changed = true
		if key.Kind == "" {
			tx.allChanges, tx.changedKeys = true, nil
		} else if !tx.allChanges {
			if tx.changedKeys == nil {
				tx.changedKeys = map[ChangeKey]struct{}{}
			}
			tx.changedKeys[key] = struct{}{}
		}
	}
	return result, err
}

func (tx *Tx) publishChanges() {
	if tx.allChanges {
		tx.store.notifyChanges()
		return
	}
	keys := make([]ChangeKey, 0, len(tx.changedKeys))
	for key := range tx.changedKeys {
		keys = append(keys, key)
	}
	tx.store.notifyChanges(keys...)
}

type changeWriter struct {
	exec func(context.Context, ChangeKey, string, ...any) (sql.Result, error)
	key  ChangeKey
}

func (w changeWriter) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return w.exec(ctx, w.key, query, args...)
}
