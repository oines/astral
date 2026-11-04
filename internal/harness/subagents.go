package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

const identityKind = "astral.identity"
const subagentUpdateKind = "astral.subagent"
const maxSubagentDepth = 1
const maxSubagentsPerBot = 64

type subagentSettings struct {
	domain.Meta
	Configuration  core.Configuration  `json:"configuration"`
	MessageTool    core.ToolDefinition `json:"message_tool"`
	InheritContext bool                `json:"inherit_context"`
	ForkEntryID    string              `json:"fork_entry_id,omitempty"`
	Task           string              `json:"task"`
	Started        bool                `json:"started"`
}

type SpawnAgentInput struct {
	Name           string                 `json:"name,omitempty"`
	Task           string                 `json:"task"`
	InheritContext bool                   `json:"inherit_context,omitempty"`
	Model          string                 `json:"model,omitempty"`
	Reasoning      *core.ReasoningOptions `json:"reasoning,omitempty"`
	ComputerID     string                 `json:"computer_id,omitempty"`
}
type AgentControlInput struct {
	AgentID   string                 `json:"agent_id,omitempty"`
	Input     string                 `json:"input,omitempty"`
	Signal    string                 `json:"signal,omitempty"`
	Model     string                 `json:"model,omitempty"`
	Reasoning *core.ReasoningOptions `json:"reasoning,omitempty"`
}
type SubagentView struct {
	SessionID       string              `json:"session_id"`
	ParentSessionID string              `json:"parent_session_id"`
	Title           string              `json:"title"`
	Name            string              `json:"name"`
	Selection       core.ModelSelection `json:"selection"`
	Status          string              `json:"status"`
	Error           string              `json:"error,omitempty"`
}

type spawnAgentTool struct{ runtime *runtime }

func (t spawnAgentTool) Definition() core.ToolDefinition {
	if saved, ok := t.runtime.savedDelegationTool("spawn_agent"); ok {
		return saved
	}
	return core.ToolDefinition{Name: "spawn_agent", Version: "astral-2", Description: "Create a persistent direct subagent and start it asynchronously. Only user-facing root Sessions may spawn subagents; subagents cannot spawn further agents. Assign independently actionable work. name optionally assigns a stable agent name; omitted names receive a generated code. task is only its first instruction, never its name. Delegate long-running or parallel work, or hand off unfinished work with inherit_context=true. Inherited history uses a shared Rollout fork; otherwise start fresh. Both use this Session's current Computer by default. Omit model and reasoning to inherit the parent's latest complete selection. Explicit model uses that profile's defaults unless reasoning is supplied. Read the models Gateway capability for available profile IDs and legal reasoning values. Do not ask subagents to poll or keep their Runs alive while waiting for you, other agents, or future decisions. Subagents report meaningful progress through assistant text passively and naturally end their Runs when completed or blocked. Natural Run end or failure automatically wakes you. Continue useful work yourself, or end your own Run rather than polling for subagents. Reuse persistent Sessions through agent_control. Returns session_id, name and selection.",
		Parameters: encode(map[string]any{"type": "object", "properties": map[string]any{
			"name":            map[string]any{"type": "string", "maxLength": 100},
			"task":            map[string]any{"type": "string", "minLength": 1},
			"inherit_context": map[string]any{"type": "boolean"},
			"model":           map[string]any{"type": "string"},
			"reasoning":       subagentReasoningSchema(),
			"computer_id":     map[string]any{"type": "string"}},
			"required": []string{"task"}, "additionalProperties": false})}
}
func (t spawnAgentTool) Execute(ctx context.Context, inv core.Invocation, _ func(core.ToolProgress)) (core.ToolExecution, error) {
	var input SpawnAgentInput
	if err := json.Unmarshal(inv.Call.Arguments, &input); err != nil {
		return core.ToolExecution{}, notExecuted("invalid_subagent", err)
	}
	result, err := t.runtime.service.spawnSubagent(ctx, t.runtime, inv, input)
	if err != nil {
		return core.ToolExecution{}, notExecuted("subagent_creation", err)
	}
	return resultJSON(result), nil
}

type agentControlTool struct{ runtime *runtime }

func (t agentControlTool) Definition() core.ToolDefinition {
	if saved, ok := t.runtime.savedDelegationTool("agent_control"); ok {
		return saved
	}
	return core.ToolDefinition{Name: "agent_control", Version: "astral-2", Description: "Manage only your direct persistent subagents. Empty arguments list them; agent_id queries one (use its session_id). input submits a new instruction, steering an active Run or starting an idle Session. model optionally replaces only this child's complete selection, not its descendants; reasoning requires model. Omitted reasoning uses the selected profile's defaults; an empty object uses API defaults. Read the models Gateway capability for available profile IDs and legal reasoning values. Selection alone never starts work; with input it is saved before that instruction. Frozen requests retain their original selection. signal=stop stops its current Run and preserves history; signal=delete permanently deletes it and all descendants, including independent Rollouts. Signals cannot be combined with input or selection. Query without polling to wait: natural Run end triggers you automatically.",
		Parameters: encode(map[string]any{"type": "object", "properties": map[string]any{
			"agent_id":  map[string]any{"type": "string"},
			"input":     map[string]any{"type": "string", "minLength": 1},
			"model":     map[string]any{"type": "string", "minLength": 1},
			"reasoning": subagentReasoningSchema(),
			"signal":    map[string]any{"type": "string", "enum": []string{"stop", "delete"}}}, "additionalProperties": false})}
}

func subagentReasoningSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"enabled":       map[string]any{"type": "boolean"},
		"effort":        map[string]any{"type": "string"},
		"budget_tokens": map[string]any{"type": "integer", "minimum": 0},
	}, "additionalProperties": false}
}

func (r *runtime) savedDelegationTool(name string) (core.ToolDefinition, bool) {
	if r != nil && r.subagent != nil {
		for _, definition := range r.subagent.Configuration.Tools {
			if definition.Name == name {
				return definition, true
			}
		}
	}
	return core.ToolDefinition{}, false
}
func (t agentControlTool) Execute(ctx context.Context, inv core.Invocation, _ func(core.ToolProgress)) (core.ToolExecution, error) {
	var input AgentControlInput
	if err := json.Unmarshal(inv.Call.Arguments, &input); err != nil {
		return core.ToolExecution{}, notExecuted("invalid_subagent", err)
	}
	result, err := t.runtime.service.controlSubagent(ctx, t.runtime.id, inv.ExecutionID, input)
	if err != nil {
		return core.ToolExecution{}, notExecuted("subagent_control", err)
	}
	return resultJSON(result), nil
}

func (s *Service) subagentRecord(ctx context.Context, parent, id string) (domain.Session, error) {
	var record domain.Session
	if err := s.Store.Get(ctx, "sessions", id, &record); err != nil {
		return record, err
	}
	if record.SubagentParentID != parent || record.Deletion != nil {
		return record, domain.Forbidden("only a direct parent can manage this subagent")
	}
	var owner domain.Session
	if err := s.Store.Get(ctx, "sessions", parent, &owner); err != nil {
		return record, err
	}
	if owner.Deletion != nil || owner.AgentID != record.AgentID {
		return record, domain.Forbidden("subagent parent is unavailable")
	}
	return record, nil
}
func (s *Service) subagentView(ctx context.Context, record domain.Session) (SubagentView, error) {
	view, err := s.View(ctx, record.ID)
	if err != nil {
		return SubagentView{}, err
	}
	var settings subagentSettings
	if err = s.Store.Get(ctx, "subagent_settings", record.ID, &settings); err != nil {
		return SubagentView{}, err
	}
	return SubagentView{SessionID: record.ID, ParentSessionID: record.SubagentParentID, Title: record.Title, Name: record.Title, Selection: settings.Configuration.Selection, Status: view.Status, Error: view.Error}, nil
}

