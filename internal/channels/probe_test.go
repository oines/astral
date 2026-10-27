package channels

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/testkit"
)

func decisionCall(name, args string) core.Content {
	return core.Content{Type: "tool_call", ToolCall: &core.ToolCall{ID: domain.ID("call"), Name: name, Arguments: json.RawMessage(args)}}
}
func TestProbeDecisionUsesFirstValidBooleanWithoutExecutingAnything(t *testing.T) {
	tests := []struct {
		name    string
		parts   []core.Content
		want    bool
		invalid bool
	}{
		{"yes", []core.Content{decisionCall("decide", `{"participate":true}`)}, true, false},
		{"no", []core.Content{decisionCall("decide", `{"participate":false}`)}, false, false},
		{"plain text", core.Text("yes"), false, true},
		{"wrong tool", []core.Content{decisionCall("send_message", `{"text":"yes"}`)}, false, true},
		{"invalid json", []core.Content{decisionCall("decide", `{"participate":`)}, false, true},
		{"string boolean", []core.Content{decisionCall("decide", `{"participate":"true"}`)}, false, true},
		{"null boolean", []core.Content{decisionCall("decide", `{"participate":null}`)}, false, true},
		{"reason forbidden", []core.Content{decisionCall("decide", `{"participate":true,"reason":"do this"}`)}, false, true},
		{"invalid then valid", []core.Content{decisionCall("decide", `{}`), decisionCall("decide", `{"participate":true}`)}, true, false},
		{"first valid wins", []core.Content{decisionCall("decide", `{"participate":false}`), decisionCall("decide", `{"participate":true}`)}, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := probeDecision(core.Message{Role: core.RoleAssistant, Content: tt.parts})
			if got != tt.want || (err != nil) != tt.invalid {
				t.Fatalf("decision = %v, %v", got, err)
			}
		})
	}
}

func TestProbeProjectionRetainsObservationsSummaryAndToolsOnly(t *testing.T) {
	history := []core.Message{
		{Role: core.RoleUser, ContextKind: "summary", Content: core.Text("earlier summary")},
		{Role: core.RoleUser, ContextKind: "im.observation", Content: append(core.Text("group context"), core.Content{Type: "image", MediaType: "image/png", Data: json.RawMessage(`"image"`)})},
		core.User("private Web input"),
		{Role: core.RoleAssistant, Content: append(core.Text("private answer"), core.Content{Type: "reasoning", Text: "private reasoning"}, decisionCall("exec_command", `{"cmd":"inspect"}`))},
	}
	call := history[3].ToolCalls()[0]
	history = append(history, core.Message{Role: core.RoleTool, ToolCallID: call.ID, Content: core.Text("inspection result")})
	with := probeMessages(history, true)
	text := string(encoded(with))
	for _, want := range []string{"earlier summary", "group context", "inspection result", "image/png"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %s", want)
		}
	}
	for _, hidden := range []string{"private Web input", "private answer", "private reasoning"} {
		if strings.Contains(text, hidden) {
			t.Errorf("leaked %s", hidden)
		}
	}
	if strings.Contains(string(encoded(probeMessages(history, false))), "image/png") {
		t.Fatal("text-only Probe received image")
	}
}

