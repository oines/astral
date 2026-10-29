package channels

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral-core/store/sqlite"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/harness"
	"github.com/oines/astral/internal/messages"
	"github.com/oines/astral/internal/modelbridge"
	"github.com/oines/astral/internal/testkit"
)

// These tests hold actual provider requests at explicit boundaries. A receipt
// being applied to history is deliberately not used as proof that the model
// received a newly delivered observation.
func activeDeliveryGate(t *testing.T) (chan struct{}, func()) {
	t.Helper()
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	return gate, release
}

func activeDeliveryWait(t *testing.T, ctx context.Context, signal <-chan struct{}, phase string) {
	t.Helper()
	select {
	case <-signal:
	case <-ctx.Done():
		t.Fatalf("waiting for %s: %v", phase, ctx.Err())
	}
}

func activeDeliveryText(request map[string]any) string {
	var text strings.Builder
	for _, raw := range request["messages"].([]any) {
		message := raw.(map[string]any)
		if message["role"] != "system" {
			if content, ok := message["content"].(string); ok {
				text.WriteString(content)
			} else if parts, ok := message["content"].([]any); ok {
				for _, rawPart := range parts {
					part, _ := rawPart.(map[string]any)
					if part["type"] == "text" {
						value, _ := part["text"].(string)
						text.WriteString(value)
					}
				}
			}
			text.WriteByte('\n')
		}
	}
	return text.String()
}

func activeDeliveryChat(t *testing.T, p *testkit.Platform, s *Service, proactive, private bool) domain.ConversationBinding {
	t.Helper()
	bot := newBot(t)
	connection := bot.connection(t, s, p.Agent.ID)
	chatID := "-100"
	if private {
		chatID = "7"
	}
	binding := createBinding(t, p, s, connection, chatID, "")
	var conversation domain.Conversation
	if err := p.Store.Get(p.Context, "conversations", binding.ConversationID, &conversation); err != nil {
		t.Fatal(err)
	}
	// The shared Telegram getChat fixture returns groups. Set the private chat
	// identity explicitly; this test exercises the common inbox, not getChat.
	if private {
		conversation.Kind = "private"
		if err := p.Store.Save(p.Context, "conversations", &conversation.Meta, &conversation); err != nil {
			t.Fatal(err)
		}
	}
	if proactive {
		if _, err := s.SetProactive(p.Context, conversation.ID, conversation.Revision, true); err != nil {
			t.Fatal(err)
		}
	}
	return binding
}

func activeDeliveryArchive(t *testing.T, p *testkit.Platform, b domain.ConversationBinding, id, trigger string) (domain.Observation, int64) {
	t.Helper()
	var conversation domain.Conversation
	if err := p.Store.Get(p.Context, "conversations", b.ConversationID, &conversation); err != nil {
		t.Fatal(err)
	}
	event := domain.Observation{ID: id, ExternalEventID: id, SchemaVersion: 1, Kind: "message.created", ConnectionID: conversation.ConnectionID, ConversationID: b.ConversationID,
		MessageID: id, Text: "unique body for " + id, Trigger: trigger, Sender: domain.Sender{ID: "7", Name: "Ada"},
		SentAt: time.Now().Add(-time.Minute), ReceivedAt: time.Now().Add(-time.Minute)}
	if trigger == "poke" {
		event.Kind, event.MessageID = "notice.poke", ""
	}
	if _, err := p.Store.Archive(p.Context, event); err != nil {
		t.Fatal(err)
	}
	through, err := p.Store.ObservationSequence(p.Context, event.ID)
	if err != nil {
		t.Fatal(err)
	}
	return event, through
}

func activeDeliveryRouted(t *testing.T, p *testkit.Platform, id string) {
	t.Helper()
	testkit.Await(t, p.Context, func() bool {
		delivery, err := p.Store.Delivery(p.Context, id)
		return err == nil && delivery.State == "routed"
	})
}