func (s *Service) spawnSubagent(ctx context.Context, parent *runtime, inv core.Invocation, input SpawnAgentInput) (SubagentView, error) {
	if strings.TrimSpace(input.Task) == "" {
		return SubagentView{}, domain.Invalid("subagent task is required")
	}
	if input.Model == "" && input.Reasoning != nil {
		return SubagentView{}, domain.Invalid("reasoning requires an explicit model")
	}
	if err := lockSubagentContext(ctx, &s.subagentMu); err != nil {
		return SubagentView{}, err
	}
	defer s.subagentMu.Unlock()
	if err := s.reconcileSubagentModelChangesLocked(ctx); err != nil {
		return SubagentView{}, err
	}
	var source domain.Session
	if err := s.Store.Get(ctx, "sessions", parent.id, &source); err != nil {
		return SubagentView{}, err
	}
	release, err := s.LockAgent(ctx, source.AgentID)
	if err != nil {
		return SubagentView{}, err
	}
	defer release()
	id := "session_subagent_" + digest([]string{parent.id, inv.ExecutionID})[:32]
	var existing domain.Session
	if err = s.Store.Get(ctx, "sessions", id, &existing); err == nil {
		if existing.SubagentParentID != parent.id || existing.Deletion != nil {
			return SubagentView{}, domain.Conflict("subagent identity is unavailable")
		}
		return s.subagentView(ctx, existing)
	} else if !domain.IsMissing(err) {
		return SubagentView{}, err
	}
	depth := 0
	for ancestor := source; ; {
		if ancestor.Deletion != nil {
			return SubagentView{}, domain.Conflict("this subagent tree is being deleted")
		}
		if ancestor.SubagentParentID == "" {
			break
		}
		depth++
		if depth >= maxSubagentDepth {
			return SubagentView{}, domain.Conflict("subagent depth limit reached: only root -> subagent is allowed; subagents cannot spawn further agents")
		}
		var next domain.Session
		if err = s.Store.Get(ctx, "sessions", ancestor.SubagentParentID, &next); err != nil {
			return SubagentView{}, err
		}
		ancestor = next
	}
	records, err := s.Store.List(ctx, "sessions")
	if err != nil {
		return SubagentView{}, err
	}
	count := 0
	usedNames := map[string]bool{}
	for _, row := range records {
		var v domain.Session
		if err = json.Unmarshal(row.Data, &v); err != nil {
			return SubagentView{}, err
		}
		if v.AgentID == source.AgentID && v.SubagentParentID != "" {
			count++
			usedNames[v.Title] = true
		}
	}
	if count >= maxSubagentsPerBot {
		return SubagentView{}, domain.Conflict("subagent Session limit reached; delete unused subagents before creating more")
	}
	configuration, err := core.ReadConfiguration(ctx, s.CoreStore, parent.id)
	if err != nil {
		return SubagentView{}, err
	}
	if parent.subagent != nil {
		var settings subagentSettings
		if err = s.Store.Get(ctx, "subagent_settings", parent.id, &settings); err != nil {
			return SubagentView{}, err
		}
		configuration.Selection = settings.Configuration.Selection
	}
	prompt, err := parent.currentSystemPrompt(ctx, configuration.SystemPrompt)
	if err != nil {
		return SubagentView{}, err
	}
	forkHead := parent.core.Load().Head()
	if inv.AssistantID != "" {
		state := parent.core.Load().State()
		assistant, readErr := s.CoreStore.Get(ctx, state.RolloutID, inv.AssistantID)
		if readErr != nil {
			return SubagentView{}, readErr
		}
		forkHead = assistant.Entry.ParentID
		if assistant.Entry.Generation != nil {
			request, requestErr := parent.core.Load().Attempts().Request(ctx, assistant.Entry.Generation.AttemptID)
			if requestErr != nil {
				return SubagentView{}, requestErr
			}
			prompt = request.SystemPrompt
			configuration.Tools = request.Tools
		}
	}
	configuration.SystemPrompt = prompt
	if input.Model != "" {
		configuration.Selection = core.ModelSelection{ID: input.Model, Reasoning: input.Reasoning}
		if _, err = s.Models.ResolveSelection(ctx, configuration.Selection); err != nil {
			return SubagentView{}, err
		}
	}
	computerID, cwd := source.ComputerID, source.CWD
	if input.ComputerID != "" && input.ComputerID != computerID {
		computerID, cwd = input.ComputerID, ""
	}
	if computerID != "" {
		var agent domain.Agent
		if err = s.Store.Get(ctx, "agents", source.AgentID, &agent); err != nil {
			return SubagentView{}, err
		}
		if !slices.Contains(agent.AllowedComputers, computerID) {
			return SubagentView{}, domain.Forbidden("Computer is not allowed for this Bot")
		}
	}
	title := strings.TrimSpace(input.Name)
	if len([]rune(title)) > 100 {
		return SubagentView{}, domain.Invalid("subagent name must be at most 100 characters")
	}
	for _, char := range title {
		if unicode.IsControl(char) {
			return SubagentView{}, domain.Invalid("subagent name must be a single line without control characters")
		}
	}
	if title == "" {
		codes := []string{"松塔", "海鸥", "北斗", "云杉", "青石", "远帆", "白鹭", "山雀"}
		for {
			token := domain.ID("agent")
			title = codes[int(token[len(token)-1])%len(codes)] + "-" + strings.ToUpper(token[len(token)-6:])
			if !usedNames[title] {
				break
			}
		}
	}
	record := domain.Session{Meta: domain.Meta{ID: id}, AgentID: source.AgentID, Title: title, TitleSource: domain.TitleManual, Source: "subagent", SubagentParentID: source.ID, ComputerID: computerID, CWD: cwd, Private: source.Private}
	settings := subagentSettings{Meta: domain.Meta{ID: id}, Configuration: configuration, MessageTool: parent.messageTool, InheritContext: input.InheritContext, ForkEntryID: forkHead, Task: input.Task}
	if settings.InheritContext && forkHead == "" {
		return SubagentView{}, domain.Conflict("no committed parent history is available to fork")
	}
	err = s.Store.Update(ctx, func(tx *persistence.Tx) error {
		var current domain.Session
		if err := tx.Get(ctx, "sessions", source.ID, &current); err != nil {
			return err
		}
		if current.Deletion != nil {
			return domain.Conflict("parent Session is being deleted")
		}
		record.Private = current.Private
		if err := availableAgent(ctx, tx, source.AgentID); err != nil {
			return err
		}
		if err := tx.Save(ctx, "subagent_settings", &settings.Meta, &settings); err != nil {
			return err
		}
		return tx.Save(ctx, "sessions", &record.Meta, &record)
	})
	if err != nil {
		return SubagentView{}, err
	}
	// The durable creation is the tool result. Initialization and the first Run
	// are reconciled asynchronously, including after a crash before this signal.
	s.notify(id)
	s.wakeSubagents()
	return SubagentView{SessionID: id, ParentSessionID: source.ID, Title: title, Name: title, Selection: configuration.Selection, Status: "queued"}, nil
}

