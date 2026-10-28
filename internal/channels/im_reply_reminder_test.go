package channels

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/messages"
	"github.com/oines/astral/internal/testkit"
)

func reminderWireText(message map[string]any) string {
	if text, ok := message["content"].(string); ok {
		return text
	}
	var text strings.Builder
	for _, raw := range message["content"].([]any) {
		part := raw.(map[string]any)
		if part["type"] == "text" {
			text.WriteString(part["text"].(string))
		}
	}
	return text.String()
}

func assertDriverReplyReminder(t *testing.T, request map[string]any, marker string) {
	t.Helper()
	found := false
	for _, raw := range request["messages"].([]any) {
		message := raw.(map[string]any)
		text := reminderWireText(message)
		if message["role"] != "user" || !strings.Contains(text, marker) {
			continue
		}
		found = true
		if strings.Count(text, messages.IMReplyReminder) != 1 || !strings.HasSuffix(text, messages.IMReplyReminder) {
			t.Fatalf("IM driver lost or duplicated its explicit reply reminder: %s", text)
		}
	}
	if !found {
		t.Fatalf("the actual model request did not include its IM driver %s", marker)
	}
	if !strings.Contains(string(encoded(request["tools"])), `"name":"send_message"`) {
		t.Fatal("the IM reply reminder referred to an unavailable send_message tool")
	}
}

func TestIMTriggerModelRequestsIncludeReplyReminderForEveryReason(t *testing.T) {
	requests := make(chan map[string]any, 4)
	p := testkit.New(t, 0, func(_ context.Context, _ int, request map[string]any) any {
		requests <- request
		return testkit.Text("Assistant text remains visible in Web only.")
	})
	s := New(p.Store, p.Harness)
	bot := newBot(t)
	connection := bot.connection(t, s, p.Agent.ID)
	binding := createBinding(t, p, s, connection, "-100", "")
	for index, reason := range []string{"private", "mention", "reply", "poke"} {
		event := domain.Observation{ID: "reminder-" + reason, SchemaVersion: 1, Kind: "message.created", ConnectionID: connection.ID, ConversationID: binding.ConversationID,
			MessageID: fmt.Sprint(index + 1), Trigger: reason, Text: "Trigger this Bot for " + reason, Sender: domain.Sender{ID: "7", Name: "Ada"}, SentAt: time.Now(), ReceivedAt: time.Now()}
		if reason == "poke" {
			event.Kind, event.MessageID = "notice.poke", ""
		}
		if _, err := p.Store.Archive(p.Context, event); err != nil {
			t.Fatal(err)
		}
		if err := s.deliver(p.Context, binding, event); err != nil {
			t.Fatal(err)
		}
		testkit.Await(t, p.Context, func() bool { return p.Requests.Load() >= int64(index+1) })
		p.Idle(t, binding.SessionID)
		request := <-requests
		assertDriverReplyReminder(t, request, `event-id="`+event.ID+`" reason="`+reason+`"`)
	}
	if bot.sends.Load() != 0 {
		t.Fatal("assistant text was automatically sent to IM")
	}
}

func TestProbeWakeupModelRequestIncludesReplyReminderButProbeRequestDoesNot(t *testing.T) {
	requests := make(chan map[string]any, 4)
	p := testkit.New(t, 0, func(_ context.Context, _ int, request map[string]any) any {
		requests <- request
		if strings.Contains(string(encoded(request["tools"])), `"name":"decide"`) {
			return testkit.Tool("decide", map[string]bool{"participate": true})
		}
		return testkit.Text("Assistant text remains visible in Web only.")
	})
	s := New(p.Store, p.Harness)
	bot := newBot(t)
	connection := bot.connection(t, s, p.Agent.ID)
	binding := createBinding(t, p, s, connection, "-100", "")
	var chat domain.Conversation
	if err := p.Store.Get(p.Context, "conversations", binding.ConversationID, &chat); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetProactive(p.Context, chat.ID, chat.Revision, true); err != nil {
		t.Fatal(err)
	}
	event := domain.Observation{ID: "proactive-reminder", SchemaVersion: 1, Kind: "message.created", ConnectionID: connection.ID, ConversationID: binding.ConversationID,
		MessageID: "1", Text: "A relevant group question.", Sender: domain.Sender{ID: "7", Name: "Ada"}, SentAt: time.Now(), ReceivedAt: time.Now()}
	if _, err := p.Store.Archive(p.Context, event); err != nil {
		t.Fatal(err)
	}
	if err := s.deliver(p.Context, binding, event); err != nil {
		t.Fatal(err)
	}
	view, err := p.Harness.ReadContext(p.Context, binding.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	participate, err := s.shouldParticipate(p.Context, binding, view)
	if err != nil || !participate {
		t.Fatalf("Probe did not return a positive decision: %v %v", participate, err)
	}
	if strings.Contains(string(encoded(<-requests)), messages.IMReplyReminder) {
		t.Fatal("the Probe decision request received the main-Agent reply reminder")
	}
	through, err := p.Store.ObservationSequence(p.Context, event.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.finishProbe(p.Context, binding, probeResult{through: through, participate: participate}); err != nil {
		t.Fatal(err)
	}
	testkit.Await(t, p.Context, func() bool { return p.Requests.Load() == 2 })
	p.Idle(t, binding.SessionID)
	assertDriverReplyReminder(t, <-requests, `<im_wakeup conversation-id="`+binding.ConversationID+`"/>`)
	// A later Probe observes the main Session after that wakeup. Its projection
	// must still exclude the driver's reminder rather than adopt reply duties.
	view, err = p.Harness.ReadContext(p.Context, binding.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.shouldParticipate(p.Context, binding, view); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded(<-requests)), messages.IMReplyReminder) {
		t.Fatal("a subsequent Probe inherited the earlier main-Agent wakeup reminder")
	}
	if bot.sends.Load() != 0 {
		t.Fatal("Probe or assistant text automatically sent an IM message")
	}
}