func activeDeliveryReopen(t *testing.T, p *testkit.Platform) {
	t.Helper()
	models := p.Harness.Models
	if err := p.Harness.Close(p.Context); err != nil {
		t.Fatal(err)
	}
	if err := p.CoreStore.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	p.CoreStore, err = sqlite.Open(filepath.Join(p.Directory, "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	p.Harness = harness.New(p.Store, p.CoreStore, models, p.Hub)
}

func activeDeliveryAcceptWakeup(t *testing.T, p *testkit.Platform, binding domain.ConversationBinding, event domain.Observation, through int64) string {
	t.Helper()
	var conversation domain.Conversation
	if err := p.Store.Get(p.Context, "conversations", binding.ConversationID, &conversation); err != nil {
		t.Fatal(err)
	}
	var connection domain.Connection
	if err := p.Store.Get(p.Context, "connections", conversation.ConnectionID, &connection); err != nil {
		t.Fatal(err)
	}
	inputID := event.ID
	if event.Trigger != "" {
		inputID += ":trigger"
	}
	coreSession, err := p.Harness.Core(p.Context, binding.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coreSession.InjectContext(p.Context, core.Input{ID: inputID, Contexts: []core.Message{messages.Observation(event, connection, conversation, nil, "")}}); err != nil {
		t.Fatal(err)
	}
	wakeupID := inputID + ":wakeup"
	wakeup := messages.Trigger(event)
	wakeup.Metadata = encoded(messages.Metadata{Source: "im.trigger", DeliveryID: wakeupID, EventID: event.ID, ConversationID: binding.ConversationID, Sender: event.Sender})
	if event.Trigger == "" {
		wakeupID = probeInputID(binding.ID, through)
		wakeup = core.User("<im_wakeup conversation-id=\"" + messages.Escape(binding.ConversationID) + "\" event-ids=\"" + messages.Escape(event.ID) + "\"/>\nConsider the referenced chat observations in your context.\n" + messages.IMReplyReminder)
		wakeup.Metadata = encoded(messages.Metadata{Source: "im.wakeup", DeliveryID: wakeupID, ConversationID: binding.ConversationID})
	}
	if _, err := coreSession.Steer(p.Context, core.Input{ID: wakeupID, Message: wakeup}); err != nil {
		t.Fatalf("failed to establish the accepted-but-not-started crash boundary: %v", err)
	}
	return wakeupID
}

func activeDeliveryAssertContextOnly(t *testing.T, request map[string]any, event domain.Observation) {
	t.Helper()
	text := activeDeliveryText(request)
	if strings.Count(text, event.Text) != 1 {
		t.Fatalf("new observation was omitted or duplicated in the actual model request: %s", text)
	}
	if strings.Contains(text, "<im_trigger") || strings.Contains(text, "<im_wakeup") {
		t.Fatalf("active delivery added an independent IM driver: %s", text)
	}
	if event.Trigger != "" && !strings.Contains(text, `trigger="`+event.Trigger+`"`) {
		t.Fatalf("context-only delivery lost its direct trigger reason %q: %s", event.Trigger, text)
	}
}

func TestWorkingAgentConsumesNewIMAtNextRequestWithoutProbeOrSteer(t *testing.T) {
	for _, scenario := range []struct {
		name, trigger string
		proactive     bool
		private       bool
	}{
		{name: "ordinary proactive group", proactive: true},
		{name: "ordinary default-off group"},
		{name: "group mention", trigger: "mention", proactive: true},
		{name: "group reply", trigger: "reply", proactive: true},
		{name: "group poke", trigger: "poke", proactive: true},
		{name: "private", trigger: "private", private: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			firstEntered, secondEntered := make(chan struct{}), make(chan struct{})
			firstGate, releaseFirst := activeDeliveryGate(t)
			secondGate, releaseSecond := activeDeliveryGate(t)
			requests := make(chan map[string]any, 4)
			var mains, probes atomic.Int64
			p := testkit.New(t, 0, func(ctx context.Context, _ int, request map[string]any) any {
				if strings.Contains(string(encoded(request["tools"])), `"name":"decide"`) {
					probes.Add(1)
					return testkit.Tool("decide", map[string]bool{"participate": true})
				}
				requests <- request
				switch mains.Add(1) {
				case 1:
					close(firstEntered)
					select {
					case <-firstGate:
					case <-ctx.Done():
						return nil
					}
					// No Computer exists in this fixture. The rejected command
					// supplies a deterministic tool boundary without any shell.
					return testkit.Tool("exec_command", map[string]any{"cmd": "fixture must never execute", "login": false})
				case 2:
					close(secondEntered)
					select {
					case <-secondGate:
					case <-ctx.Done():
						return nil
					}
				}
				return testkit.Text("Finished without an IM send.")
			})
			s := New(p.Store, p.Harness)
			s.probeQuiet, s.probeMaxWait = time.Millisecond, 2*time.Millisecond
			binding := activeDeliveryChat(t, p, s, scenario.proactive, scenario.private)
			startChannels(t, s, p.Context)
			if _, err := p.Harness.Submit(p.Context, binding.SessionID, harness.InputRequest{ID: "start-work", Text: "Continue the existing work."}); err != nil {
				t.Fatal(err)
			}
			activeDeliveryWait(t, p.Context, firstEntered, "first main request")
			event, through := activeDeliveryArchive(t, p, binding, "arrived-during-work", scenario.trigger)
			activeDeliveryRouted(t, p, event.ID)
			if window, err := s.pendingProbe(p.Context, binding, through, time.Now().Add(time.Minute)); err != nil || window.Through != 0 {
				t.Fatalf("working Agent left messages eligible for Probe: %+v %v", window, err)
			}
			releaseFirst()
			activeDeliveryWait(t, p.Context, secondEntered, "next main request after tool result")
			first, second := <-requests, <-requests
			if strings.Contains(activeDeliveryText(first), event.Text) {
				t.Fatal("a frozen earlier request changed after the observation arrived")
			}
			activeDeliveryAssertContextOnly(t, second, event)
			if window, err := s.pendingProbe(p.Context, binding, through, time.Now().Add(time.Minute)); err != nil || window.Through != 0 {
				t.Fatalf("already consumed observation remained Probe eligible: %+v %v", window, err)
			}
			releaseSecond()
			p.Idle(t, binding.SessionID)
			// Replay the same archived event after its working run has ended.
			// It must not be reinterpreted as a fresh idle direct trigger.
			if err := s.deliver(p.Context, binding, event); err != nil {
				t.Fatal(err)
			}
			if err := s.finishProbe(p.Context, binding, probeResult{through: through, participate: true}); err != nil {
				t.Fatal(err)
			}
			p.Idle(t, binding.SessionID)
			if mains.Load() != 2 || probes.Load() != 0 {
				t.Fatalf("context-only delivery or replay caused extra work: main=%d Probe=%d", mains.Load(), probes.Load())
			}
		})
	}
}

func TestWorkingAgentFinalResponseHandsUnseenIMBackToIdleRules(t *testing.T) {
	for _, scenario := range []struct {
		name, trigger string
		proactive     bool
		private       bool
		wantMain      int64
		wantProbes    int64
	}{
		{name: "group mention", trigger: "mention", proactive: true, wantMain: 2},
		{name: "private", trigger: "private", private: true, wantMain: 2},
		{name: "ordinary proactive group", proactive: true, wantMain: 2, wantProbes: 1},
		{name: "ordinary default-off group", wantMain: 1},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			entered := make(chan struct{})
			gate, release := activeDeliveryGate(t)
			requests := make(chan map[string]any, 4)
			var mains, probes atomic.Int64
			p := testkit.New(t, 0, func(ctx context.Context, _ int, request map[string]any) any {
				if strings.Contains(string(encoded(request["tools"])), `"name":"decide"`) {
					probes.Add(1)
					return testkit.Tool("decide", map[string]bool{"participate": true})
				}
				requests <- request
				if mains.Add(1) == 1 {
					close(entered)
					select {
					case <-gate:
					case <-ctx.Done():
						return nil
					}
				}
				return testkit.Text("Final without another model request in this run.")
			})
			s := New(p.Store, p.Harness)
			s.probeQuiet, s.probeMaxWait = time.Millisecond, 2*time.Millisecond
			binding := activeDeliveryChat(t, p, s, scenario.proactive, scenario.private)
			startChannels(t, s, p.Context)
			if _, err := p.Harness.Submit(p.Context, binding.SessionID, harness.InputRequest{ID: "finishing-work", Text: "Finish the current task."}); err != nil {
				t.Fatal(err)
			}
			activeDeliveryWait(t, p.Context, entered, "final main request")
			event, through := activeDeliveryArchive(t, p, binding, "arrived-after-final-freeze", scenario.trigger)
			activeDeliveryRouted(t, p, event.ID)
			if strings.Contains(activeDeliveryText(<-requests), event.Text) {
				t.Fatal("the in-flight final request unexpectedly received a later observation")
			}
			release()
			testkit.Await(t, p.Context, func() bool { return mains.Load() >= scenario.wantMain && probes.Load() >= scenario.wantProbes })
			p.Idle(t, binding.SessionID)
			if scenario.wantMain == 2 {
				text := activeDeliveryText(<-requests)
				if strings.Count(text, event.Text) != 1 {
					t.Fatalf("idle handoff lost or duplicated the observation: %s", text)
				}
				if scenario.trigger != "" && !strings.Contains(text, "<im_trigger") {
					t.Fatalf("unseen direct message was not awakened after normal completion: %s", text)
				}
				if scenario.trigger == "" && !strings.Contains(text, "<im_wakeup") {
					t.Fatalf("unseen ordinary group message bypassed the idle Probe wakeup: %s", text)
				}
			}
			if err := s.deliver(p.Context, binding, event); err != nil {
				t.Fatal(err)
			}
			if window, err := s.pendingProbe(p.Context, binding, through, time.Now().Add(time.Minute)); err != nil || window.Through != 0 {
				t.Fatalf("settled handoff remained eligible: %+v %v", window, err)
			}
			p.Idle(t, binding.SessionID)
			if mains.Load() != scenario.wantMain || probes.Load() != scenario.wantProbes {
				t.Fatalf("wrong idle handoff count: main=%d/%d Probe=%d/%d", mains.Load(), scenario.wantMain, probes.Load(), scenario.wantProbes)
			}
		})
	}
}