func (s *Service) controlSubagent(ctx context.Context, parent, execution string, input AgentControlInput) (any, error) {
	if input.Model == "" && input.Reasoning != nil {
		return nil, domain.Invalid("reasoning requires an explicit model")
	}
	if input.AgentID == "" {
		if input.Input != "" || input.Signal != "" || input.Model != "" {
			return nil, domain.Invalid("agent_id is required")
		}
		records, err := s.Store.List(ctx, "sessions")
		if err != nil {
			return nil, err
		}
		result := []SubagentView{}
		for _, row := range records {
			var v domain.Session
			if err = json.Unmarshal(row.Data, &v); err != nil {
				return nil, err
			}
			if v.SubagentParentID == parent && v.Deletion == nil {
				view, e := s.subagentView(ctx, v)
				if e != nil {
					return nil, e
				}
				result = append(result, view)
			}
		}
		return result, nil
	}
	record, err := s.subagentRecord(ctx, parent, input.AgentID)
	if err != nil {
		return nil, err
	}
	if input.Signal != "" && (input.Input != "" || input.Model != "") {
		return nil, domain.Invalid("choose signal or input/selection")
	}
	if input.Model != "" {
		if err = s.selectDirectSubagentModel(ctx, parent, record.ID, core.ModelSelection{ID: input.Model, Reasoning: input.Reasoning}); err != nil {
			return nil, err
		}
	}
	switch input.Signal {
	case "":
		if input.Input != "" {
			r, err := s.open(ctx, record.ID)
			if err != nil {
				return nil, err
			}
			r.mu.Lock()
			defer r.mu.Unlock()
			if err = r.startSubagentLocked(ctx); err != nil {
				return nil, err
			}
			message := core.User(input.Input)
			message.ContextKind = "astral.subagent_instruction"
			message.Metadata = encode(map[string]string{"source": "parent", "parent_session_id": parent})
			activation := subagentActivation{Input: core.Input{ID: "subagent_instruction_" + execution, Digest: digest(input), Message: message}}
			if err = s.Store.SaveMaintenance(ctx, "subagent_activation", record.ID, "running", activation); err != nil {
				return nil, err
			}
			err = r.activateSubagentLocked(ctx, &activation)
			if err != nil {
				return nil, err
			}
			status := "queued"
			if r.core.Load().Snapshot().Running {
				status = "running"
			}
			var settings subagentSettings
			if err = s.Store.Get(ctx, "subagent_settings", record.ID, &settings); err != nil {
				return nil, err
			}
			return SubagentView{SessionID: record.ID, ParentSessionID: parent, Title: record.Title, Name: record.Title, Selection: settings.Configuration.Selection, Status: status}, nil
		}
	case "stop":
		if err = s.Stop(ctx, record.ID); err != nil {
			return nil, err
		}
	case "delete":
		if err = s.deleteSubagent(ctx, parent, record.ID); err != nil {
			return nil, err
		}
		return map[string]any{"session_id": record.ID, "deleted": true}, nil
	default:
		return nil, domain.Invalid("unknown subagent signal")
	}
	return s.subagentView(ctx, record)
}

const identityRevision = 1

const rootCoordinationInstructions = `Stay responsive to the user. Don't let ongoing tasks or subagent coordination delay your replies.

Delegate actionable work that subagents can advance independently. Do not ask subagents to poll or stay alive waiting for you or other agents.

After delegating, continue useful independent work. If nothing useful remains, naturally end your Run instead of polling subagent status or sleeping to wait. Ending your Run does not abandon the task: the Host will automatically reactivate you when a direct subagent's Run naturally ends or fails. Reuse its persistent Session through agent_control when needed.`

const subagentCoordinationInstructions = `You are a persistent subagent working exclusively for your direct parent Session. You do not spawn or manage further subagents. Your assistant text is automatically delivered to that parent as passive progress context. The Host automatically notifies your parent when your Run naturally ends or fails.

PROACTIVE PROGRESS REPORTING
- Keep your parent informed without being asked.
- Before substantial work, briefly state your approach and immediate next step, preferably alongside the first tool call.
- During multi-step work, proactively report meaningful discoveries, completed milestones, blockers, unexpected results, and changes of approach. Aim to report after roughly 2–3 tool-use steps when there is useful new information; avoid empty updates.
- Briefly explain what you learned or accomplished and what comes next.
- When more work remains, include progress text alongside your next tool call so the Run can continue. A text-only assistant response without tool calls may naturally end the Run.
- Do not wait for your parent's acknowledgment.

DO WORK OR YIELD
- Never keep your Run alive solely to wait for your parent, another agent, an external decision, further instructions, or an unresolved dependency.
- Do not use repeated status queries, sleep commands, or polling loops merely to stay active until someone else acts.
- Continue any useful independent work.
- If further progress requires external action or information, report what is done, what is blocking you, and what is needed next. Then naturally end the Run without additional tool calls.
- Normal waiting for an actively executing command and bounded checks necessary for your own work are allowed.
- Your Session persists after the Run ends. Your direct parent will be notified and can resume you through agent_control.

COMPLETION
- When finished, provide a concise final report with outcomes, evidence or artifacts, and unresolved issues, then naturally end the Run.
- Clearly distinguish completed work from blocked or yielded work. A natural Run end alone does not imply business success.
- Communicate through assistant text to your direct parent, never directly to the user. Do not delegate work to further subagents.

Principle: Do useful work, report meaningful progress, and yield when blocked. Never wait for work.`

