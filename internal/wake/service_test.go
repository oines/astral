package wake

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

type fixture struct {
	s         *Service
	store     *persistence.Store
	ctx       context.Context
	agent     domain.Agent
	computer  domain.Computer
	session   domain.Session
	delivered chan Batch
}

func setup(t testing.TB, deliver Deliver) *fixture {
	t.Helper()
	store, err := persistence.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{store: store, ctx: context.Background(), computer: domain.Computer{Meta: domain.Meta{ID: "computer_a"}, Name: "Example Computer"}, session: domain.Session{Meta: domain.Meta{ID: "session_root"}, AgentID: "agent_a", ComputerID: "computer_a"}, agent: domain.Agent{Meta: domain.Meta{ID: "agent_a"}, Name: "Example", AllowedComputers: []string{"computer_a", "computer_b"}}, delivered: make(chan Batch, 100)}
	for _, r := range []struct {
		kind  string
		meta  *domain.Meta
		value any
	}{{"agents", &f.agent.Meta, &f.agent}, {"computers", &f.computer.Meta, &f.computer}, {"sessions", &f.session.Meta, &f.session}} {
		if err = store.Save(f.ctx, r.kind, r.meta, r.value); err != nil {
			t.Fatal(err)
		}
	}
	if deliver == nil {
		deliver = func(_ context.Context, b Batch) (DeliveryResult, error) {
			f.delivered <- b
			return DeliveryResult{Accepted: true, Handled: true}, nil
		}
	}
	f.s = New(store, deliver, nil)
	at := time.Now()
	f.s.now = func() time.Time { return at }
	f.s.SetComputerURLResolver(func(string) string { return "http://127.0.0.1:32145" })
	f.ctx = WithComputerID(persistence.WithSessionScope(context.Background(), f.agent.ID, f.session.ID), f.computer.ID)
	t.Cleanup(func() { _ = f.s.Close(context.Background()); _ = store.Close() })
	return f
}
func (f *fixture) create(t testing.TB) domain.WakeEndpoint {
	t.Helper()
	e, err := f.s.Create(f.ctx, f.agent.ID, CreateInput{Title: "Minecraft", Notes: "/opt/example/listener.py", Prompt: "Review game events."})
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func (f *fixture) advance(d time.Duration) {
	f.s.mu.Lock()
	now := f.s.now().Add(d)
	f.s.now = func() time.Time { return now }
	f.s.mu.Unlock()
	f.s.WakeSession(f.session.ID)
}
func receive(t testing.TB, ch <-chan Batch) Batch {
	t.Helper()
	select {
	case b := <-ch:
		return b
	case <-time.After(3 * time.Second):
		t.Fatal("event batch was not delivered")
		return Batch{}
	}
}
func TestCreateScopeAndImmutableIdentity(t *testing.T) {
	f := setup(t, nil)
	e := f.create(t)
	if e.ComputerID != f.computer.ID || e.SessionID != f.session.ID || e.SourceSessionID != f.session.ID || e.TriggerURL != "http://127.0.0.1:32145/hooks/"+e.ID {
		t.Fatalf("wrong defaults: %+v", e)
	}
	if _, err := f.s.Create(context.Background(), f.agent.ID, CreateInput{Title: "Owner"}); !isForbidden(err) {
		t.Fatalf("Owner creation accepted: %v", err)
	}
	child := f.session
	child.Meta = domain.Meta{ID: "session_child"}
	child.SubagentParentID = f.session.ID
	if err := f.store.Save(f.ctx, "sessions", &child.Meta, &child); err != nil {
		t.Fatal(err)
	}
	ctx := WithComputerID(persistence.WithSessionScope(context.Background(), f.agent.ID, child.ID), f.computer.ID)
	for _, check := range []func() error{func() error { _, e := f.s.List(ctx, f.agent.ID); return e }, func() error { _, e := f.s.Get(ctx, f.agent.ID, e.ID); return e }, func() error { _, e := f.s.Create(ctx, f.agent.ID, CreateInput{Title: "child"}); return e }} {
		if err := check(); !isForbidden(err) {
			t.Fatalf("child access: %v", err)
		}
	}
	if _, err := f.s.Create(f.ctx, f.agent.ID, CreateInput{Title: "target child", SessionID: child.ID}); !isForbidden(err) {
		t.Fatalf("child target: %v", err)
	}
	// Management may originate on another Computer; the immutable source does not move.
	ctx = WithComputerID(persistence.WithSessionScope(context.Background(), f.agent.ID, f.session.ID), "computer_b")
	note := "/opt/example/updated.py"
	updated, err := f.s.Update(ctx, f.agent.ID, e.ID, Patch{Revision: e.Revision, Notes: &note})
	if err != nil || updated.ComputerID != e.ComputerID || updated.SessionID != e.SessionID {
		t.Fatalf("cross Computer manage: %+v %v", updated, err)
	}
	if _, err = f.s.Accept(context.Background(), "computer_b", e.ID, Event{EventID: "wrong", Text: "hello"}); !isForbidden(err) {
		t.Fatalf("wrong Daemon trigger: %v", err)
	}
}
func TestFixedWindowBatchAndStableReceipts(t *testing.T) {
	f := setup(t, nil)
	e := f.create(t)
	a, err := f.s.Accept(context.Background(), e.ComputerID, e.ID, Event{EventID: "one", Text: "Steve: hello"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.s.Accept(context.Background(), e.ComputerID, e.ID, Event{EventID: "one", Text: "Steve: hello"})
	if err != nil || a != b {
		t.Fatalf("duplicate: %+v %v", b, err)
	}
	if _, err = f.s.Accept(context.Background(), e.ComputerID, e.ID, Event{EventID: "one", Text: "different"}); err == nil {
		t.Fatal("conflicting event accepted")
	}
	f.s.mu.Lock()
	deadline := f.s.flows[e.SessionID].Due
	f.s.mu.Unlock()
	if _, err = f.s.Accept(context.Background(), e.ComputerID, e.ID, Event{EventID: "two", Text: "Alex: wait"}); err != nil {
		t.Fatal(err)
	}
	f.s.mu.Lock()
	same := f.s.flows[e.SessionID].Due.Equal(deadline)
	f.s.mu.Unlock()
	if !same {
		t.Fatal("merge deadline extended")
	}
	select {
	case <-f.delivered:
		t.Fatal("delivered before window")
	default:
	}
	f.advance(2 * time.Second)
	batch := receive(t, f.delivered)
	if len(batch.Events) != 2 || batch.Events[0].Notes != e.Notes || batch.Events[0].ComputerName != f.computer.Name {
		t.Fatalf("wrong batch: %+v", batch)
	}
	select {
	case <-f.delivered:
		t.Fatal("delivered duplicate")
	case <-time.After(20 * time.Millisecond):
	}
}
func TestCapacityBlocksSourceAndIncludesActualMessages(t *testing.T) {
	f := setup(t, nil)
	e := f.create(t)
	for i := range MaxBatchEvents {
		if _, err := f.s.Accept(context.Background(), e.ComputerID, e.ID, Event{EventID: fmt.Sprint(i), Text: fmt.Sprintf("error %d", i)}); err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
	}
	batch := receive(t, f.delivered)
	if !batch.Flood || len(batch.Events) != 20 {
		t.Fatalf("not one flood batch: %+v", batch)
	}
	_, err := f.s.Accept(context.Background(), e.ComputerID, e.ID, Event{EventID: "overflow", Text: "more"})
	var limited *LimitError
	if !errors.As(err, &limited) || limited.RetryAfter < 59 {
		t.Fatalf("missing fixed cooldown: %v", err)
	}
	before, err := f.s.Get(f.ctx, f.agent.ID, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	for range 50 {
		_, _ = f.s.Accept(context.Background(), e.ComputerID, e.ID, Event{EventID: "overflow", Text: "more"})
	}
	after, _ := f.s.Get(f.ctx, f.agent.ID, e.ID)
	if !after.BlockedUntil.Equal(*before.BlockedUntil) {
		t.Fatal("rejected calls extended cooldown")
	}
	f.advance(61 * time.Second)
	if _, err = f.s.Accept(context.Background(), e.ComputerID, e.ID, Event{EventID: "new", Text: "normal"}); err != nil {
		t.Fatalf("did not recover: %v", err)
	}
	pause := false
	_, err = f.s.Update(f.ctx, f.agent.ID, e.ID, Patch{Revision: e.Revision, Enabled: &pause})
	if err != nil {
		t.Fatal(err)
	}
	f.advance(time.Hour)
	if _, err = f.s.Accept(context.Background(), e.ComputerID, e.ID, Event{EventID: "paused", Text: "normal"}); err == nil {
		t.Fatal("manual pause was automatically undone")
	}
}
func TestTruncationBoundariesAndBoundedContext(t *testing.T) {
	for limit := 1; limit < 150; limit++ {
		out, truncated := clip(strings.Repeat("中文🙂", 40), limit)
		if !truncated || !utf8.ValidString(out) || len(out) > limit {
			t.Fatalf("bad clip(%d): %q", limit, out)
		}
	}
	f := setup(t, nil)
	e := f.create(t)
	for i := range 4 {
		r, err := f.s.Accept(context.Background(), e.ComputerID, e.ID, Event{EventID: fmt.Sprint(i), Text: strings.Repeat("中文🙂", 1000)})
		if err != nil || !r.Truncated {
			t.Fatalf("truncation: %+v %v", r, err)
		}
	}
	batch := receive(t, f.delivered)
	size := 0
	for _, event := range batch.Events {
		size += len(event.Text)
	}
	if size > MaxBatchBytes || !batch.Flood {
		t.Fatalf("context grew: %d %+v", size, batch)
	}
}
func TestStopAndRestartKeepAcceptedBufferDurable(t *testing.T) {
	f := setup(t, nil)
	e := f.create(t)
	event := Event{EventID: "old", Text: "accepted before restart"}
	first, err := f.s.Accept(context.Background(), e.ComputerID, e.ID, event)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.s = New(f.store, func(_ context.Context, b Batch) (DeliveryResult, error) {
		f.delivered <- b
		return DeliveryResult{Accepted: true, Handled: true}, nil
	}, nil)
	if err = f.s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.advance(2 * time.Second)
	batch := receive(t, f.delivered)
	if len(batch.Events) != 1 || batch.Events[0].EventID != "old" {
		t.Fatalf("lost restart data: %+v", batch)
	}
	if err = f.s.StopSession(context.Background(), e.SessionID); err != nil {
		t.Fatal(err)
	}
	again, err := f.s.Accept(context.Background(), e.ComputerID, e.ID, event)
	if err != nil || first != again {
		t.Fatalf("receipt lost: %+v %v", again, err)
	}
	f.advance(2 * time.Second)
	select {
	case <-f.delivered:
		t.Fatal("old retry revived stopped input")
	case <-time.After(20 * time.Millisecond):
	}
	if _, err = f.s.Accept(context.Background(), e.ComputerID, e.ID, Event{EventID: "new", Text: "new event"}); err != nil {
		t.Fatal(err)
	}
	f.advance(2 * time.Second)
	_ = receive(t, f.delivered)
}
func TestConcurrentFloodCannotGrowQueue(t *testing.T) {
	f := setup(t, func(context.Context, Batch) (DeliveryResult, error) { return DeliveryResult{}, nil })
	e := f.create(t)
	var wg sync.WaitGroup
	for i := range 200 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = f.s.Accept(context.Background(), e.ComputerID, e.ID, Event{EventID: fmt.Sprint(i), Text: "error"})
		}()
	}
	wg.Wait()
	f.s.mu.Lock()
	flow := f.s.flows[e.SessionID]
	count := len(flow.Buffer)
	if flow.Inflight != nil {
		count += len(flow.Inflight.Events)
	}
	f.s.mu.Unlock()
	if count != 20 {
		t.Fatalf("expected bounded 20 events, got %d", count)
	}
}
func TestNoPayloadVersionsAndDeletionCleanup(t *testing.T) {
	f := setup(t, nil)
	e := f.create(t)
	_, err := f.s.Accept(context.Background(), e.ComputerID, e.ID, Event{EventID: "one", Text: "unique payload"})
	if err != nil {
		t.Fatal(err)
	}
	retained, err := f.store.Retained(context.Background(), Kind)
	if err != nil || len(retained) != 1 {
		t.Fatalf("events created configuration versions: %d %v", len(retained), err)
	}
	if err = f.store.Update(context.Background(), func(tx *persistence.Tx) error { return tx.PurgeSessionDeliveries(context.Background(), e.SessionID) }); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Get(f.ctx, f.agent.ID, e.ID); !domain.IsMissing(err) {
		t.Fatalf("target deletion left endpoint: %v", err)
	}
	var receipt eventReceipt
	if err = f.store.Maintenance(context.Background(), receiptKind, eventKey(e.ID, "one"), &receipt); !domain.IsMissing(err) {
		t.Fatalf("orphan receipt: %v", err)
	}
}