func TestIdleProbeResultCannotWakeObservationAlreadyConsumedByMain(t *testing.T) {
	probeEntered, mainEntered := make(chan struct{}), make(chan struct{})
	probeGate, releaseProbe := activeDeliveryGate(t)
	mainGate, releaseMain := activeDeliveryGate(t)
	mainRequests := make(chan map[string]any, 4)
	var mains, probes atomic.Int64
	p := testkit.New(t, 0, func(ctx context.Context, _ int, request map[string]any) any {
		if strings.Contains(string(encoded(request["tools"])), `"name":"decide"`) {
			if probes.Add(1) == 1 {
				close(probeEntered)
			}
			select {
			case <-probeGate:
			case <-ctx.Done():
				return nil
			}
			return testkit.Tool("decide", map[string]bool{"participate": true})
		}
		mainRequests <- request
		if mains.Add(1) == 1 {
			close(mainEntered)
			select {
			case <-mainGate:
			case <-ctx.Done():
				return nil
			}
		}
		return testkit.Text("The current run has handled its received context.")
	})
	s := New(p.Store, p.Harness)
	s.probeQuiet, s.probeMaxWait = time.Millisecond, 2*time.Millisecond
	binding := activeDeliveryChat(t, p, s, true, false)
	startChannels(t, s, p.Context)
	event, through := activeDeliveryArchive(t, p, binding, "idle-probe-then-main-takeover", "")
	activeDeliveryWait(t, p.Context, probeEntered, "idle Probe request")
	if _, err := p.Harness.Submit(p.Context, binding.SessionID, harness.InputRequest{ID: "owner-takes-over", Text: "Continue working with the current chat context."}); err != nil {
		t.Fatal(err)
	}
	activeDeliveryWait(t, p.Context, mainEntered, "main run taking over the observation")
	activeDeliveryAssertContextOnly(t, <-mainRequests, event)
	releaseProbe()
	testkit.Await(t, p.Context, func() bool {
		position, err := s.probeCursor(p.Context, binding)
		return err == nil && position.Sequence >= through
	})
	if _, err := p.Harness.Receipt(p.Context, binding.SessionID, probeInputID(binding.ID, through)); err == nil {
		t.Fatal("a stale positive Probe created a driver after main-model consumption")
	} else if !domain.IsMissing(err) {
		t.Fatal(err)
	}
	releaseMain()
	p.Idle(t, binding.SessionID)
	if mains.Load() != 1 || probes.Load() != 1 {
		t.Fatalf("late positive Probe caused another run: main=%d Probe=%d", mains.Load(), probes.Load())
	}
}