type identityMetadata struct {
	SessionID    string `json:"session_id"`
	ParentID     string `json:"parent_session_id,omitempty"`
	CompactionID string `json:"compaction_id,omitempty"`
	Revision     int    `json:"revision,omitempty"`
}

func (r *runtime) identityContext(ctx context.Context, boundary core.ContextBoundary) ([]core.Message, error) {
	var record domain.Session
	if err := r.service.Store.Get(ctx, "sessions", r.id, &record); err != nil {
		return nil, err
	}
	for i := len(boundary.Context.Request.Messages) - 1; i >= 0; i-- {
		message := boundary.Context.Request.Messages[i]
		if message.ContextKind != identityKind {
			continue
		}
		var metadata identityMetadata
		if json.Unmarshal(message.Metadata, &metadata) == nil && metadata.SessionID == r.id && metadata.ParentID == record.SubagentParentID {
			if metadata.Revision == identityRevision && (boundary.CompactionID == "" || metadata.CompactionID == boundary.CompactionID) {
				return nil, nil
			}
			break
		}
	}
	metadata := identityMetadata{SessionID: r.id, ParentID: record.SubagentParentID, CompactionID: boundary.CompactionID, Revision: identityRevision}
	text := fmt.Sprintf("Current Astral Session identity: %s. You are the user-facing root agent. Reply to the user and coordinate your direct subagents.\n\n%s", r.id, rootCoordinationInstructions)
	if record.SubagentParentID != "" {
		text = fmt.Sprintf("Current Astral Session identity: %s. Your agent name is %s. You are a subagent of Session %s. Actively report progress, findings, blockers and results in assistant text; the Host delivers your text only to this direct parent. send_message is forbidden for every subagent. Earlier identities in inherited history or copied System text belong to earlier Sessions.\n\n%s", r.id, string(encode(record.Title)), record.SubagentParentID, subagentCoordinationInstructions)
	}
	return []core.Message{{Role: core.RoleUser, ContextKind: identityKind, Metadata: encode(metadata), Content: core.Text(text)}}, nil
}

type subagentCheckpoint struct {
	Protocol int    `json:"protocol,omitempty"`
	Version  uint64 `json:"version"`
	Sequence uint64 `json:"sequence"`
	Cursor   string `json:"cursor,omitempty"`
}

const subagentDeliveryProtocol = 1

