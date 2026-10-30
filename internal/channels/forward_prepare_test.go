package channels

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
)

func TestForwardPreparationUsesSharedBudgetAndNeverRewritesSource(t *testing.T) {
	f := newPartResourceFixture(t, 0)
	f.parent = json.RawMessage(`[{"type":"forward","data":{"id":"current-handle"}}]`)
	f.forward = json.RawMessage(`{"messages":[{"sender":{"user_id":7},"message":[{"type":"text","data":{"text":"one"}}]},{"sender":{"user_id":8},"message":[{"type":"text","data":{"text":"two"}}]}]}`)
	parts := []domain.MessagePart{{Ref: "p0", Type: "forward", Forward: &domain.MessageForward{ExternalID: "current-handle", Status: "unexpanded"}}}
	event := f.archive(t, parts)
	budget := newForwardPreparationBudget()
	ctx, cancel := context.WithTimeout(f.p.Context, 6*time.Second)
	defer cancel()
	for range 2 {
		prepared, err := f.s.prepareForwardParts(ctx, f.caller, f.chat.ID, "42", parts, budget)
		if err != nil || len(prepared[0].Forward.Messages) != 2 || prepared[0].Forward.Messages[0].Parts[0].Text != "one" {
			t.Fatalf("finite forward preparation failed: %+v %v", prepared, err)
		}
	}
	prepared, err := f.s.prepareForwardParts(ctx, f.caller, f.chat.ID, "42", parts, budget)
	if err != nil || prepared[0].Forward.Status != "truncated" || len(prepared[0].Forward.Messages) != 0 || f.getMessage.Load() != 2 || f.getForward.Load() != 2 {
		t.Fatalf("root/reply preparation exceeded shared four-call budget: %+v %+v %v", prepared, budget, err)
	}
	stored, err := f.p.Store.Observation(f.p.Context, event.ID)
	if err != nil || stored.Parts[0].Forward.Status != "unexpanded" || len(stored.Parts[0].Forward.Messages) != 0 || parts[0].Forward.Status != "unexpanded" || f.p.Requests.Load() != 0 {
		t.Fatal("preparation changed archived/source parts or invoked an Agent")
	}
}

func TestForwardPreparationKeepsRemoteFailuresOptionalAndLocalPrerequisitesHard(t *testing.T) {
	f := newPartResourceFixture(t, 0)
	f.parent = json.RawMessage(`[{"type":"forward","data":{"id":"current-handle"}}]`)
	f.forward = json.RawMessage(`{"messages":[]}`)
	parts := []domain.MessagePart{{Ref: "p0", Type: "forward", Forward: &domain.MessageForward{ExternalID: "current-handle", Status: "unexpanded"}}}
	f.parentChat = "789"
	f.archive(t, parts)
	prepared, err := f.s.prepareForwardParts(f.p.Context, f.caller, f.chat.ID, "42", parts, nil)
	if err != nil || prepared[0].Forward.Status != "unavailable" || f.getForward.Load() != 0 {
		t.Fatalf("remote scope mismatch blocked inbox or expanded a foreign chat: %+v %v", prepared, err)
	}
	f.parentChat = "456"
	f.forward = json.RawMessage(`{"messages":"broken"}`)
	prepared, err = f.s.prepareForwardParts(f.p.Context, f.caller, f.chat.ID, "42", parts, nil)
	if err != nil || prepared[0].Forward.Status != "unavailable" {
		t.Fatalf("malformed remote forward blocked inbox: %+v %v", prepared, err)
	}
	f.account.AccessMode = "specified"
	if err := f.p.Store.Save(f.p.Context, "connections", &f.account.Meta, &f.account); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.prepareForwardParts(f.p.Context, f.caller, f.chat.ID, "42", parts, nil)
	var prerequisite *attachmentPrerequisiteError
	if !errors.As(err, &prerequisite) {
		t.Fatalf("local authorization failure was swallowed: %v", err)
	}
	canceled, cancel := context.WithCancel(f.p.Context)
	cancel()
	prepared, err = f.s.prepareForwardParts(canceled, f.caller, f.chat.ID, "42", parts, nil)
	if err != nil || prepared[0].Forward.Status != "unavailable" {
		t.Fatalf("exhausted optional preparation deadline became hard error: %+v %v", prepared, err)
	}
}