func TestProactiveModeUsesLLMOnlyAndDoesNotRepeatCoveredInputs(t *testing.T) {
	bot := newBot(t)
	var probes, mains atomic.Int64
	p := testkit.New(t, 0, func(_ context.Context, _ int, request map[string]any) any {
		if strings.Contains(string(encoded(request["tools"])), `"name":"decide"`) {
			n := probes.Add(1)
			if n == 1 {
				return testkit.Tool("decide", map[string]any{"participate": false})
			}
			if n == 2 {
				return testkit.Tool("decide", map[string]any{"participate": true})
			}
			return testkit.Text("invalid decision")
		}
		mains.Add(1)
		if strings.Contains(string(encoded(request["messages"])), "invalid decision") {
			t.Error("Probe content reached main context")
		}
		return testkit.Text("Web-only answer")
	})
	s := New(p.Store, p.Harness)
	s.probeQuiet = 10 * time.Millisecond
	s.probeMaxWait = 50 * time.Millisecond
	c := bot.connection(t, s, p.Agent.ID)
	b := createBinding(t, p, s, c, "-100", "")
	startChannels(t, s, p.Context)
	deliver := func(id int64, text string) int64 {
		bot.updates <- []telegramUpdate{update(id, text)}
		eventID := stableID("event", c.ID, strings.TrimSpace(string(encoded(id))))
		testkit.Await(t, p.Context, func() bool { d, e := s.Delivery(p.Context, eventID); return e == nil && d.State == "applied" })
		sequence, e := p.Store.ObservationSequence(p.Context, eventID)
		if e != nil {
			t.Fatal(e)
		}
		return sequence
	}
	deliver(1, "ordinary chatter while disabled")
	if p.Requests.Load() != 0 {
		t.Fatal("default-off chat ran the model")
	}
	var chat domain.Conversation
	if err := p.Store.Get(p.Context, "conversations", b.ConversationID, &chat); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetProactive(p.Context, chat.ID, chat.Revision, true); err != nil {
		t.Fatal(err)
	}
	for i := int64(2); i <= 4; i++ {
		seq := deliver(i, "new relevant group message")
		testkit.Await(t, p.Context, func() bool { pos, e := s.probeCursor(p.Context, b); return e == nil && pos.Sequence >= seq })
		p.Idle(t, b.SessionID)
	}
	if probes.Load() != 3 || mains.Load() != 1 || bot.sends.Load() != 0 {
		t.Fatalf("calls: Probe=%d main=%d sends=%d", probes.Load(), mains.Load(), bot.sends.Load())
	}
	// Inspect the exact persisted request: the wakeup contains only Host metadata.
	session, _ := p.Harness.Core(p.Context, b.SessionID)
	view, err := session.InspectContext(p.Context)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range view.Request.Messages {
		if strings.Contains(string(message.Metadata), "im.wakeup") && strings.Contains(string(encoded(message)), "participate") {
			t.Fatal("Probe decision was forwarded as an instruction")
		}
	}
}

func TestStaleProbeCannotWakeAfterMentionOffOrRebind(t *testing.T) {
	for _, change := range []string{"mention", "off", "rebind"} {
		t.Run(change, func(t *testing.T) {
			bot := newBot(t)
			p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("unexpected") })
			s := New(p.Store, p.Harness)
			c := bot.connection(t, s, p.Agent.ID)
			b := createBinding(t, p, s, c, "-100", "")
			var chat domain.Conversation
			if err := p.Store.Get(p.Context, "conversations", b.ConversationID, &chat); err != nil {
				t.Fatal(err)
			}
			chat, err := s.SetProactive(p.Context, chat.ID, chat.Revision, true)
			if err != nil {
				t.Fatal(err)
			}
			event := domain.Observation{ID: "ordinary", SchemaVersion: 1, ConnectionID: c.ID, ConversationID: chat.ID, MessageID: "1", Text: "question", ReceivedAt: time.Now(), SentAt: time.Now()}
			if _, err = s.Store.Archive(p.Context, event); err != nil {
				t.Fatal(err)
			}
			through, _ := p.Store.ObservationSequence(p.Context, event.ID)
			switch change {
			case "mention":
				err = s.coverProbe(p.Context, b, through)
			case "off":
				_, err = s.SetProactive(p.Context, chat.ID, chat.Revision, false)
			case "rebind":
				_, err = s.Bind(p.Context, chat.ID, "")
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = s.finishProbe(p.Context, b, probeResult{through: through, participate: true}); err != nil {
				t.Fatal(err)
			}
			if p.Requests.Load() != 0 {
				t.Fatal("stale Probe woke main model")
			}
			if _, err = p.Harness.Receipt(p.Context, b.SessionID, probeInputID(b.ID, through)); err == nil {
				t.Fatal("stale wakeup was accepted")
			}
		})
	}
}

