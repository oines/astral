package harness

import (
	"context"
	"encoding/json"
	"strings"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
)

type MessageArgs struct {
	domain.MessageOptions
	Text        string   `json:"text"`
	ReplyTo     string   `json:"reply_to,omitempty"`
	Attachments []string `json:"attachments,omitempty"`
}

type MessageTarget struct {
	BindingID      string                   `json:"binding_id"`
	ConversationID string                   `json:"conversation_id"`
	Source         *domain.ExecutionBinding `json:"source,omitempty"`
}

type Messenger interface {
	MessageTool(context.Context, string, string) (core.ToolDefinition, error)
	SendMessage(context.Context, string, string, string, MessageTarget, MessageArgs) ([]domain.Outbox, error)
}

type sendMessageTool struct{ runtime *runtime }

func (t sendMessageTool) Definition() core.ToolDefinition {
	if t.runtime != nil && t.runtime.messageTool.Name != "" {
		return t.runtime.messageTool
	}
	return DefaultMessageTool()
}

// DefaultMessageTool is registered while a Session has no IM binding; it is
// omitted from model requests until a destination has been selected.
func DefaultMessageTool() core.ToolDefinition {
	return core.ToolDefinition{Name: "send_message", Version: "astral-1", Sequential: true,
		Description: "Send to this Session's bound chat. The Host selects the account, chat and Topic. Text may be empty when sending attachments. Attachments are Computer paths or astral-file:// references. Waits for platform acceptance or a reported failure and returns durable receipts. Acceptance is not final delivery. Inspect unknown effects before retrying. Assistant text alone is never sent to IM.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"},"reply_to":{"type":"string"},"attachments":{"type":"array","items":{"type":"string","minLength":1}}},"required":["text"],"additionalProperties":false}`)}
}

func (r *runtime) messageToolFor(ctx context.Context, record domain.Session) (core.ToolDefinition, error) {
	if record.BindingID == "" || r.service.IM == nil {
		return DefaultMessageTool(), nil
	}
	return r.service.IM.MessageTool(ctx, record.AgentID, record.ConversationID)
}

// Core registers immutable tool declarations and compaction policy. A stopped
// binding change opens the same committed Session with its new configuration,
// keeping the tool backend (including running Computer processes) and history.
func (r *runtime) syncBindingConfig(ctx context.Context, record domain.Session) error {
	definition, err := r.messageToolFor(ctx, record)
	prompt := compactionPromptFor(record)
	if err != nil || jsonEqual(definition, r.messageTool) && prompt == r.compactionPrompt {
		return err
	}
	model, err := r.resolveModel(ctx, record.ModelRef)
	if err != nil {
		return err
	}
	if err = r.core.Load().Close(ctx); err != nil {
		return err
	}
	previousTool, previousPrompt := r.messageTool, r.compactionPrompt
	r.messageTool, r.compactionPrompt = definition, prompt
	next, err := core.OpenSession(ctx, r.coreConfig(model, record.Options), r.id)
	if err != nil {
		r.messageTool, r.compactionPrompt = previousTool, previousPrompt
		return err
	}
	r.core.Store(next)
	return nil
}

func (t sendMessageTool) Bind(ctx context.Context, inv core.Invocation) (json.RawMessage, error) {
	r := t.runtime
	var record domain.Session
	if err := r.service.Store.Get(ctx, "sessions", r.id, &record); err != nil {
		return nil, err
	}
	if record.BindingID == "" {
		return nil, notExecuted("chat_unbound", domain.Conflict("this Session has no bound chat"))
	}
	var args MessageArgs
	if err := json.Unmarshal(inv.Call.Arguments, &args); err != nil {
		return nil, err
	}
	target := MessageTarget{BindingID: record.BindingID, ConversationID: record.ConversationID}
	for _, attachment := range args.Attachments {
		if !strings.HasPrefix(attachment, "astral-file://") {
			source, err := r.currentBinding(ctx)
			if err != nil {
				return nil, notExecuted("computer_unavailable", err)
			}
			target.Source = &source
			break
		}
	}
	return encode(target), nil
}

func (t sendMessageTool) Execute(ctx context.Context, inv core.Invocation, _ func(core.ToolProgress)) (core.ToolExecution, error) {
	var args MessageArgs
	if err := json.Unmarshal(inv.Call.Arguments, &args); err != nil {
		return core.ToolExecution{}, err
	}
	var target MessageTarget
	if err := json.Unmarshal(inv.Binding, &target); err != nil {
		return core.ToolExecution{}, err
	}
	r := t.runtime
	if r.service.IM == nil {
		return core.ToolExecution{}, notExecuted("im_unavailable", domain.Offline("IM service is unavailable"))
	}
	receipts, err := r.service.IM.SendMessage(ctx, r.agentID, r.id, inv.ExecutionID, target, args)
	if err != nil {
		if len(receipts) > 0 {
			outcome := core.OutcomeFailed
			for _, receipt := range receipts {
				if receipt.Status == "unknown" || receipt.Status == "dispatching" || receipt.Status == "pending" {
					outcome = core.OutcomeUnknown
				}
			}
			return core.ToolExecution{Result: &core.ToolResult{Content: core.Text(string(encode(receipts)) + "\nMessage send did not complete: " + err.Error()), IsError: true, Outcome: outcome}}, nil
		}
		return core.ToolExecution{}, notExecuted("im_send_failed", err)
	}
	return resultJSON(receipts), nil
}