func TestBusyProbeCompletionDoesNotCoverAnUnseenObservation(t *testing.T) {
	entered := make(chan struct{})
	gate, release := activeDeliveryGate(t)
	requests := make(chan map[string]any, 4)
	var mains atomic.Int64
	p := testkit.New(t, 0, func(ctx context.Context, _ int, request map[string]any) any {
		requests <- request
		if mains.Add(1) == 1 {
			close(entered)
			select {
			case <-gate:
			case <-ctx.Done():
				return nil
			}
		}
		return testkit.Text("Finished.")
	})
	s := New(p.Store, p.Harness)
	binding := activeDeliveryChat(t, p, s, true, false)
	// The result spans an older observation this run really receives and a
	// newer one that misses its frozen final request. The covered prefix must
	// not cause the still-unseen tail to be covered together with it.
	earlier, _ := activeDeliveryArchive(t, p, binding, "covered-prefix-before-main", "")
	if err := s.deliver(p.Context, binding, earlier); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Harness.Submit(p.Context, binding.SessionID, harness.InputRequest{ID: "finishing-before-observation", Text: "Finish now."}); err != nil {
		t.Fatal(err)
	}
	activeDeliveryWait(t, p.Context, entered, "frozen main request")
	firstRequest := activeDeliveryText(<-requests)
	if strings.Count(firstRequest, earlier.Text) != 1 {
		t.Fatalf("the earlier batch prefix was not in the actual main request: %s", firstRequest)
	}
	event, through := activeDeliveryArchive(t, p, binding, "probe-result-while-main-has-not-seen", "")
	if strings.Contains(firstRequest, event.Text) {
		t.Fatal("the frozen earlier request already contained the later observation")
	}
	if err := s.deliver(p.Context, binding, event); err != nil {
		t.Fatal(err)
	}
	result := probeResult{through: through, participate: true}
	if err := s.finishProbe(p.Context, binding, result); err != nil {
		t.Fatal(err)
	}
	position, err := s.probeCursor(p.Context, binding)
	if err != nil || position.Sequence >= through {
		t.Fatalf("busy completion permanently covered an observation the model has not seen: %+v %v", position, err)
	}
	if _, err := p.Harness.Receipt(p.Context, binding.SessionID, probeInputID(binding.ID, through)); err == nil {
		t.Fatal("busy completion queued an independent steer")
	} else if !domain.IsMissing(err) {
		t.Fatal(err)
	}
	release()
	p.Idle(t, binding.SessionID)
	if err := p.Harness.ResumeContext(p.Context, binding.SessionID); err != nil {
		t.Fatal(err)
	}
	// A real Probe records its exact input set. A partially consumed decision
	// must be discarded rather than reused to drive the leftover message.
	mixed := probeResult{through: through, inputs: []string{earlier.ID, event.ID}, participate: true}
	if err := s.finishProbe(p.Context, binding, mixed); err != nil {
		t.Fatal(err)
	}
	if mains.Load() != 1 {
		t.Fatal("a stale mixed-batch decision woke the Agent for an already consumed prefix")
	}
	window, err := s.pendingProbe(p.Context, binding, through, time.Now().Add(time.Minute))
	if err != nil || window.Through != through {
		t.Fatalf("unseen observation was lost instead of returning to idle handling: %+v %v", window, err)
	}
	if len(window.Inputs) != 1 || window.Inputs[0] != event.ID {
		t.Fatalf("the next idle decision included already owned inputs: %+v", window.Inputs)
	}
	fresh := probeResult{through: window.Through, inputs: window.Inputs, participate: true}
	if err := s.finishProbe(p.Context, binding, fresh); err != nil {
		t.Fatal(err)
	}
	testkit.Await(t, p.Context, func() bool { return mains.Load() >= 2 })
	p.Idle(t, binding.SessionID)
	if err := s.finishProbe(p.Context, binding, fresh); err != nil {
		t.Fatal(err)
	}
	if mains.Load() != 2 {
		t.Fatalf("retrying the same accepted wakeup duplicated activation: %d", mains.Load())
	}
	if text := activeDeliveryText(<-requests); strings.Count(text, event.Text) != 1 {
		t.Fatalf("the final actual main request omitted or duplicated the observation: %s", text)
	}
	coreSession, err := p.Harness.Core(p.Context, binding.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := coreSession.LookupInput(p.Context, event.ID)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.State != core.InputApplied {
		t.Fatalf("the claimed observation lost its ordinary history receipt: %s", receipt.State)
	}
}

func TestWorkingIMDeliveryReplayAfterHarnessReopenDoesNotWakeAgain(t *testing.T) {
	entered, next := make(chan struct{}), make(chan struct{})
	gate, release := activeDeliveryGate(t)
	var mains atomic.Int64
	p := testkit.New(t, 0, func(ctx context.Context, _ int, _ map[string]any) any {
		switch mains.Add(1) {
		case 1:
			close(entered)
			select {
			case <-gate:
			case <-ctx.Done():
				return nil
			}
			return testkit.Tool("exec_command", map[string]any{"cmd": "fixture must never execute", "login": false})
		case 2:
			close(next)
		}
		return testkit.Text("Finished.")
	})
	s := New(p.Store, p.Harness)
	binding := activeDeliveryChat(t, p, s, true, false)
	if _, err := p.Harness.Submit(p.Context, binding.SessionID, harness.InputRequest{ID: "work-before-reopen", Text: "Work across a tool boundary."}); err != nil {
		t.Fatal(err)
	}
	activeDeliveryWait(t, p.Context, entered, "initial request before reopen")
	event, through := activeDeliveryArchive(t, p, binding, "already-seen-before-reopen", "mention")
	if err := s.deliver(p.Context, binding, event); err != nil {
		t.Fatal(err)
	}
	release()
	activeDeliveryWait(t, p.Context, next, "request consuming the new observation")
	p.Idle(t, binding.SessionID)
	// A fresh Harness runtime reopens the authoritative Core database. The
	// channel cursor is intentionally absent, modelling delivery replay after a
	// crash between Core admission and the Host cursor commit.
	activeDeliveryReopen(t, p)
	restarted := New(p.Store, p.Harness)
	if err := restarted.deliver(p.Context, binding, event); err != nil {
		t.Fatal(err)
	}
	if err := restarted.finishProbe(p.Context, binding, probeResult{through: through, participate: true}); err != nil {
		t.Fatal(err)
	}
	p.Idle(t, binding.SessionID)
	if mains.Load() != 2 {
		t.Fatalf("reopened delivery interpreted old context as a fresh trigger: %d", mains.Load())
	}
	coreSession, err := p.Harness.Core(p.Context, binding.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	page, err := coreSession.History().Scan(p.Context, core.Query{Kind: core.EntryContext, Limit: 1000, MaxBytes: 8 << 20})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range page.Entries {
		if entry.Message != nil && strings.Contains(fmt.Sprint(entry.Message.Content), event.Text) {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("reopened inbox duplicated its observation history: %d", count)
	}
}

func TestStoppedOrFailedWorkDoesNotAutomaticallyWakeItsUnseenIM(t *testing.T) {
	for _, terminal := range []string{"cancelled", "failed"} {
		for _, trigger := range []string{"", "mention"} {
			t.Run(terminal+"/trigger="+trigger, func(t *testing.T) {
				entered := make(chan struct{})
				gate, release := activeDeliveryGate(t)
				requests := make(chan map[string]any, 4)
				var mains atomic.Int64
				p := testkit.New(t, 0, func(ctx context.Context, _ int, request map[string]any) any {
					requests <- request
					if mains.Add(1) == 1 {
						close(entered)
						select {
						case <-gate:
						case <-ctx.Done():
							return nil
						}
						return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
							http.Error(w, "fixture permanent model failure", http.StatusBadRequest)
						})
					}
					return testkit.Text("An explicit continuation may read the retained observation.")
				})
				s := New(p.Store, p.Harness)
				binding := activeDeliveryChat(t, p, s, true, false)
				if _, err := p.Harness.Submit(p.Context, binding.SessionID, harness.InputRequest{ID: "terminal-work", Text: "Work until the owner stops or the model fails."}); err != nil {
					t.Fatal(err)
				}
				activeDeliveryWait(t, p.Context, entered, "in-flight main request")
				event, through := activeDeliveryArchive(t, p, binding, "unseen-before-terminal-work", trigger)
				if strings.Contains(activeDeliveryText(<-requests), event.Text) {
					t.Fatal("the frozen earlier request already contained the later observation")
				}
				if err := s.deliver(p.Context, binding, event); err != nil {
					t.Fatal(err)
				}
				inputID := event.ID
				if trigger != "" {
					inputID += ":trigger"
				}
				if terminal == "cancelled" {
					if err := p.Harness.Stop(p.Context, binding.SessionID); err != nil {
						t.Fatal(err)
					}
				} else {
					release()
				}
				testkit.Await(t, p.Context, func() bool {
					view, err := p.Harness.View(p.Context, binding.SessionID)
					return err == nil && view.RunID == "" && view.LastRun != nil && view.LastRun.Status == terminal
				})
				receipt, err := p.Harness.Receipt(p.Context, binding.SessionID, inputID)
				if err != nil || receipt.InputID != inputID {
					t.Fatalf("terminal fixture did not retain its accepted observation: %+v %v", receipt, err)
				}
				// Recovery must preserve the owner's stop and terminal failures;
				// opening the inbox again is not an explicit request to retry work.
				activeDeliveryReopen(t, p)
				s = New(p.Store, p.Harness)
				if err := p.Harness.ResumeContext(p.Context, binding.SessionID); err != nil {
					t.Fatal(err)
				}
				if err := s.handoffDirectObservations(p.Context, binding, through); err != nil {
					t.Fatal(err)
				}
				if window, err := s.pendingProbe(p.Context, binding, through, time.Now().Add(time.Minute)); err != nil || window.Through != 0 {
					t.Fatalf("%s work automatically scheduled an IM retry: %+v %v", terminal, window, err)
				}
				if trigger == "" {
					if err := s.finishProbe(p.Context, binding, probeResult{through: through, participate: true}); err != nil {
						t.Fatal(err)
					}
				}
				if mains.Load() != 1 {
					t.Fatalf("%s work was automatically restarted by an observation: %d", terminal, mains.Load())
				}
			})
		}
	}
}

func TestAcceptedIMWakeupRecoversAfterCrashBeforeContinueAndCursor(t *testing.T) {
	for _, trigger := range []string{"mention", ""} {
		name := "direct"
		if trigger == "" {
			name = "Probe"
		}
		t.Run(name, func(t *testing.T) {
			var mains atomic.Int64
			requests := make(chan map[string]any, 4)
			p := testkit.New(t, 0, func(_ context.Context, _ int, request map[string]any) any {
				if strings.Contains(string(encoded(request["tools"])), `"name":"decide"`) {
					t.Error("recovery repeated a Probe whose wakeup was already accepted")
					return testkit.Tool("decide", map[string]bool{"participate": false})
				}
				mains.Add(1)
				requests <- request
				return testkit.Text("Recovered the one accepted wakeup.")
			})
			s := New(p.Store, p.Harness)
			binding := activeDeliveryChat(t, p, s, trigger == "", false)
			event, through := activeDeliveryArchive(t, p, binding, "accepted-before-crash", trigger)
			activeDeliveryAcceptWakeup(t, p, binding, event, through)
			if mains.Load() != 0 {
				t.Fatal("accepting the conditional input started execution before Continue")
			}
			activeDeliveryReopen(t, p)
			restarted := New(p.Store, p.Harness)
			startChannels(t, restarted, p.Context)
			deadline, cancel := context.WithTimeout(p.Context, 8*time.Second)
			defer cancel()
			testkit.Await(t, deadline, func() bool { return mains.Load() >= 1 })
			p.Idle(t, binding.SessionID)
			if text := activeDeliveryText(<-requests); strings.Count(text, event.Text) != 1 {
				t.Fatalf("recovered input lost or duplicated its original observation: %s", text)
			}
			if err := restarted.handoffDirectObservations(p.Context, binding, through); err != nil {
				t.Fatal(err)
			}
			if err := restarted.finishProbe(p.Context, binding, probeResult{through: through, participate: true}); err != nil {
				t.Fatal(err)
			}
			if mains.Load() != 1 {
				t.Fatalf("recovery duplicated an already accepted wakeup: %d", mains.Load())
			}
		})
	}
}

func TestAcceptedIMWakeupStoppedBeforeRequestConsumptionDoesNotResume(t *testing.T) {
	for _, terminal := range []string{"cancelled", "failed"} {
		for _, trigger := range []string{"mention", ""} {
			name := "direct"
			if trigger == "" {
				name = "Probe"
			}
			t.Run(terminal+"/"+name, func(t *testing.T) {
				p := testkit.New(t, 0, func(_ context.Context, _ int, _ map[string]any) any {
					t.Error("stopped pre-consumption wakeup unexpectedly reached the model")
					return testkit.Text("This request must not run automatically.")
				})
				s := New(p.Store, p.Harness)
				binding := activeDeliveryChat(t, p, s, trigger == "", false)
				event, through := activeDeliveryArchive(t, p, binding, "accepted-before-stopped-preparation", trigger)
				wakeupID := activeDeliveryAcceptWakeup(t, p, binding, event, through)
				session, err := p.Harness.Core(p.Context, binding.SessionID)
				if err != nil {
					t.Fatal(err)
				}
				configuration := session.Configuration()
				models := p.Harness.Models
				if err := p.Harness.Close(p.Context); err != nil {
					t.Fatal(err)
				}
				var tools []core.Tool
				for _, definition := range configuration.Tools {
					tools = append(tools, core.ToolFunc{Spec: definition, Run: func(context.Context, core.Invocation, func(core.ToolProgress)) (core.ToolExecution, error) {
						return core.ToolExecution{}, errors.New("fixture must stop before executing tools")
					}})
				}
				entered := make(chan struct{})
				var once sync.Once
				// Hold preparation before Core commits the selected input. This
				// creates a real terminal Run after the accepted queue record,
				// while keeping that record pending and every model uncalled.
				preparing, err := core.OpenSession(p.Context, core.Config{Store: p.CoreStore, EstimateTokens: modelbridge.EstimateTokens,
					ResolveModel: models.ResolveSelection, RestoreModel: models.RestoreModel, Tools: tools, Compaction: core.CompactionPolicy{Disabled: true},
					CollectContext: func(ctx context.Context, _ core.ContextBoundary) ([]core.Message, error) {
						once.Do(func() { close(entered) })
						if terminal == "failed" {
							return nil, errors.New("fixture preparation failure before input consumption")
						}
						<-ctx.Done()
						return nil, ctx.Err()
					}}, binding.SessionID)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = preparing.Close(context.Background()) })
				run, err := preparing.Continue(p.Context)
				if err != nil {
					t.Fatal(err)
				}
				activeDeliveryWait(t, p.Context, entered, "pre-consumption preparation")
				if terminal == "cancelled" {
					run.Cancel()
				}
				if _, err := run.Wait(p.Context); err == nil {
					t.Fatal("the fixture preparation unexpectedly completed")
				}
				if state := preparing.State(); state.LastRun == nil || state.LastRun.Status != terminal {
					t.Fatalf("preparation did not persist its terminal state: %+v", state.LastRun)
				}
				if receipt, err := preparing.LookupInput(p.Context, wakeupID); err != nil || receipt.State != core.InputPending {
					t.Fatalf("the stopped fixture consumed its accepted wakeup: %+v %v", receipt, err)
				}
				if err := preparing.Close(p.Context); err != nil {
					t.Fatal(err)
				}
				activeDeliveryReopen(t, p)
				restarted := New(p.Store, p.Harness)
				if err := restarted.handoffDirectObservations(p.Context, binding, through); err != nil {
					t.Fatal(err)
				}
				if err := restarted.resumeAcceptedProbe(p.Context, binding); err != nil {
					t.Fatal(err)
				}
				view, err := p.Harness.View(p.Context, binding.SessionID)
				if err != nil || view.RunID != "" || view.LastRun == nil || view.LastRun.Status != terminal || p.Requests.Load() != 0 {
					t.Fatalf("recovery resumed an accepted wakeup stopped before consumption: %+v calls=%d err=%v", view, p.Requests.Load(), err)
				}
			})
		}
	}
}