func TestProbeFollowsSessionModelAndOverride(t *testing.T) {
	selected := make(chan string, 4)
	p := testkit.New(t, 0, func(_ context.Context, _ int, r map[string]any) any {
		selected <- r["model"].(string)
		return testkit.Tool("decide", map[string]bool{"participate": false})
	})
	bot := newBot(t)
	s := New(p.Store, p.Harness)
	c := bot.connection(t, s, p.Agent.ID)
	b := createBinding(t, p, s, c, "-100", "")
	other := p.Profile
	other.Meta = domain.Meta{}
	other.Name = "other"
	other.ModelID = "second-model"
	// This mock model has explicitly configured limits and transport. Changing
	// its identity must invalidate the first model's catalog recommendations.
	other.Overrides = p.Profile.Recommendations
	if err := p.Harness.Models.SaveProfile(p.Context, &other); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Harness.Models.Verify(p.Context, other.ID); err != nil {
		t.Fatal(err)
	}
	if err := p.Harness.SelectModel(p.Context, b.SessionID, other.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.shouldParticipate(p.Context, b, core.Context{}); err != nil {
		t.Fatal(err)
	}
	if got := <-selected; got != "second-model" {
		t.Fatalf("did not follow Session model: %s", got)
	}
	agent := p.Agent
	agent.ProbeModelID = p.Profile.ID
	if err := p.Harness.SaveAgent(p.Context, &agent); err != nil {
		t.Fatal(err)
	}
	if _, err := s.shouldParticipate(p.Context, b, core.Context{}); err != nil {
		t.Fatal(err)
	}
	if got := <-selected; got != "fixture" {
		t.Fatalf("ignored override: %s", got)
	}
}

func TestProbeFailureCoversOnlySnapshotAndAllowsNewMessages(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("unexpected") })
	bot := newBot(t)
	s := New(p.Store, p.Harness)
	c := bot.connection(t, s, p.Agent.ID)
	b := createBinding(t, p, s, c, "-100", "")
	var chat domain.Conversation
	_ = p.Store.Get(p.Context, "conversations", b.ConversationID, &chat)
	if _, err := s.SetProactive(p.Context, chat.ID, chat.Revision, true); err != nil {
		t.Fatal(err)
	}
	sequences := []int64{}
	for _, id := range []string{"snapshot", "arrived-during-probe"} {
		event := domain.Observation{ID: id, ExternalEventID: id, SchemaVersion: 1, ConnectionID: c.ID, ConversationID: chat.ID, MessageID: id, Text: id, Kind: "message.created", ReceivedAt: time.Now().Add(-time.Minute), SentAt: time.Now()}
		if _, err := s.Store.Archive(p.Context, event); err != nil {
			t.Fatal(err)
		}
		if err := s.deliver(p.Context, b, event); err != nil {
			t.Fatal(err)
		}
		seq, _ := s.Store.ObservationSequence(p.Context, id)
		sequences = append(sequences, seq)
	}
	if err := s.finishProbe(p.Context, b, probeResult{through: sequences[0], err: errors.New("request failed")}); err != nil {
		t.Fatal(err)
	}
	pos, err := s.probeCursor(p.Context, b)
	if err != nil || pos.Sequence != sequences[0] {
		t.Fatalf("position=%+v %v", pos, err)
	}
	next, err := s.pendingProbe(p.Context, b, sequences[1], time.Now())
	if err != nil || next.Through != sequences[1] {
		t.Fatalf("new message was covered by old Probe: %+v %v", next, err)
	}
	if p.Requests.Load() != 0 {
		t.Fatal("failed Probe woke main model")
	}
}