func (s *Service) wakeSubagents() {
	select {
	case s.subagentWake <- struct{}{}:
	default:
	}
}
func (s *Service) subagentWorker() {
	defer s.subagentWorkers.Done()
	timer := time.NewTicker(500 * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.subagentWake:
		case <-timer.C:
		}
		_ = s.reconcileSubagents(s.ctx)
	}
}
func (s *Service) reconcileSubagents(ctx context.Context) error {
	if err := s.reconcileSubagentModelChanges(ctx); err != nil {
		return err
	}
	items, err := s.Store.List(ctx, "sessions")
	if err != nil {
		return err
	}
	for _, item := range items {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var record domain.Session
		if err = json.Unmarshal(item.Data, &record); err != nil {
			return err
		}
		if record.SubagentParentID == "" || record.Deletion != nil {
			continue
		}
		if err = s.reconcileSubagent(ctx, record); err != nil {
			// Failed delivery remains unacknowledged and is retried. Runtime errors
			// remain visible in the ordinary Session view, never treated as success.
			continue
		}
	}
	return nil
}
func (s *Service) reconcileSubagent(ctx context.Context, record domain.Session) error {
	// Serialize admission and its delivery checkpoint with recursive deletion.
	// Take the tree lock before runtime locks, as deletion does when joining
	// runtimes, so no stale report or checkpoint can survive a completed delete.
	if err := lockSubagentContext(ctx, &s.subagentMu); err != nil {
		return err
	}
	defer s.subagentMu.Unlock()
	if _, err := s.subagentRecord(ctx, record.SubagentParentID, record.ID); err != nil {
		return err
	}
	// Deliver persisted facts independently of opening or reconfiguring this
	// runtime. A now-unavailable desired model must not hide an earlier failure
	// from its parent, or hold ordinary progress behind a busy model switch.
	s.mu.Lock()
	r := s.runtimes[record.ID]
	s.mu.Unlock()
	if r != nil {
		select {
		case <-r.ready:
			if r.openErr != nil {
				return r.openErr
			}
		default:
			r = nil
		}
	}
	var state core.RuntimeState
	var stateErr error
	if r != nil {
		r.mu.Lock()
		state = r.core.Load().State()
		r.mu.Unlock()
	} else {
		state, stateErr = s.CoreStore.LoadState(ctx, record.ID)
	}
	if stateErr == nil {
		if err := s.deliverSubagentHistoryLocked(ctx, record, state); err != nil && !errors.Is(err, errSubagentReportsPending) {
			return err
		}
		// A parent's unapplied report must not hold up recovery of an already
		// accepted new instruction for the child. Retry that terminal delivery
		// after reconciling activation below.
	} else if !errors.Is(stateErr, core.ErrNotFound) {
		return stateErr
	}
	if r == nil {
		if stateErr == nil {
			needed, err := s.subagentNeedsRuntime(ctx, record, state)
			if err != nil || !needed {
				return err
			}
		}
		var err error
		r, err = s.open(ctx, record.ID)
		if err != nil {
			return err
		}
	}
	r.mu.Lock()
	err := r.syncSubagentModel(ctx)
	if err == nil {
		err = r.startSubagentLocked(ctx)
	}
	if err == nil {
		var activation subagentActivation
		if readErr := s.Store.Maintenance(ctx, "subagent_activation", record.ID, &activation); readErr == nil && !activation.Complete {
			err = r.activateSubagentLocked(ctx, &activation)
		} else if readErr != nil && !domain.IsMissing(readErr) {
			err = readErr
		}
	}
	r.mu.Unlock()
	if err != nil {
		return err
	}
	// A process can stop after durable admission but before Continue starts a
	// Run. Reconcile the stable initial receipt, and interrupted started Runs,
	// without resuming a deliberately cancelled or failed Run.
	r.mu.Lock()
	initial, initialErr := r.core.Load().LookupInput(ctx, "subagent_initial_"+record.ID)
	if initialErr != nil && !errors.Is(initialErr, core.ErrNotFound) {
		r.mu.Unlock()
		return initialErr
	}
	// Use one snapshot for both the durable lifecycle and execution flags. A
	// fast failure between separate reads must not revive the retained input.
	snapshot := r.core.Load().Snapshot()
	lastRun := snapshot.State.LastRun
	shouldActivate := initialErr == nil && initial.State == core.InputPending && lastRun == nil
	shouldActivate = shouldActivate || lastRun != nil && lastRun.Status == "started"
	if shouldActivate && !snapshot.Running && !snapshot.Maintenance && !snapshot.Stopping {
		err = r.continueLocked(ctx)
	}
	r.mu.Unlock()
	if err != nil && !errors.Is(err, core.ErrBusy) && !errors.Is(err, core.ErrNotContinuable) {
		return err
	}
	return s.deliverSubagentHistoryLocked(ctx, record, r.core.Load().State())
}

// Only a cold child needs durable admission checks. Hot children already own
// their execution state and retain the ordinary synchronization path above.
func (s *Service) subagentNeedsRuntime(ctx context.Context, record domain.Session, state core.RuntimeState) (bool, error) {
	settings, err := s.childMetadata(ctx, record.ID)
	if err != nil {
		return false, err
	}
	if !settings.started || state.LastRun == nil || state.LastRun.Status == "started" || state.CurrentAttemptID != "" || !state.Branch.Complete() || state.PendingInputs[core.InputContext] > 0 {
		return true, nil
	}
	selection, err := s.selectionAtState(ctx, record.ID, state)
	if err != nil {
		return false, err
	}
	if selection.fingerprint != settings.fingerprint {
		return true, nil
	}
	var activation subagentActivation
	if err := s.Store.Maintenance(ctx, "subagent_activation", record.ID, &activation); err == nil {
		return !activation.Complete, nil
	} else if !domain.IsMissing(err) {
		return false, err
	}
	return false, nil
}

func (s *Service) deliverSubagentHistoryLocked(ctx context.Context, record domain.Session, state core.RuntimeState) error {
	var checkpoint subagentCheckpoint
	if err := s.Store.Maintenance(ctx, "subagent_delivery", record.ID, &checkpoint); err != nil && !domain.IsMissing(err) {
		return err
	}
	if checkpoint.Protocol != subagentDeliveryProtocol {
		// Earlier builds advanced past failed Runs without notifying the parent.
		// Rescan once on upgrade; authoritative input receipts deduplicate reports
		// and completed Runs that were already delivered.
		checkpoint = subagentCheckpoint{Protocol: subagentDeliveryProtocol}
	}
	if checkpoint.Version == state.Version {
		return nil
	}
	cursor := checkpoint.Cursor
	for {
		page, err := s.CoreStore.Scan(ctx, state.RolloutID, core.Query{SessionID: record.ID, Cursor: cursor, Limit: core.DefaultPageSize, MaxBytes: 128 << 20})
		if err != nil {
			return err
		}
		for _, entry := range page.Records {
			if entry.Entry.Sequence <= checkpoint.Sequence {
				continue
			}
			if err = s.deliverSubagentEntryLocked(ctx, record, entry.Entry); err != nil {
				return err
			}
			checkpoint.Sequence = entry.Entry.Sequence
			if err = s.Store.SaveMaintenance(ctx, "subagent_delivery", record.ID, "running", checkpoint); err != nil {
				return err
			}
		}
		if page.Next == "" {
			break
		}
		cursor = page.Next
		checkpoint.Cursor = cursor
		if err = s.Store.SaveMaintenance(ctx, "subagent_delivery", record.ID, "running", checkpoint); err != nil {
			return err
		}
	}
	checkpoint.Version = state.Version
	return s.Store.SaveMaintenance(ctx, "subagent_delivery", record.ID, "running", checkpoint)
}

