package channels

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/harness"
	"github.com/oines/astral/internal/messages"
	"github.com/oines/astral/internal/testkit"
)

func TestProbeEventDeadlineWaitsForQuietOrMaximum(t *testing.T) {
	for _, test := range []struct {
		name              string
		last, now, nextAt time.Duration
	}{
		{name: "quiet", last: 4 * time.Second, now: 5 * time.Second, nextAt: 14 * time.Second},
		{name: "maximum", last: 55 * time.Second, now: 56 * time.Second, nextAt: time.Minute},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := testkit.New(t, 0, func(context.Context, int, map[string]any) any {
				t.Error("checking a Probe deadline called a model")
				return testkit.Text("unexpected")
			})
			s := New(p.Store, p.Harness)
			binding := activeDeliveryChat(t, p, s, true, false)
			s.probeQuiet, s.probeMaxWait = 10*time.Second, time.Minute
			base := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
			var conversation domain.Conversation
			if err := p.Store.Get(p.Context, "conversations", binding.ConversationID, &conversation); err != nil {
				t.Fatal(err)
			}
			var through int64
			for i, offset := range []time.Duration{0, test.last} {
				id := fmt.Sprintf("timed-deadline-%d", i)
				event := domain.Observation{ID: id, ExternalEventID: id, MessageID: id, SchemaVersion: 1, Kind: "message.created",
					ConnectionID: conversation.ConnectionID, ConversationID: binding.ConversationID,
					Text: "Passive deadline candidate " + id, Sender: domain.Sender{ID: "7", Name: "Ada"},
					ReceivedAt: base.Add(offset), SentAt: base.Add(offset)}
				if _, err := p.Store.Archive(p.Context, event); err != nil {
					t.Fatal(err)
				}
				if err := s.deliver(p.Context, binding, event); err != nil {
					t.Fatal(err)
				}
				var err error
				through, err = p.Store.ObservationSequence(p.Context, event.ID)
				if err != nil {
					t.Fatal(err)
				}
			}
			want := base.Add(test.nextAt)
			for _, now := range []time.Time{base.Add(test.now), want.Add(-time.Nanosecond)} {
				window, err := s.pendingProbe(p.Context, binding, through, now)
				if err != nil || window.Through != 0 || window.Retry || !window.NextAt.Equal(want) {
					t.Fatalf("Probe became ready before its deadline or lost its deadline: %+v err=%v want=%s", window, err, want)
				}
			}
			window, err := s.pendingProbe(p.Context, binding, through, want)
			if err != nil || window.Through != through || window.Retry || !window.NextAt.IsZero() || len(window.Inputs) != 2 {
				t.Fatalf("Probe was not ready exactly at its deadline: %+v %v", window, err)
			}
			if err := s.coverProbe(p.Context, binding, through); err != nil {
				t.Fatal(err)
			}
			window, err = s.pendingProbe(p.Context, binding, through, want)
			if err != nil || window.Through != 0 || window.Retry || !window.NextAt.IsZero() || p.Requests.Load() != 0 {
				t.Fatalf("covered empty window kept a timer or called a model: %+v %v", window, err)
			}
		})
	}
}