func TestNewDirectIMAfterStopCanResumePreservedOwnerQueue(t *testing.T) {
	for _, trigger := range []string{"mention", "private"} {
		t.Run(trigger, func(t *testing.T) {
			entered := make(chan struct{})
			requests := make(chan map[string]any, 4)
			var mains atomic.Int64
			p := testkit.New(t, 0, func(ctx context.Context, _ int, request map[string]any) any {
				requests <- request
				if mains.Add(1) == 1 {
					close(entered)
					<-ctx.Done()
					return nil
				}
				return testkit.Text("Handled the explicit new IM request and preserved owner input.")
			})
			s := New(p.Store, p.Harness)
			binding := activeDeliveryChat(t, p, s, false, trigger == "private")
			if _, err := p.Harness.Submit(p.Context, binding.SessionID, harness.InputRequest{ID: "work-before-stop", Text: "Work until the owner stops this run."}); err != nil {
				t.Fatal(err)
			}
			activeDeliveryWait(t, p.Context, entered, "in-flight request before owner input")
			const queuedText = "Unique owner request queued before Stop."
			if _, err := p.Harness.Submit(p.Context, binding.SessionID, harness.InputRequest{ID: "preserved-owner-queue", Text: queuedText}); err != nil {
				t.Fatal(err)
			}
			if err := p.Harness.Stop(p.Context, binding.SessionID); err != nil {
				t.Fatal(err)
			}
			p.Idle(t, binding.SessionID)
			view, err := p.Harness.View(p.Context, binding.SessionID)
			if err != nil || view.LastRun == nil || view.LastRun.Status != "cancelled" || len(view.Pending) != 1 || view.Pending[0].ID != "preserved-owner-queue" {
				t.Fatalf("Stop did not preserve the queued owner input: %+v %v", view, err)
			}
			<-requests
			event, through := activeDeliveryArchive(t, p, binding, "explicit-new-message-after-stop", trigger)
			if err := s.deliver(p.Context, binding, event); err != nil {
				t.Fatal(err)
			}
			receipt, err := p.Harness.Receipt(p.Context, binding.SessionID, event.ID+":trigger")
			if err != nil || receipt.InputID != event.ID+":trigger" {
				t.Fatalf("the new post-stop message was not accepted: %+v %v", receipt, err)
			}
			if err := s.handoffDirectObservations(p.Context, binding, through); err != nil {
				t.Fatal(err)
			}
			deadline, cancel := context.WithTimeout(p.Context, 5*time.Second)
			defer cancel()
			testkit.Await(t, deadline, func() bool { return mains.Load() >= 2 })
			p.Idle(t, binding.SessionID)
			text := activeDeliveryText(<-requests)
			if strings.Count(text, event.Text) != 1 || strings.Count(text, queuedText) != 1 {
				t.Fatalf("the new activation lost or duplicated its observation or preserved owner input: %s", text)
			}
			if mains.Load() != 2 {
				t.Fatalf("one explicit post-stop message started multiple requests: %d", mains.Load())
			}
		})
	}
}