// Admission, activation and Host acknowledgement are deliberately separate.
// The stable Core input receipt closes crashes between any of these steps.
type subagentActivation struct {
	Input    core.Input `json:"input"`
	Complete bool       `json:"complete"`
}

func (r *runtime) startSubagentLocked(ctx context.Context) error {
	metadata, err := r.service.childMetadata(ctx, r.id)
	if err != nil || metadata.started {
		return err
	}
	var settings subagentSettings
	if err := r.service.Store.Get(ctx, "subagent_settings", r.id, &settings); err != nil {
		return err
	}
	if settings.Started {
		return nil
	}
	// A shared fork can be durable before initialize applies the saved child
	// configuration. Reconcile it before initial admission as well, so reopening
	// that fork cannot silently lose an explicit child model selection.
	if !jsonEqual(r.core.Load().Configuration(), settings.Configuration) {
		if err := r.core.Load().Configure(ctx, settings.Configuration); err != nil {
			return err
		}
	}
	message := core.User(settings.Task)
	message.ContextKind = "astral.subagent_instruction"
	input := core.Input{ID: "subagent_initial_" + r.id, Digest: digest(settings.Task), Message: message}
	_, exists, err := r.existingReceipt(ctx, input.ID, input.Digest)
	if err != nil {
		return err
	}
	if !exists {
		if _, err = r.accept(ctx, input, core.InputSteer); err != nil {
			return err
		}
	}
	settings.Started = true
	return r.service.Store.Save(ctx, "subagent_settings", &settings.Meta, &settings)
}
func (r *runtime) activateSubagentLocked(ctx context.Context, activation *subagentActivation) error {
	receipt, exists, err := r.existingReceipt(ctx, activation.Input.ID, activation.Input.Digest)
	if err != nil {
		return err
	}
	if !exists {
		receipt, err = r.accept(ctx, activation.Input, core.InputSteer)
	} else if receipt.State == string(core.InputPending) {
		err = r.continueLocked(ctx)
	}
	if err != nil && !errors.Is(err, core.ErrBusy) && !errors.Is(err, core.ErrNotContinuable) {
		return err
	}
	snapshot := r.core.Load().Snapshot()
	activation.Complete = receipt.State != string(core.InputPending) || snapshot.Running
	state := "running"
	if activation.Complete {
		state = "complete"
	}
	return r.service.Store.SaveMaintenance(ctx, "subagent_activation", r.id, state, activation)
}
func assistantText(message *core.Message) string {
	if message == nil || message.Role != core.RoleAssistant {
		return ""
	}
	var result strings.Builder
	for _, part := range message.Content {
		if part.Type == "text" {
			result.WriteString(part.Text)
		}
	}
	return result.String()
}
func (s *Service) deliverSubagentEntry(ctx context.Context, child domain.Session, entry core.Entry) error {
	if err := lockSubagentContext(ctx, &s.subagentMu); err != nil {
		return err
	}
	defer s.subagentMu.Unlock()
	return s.deliverSubagentEntryLocked(ctx, child, entry)
}

