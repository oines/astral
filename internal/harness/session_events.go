package harness

import (
	"context"
	"errors"
	"sync/atomic"

	core "github.com/oines/astral-core"
)

// SessionEvent belongs to the Session stream. Core events and durable entries
// are immutable after fan-out. Consumers may copy them for their projections.
type SessionEvent struct {
	Core     *core.Event
	Entry    *core.Entry
	Activity *ActivityStatus
}

type SessionSubscription struct {
	Events <-chan SessionEvent
	events chan SessionEvent
	feed   *sessionEventFeed
}

type sessionEventFeed struct {
	runtime     *runtime
	viewers     map[*SessionSubscription]struct{}
	latest      *ActivityStatus
	activitySeq uint64
	generation  atomic.Uint64
	raw         *core.Subscription
	done        chan struct{}
	cancel      context.CancelFunc
	reopening   bool
}

// SubscribeSession shares one Core subscription and activity observer across
// the Session's online clients. Slow consumers cannot block other consumers or
// the executor. A Session without consumers has no event feed.
func (s *Service) SubscribeSession(ctx context.Context, id string, buffer int) (*SessionSubscription, error) {
	r, err := s.open(ctx, id)
	if err != nil {
		return nil, err
	}
	r.snapshotMu.RLock()
	defer r.snapshotMu.RUnlock()
	session := r.core.Load()
	if r.corePhase != runtimeCoreActive || session == nil {
		return nil, core.ErrClosed
	}
	r.eventsMu.Lock()
	defer r.eventsMu.Unlock()
	if r.events == nil {
		r.events = &sessionEventFeed{runtime: r, viewers: make(map[*SessionSubscription]struct{})}
		r.events.attach(session, r.eventOffset)
	}
	events := make(chan SessionEvent, min(max(buffer, 1), 512))
	sub := &SessionSubscription{Events: events, events: events, feed: r.events}
	r.events.viewers[sub] = struct{}{}
	if current := r.events.latest; current != nil && current.State == "working" {
		events <- SessionEvent{Activity: current}
	}
	return sub, nil
}

func (w *SessionSubscription) Close() {
	f := w.feed
	f.runtime.eventsMu.Lock()
	defer f.runtime.eventsMu.Unlock()
	f.remove(w)
}

func (f *sessionEventFeed) remove(w *SessionSubscription) {
	if _, ok := f.viewers[w]; !ok {
		return
	}
	delete(f.viewers, w)
	close(w.events)
	if len(f.viewers) == 0 {
		f.finish()
	}
}

func (f *sessionEventFeed) finish() {
	if f.runtime.events == f {
		f.runtime.events = nil
	}
	for w := range f.viewers {
		delete(f.viewers, w)
		close(w.events)
	}
	if f.cancel != nil {
		f.cancel()
		f.cancel = nil
	}
	if f.raw != nil {
		f.raw.Close()
		f.raw = nil
	}
}

func (f *sessionEventFeed) attach(session *core.Session, offset uint64) {
	ctx, cancel := context.WithCancel(f.runtime.service.ctx)
	raw := session.Subscribe(256)
	done := make(chan struct{})
	f.raw, f.cancel, f.done = raw, cancel, done
	f.reopening = false
	f.latest = nil
	generation := f.generation.Add(1)
	activity := f.runtime.service.WatchActivity(f.runtime.id, session, session.Snapshot())
	go f.forward(ctx, session, raw, activity, offset, generation, done)
}

func (f *sessionEventFeed) forward(ctx context.Context, session *core.Session, raw *core.Subscription, activity *ActivitySubscription, offset, generation uint64, done chan struct{}) {
	defer close(done)
	if activity != nil {
		defer activity.Close()
	}
	var updates <-chan ActivityStatus
	if activity != nil {
		updates = activity.Updates
	}
	for {
		select {
		case <-ctx.Done():
			f.runtime.eventsMu.Lock()
			if f.generation.Load() == generation {
				f.finish()
			}
			f.runtime.eventsMu.Unlock()
			return
		case event, ok := <-raw.C:
			if !ok {
				f.runtime.eventsMu.Lock()
				if f.generation.Load() == generation && (!f.reopening || !errors.Is(raw.Err(), core.ErrClosed)) {
					f.finish()
				}
				f.runtime.eventsMu.Unlock()
				return
			}
			var entry *core.Entry
			if event.Type == core.EventCommitted && event.EntryID != "" {
				value, err := session.History().Get(ctx, event.EntryID)
				if err != nil {
					f.runtime.eventsMu.Lock()
					f.finish()
					f.runtime.eventsMu.Unlock()
					return
				}
				entry = &value
				if activity != nil {
					activity.ObserveEntry(value)
				}
			}
			if activity != nil {
				activity.Observe(event)
			}
			event.Sequence += offset
			f.broadcast(SessionEvent{Core: &event, Entry: entry})
		case status := <-updates:
			status.CoreSequence += offset
			f.broadcast(SessionEvent{Activity: &status})
		}
	}
}

func (f *sessionEventFeed) broadcast(event SessionEvent) {
	f.runtime.eventsMu.Lock()
	defer f.runtime.eventsMu.Unlock()
	if event.Activity != nil {
		f.activitySeq++
		event.Activity.Seq = f.activitySeq
		f.latest = event.Activity
	}
	for viewer := range f.viewers {
		select {
		case viewer.events <- event:
		default:
			f.remove(viewer)
		}
	}
}