func TestExistingDirectDriverWithLaggingHostCursorIsNotDrivenAgain(t *testing.T) {
	p := testkit.New(t, 0, func(_ context.Context, _ int, _ map[string]any) any {
		t.Error("reconciling an already accepted driver unexpectedly called the model")
		return testkit.Text("This input must remain queued until explicitly continued.")
	})
	s := New(p.Store, p.Harness)
	binding := activeDeliveryChat(t, p, s, false, false)
	event, through := activeDeliveryArchive(t, p, binding, "accepted-driver-before-cursor-commit", "mention")
	var conversation domain.Conversation
	if err := p.Store.Get(p.Context, "conversations", binding.ConversationID, &conversation); err != nil {
		t.Fatal(err)
	}
	var connection domain.Connection
	if err := p.Store.Get(p.Context, "connections", conversation.ConnectionID, &connection); err != nil {
		t.Fatal(err)
	}
	session, err := p.Harness.Core(p.Context, binding.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	inputID := event.ID + ":trigger"
	if _, err := session.Steer(p.Context, core.Input{ID: inputID, Contexts: []core.Message{messages.Observation(event, connection, conversation, nil, "")}, Message: messages.Trigger(event)}); err != nil {
		t.Fatal(err)
	}
	// Core committed the driving input; the Host cursor commit has not happened.
	// Reconciliation must preserve its queue ownership, rather than treating it
	// as passive context needing another conditional wakeup.
	if err := s.deliver(p.Context, binding, event); err != nil {
		t.Fatal(err)
	}
	if err := s.handoffDirectObservations(p.Context, binding, through); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	position := s.handoffs[binding.ID]
	s.mu.Unlock()
	if position != through {
		t.Fatalf("the already accepted direct driver blocked Host handoff recovery: %d want %d", position, through)
	}
	view, err := p.Harness.View(p.Context, binding.SessionID)
	if err != nil || view.RunID != "" || len(view.Pending) != 1 || view.Pending[0].ID != inputID || view.Pending[0].Kind != string(core.InputSteer) || p.Requests.Load() != 0 {
		t.Fatalf("reconciliation changed or activated the accepted driver: %+v calls=%d err=%v", view, p.Requests.Load(), err)
	}
	if _, err := session.LookupInput(p.Context, inputID+":wakeup"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("reconciliation invented a second driver: %v", err)
	}
}

func TestAppliedDirectWakeupReplayDoesNotResumeStoppedRun(t *testing.T) {
	entered := make(chan struct{})
	requests := make(chan map[string]any, 4)
	var mains atomic.Int64
	p := testkit.New(t, 0, func(ctx context.Context, _ int, request map[string]any) any {
		requests <- request
		if mains.Add(1) == 1 {
			close(entered)
			<-ctx.Done()
			return nil
		}
		return testkit.Text("A replay must not resume this stopped generation.")
	})
	s := New(p.Store, p.Harness)
	binding := activeDeliveryChat(t, p, s, false, false)
	event, through := activeDeliveryArchive(t, p, binding, "idle-direct-wakeup-before-stop", "mention")
	if err := s.deliver(p.Context, binding, event); err != nil {
		t.Fatal(err)
	}
	activeDeliveryWait(t, p.Context, entered, "idle direct activation")
	observation, err := p.Harness.Receipt(p.Context, binding.SessionID, event.ID+":trigger")
	if err != nil || observation.InputID != event.ID+":trigger" {
		t.Fatalf("idle observation was not accepted: %+v %v", observation, err)
	}
	if text := activeDeliveryText(<-requests); strings.Count(text, event.Text) != 1 {
		t.Fatalf("the actual main request did not include the idle observation once: %s", text)
	}
	if observation.State != string(core.InputApplied) {
		t.Fatalf("the atomic direct observation/driver was not consumed before Stop: %+v", observation)
	}
	if err := p.Harness.Stop(p.Context, binding.SessionID); err != nil {
		t.Fatal(err)
	}
	before := p.Idle(t, binding.SessionID)
	if before.LastRun == nil || before.LastRun.Status != "cancelled" || !before.CanContinue {
		t.Fatalf("the idle-started run did not stop with resumable work: %+v", before)
	}
	// No activation cursor was committed before Stop. Replaying its already
	// applied wakeup is reconciliation, not a fresh explicit request to Continue.
	if err := s.deliver(p.Context, binding, event); err != nil {
		t.Fatal(err)
	}
	if err := s.handoffDirectObservations(p.Context, binding, through); err != nil {
		t.Fatal(err)
	}
	after, err := p.Harness.View(p.Context, binding.SessionID)
	if err != nil || after.RunID != "" || after.LastRun == nil || after.LastRun.ID != before.LastRun.ID || after.LastRun.Status != "cancelled" || mains.Load() != 1 {
		t.Fatalf("replaying an applied wakeup resumed stopped work: %+v calls=%d err=%v", after, mains.Load(), err)
	}
}

func TestFreshDirectIMResumesFrozenGenerationAfterReopen(t *testing.T) {
	for _, trigger := range []string{"mention", "private"} {
		t.Run(trigger, func(t *testing.T) {
			requests := make(chan map[string]any, 4)
			var mains atomic.Int64
			p := testkit.New(t, 0, func(_ context.Context, _ int, request map[string]any) any {
				if strings.Contains(string(encoded(request["tools"])), `"name":"decide"`) {
					t.Error("a fresh direct message used Probe during frozen-work recovery")
				}
				mains.Add(1)
				requests <- request
				return testkit.Text("Finished the frozen request or handled the fresh direct input.")
			})
			s := New(p.Store, p.Harness)
			binding := activeDeliveryChat(t, p, s, false, trigger == "private")
			resolved, err := p.Harness.Models.ResolveSelection(p.Context, core.ModelSelection{ID: p.Agent.DefaultModelID})
			if err != nil {
				t.Fatal(err)
			}
			if err := p.Harness.Close(p.Context); err != nil {
				t.Fatal(err)
			}
			state, err := p.CoreStore.LoadState(p.Context, binding.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			const oldText = "The unfinished task whose generation was frozen before the crash."
			message := core.User(oldText)
			entries := []core.Entry{
				{ID: "crash-user", Kind: core.EntryMessage, ParentID: state.Branch.HeadID, Message: &message},
				{ID: "crash-start", Kind: core.EntryRun, RunID: "crash-run", Run: &core.RunRecord{ID: "crash-run", Status: "started"}},
			}
			for i := range entries {
				entries[i].SessionID = binding.SessionID
				entries[i].Version = state.Version + 1
				entries[i].Time = time.Now().UTC()
			}
			if _, err := p.CoreStore.Commit(p.Context, binding.SessionID, core.Commit{ID: "crash-input", ExpectedVersion: state.Version, Entries: entries}); err != nil {
				t.Fatal(err)
			}
			state, err = p.CoreStore.LoadState(p.Context, binding.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			base, err := core.ReadContext(p.Context, p.CoreStore, state.RolloutID, state.Branch.HeadID, 64<<20)
			if err != nil {
				t.Fatal(err)
			}
			request := base.Request
			request.ModelID, request.ModelRevision = resolved.Model.Spec().ID, resolved.Model.Spec().Revision
			request.MaxOutputTokens, request.Options = 1024, resolved.Options
			request, err = core.PrepareRequest(p.Context, resolved.Model, request)
			if err != nil {
				t.Fatal(err)
			}
			frozen, err := core.FreezeRequest(state.RolloutID, base, request, resolved.Model.Spec())
			if err != nil {
				t.Fatal(err)
			}
			frozen.Snapshot = resolved.Snapshot
			attempt := core.ModelAttempt{ID: "crash-attempt", SessionID: binding.SessionID, RunID: "crash-run", TurnID: "crash-turn", Purpose: "generation", Number: 1, Version: state.Version + 1, StartedAt: time.Now().UTC(), Input: frozen}
			if _, err := p.CoreStore.Commit(p.Context, binding.SessionID, core.Commit{ID: "crash-frozen", ExpectedVersion: state.Version, Attempts: []core.AttemptMutation{{ID: attempt.ID, Create: &attempt}}}); err != nil {
				t.Fatal(err)
			}
			activeDeliveryReopen(t, p)
			restarted := New(p.Store, p.Harness)
			session, err := p.Harness.Core(p.Context, binding.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			before := session.Snapshot()
			if before.Running || before.Maintenance || before.State.CurrentAttemptID != attempt.ID || mains.Load() != 0 {
				t.Fatalf("fixture did not reopen an unstarted frozen generation: %+v calls=%d", before, mains.Load())
			}
			startChannels(t, restarted, p.Context)
			event, _ := activeDeliveryArchive(t, p, binding, "fresh-direct-after-frozen-crash", trigger)
			deadline, cancel := context.WithTimeout(p.Context, 8*time.Second)
			defer cancel()
			testkit.Await(t, deadline, func() bool { return mains.Load() >= 2 })
			p.Idle(t, binding.SessionID)
			first, second := activeDeliveryText(<-requests), activeDeliveryText(<-requests)
			if !strings.Contains(first, oldText) || strings.Contains(first, event.Text) {
				t.Fatalf("recovery changed the already frozen request: %s", first)
			}
			if strings.Count(second, event.Text) != 1 || mains.Load() != 2 {
				t.Fatalf("the fresh direct message was lost or duplicated after recovery: %s calls=%d", second, mains.Load())
			}
		})
	}
}

func TestDeferredDirectIMHandoffResumesExistingOwnerDriverOnly(t *testing.T) {
	requests := make(chan map[string]any, 4)
	var mains atomic.Int64
	p := testkit.New(t, 0, func(_ context.Context, _ int, request map[string]any) any {
		mains.Add(1)
		requests <- request
		return testkit.Text("Handled the pending Owner task with the newly received IM context.")
	})
	s := New(p.Store, p.Harness)
	binding := activeDeliveryChat(t, p, s, false, false)
	event, through := activeDeliveryArchive(t, p, binding, "direct-deferred-during-maintenance", "mention")
	var conversation domain.Conversation
	if err := p.Store.Get(p.Context, "conversations", binding.ConversationID, &conversation); err != nil {
		t.Fatal(err)
	}
	var connection domain.Connection
	if err := p.Store.Get(p.Context, "connections", conversation.ConnectionID, &connection); err != nil {
		t.Fatal(err)
	}
	session, err := p.Harness.Core(p.Context, binding.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	const ownerText = "Unique Owner task queued while maintenance was pending."
	if _, err := session.Steer(p.Context, core.Input{ID: "pending-owner-driver", Message: messages.Web("pending-owner-driver", ownerText, "Owner", time.Now().UTC(), nil)}); err != nil {
		t.Fatal(err)
	}
	// Establish maintenance's completed state: its IM delivery was passive,
	// and the pre-existing Owner driver has not been continued yet.
	if _, err := session.InjectContext(p.Context, core.Input{ID: event.ID + ":trigger", Contexts: []core.Message{messages.Observation(event, connection, conversation, nil, "")}}); err != nil {
		t.Fatal(err)
	}
	if p.Requests.Load() != 0 {
		t.Fatal("preparing the deferred handoff unexpectedly started a model request")
	}
	if err := s.handoffDirectObservations(p.Context, binding, through); err != nil {
		t.Fatal(err)
	}
	testkit.Await(t, p.Context, func() bool { return mains.Load() >= 1 })
	p.Idle(t, binding.SessionID)
	if text := activeDeliveryText(<-requests); strings.Count(text, event.Text) != 1 || strings.Count(text, ownerText) != 1 {
		t.Fatalf("the resumed Owner request lost or duplicated its new context: %s", text)
	}
	if mains.Load() != 1 {
		t.Fatalf("the deferred handoff created another driver after resuming Owner work: %d", mains.Load())
	}
	if _, err := session.LookupInput(p.Context, event.ID+":trigger:wakeup"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("the deferred handoff appended an unnecessary IM driver: %v", err)
	}
}