func TestForwardPreparationInlineNeedsNoDatabaseAndAcquiredTreeHasFiniteBudget(t *testing.T) {
	inline := []domain.MessagePart{{Ref: "p0", Type: "forward", Forward: &domain.MessageForward{Status: "inline", Raw: json.RawMessage(`[]`), Messages: []domain.ForwardedMessage{{Parts: []domain.MessagePart{{Ref: "p0.m0.p0", Type: "text", Text: "already inline"}}}}}}}
	prepared, err := (&Service{}).prepareForwardParts(context.Background(), domain.ExecutionBinding{}, "unused", "unused", inline, nil)
	if err != nil || prepared[0].Forward.Messages[0].Parts[0].Text != "already inline" {
		t.Fatalf("inline preparation acquired unnecessary local prerequisites: %+v %v", prepared, err)
	}
	f := newPartResourceFixture(t, 0)
	f.parent = json.RawMessage(`[{"type":"forward","data":{"id":"current-handle"}}]`)
	node := func(children []any) map[string]any {
		return map[string]any{"sender": map[string]any{"user_id": 7}, "message": []any{map[string]any{"type": "forward", "data": map[string]any{"content": children}}}}
	}
	deep := []any{node([]any{node([]any{node([]any{map[string]any{"sender": map[string]any{"user_id": 7}, "message": []any{map[string]any{"type": "text", "data": map[string]any{"text": "too deep"}}}}})})})}
	f.forward = encoded(map[string]any{"messages": []any{node(deep), node(deep), node(deep), node(deep), node(deep)}})
	parts := []domain.MessagePart{{Ref: "p0", Type: "forward", Forward: &domain.MessageForward{ExternalID: "current-handle", Status: "unexpanded"}}}
	budget := newForwardPreparationBudget()
	f.archive(t, parts)
	prepared, err = f.s.prepareForwardParts(f.p.Context, f.caller, f.chat.ID, "42", parts, budget)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	var inspect func(*domain.MessageForward, int)
	inspect = func(forward *domain.MessageForward, depth int) {
		if depth >= 2 && len(forward.Messages) != 0 {
			t.Errorf("automatic expansion exceeded two levels: depth=%d", depth)
		}
		for _, message := range forward.Messages {
			count++
			for _, part := range message.Parts {
				if part.Forward != nil {
					inspect(part.Forward, depth+1)
				}
			}
		}
	}
	inspect(prepared[0].Forward, 0)
	if count > 12 || budget.messages != 12-count {
		t.Fatalf("automatic expansion did not count nested messages: count=%d budget=%+v", count, budget)
	}
}

func TestForwardPreparationInlineRootAndReplyShareTwelveMessageBudget(t *testing.T) {
	for _, nested := range []bool{false, true} {
		t.Run(map[bool]string{false: "flat", true: "nested"}[nested], func(t *testing.T) {
			makeParts := func() []domain.MessagePart {
				forward := &domain.MessageForward{Status: "inline", Total: 10}
				for range 10 {
					message := domain.ForwardedMessage{Parts: []domain.MessagePart{{Type: "text", Text: "quoted"}}}
					if nested {
						message.Parts = append(message.Parts, domain.MessagePart{Type: "forward", Forward: &domain.MessageForward{Status: "inline", Total: 1, Messages: []domain.ForwardedMessage{{Parts: []domain.MessagePart{{Type: "text", Text: "nested quote"}}}}}})
					}
					forward.Messages = append(forward.Messages, message)
				}
				return []domain.MessagePart{{Ref: "p0", Type: "forward", Forward: forward}}
			}
			root, reply := makeParts(), makeParts()
			budget := newForwardPreparationBudget()
			first, err := (&Service{}).prepareForwardParts(context.Background(), domain.ExecutionBinding{}, "unused", "root", root, budget)
			if err != nil {
				t.Fatal(err)
			}
			second, err := (&Service{}).prepareForwardParts(context.Background(), domain.ExecutionBinding{}, "unused", "reply", reply, budget)
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			var inspect func([]domain.MessagePart)
			inspect = func(parts []domain.MessagePart) {
				for _, part := range parts {
					if part.Forward != nil {
						count += len(part.Forward.Messages)
						for _, message := range part.Forward.Messages {
							inspect(message.Parts)
						}
					}
				}
			}
			inspect(first)
			inspect(second)
			if count != 12 || budget.messages != 0 || len(root[0].Forward.Messages) != 10 || len(reply[0].Forward.Messages) != 10 || second[0].Forward.Status != "truncated" {
				t.Fatalf("inline root/reply did not share finite copy-only budget: count=%d budget=%+v", count, budget)
			}
		})
	}
}

