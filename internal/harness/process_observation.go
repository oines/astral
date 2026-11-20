package harness

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

const (
	processMemoryBufferBytes    = 128 * 1024
	processPersistedOutputBytes = 64 * 1024
	maxRetainedEndedProcesses   = 16
	processOutputKind           = "process_outputs"
)

// ProcessStatus describes the live or persisted terminal state of an
// asynchronous exec_command process without transferring its output body.
type ProcessStatus struct {
	ExecutionID string `json:"execution_id"`
	Running     bool   `json:"running"`
	ExitCode    *int   `json:"exit_code,omitempty"`
	Error       string `json:"error,omitempty"`
	HasOutput   bool   `json:"has_output,omitempty"`
	Truncated   bool   `json:"truncated,omitempty"`
}

// ProcessOutputSnapshot is the initial payload returned when a reader expands
// an asynchronous exec_command process to inspect its output.
type ProcessOutputSnapshot struct {
	ExecutionID string `json:"execution_id"`
	Running     bool   `json:"running"`
	ExitCode    *int   `json:"exit_code,omitempty"`
	Error       string `json:"error,omitempty"`
	Output      string `json:"output"`
	Truncated   bool   `json:"truncated,omitempty"`
}

// ProcessOutputUpdate represents an incremental output chunk or terminal state
// emitted to an active expanded output subscriber.
type ProcessOutputUpdate struct {
	Chunk     string `json:"chunk,omitempty"`
	Reset     bool   `json:"reset,omitempty"`
	Output    string `json:"output,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	Done      bool   `json:"done,omitempty"`
	ExitCode  *int   `json:"exit_code,omitempty"`
	Error     string `json:"error,omitempty"`
}

type processOutputRecord struct {
	domain.Meta
	SessionID   string `json:"session_id"`
	ExecutionID string `json:"execution_id"`
	RuntimeID   string `json:"runtime_id"`
	ExitCode    *int   `json:"exit_code,omitempty"`
	Error       string `json:"error,omitempty"`
	Output      string `json:"output"`
	Truncated   bool   `json:"truncated,omitempty"`
}

type processObserver struct {
	runtime     *runtime
	sessionID   string
	executionID string

	mu           sync.Mutex
	buf          []byte
	baseOffset   int64
	truncated    bool
	async        bool
	exited       bool
	exitCode     *int
	exitErr      string
	outputClosed bool
	closedCh     chan struct{}
	persisted    bool
	endedAt      time.Time
	subs         map[chan struct{}]struct{}
}

func newProcessObserver(r *runtime, executionID string) *processObserver {
	return &processObserver{
		runtime:     r,
		sessionID:   r.id,
		executionID: executionID,
		closedCh:    make(chan struct{}),
		subs:        map[chan struct{}]struct{}{},
	}
}

func trimUTF8Tail(data []byte, limit int) ([]byte, bool) {
	if len(data) <= limit {
		return data, false
	}
	cut := len(data) - limit
	for cut < len(data) && !utf8.RuneStart(data[cut]) {
		cut++
	}
	return data[cut:], true
}

func (o *processObserver) append(chunk []byte) {
	if len(chunk) == 0 {
		return
	}
	o.mu.Lock()
	o.buf = append(o.buf, chunk...)
	if len(o.buf) > processMemoryBufferBytes {
		cut := len(o.buf) - processMemoryBufferBytes
		for cut < len(o.buf) && !utf8.RuneStart(o.buf[cut]) {
			cut++
		}
		o.baseOffset += int64(cut)
		next := make([]byte, len(o.buf)-cut)
		copy(next, o.buf[cut:])
		o.buf = next
		o.truncated = true
	}
	o.notifySubscribersLocked()
	o.mu.Unlock()
}

func (o *processObserver) closeOutput() {
	o.mu.Lock()
	if !o.outputClosed {
		o.outputClosed = true
		close(o.closedCh)
		o.notifySubscribersLocked()
	}
	o.mu.Unlock()
}

func (o *processObserver) notifySubscribersLocked() {
	for ch := range o.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (o *processObserver) statusLocked() ProcessStatus {
	var code *int
	if o.exitCode != nil {
		v := *o.exitCode
		code = &v
	}
	return ProcessStatus{
		ExecutionID: o.executionID,
		Running:     !o.exited,
		ExitCode:    code,
		Error:       o.exitErr,
		HasOutput:   len(o.buf) > 0,
		Truncated:   o.truncated,
	}
}

func (o *processObserver) snapshotLocked() (ProcessOutputSnapshot, int64) {
	var code *int
	if o.exitCode != nil {
		v := *o.exitCode
		code = &v
	}
	return ProcessOutputSnapshot{
		ExecutionID: o.executionID,
		Running:     !o.exited,
		ExitCode:    code,
		Error:       o.exitErr,
		Output:      string(o.buf),
		Truncated:   o.truncated,
	}, o.baseOffset + int64(len(o.buf))
}

func (o *processObserver) markAsync() {
	o.mu.Lock()
	if o.async {
		o.mu.Unlock()
		return
	}
	o.async = true
	status := o.statusLocked()
	exited := o.exited
	o.mu.Unlock()

	o.runtime.service.publishProcessStatus(o.sessionID, status)
	if exited {
		go o.finalizeAsync()
	}
}

func (o *processObserver) markExited(code int, err error) {
	o.mu.Lock()
	if o.exited {
		o.mu.Unlock()
		return
	}
	o.exited = true
	o.endedAt = time.Now()
	if err == nil {
		v := code
		o.exitCode = &v
	} else {
		if code != 0 {
			v := code
			o.exitCode = &v
		}
		o.exitErr = err.Error()
	}
	async := o.async
	o.notifySubscribersLocked()
	o.mu.Unlock()

	if async {
		go o.finalizeAsync()
	} else {
		o.runtime.dropSyncObserver(o.executionID, o)
	}
}

func (o *processObserver) finalizeAsync() {
	// Briefly wait for the final bytes draining from Output() so the persisted
	// terminal snapshot captures the tail of the process output.
	select {
	case <-o.closedCh:
	case <-time.After(200 * time.Millisecond):
	}

	o.mu.Lock()
	if o.persisted {
		o.mu.Unlock()
		return
	}
	o.persisted = true
	status := o.statusLocked()
	tail, tailTrimmed := trimUTF8Tail(o.buf, processPersistedOutputBytes)
	output := string(tail)
	truncated := o.truncated || tailTrimmed
	status.Truncated = truncated
	var code *int
	if o.exitCode != nil {
		v := *o.exitCode
		code = &v
	}
	errText := o.exitErr
	o.notifySubscribersLocked()
	o.mu.Unlock()

	o.runtime.service.publishProcessStatus(o.sessionID, status)
	o.runtime.pruneEndedObservers()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	record := processOutputRecord{
		Meta:        domain.Meta{ID: o.executionID},
		SessionID:   o.sessionID,
		ExecutionID: o.executionID,
		RuntimeID:   o.runtime.service.RuntimeID,
		ExitCode:    code,
		Error:       errText,
		Output:      output,
		Truncated:   truncated,
	}
	_ = o.runtime.service.Store.Save(ctx, processOutputKind, &record.Meta, &record)
}

// ProcessOutputSubscription streams incremental output from a live observer.
type ProcessOutputSubscription struct {
	observer *processObserver
	ch       chan struct{}
	offset   int64
	once     sync.Once
}

// Next waits for new output or completion, coalescing rapid writes into one update.
func (s *ProcessOutputSubscription) Next(ctx context.Context) (ProcessOutputUpdate, bool) {
	if s == nil || s.observer == nil {
		return ProcessOutputUpdate{}, false
	}
	for {
		update, ready := s.readPending()
		if ready {
			return update, true
		}
		select {
		case <-ctx.Done():
			return ProcessOutputUpdate{}, false
		case _, ok := <-s.ch:
			if !ok {
				return ProcessOutputUpdate{}, false
			}
			// Coalesce bursts briefly to keep SSE traffic and browser painting smooth.
			timer := time.NewTimer(120 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ProcessOutputUpdate{}, false
			case <-timer.C:
			}
		}
	}
}

func (s *ProcessOutputSubscription) readPending() (ProcessOutputUpdate, bool) {
	o := s.observer
	o.mu.Lock()
	defer o.mu.Unlock()

	endOffset := o.baseOffset + int64(len(o.buf))
	var update ProcessOutputUpdate
	hasChange := false
	if s.offset < o.baseOffset {
		update.Reset = true
		update.Output = string(o.buf)
		update.Truncated = o.truncated
		s.offset = endOffset
		hasChange = true
	} else if endOffset > s.offset {
		start := int(s.offset - o.baseOffset)
		update.Chunk = string(o.buf[start:])
		update.Truncated = o.truncated
		s.offset = endOffset
		hasChange = true
	}
	if o.exited && (o.outputClosed || o.persisted) {
		update.Done = true
		if o.exitCode != nil {
			v := *o.exitCode
			update.ExitCode = &v
		}
		update.Error = o.exitErr
		update.Truncated = o.truncated
		hasChange = true
	}
	return update, hasChange
}

func (s *ProcessOutputSubscription) Close() {
	if s == nil || s.observer == nil {
		return
	}
	s.once.Do(func() {
		o := s.observer
		o.mu.Lock()
		delete(o.subs, s.ch)
		close(s.ch)
		o.mu.Unlock()
	})
}

type observedOutput struct {
	io.ReadCloser
	observer *processObserver
}

func (o *observedOutput) Read(p []byte) (int, error) {
	n, err := o.ReadCloser.Read(p)
	if n > 0 {
		o.observer.append(p[:n])
	}
	if err != nil {
		o.observer.closeOutput()
	}
	return n, err
}

func (o *observedOutput) Close() error {
	o.observer.closeOutput()
	return o.ReadCloser.Close()
}

func (r *runtime) registerObserver(executionID string) *processObserver {
	obs := newProcessObserver(r, executionID)
	r.processMu.Lock()
	if r.processObservers == nil {
		r.processObservers = map[string]*processObserver{}
	}
	r.processObservers[executionID] = obs
	r.processMu.Unlock()
	return obs
}

func (r *runtime) markProcessAsync(executionID string) {
	r.processMu.Lock()
	obs := r.processObservers[executionID]
	r.processMu.Unlock()
	if obs != nil {
		obs.markAsync()
	}
}

func (r *runtime) dropSyncObserver(executionID string, target *processObserver) {
	r.processMu.Lock()
	if r.processObservers[executionID] == target {
		delete(r.processObservers, executionID)
	}
	r.processMu.Unlock()
}

func (r *runtime) pruneEndedObservers() {
	r.processMu.Lock()
	defer r.processMu.Unlock()
	var ended []*processObserver
	for _, obs := range r.processObservers {
		obs.mu.Lock()
		isEnded := obs.async && obs.exited && len(obs.subs) == 0
		obs.mu.Unlock()
		if isEnded {
			ended = append(ended, obs)
		}
	}
	if len(ended) <= maxRetainedEndedProcesses {
		return
	}
	// Evict oldest ended observers; their bounded terminal snapshot is already persisted.
	for len(ended) > maxRetainedEndedProcesses {
		oldestIdx := 0
		for i := 1; i < len(ended); i++ {
			if ended[i].endedAt.Before(ended[oldestIdx].endedAt) {
				oldestIdx = i
			}
		}
		delete(r.processObservers, ended[oldestIdx].executionID)
		ended = append(ended[:oldestIdx], ended[oldestIdx+1:]...)
	}
}

func (r *runtime) liveProcessStatuses() []ProcessStatus {
	r.processMu.Lock()
	observers := make([]*processObserver, 0, len(r.processObservers))
	for _, obs := range r.processObservers {
		observers = append(observers, obs)
	}
	r.processMu.Unlock()

	var statuses []ProcessStatus
	for _, obs := range observers {
		obs.mu.Lock()
		if obs.async {
			statuses = append(statuses, obs.statusLocked())
		}
		obs.mu.Unlock()
	}
	return statuses
}

func (s *Service) publishProcessStatus(sessionID string, status ProcessStatus) {
	s.processWatchMu.Lock()
	defer s.processWatchMu.Unlock()
	for ch := range s.processWatchers[sessionID] {
		select {
		case ch <- status:
		default:
			// Drop stale queued status in favor of the newest transition.
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- status:
			default:
			}
		}
	}
}

// WatchProcesses subscribes to asynchronous process lifecycle updates for a Session.
func (s *Service) WatchProcesses(sessionID string) (<-chan ProcessStatus, func()) {
	ch := make(chan ProcessStatus, 32)
	s.processWatchMu.Lock()
	if s.processWatchers == nil {
		s.processWatchers = map[string]map[chan ProcessStatus]struct{}{}
	}
	if s.processWatchers[sessionID] == nil {
		s.processWatchers[sessionID] = map[chan ProcessStatus]struct{}{}
	}
	s.processWatchers[sessionID][ch] = struct{}{}
	s.processWatchMu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			s.processWatchMu.Lock()
			if group := s.processWatchers[sessionID]; group != nil {
				delete(group, ch)
				if len(group) == 0 {
					delete(s.processWatchers, sessionID)
				}
			}
			s.processWatchMu.Unlock()
		})
	}
	return ch, cancel
}

// SessionProcesses returns live asynchronous process statuses merged with
// bounded persisted terminal records for the given Session.
func (s *Service) SessionProcesses(ctx context.Context, sessionID string) ([]ProcessStatus, error) {
	byID := map[string]ProcessStatus{}

	s.mu.Lock()
	r := s.runtimes[sessionID]
	s.mu.Unlock()
	if r != nil {
		for _, status := range r.liveProcessStatuses() {
			byID[status.ExecutionID] = status
		}
	}

	rows, err := s.Store.List(ctx, processOutputKind)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		var rec processOutputRecord
		if json.Unmarshal(row.Data, &rec) != nil || rec.SessionID != sessionID {
			continue
		}
		if _, exists := byID[rec.ExecutionID]; exists {
			continue
		}
		byID[rec.ExecutionID] = ProcessStatus{
			ExecutionID: rec.ExecutionID,
			Running:     false,
			ExitCode:    rec.ExitCode,
			Error:       rec.Error,
			HasOutput:   len(rec.Output) > 0,
			Truncated:   rec.Truncated,
		}
	}

	out := make([]ProcessStatus, 0, len(byID))
	for _, status := range byID {
		out = append(out, status)
	}
	return out, nil
}

// ObserveProcessOutput returns the current output snapshot for an asynchronous
// process and, when the process is still live in memory, an incremental stream.
func (s *Service) ObserveProcessOutput(ctx context.Context, sessionID, executionID string) (ProcessOutputSnapshot, *ProcessOutputSubscription, error) {
	// Ensure the Session exists and is accessible.
	var record domain.Session
	if err := s.Store.Get(ctx, "sessions", sessionID, &record); err != nil {
		return ProcessOutputSnapshot{}, nil, err
	}
	if record.Deletion != nil {
		return ProcessOutputSnapshot{}, nil, domain.Missing("Session", sessionID)
	}

	s.mu.Lock()
	r := s.runtimes[sessionID]
	s.mu.Unlock()
	if r != nil {
		r.processMu.Lock()
		obs := r.processObservers[executionID]
		r.processMu.Unlock()
		if obs != nil {
			obs.mu.Lock()
			snap, offset := obs.snapshotLocked()
			var sub *ProcessOutputSubscription
			if !obs.exited || !obs.outputClosed {
				ch := make(chan struct{}, 1)
				obs.subs[ch] = struct{}{}
				sub = &ProcessOutputSubscription{observer: obs, ch: ch, offset: offset}
			}
			obs.mu.Unlock()
			return snap, sub, nil
		}
	}

	var stored processOutputRecord
	if err := s.Store.Get(ctx, processOutputKind, executionID, &stored); err != nil {
		return ProcessOutputSnapshot{}, nil, err
	}
	if stored.SessionID != sessionID {
		return ProcessOutputSnapshot{}, nil, domain.Missing("process output", executionID)
	}
	return ProcessOutputSnapshot{
		ExecutionID: stored.ExecutionID,
		Running:     false,
		ExitCode:    stored.ExitCode,
		Error:       stored.Error,
		Output:      stored.Output,
		Truncated:   stored.Truncated,
	}, nil, nil
}

func purgeSessionProcessOutputs(ctx context.Context, tx *persistence.Tx, sessionID string) error {
	rows, err := tx.List(ctx, processOutputKind)
	if err != nil {
		return err
	}
	for _, row := range rows {
		var rec processOutputRecord
		if json.Unmarshal(row.Data, &rec) != nil || rec.SessionID != sessionID {
			continue
		}
		if err := tx.Purge(ctx, processOutputKind, rec.ID, rec.Revision); err != nil && !domain.IsMissing(err) {
			return err
		}
	}
	handleRows, err := tx.List(ctx, "process_handles")
	if err != nil {
		return err
	}
	for _, row := range handleRows {
		var handle processHandle
		if json.Unmarshal(row.Data, &handle) != nil || handle.SessionID != sessionID {
			continue
		}
		if err := tx.Purge(ctx, "process_handles", handle.ID, handle.Revision); err != nil && !domain.IsMissing(err) {
			return err
		}
	}
	return nil
}
