package channels

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/testkit"
)

func TestObservationContextKeepsArchiveAndDirectTriggersAcrossToggle(t *testing.T) {
	p := testkit.New(t, 0, func(_ context.Context, _ int, request map[string]any) any {
		text := activeDeliveryText(request)
		if strings.Contains(text, "excluded group chatter") || strings.Contains(text, "excluded reaction") {
			t.Error("off-period observation reached the model")
		}
		for _, want := range []string{"ordinary before toggle", "own message while off", "direct mention while off"} {
			if !strings.Contains(text, want) {
				t.Errorf("context is missing %q", want)
			}
		}
		return testkit.Text("web reply")
	})
	s := New(p.Store, p.Harness)
	binding := activeDeliveryChat(t, p, s, true, false)
	var chat domain.Conversation
	if err := p.Store.Get(p.Context, "conversations", binding.ConversationID, &chat); err != nil {
		t.Fatal(err)
	}
	archive := func(body, trigger string, self bool, reaction *domain.ReactionEvent) {
		t.Helper()
		event := domain.Observation{SchemaVersion: 1, ID: body, ExternalEventID: body, Kind: "message.created", MessageID: body, Text: body,
			ConnectionID: chat.ConnectionID, ConversationID: chat.ID, Trigger: trigger, Self: self, Reaction: reaction,
			Sender: domain.Sender{ID: "7", Name: "Ada"}, ReceivedAt: time.Now(), SentAt: time.Now()}
		if _, err := s.archiveEvent(p.Context, event); err != nil {
			t.Fatal(err)
		}
	}
	archive("ordinary before toggle", "", false, nil)
	disabled, err := s.SetObservationContext(p.Context, chat.ID, chat.Revision, false)
	if err != nil || !disabled.ObservationContextDisabled || !disabled.ProactiveEnabled {
		t.Fatal("could not turn off group context independently", disabled, err)
	}
	if _, err := s.SetObservationContext(p.Context, chat.ID, chat.Revision, true); err == nil {
		t.Fatal("stale revision overwrote a changed setting")
	}
	// Adapter refreshes only own the platform fields, even while a group is quiet.
	chat.Title = "Renamed"
	if err := s.saveConversation(p.Context, &chat); err != nil || !chat.ObservationContextDisabled {
		t.Fatal("refresh lost chat settings", chat, err)
	}
	archive("excluded group chatter", "", false, nil)
	archive("excluded reaction", "", false, &domain.ReactionEvent{ActorType: "user"})
	archive("own message while off", "", true, nil)
	archive("direct mention while off", "mention", false, nil)
	head, err := p.Store.ObservationHead(p.Context, chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	window, err := s.pendingProbe(p.Context, binding, head, time.Now().Add(time.Hour))
	if err != nil || window.Retry || window.Through != 0 || !window.NextAt.IsZero() {
		t.Fatalf("disabled context scheduled a Probe: %+v %v", window, err)
	}
	// Re-enable before the inbox opens, as when a paused worker catches up.
	enabled, err := s.SetObservationContext(p.Context, chat.ID, chat.Revision, true)
	if err != nil || !proactiveActive(enabled) {
		t.Fatal("re-enabling did not restore proactive preference", enabled, err)
	}
	startChannels(t, s, p.Context)
	awaitDirectReminderRequests(t, p, binding.SessionID, "handle the off-period mention", 2)
	p.Idle(t, binding.SessionID)
	for _, id := range []string{"excluded group chatter", "excluded reaction"} {
		event, err := p.Store.Observation(p.Context, id)
		if err != nil || !event.ContextExcluded || event.Text != id {
			t.Fatal("skipped observation missing from platform archive", id, err)
		}
		delivery, err := s.Delivery(p.Context, id)
		if err != nil || delivery.State != "excluded" || delivery.InputID != "" {
			t.Fatal("unexpected delivery for excluded observation", delivery, err)
		}
		if _, err := p.Harness.Receipt(p.Context, binding.SessionID, id); !domain.IsMissing(err) {
			t.Fatal("excluded observation has a Core receipt", id, err)
		}
	}
	archive("ordinary after toggle", "", false, nil)
	testkit.Await(t, p.Context, func() bool {
		d, err := s.Delivery(p.Context, "ordinary after toggle")
		return err == nil && d.State == "applied"
	})
	seq, err := p.Store.ObservationSequence(p.Context, "ordinary after toggle")
	if err != nil {
		t.Fatal(err)
	}
	window, err = s.pendingProbe(p.Context, binding, seq, time.Now().Add(time.Hour))
	if err != nil || len(window.Inputs) != 1 || window.Inputs[0] != "ordinary after toggle" {
		t.Fatalf("restored Probe window = %+v, %v", window, err)
	}
}
