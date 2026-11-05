package harness

import "sync"

// SessionWatcher coalesces Host state invalidations for one open Session view.
// Each reader has its own signal; no runtime state or history is retained here.
type SessionWatcher struct {
	C       <-chan struct{}
	channel chan struct{}
	signal  *sessionChangeSignal
	id      string
}

type sessionChangeSignal struct {
	mu       sync.Mutex
	watchers map[*SessionWatcher]struct{}
	closed   bool
}

// WatchSession includes changes such as Stop, which cancels Core immediately
// but may not produce a Core event until the running operation has unwound.
// Register before reading the initial view, then Close when the reader exits.
// An empty ID watches the directory rather than one Session.
func (s *Service) WatchSession(id string) *SessionWatcher {
	signal := &s.sessionChanges
	channel := make(chan struct{}, 1)
	w := &SessionWatcher{C: channel, channel: channel, signal: signal, id: id}
	signal.mu.Lock()
	defer signal.mu.Unlock()
	if signal.closed {
		close(channel)
		return w
	}
	if signal.watchers == nil {
		signal.watchers = map[*SessionWatcher]struct{}{}
	}
	signal.watchers[w] = struct{}{}
	return w
}

func (w *SessionWatcher) Close() {
	w.signal.mu.Lock()
	defer w.signal.mu.Unlock()
	if _, ok := w.signal.watchers[w]; ok {
		delete(w.signal.watchers, w)
		close(w.channel)
	}
}

func (s *sessionChangeSignal) notify(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for w := range s.watchers {
		if w.id == "" || w.id == id {
			select {
			case w.channel <- struct{}{}:
			default:
			}
		}
	}
}

func (s *sessionChangeSignal) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	for w := range s.watchers {
		delete(s.watchers, w)
		close(w.channel)
	}
}