// The caller holds subagentMu until the report and its checkpoint are durable.
func (s *Service) deliverSubagentEntryLocked(ctx context.Context, child domain.Session, entry core.Entry) error {
	text := assistantText(entry.Message)
	natural := entry.Run != nil && entry.Run.Status == "completed"
	failed := entry.Run != nil && entry.Run.Status == "failed"
	terminal := natural || failed
	if text == "" && !terminal {
		return nil
	}
	if _, err := s.subagentRecord(ctx, child.SubagentParentID, child.ID); err != nil {
		return err
	}
	parent, err := s.open(ctx, child.SubagentParentID)
	if err != nil {
		return err
	}
	parent.mu.Lock()
	defer parent.mu.Unlock()
	inputID := "subagent_report_" + child.ID + "_" + entry.ID
	metadata := map[string]string{"source": "subagent", "child_session_id": child.ID, "message_id": entry.ID, "run_id": entry.RunID}
	message := core.User(fmt.Sprintf("Subagent %s (%s) reports:\n%s", child.ID, child.Title, text))
	message.ContextKind = subagentUpdateKind
	message.Metadata = encode(metadata)
	if terminal {
		inputID = "subagent_run_end_" + child.ID + "_" + entry.RunID
		message = core.User(fmt.Sprintf("Subagent %s (%s) naturally ended Run %s. Review its reports and decide what to do next. This is a runtime event; no business task outcome has been classified.", child.ID, child.Title, entry.RunID))
		message.ContextKind = subagentUpdateKind
		message.Metadata = encode(metadata)
	}
	// Deduplicate the source fact before rendering in the current display zone.
	// An accepted message remains byte-identical after timezone changes or replay.
	hash := digest([]any{child.ID, entry.ID, entry.RunID, entry.Time.UTC(), text, natural})
	if failed {
		hash = digest([]any{child.ID, entry.ID, entry.RunID, entry.Time.UTC(), text, entry.Run.Status, entry.Run.Error})
	}
	receipt, lookupErr := parent.core.Load().LookupInput(ctx, inputID)
	if lookupErr == nil {
		// Earlier development builds hashed the untimestamped rendered message.
		if receipt.Digest != hash && receipt.Digest != digest(message) {
			return domain.Conflict("subagent event input ID was already used for different content")
		}
		// Acceptance and Continue are separate durable boundaries. A retry after a
		// crash must still reconcile activation for this terminal event.
		if terminal && receipt.State == core.InputPending {
			err = parent.continueLocked(ctx)
			if errors.Is(err, core.ErrBusy) || errors.Is(err, core.ErrNotContinuable) {
				return nil
			}
			return err
		}
		return nil
	}
	if !errors.Is(lookupErr, core.ErrNotFound) {
		return lookupErr
	}
	location, err := s.Timezone.Location(ctx)
	if err != nil {
		return err
	}
	at := entry.Time.In(location).Format(time.RFC3339Nano)
	if failed {
		message.Content = core.Text(fmt.Sprintf("Subagent %s (%s) failed Run %s at %s.\nError: %s\nReview the error and its reports, then decide what to do next. This is a runtime failure; no business task outcome has been classified.", child.ID, child.Title, entry.RunID, at, entry.Run.Error))
		metadata["run_status"] = entry.Run.Status
		metadata["run_error"] = entry.Run.Error
	} else if natural {
		message.Content = core.Text(fmt.Sprintf("Subagent %s (%s) naturally ended Run %s at %s. Review its reports and decide what to do next. This is a runtime event; no business task outcome has been classified.", child.ID, child.Title, entry.RunID, at))
	} else {
		message.Content = core.Text(fmt.Sprintf("Subagent %s (%s) reports [%s]:\n%s", child.ID, child.Title, at, text))
	}
	metadata["event_time"] = entry.Time.UTC().Format(time.RFC3339Nano)
	message.Metadata = encode(metadata)
	input := core.Input{ID: inputID, Digest: hash, Message: message}
	if !terminal {
		input.Message = core.Message{}
		input.Contexts = []core.Message{message}
		_, err = parent.accept(ctx, input, core.InputContext)
		return err
	}
	// Context acceptance is not application. Preserve the passive/driving
	// boundary without copying reports into a second input (which duplicates
	// them). Reconcile again once Core has applied every report before waking.
	cursor := ""
	for {
		page, e := s.CoreStore.Pending(ctx, parent.id, core.PendingQuery{Kind: core.EntryInputQueued, InputKind: core.InputContext, Cursor: cursor, Limit: core.DefaultPageSize, MaxBytes: 128 << 20})
		if e != nil {
			return e
		}
		for _, pending := range page.Records {
			if pending.Entry.Input == nil {
				continue
			}
			for _, context := range pending.Entry.Input.Contexts {
				var source map[string]string
				if context.ContextKind == subagentUpdateKind && json.Unmarshal(context.Metadata, &source) == nil && source["child_session_id"] == child.ID && source["run_id"] == entry.RunID {
					return errSubagentReportsPending
				}
			}
		}
		if page.Next == "" {
			break
		}
		cursor = page.Next
	}
	kind := core.InputFollowUp
	if parent.core.Load().Snapshot().Running {
		kind = core.InputSteer
	}
	_, err = parent.accept(ctx, input, kind)
	if err == nil {
		snapshot := parent.core.Load().Snapshot()
		receipt, lookupErr := parent.core.Load().LookupInput(ctx, input.ID)
		if lookupErr != nil {
			return lookupErr
		}
		if receipt.State == core.InputPending && !snapshot.Running {
			return errSubagentReportsPending
		}
	}
	return err
}

var errSubagentReportsPending = errors.New("subagent reports are awaiting context application")

// A tool waiting for a deletion/creation boundary must remain cancellable:
// deletion closes runtimes and joins their executing tools.
func lockSubagentContext(ctx context.Context, mu *sync.Mutex) error {
	timer := time.NewTicker(5 * time.Millisecond)
	defer timer.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if mu.TryLock() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
}