func TestProbeEventUnappliedObservationRetainsRetry(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any {
		t.Error("unapplied observation called a model")
		return testkit.Text("unexpected")
	})
	s := New(p.Store, p.Harness)
	binding := activeDeliveryChat(t, p, s, true, false)
	event, through := activeDeliveryArchive(t, p, binding, "queued-passive-observation", "")
	session, err := p.Harness.Core(p.Context, binding.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	state := session.State()
	contexts := []core.Message{core.User(event.Text)}
	raw, err := json.Marshal(contexts)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	_, err = p.CoreStore.Commit(p.Context, binding.SessionID, core.Commit{
		ID: "accepted-passive-queue-before-restart", ExpectedVersion: state.Version,
		Entries: []core.Entry{{ID: "queued-passive-entry", SessionID: binding.SessionID, Version: state.Version + 1, Time: time.Now().UTC(), Kind: core.EntryInputQueued,
			Input: &core.InputRecord{ID: event.ID, Kind: core.InputContext, Digest: hex.EncodeToString(digest[:]), Contexts: contexts}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = session.Reload(p.Context); err != nil {
		t.Fatal(err)
	}
	window, err := s.pendingProbe(p.Context, binding, through, time.Now().Add(time.Minute))
	if err != nil || !window.Retry || window.Through != 0 || !window.NextAt.IsZero() || p.Requests.Load() != 0 {
		t.Fatalf("unapplied candidate lost its short retry: %+v %v", window, err)
	}
	if _, err = session.Steer(p.Context, core.Input{ID: "owner-queued-before-probe", Message: core.User("Owner input remains queued.")}); err != nil {
		t.Fatal(err)
	}
	window, err = s.pendingProbe(p.Context, binding, through, time.Now().Add(time.Minute))
	if err != nil || !window.Retry || window.Through != 0 || !window.NextAt.IsZero() {
		t.Fatalf("temporarily ineligible Session lost its short retry: %+v %v", window, err)
	}
}

func TestProbeEventOwnerQueueDoesNotOpenColdRecovery(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any {
		t.Error("Owner queue or foreign Probe was recovered automatically")
		return testkit.Text("unexpected")
	})
	s := New(p.Store, p.Harness)
	binding := activeDeliveryChat(t, p, s, true, false)
	session, err := p.Harness.Core(p.Context, binding.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []core.Input{
		{ID: "owner-steer", Message: core.User("Keep ordinary Owner work queued.")},
		{ID: "probe:other-binding:1", Message: func() core.Message {
			message := core.User("Foreign binding wakeup.")
			message.Metadata = encoded(messages.Metadata{Source: "im.wakeup", ConversationID: binding.ConversationID})
			return message
		}()},
	} {
		if _, err := session.Steer(p.Context, input); err != nil {
			t.Fatal(err)
		}
	}
	version := session.State().Version
	activeDeliveryReopen(t, p)
	s = New(p.Store, p.Harness)
	for range 2 {
		pending, err := s.resumeAcceptedProbeStatus(p.Context, binding)
		if err != nil || pending {
			t.Fatalf("unrelated queue retained a Probe retry: pending=%v err=%v", pending, err)
		}
	}
	if _, loaded := p.Harness.IMProbeRecoveryPossible(binding.SessionID); loaded {
		t.Fatal("unrelated queued input opened a cold Session")
	}
	current, err := p.CoreStore.LoadState(p.Context, binding.SessionID)
	if err != nil || current.Version != version || current.PendingInputs[core.InputSteer] != 2 || p.Requests.Load() != 0 {
		t.Fatalf("Probe status changed Owner work: %+v calls=%d err=%v", current.PendingInputs, p.Requests.Load(), err)
	}
}

func TestProbeEventAcceptedRecoveryBusyAndStopped(t *testing.T) {
	entered := make(chan struct{})
	gate, release := activeDeliveryGate(t)
	p := testkit.New(t, 0, func(ctx context.Context, call int, _ map[string]any) any {
		if call != 1 {
			t.Error("Probe status started another model request")
			return testkit.Text("unexpected")
		}
		close(entered)
		select {
		case <-gate:
			return testkit.Text("completed")
		case <-ctx.Done():
			return nil
		}
	})
	s := New(p.Store, p.Harness)
	binding := activeDeliveryChat(t, p, s, true, false)
	if _, err := p.Harness.Submit(p.Context, binding.SessionID, harness.InputRequest{ID: "held-owner-run", Text: "Hold the current generation."}); err != nil {
		t.Fatal(err)
	}
	activeDeliveryWait(t, p.Context, entered, "held generation")
	event, through := activeDeliveryArchive(t, p, binding, "accepted-probe-while-busy", "")
	wakeupID := activeDeliveryAcceptWakeup(t, p, binding, event, through)
	if pending, err := s.resumeAcceptedProbeStatus(p.Context, binding); err != nil || !pending || p.Requests.Load() != 1 {
		t.Fatalf("accepted busy Probe lost its retry or started another request: pending=%v calls=%d err=%v", pending, p.Requests.Load(), err)
	}
	if err := p.Harness.Stop(p.Context, binding.SessionID); err != nil {
		t.Fatal(err)
	}
	release()
	// Stop requests cancellation without joining the Run. Recovery treats an
	// unfinished cancellation as busy until its terminal fact covers the Probe.
	view := p.Idle(t, binding.SessionID)
	if view.LastRun == nil || view.LastRun.Status != "cancelled" {
		t.Fatalf("held generation did not persist its cancellation: %+v", view.LastRun)
	}
	if pending, err := s.resumeAcceptedProbeStatus(p.Context, binding); err != nil || pending || p.Requests.Load() != 1 {
		t.Fatalf("user-stopped Probe was scheduled again: pending=%v calls=%d err=%v", pending, p.Requests.Load(), err)
	}
	receipt, err := p.Harness.Receipt(p.Context, binding.SessionID, wakeupID)
	if err != nil || receipt.State != string(core.InputPending) {
		t.Fatalf("stopped Probe queue was consumed or discarded: %+v %v", receipt, err)
	}
}

func TestProbeEventAcceptedRecoveryRunsOnce(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any {
		return testkit.Text("Recovered the accepted Probe once.")
	})
	s := New(p.Store, p.Harness)
	binding := activeDeliveryChat(t, p, s, true, false)
	event, through := activeDeliveryArchive(t, p, binding, "accepted-probe-before-restart", "")
	wakeupID := activeDeliveryAcceptWakeup(t, p, binding, event, through)
	if pending, err := s.resumeAcceptedProbeStatus(p.Context, binding); err != nil || !pending {
		t.Fatalf("accepted Probe did not enter recovery: pending=%v err=%v", pending, err)
	}
	view := p.Idle(t, binding.SessionID)
	if view.LastRun == nil || view.LastRun.Status != "completed" || p.Requests.Load() != 1 {
		t.Fatalf("accepted Probe did not complete one ordinary generation: %+v calls=%d", view.LastRun, p.Requests.Load())
	}
	for range 3 {
		if pending, err := s.resumeAcceptedProbeStatus(p.Context, binding); err != nil || pending {
			t.Fatalf("consumed Probe kept a retry: pending=%v err=%v", pending, err)
		}
	}
	receipt, err := p.Harness.Receipt(p.Context, binding.SessionID, wakeupID)
	if err != nil || receipt.State != string(core.InputApplied) || p.Requests.Load() != 1 {
		t.Fatalf("recovery lost or repeated its Probe: %+v calls=%d err=%v", receipt, p.Requests.Load(), err)
	}
}
