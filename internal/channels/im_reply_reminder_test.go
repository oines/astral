package channels

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	core "github.com/oines/astral-core"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/messages"
	"github.com/oines/astral/internal/testkit"
)

func awaitDirectReminderRequests(t *testing.T, p *testkit.Platform, sessionID, reason string, count int64) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for p.Requests.Load() < count {
		select {
		case <-p.Context.Done():
			t.Fatal(p.Context.Err())
		case <-tick.C:
		case <-deadline.C:
			state, stateErr := p.CoreStore.LoadState(p.Context, sessionID)
			var watch map[string]any
			watchErr := p.Store.Maintenance(p.Context, "im_reply_watch", sessionID, &watch)
			consumed, consumedErr := p.CoreStore.Scan(p.Context, state.RolloutID, core.Query{SessionID: sessionID, Kind: core.EntryInputConsumed, Reverse: true, Limit: 8, MaxBytes: 1 << 20})
			inputs := []any{}
			for _, row := range consumed.Records {
				if row.Entry.Input != nil {
					inputs = append(inputs, map[string]any{"run": row.Entry.RunID, "version": row.Entry.Version, "input_id": row.Entry.Input.ID})
				}
			}
			t.Fatalf("%s: requests=%d want=%d last_run=%+v/%v watch=%+v/%v consumed=%+v/%v", reason, p.Requests.Load(), count, state.LastRun, stateErr, watch, watchErr, inputs, consumedErr)
		}
	}
}

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

func assertDriverReplyReminder(t *testing.T, request map[string]any, marker, reminder string) {
	t.Helper()
	found := false
	for _, raw := range request["messages"].([]any) {
		message := raw.(map[string]any)
		text := reminderWireText(message)
		if message["role"] != "user" || !strings.Contains(text, marker) {
			continue
		}
		found = true
		if strings.Count(text, reminder) != 1 || !strings.HasSuffix(text, reminder) {
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
	requests := make(chan map[string]any, 8)
	p := testkit.New(t, 0, func(_ context.Context, _ int, request map[string]any) any {
		requests <- request
		return testkit.Text("This reply was output on the web page, not sent to the chat.")
	})
	s := New(p.Store, p.Harness)
	bot := newBot(t)
	connection := bot.connection(t, s, p.Agent.ID)
	binding := createBinding(t, p, s, connection, "-100", "")
	for index, reason := range []string{"private", "mention", "reply", "poke"} {
		event := domain.Observation{ID: "reminder-" + reason, ExternalEventID: "reminder-" + reason, SchemaVersion: 1, Kind: "message.created", ConnectionID: connection.ID, ConversationID: binding.ConversationID,
			MessageID: fmt.Sprint(index + 1), Trigger: reason, Text: "Trigger this Bot for " + reason, Sender: domain.Sender{ID: "7", Name: "Ada"}, SentAt: time.Now(), ReceivedAt: time.Now()}
		if reason == "poke" {
			event.Kind, event.MessageID = "notice.poke", ""
		}
		if inserted, err := p.Store.Archive(p.Context, event); err != nil || !inserted {
			t.Fatalf("direct event was not archived: inserted=%v error=%v", inserted, err)
		}
		if err := s.deliver(p.Context, binding, event); err != nil {
			t.Fatal(err)
		}
		awaitDirectReminderRequests(t, p, binding.SessionID, reason, int64((index+1)*2))
		p.Idle(t, binding.SessionID)
		request := <-requests
		assertDriverReplyReminder(t, request, `event-id="`+event.ID+`" reason="`+reason+`"`, messages.IMDirectReplyInstruction)
		followup := <-requests
		found := false
		for _, raw := range followup["messages"].([]any) {
			message := raw.(map[string]any)
			if message["role"] == "user" && strings.Contains(reminderWireText(message), "如果你决定不回复，请忽略此提醒。") {
				found = true
			}
		}
		if !found {
			t.Fatal("the real follow-up request lost the one-time missing-send reminder")
		}
	}
	if bot.sends.Load() != 0 {
		t.Fatal("the directly output reply was automatically sent to IM")
	}
}

func TestProbeWakeupModelRequestIncludesReplyReminderButProbeRequestDoesNot(t *testing.T) {
	requests := make(chan map[string]any, 4)
	p := testkit.New(t, 0, func(_ context.Context, _ int, request map[string]any) any {
		requests <- request
		if strings.Contains(string(encoded(request["tools"])), `"name":"decide"`) {
			return testkit.Tool("decide", map[string]bool{"participate": true})
		}
		return testkit.Text("This reply was output on the web page, not sent to the chat.")
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
	assertDriverReplyReminder(t, <-requests, `<im_wakeup conversation-id="`+binding.ConversationID+`" event-ids="`+event.ID+`"/>`, messages.IMReplyReminder)
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
		t.Fatal("Probe or a directly output reply automatically sent an IM message")
	}
}