func TestForwardRecallBeforeOrDuringPreparationDoesNotBlockLaterIMTrigger(t *testing.T) {
	for _, during := range []bool{false, true} {
		t.Run(map[bool]string{false: "recall before delivery", true: "recall during source read"}[during], func(t *testing.T) {
			f := newPartResourceFixture(t, 0)
			f.parent = json.RawMessage(`[{"type":"forward","data":{"id":"current-handle"}}]`)
			f.forward = json.RawMessage(`{"messages":[{"sender":{"user_id":7},"message":[{"type":"text","data":{"text":"content recalled during source read"}}]}]}`)
			binding := domain.ConversationBinding{Meta: domain.Meta{ID: "recall-budget-binding"}, AgentID: f.p.Agent.ID, SessionID: f.p.Session.ID, ConversationID: f.chat.ID, Enabled: true}
			if err := f.p.Store.Save(f.p.Context, "bindings", &binding.Meta, &binding); err != nil {
				t.Fatal(err)
			}
			f.p.Session.BindingID, f.p.Session.ConversationID, f.p.Session.Source = binding.ID, f.chat.ID, "im"
			if err := f.p.Store.Save(f.p.Context, "sessions", &f.p.Session.Meta, &f.p.Session); err != nil {
				t.Fatal(err)
			}
			created := f.archive(t, []domain.MessagePart{{Ref: "p0", Type: "forward", Forward: &domain.MessageForward{ExternalID: "current-handle", Status: "unexpanded"}}})
			recalled := domain.Observation{SchemaVersion: 1, ID: "recall-after-created", ConnectionID: f.account.ID, ConversationID: f.chat.ID, MessageID: "42", Kind: "message.deleted", ExternalEventID: "recall-after-created", SentAt: time.Now(), ReceivedAt: time.Now()}
			recall := func() {
				if _, err := f.p.Store.Archive(f.p.Context, recalled); err != nil {
					t.Error(err)
				}
			}
			if during {
				f.hook = recall
			} else {
				recall()
			}
			if err := f.s.deliver(f.p.Context, binding, created); err != nil {
				t.Fatalf("optional recalled source blocked created observation: %v", err)
			}
			if err := f.s.deliver(f.p.Context, binding, recalled); err != nil {
				t.Fatalf("later recall fact was blocked: %v", err)
			}
			later := domain.Observation{SchemaVersion: 1, ID: "mention-after-recall", ConnectionID: f.account.ID, ConversationID: f.chat.ID, MessageID: "43", Kind: "message.created", ExternalEventID: "mention-after-recall", Text: "continue after recall", Trigger: "mention", SentAt: time.Now(), ReceivedAt: time.Now()}
			if _, err := f.p.Store.Archive(f.p.Context, later); err != nil {
				t.Fatal(err)
			}
			if err := f.s.deliver(f.p.Context, binding, later); err != nil {
				t.Fatalf("later mention was blocked: %v", err)
			}
			f.p.Idle(t, binding.SessionID)
			for _, event := range []domain.Observation{created, recalled, later} {
				delivery, err := f.p.Store.Delivery(f.p.Context, event.ID)
				if err != nil || delivery.State != "routed" {
					t.Fatalf("event after recall did not route: event=%s delivery=%+v err=%v", event.ID, delivery, err)
				}
			}
			if f.p.Requests.Load() != 1 {
				t.Fatalf("later mention failed to wake exactly once: requests=%d", f.p.Requests.Load())
			}
		})
	}
}
