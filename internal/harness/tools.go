package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral-core/standardtools"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/messages"
	"github.com/oines/astral/internal/persistence"
	"github.com/oines/astral/internal/search"
)

type hostTool struct {
	core.Tool
	runtime *runtime
}
type processHandle struct {
	domain.Meta
	RuntimeID  string                  `json:"runtime_id"`
	AgentID    string                  `json:"agent_id"`
	SessionID  string                  `json:"session_id"`
	InternalID int                     `json:"internal_id"`
	Binding    domain.ExecutionBinding `json:"binding"`
	Retired    bool                    `json:"retired"`
}

func (r *runtime) hostTools() []core.Tool {
	var tools []core.Tool
	for _, tool := range r.tools.Tools() {
		tools = append(tools, hostTool{Tool: tool, runtime: r})
	}
	tools = append(tools,
		sendMessageTool{runtime: r},
		core.ToolFunc{Spec: core.ToolDefinition{Name: "switch_computer", Version: "astral-1", Description: "Select an allowed Computer for subsequent file and command calls. Call alone. Empty cwd uses this Agent's home on the target. Existing process and file references keep their original Computer.", Exclusive: true,
			Parameters: json.RawMessage(`{"type":"object","properties":{"computer_id":{"type":"string","minLength":1},"cwd":{"type":"string"}},"required":["computer_id"],"additionalProperties":false}`)},
			Run: func(ctx context.Context, inv core.Invocation, _ func(core.ToolProgress)) (core.ToolExecution, error) {
				var args struct {
					ComputerID string `json:"computer_id"`
					CWD        string `json:"cwd"`
				}
				if err := json.Unmarshal(inv.Call.Arguments, &args); err != nil {
					return core.ToolExecution{}, notExecuted("invalid_computer", err)
				}
				r.mu.Lock()
				b, err := r.selectComputer(ctx, args.ComputerID, args.CWD)
				r.mu.Unlock()
				if err != nil {
					return core.ToolExecution{}, notExecuted("computer_selection", err)
				}
				return resultJSON(map[string]string{"computer_id": b.ComputerID, "cwd": b.CWD, "agent_home": b.AgentHome, "shell": b.Shell, "api_url": b.APIURL}), nil
			}},
		core.ToolFunc{Spec: core.ToolDefinition{Name: "web_search", Version: "astral-2", Description: "Search the web with this Agent's selected search provider. Only query is required; omit domains, recency (days), and limit to use the provider's defaults. Optional filters are passed to that provider; unsupported filters return an error. Returns provider_id, query and a results array. Every result has string title, url and summary fields, plus available date/author metadata. Summaries retain source-provided content or excerpts and remain empty if none are available; metadata alone does not contain page content. Results and content are not locally truncated. Errors are reported as failures, not empty results. There is no fallback provider.",
			Parameters: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","minLength":1},"domains":{"type":"array","items":{"type":"string"},"description":"Restrict to these domain names (no scheme or path)."},"recency":{"type":"integer","minimum":1,"description":"Restrict to the past N days."},"limit":{"type":"integer","minimum":1,"description":"Requested result count, passed directly to the provider."}},"required":["query"],"additionalProperties":false}`)},
			Run: func(ctx context.Context, inv core.Invocation, _ func(core.ToolProgress)) (core.ToolExecution, error) {
				var args search.Request
				if err := json.Unmarshal(inv.Call.Arguments, &args); err != nil {
					return core.ToolExecution{}, notExecuted("invalid_search", err)
				}
				response, err := r.service.Search.Search(ctx, r.agentID, args)
				if err != nil {
					return core.ToolExecution{}, &core.ToolError{Code: "search_failed", Outcome: core.OutcomeFailed, Cause: err}
				}
				return resultJSON(response), nil
			}},
	)
	return tools
}
func resultJSON(value any) core.ToolExecution {
	return core.ToolExecution{Result: &core.ToolResult{Content: core.Text(string(encode(value))), Details: encode(value), Outcome: core.OutcomeSucceeded}}
}
func notExecuted(code string, err error) error {
	return &core.ToolError{Code: code, Outcome: core.OutcomeNotExecuted, Cause: err}
}

func (t hostTool) Definition() core.ToolDefinition {
	def := t.Tool.Definition()
	def.Version = "astral-2/" + def.Version
	return def
}
func (t hostTool) Bind(ctx context.Context, inv core.Invocation) (json.RawMessage, error) {
	r := t.runtime
	if inv.SessionID != r.id {
		return nil, notExecuted("session_mismatch", domain.Forbidden("tool belongs to another Session"))
	}
	if t.Definition().Name == "write_stdin" {
		mapped, handle, err := t.translate(ctx, inv)
		if err != nil {
			return nil, err
		}
		if err = r.service.Hub.Validate(ctx, handle.Binding); err != nil {
			return nil, notExecuted("original_computer", err)
		}
		original, err := t.Tool.(core.TargetedTool).Bind(ctx, mapped)
		if err != nil {
			return nil, err
		}
		if !jsonEqual(json.RawMessage(original), json.RawMessage(encode(handle.Binding))) {
			return nil, notExecuted("process_binding", domain.Conflict("original process binding changed"))
		}
		return original, nil
	}
	b, err := r.currentBinding(ctx)
	if err != nil {
		return nil, notExecuted("computer_unavailable", err)
	}
	return encode(b), nil
}

func (t hostTool) translate(ctx context.Context, inv core.Invocation) (core.Invocation, processHandle, error) {
	var args standardtools.StdinRequest
	if err := json.Unmarshal(inv.Call.Arguments, &args); err != nil {
		return inv, processHandle{}, notExecuted("process_handle", err)
	}
	var handle processHandle
	if err := t.runtime.service.Store.Get(ctx, "process_handles", strconv.Itoa(args.SessionID), &handle); err != nil {
		return inv, handle, notExecuted("stale_process_handle", domain.Missing("process handle", strconv.Itoa(args.SessionID)))
	}
	if handle.RuntimeID != t.runtime.service.RuntimeID || handle.AgentID != t.runtime.agentID || handle.SessionID != inv.SessionID || handle.Retired {
		return inv, handle, notExecuted("stale_process_handle", domain.Conflict("this process handle is retired or belongs to an earlier Server runtime"))
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(inv.Call.Arguments, &raw); err != nil {
		return inv, handle, err
	}
	raw["session_id"] = encode(handle.InternalID)
	inv.Call.Arguments = encode(raw)
	return inv, handle, nil
}

func (t hostTool) Execute(ctx context.Context, inv core.Invocation, emit func(core.ToolProgress)) (execution core.ToolExecution, callErr error) {
	defer func() {
		if execution.Result != nil && execution.Result.Outcome == core.OutcomeUnknown {
			execution.Result.Content = append(core.Text("The tool response was interrupted; its effects are unconfirmed."), execution.Result.Content...)
			execution.Result.Outcome, execution.Result.IsError = core.OutcomeFailed, true
		}
		var classified *core.ToolError
		if errors.As(callErr, &classified) {
			if classified.Outcome == core.OutcomeUnknown {
				result := *classified
				result.Outcome = core.OutcomeFailed
				result.Message = "The tool response was interrupted; its effects are unconfirmed. " + classified.Error()
				callErr = &result
			}
		} else if errors.Is(callErr, context.Canceled) || errors.Is(callErr, context.DeadlineExceeded) {
			callErr = &core.ToolError{Outcome: core.OutcomeFailed, Message: "The tool response was interrupted; its effects are unconfirmed. " + callErr.Error(), Cause: callErr}
		}
	}()
	r := t.runtime
	original := inv
	var handle processHandle
	var err error
	if t.Definition().Name == "write_stdin" {
		inv, handle, err = t.translate(ctx, inv)
		if err != nil {
			return core.ToolExecution{}, err
		}
		if err = r.service.Hub.Validate(ctx, handle.Binding); err != nil {
			return core.ToolExecution{}, notExecuted("original_computer", err)
		}
	}
	var binding domain.ExecutionBinding
	if json.Unmarshal(inv.Binding, &binding) != nil || binding.SessionID != r.id || binding.AgentID != r.agentID {
		return core.ToolExecution{}, notExecuted("invalid_binding", domain.Forbidden("tool has no valid execution binding"))
	}
	// Authorize every dispatch, including interactions with an existing process.
	if err = r.service.Hub.Validate(ctx, binding); err != nil {
		return core.ToolExecution{}, notExecuted("computer_unavailable", err)
	}
	execution, callErr = t.Tool.Execute(ctx, inv, emit)
	if execution.Result == nil {
		return execution, callErr
	}
	result := execution.Result
	persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if t.Definition().Name == "exec_command" || t.Definition().Name == "write_stdin" {
		var command standardtools.CommandResult
		if json.Unmarshal(result.Details, &command) == nil {
			if command.SessionID > 0 {
				internalID := command.SessionID
				if handle.ID == "" {
					handle, err = r.registerHandle(persist, internalID, binding)
					if err != nil {
						r.processMu.Lock()
						process := r.processes[original.ExecutionID]
						r.processMu.Unlock()
						var stopErr error
						if process != nil {
							stopErr = process.Terminate(persist)
						}
						return core.ToolExecution{Result: &core.ToolResult{Content: core.Text("Command started, but its process handle could not be recorded. Termination was requested. Reconcile its effects before repeating."), Outcome: core.OutcomeUnknown}},
							&core.ToolError{Code: "process_handle_unrecorded", Outcome: core.OutcomeUnknown, Cause: errors.Join(err, stopErr)}
					}
				}
				publicID, _ := strconv.Atoi(handle.ID)
				command.SessionID = publicID
				result.Details = encode(command)
				for i := range result.Content {
					if result.Content[i].Type == "text" {
						result.Content[i].Text = strings.Replace(result.Content[i].Text, fmt.Sprintf("Process running with session ID %d\n", internalID), fmt.Sprintf("Process running with session ID %d\n", publicID), 1)
					}
				}
			} else if handle.ID != "" {
				handle.Retired = true
				if err = r.service.Store.Save(persist, "process_handles", &handle.Meta, &handle); err != nil {
					return execution, errors.Join(callErr, err)
				}
			}
		}
	}
	if err = r.service.Files.PersistToolImages(persist, binding, result); err != nil {
		return core.ToolExecution{Result: &core.ToolResult{Content: core.Text("The tool ran, but its image attachment could not be recorded: " + err.Error()), Outcome: result.Outcome, IsError: true, Code: "image_delivery_failed"}}, callErr
	}
	var files []domain.FileRef
	for _, part := range result.Content {
		if part.Type == "image" {
			file, getErr := r.service.Files.Get(persist, r.agentID, part.URL)
			if getErr != nil {
				return execution, getErr
			}
			files = append(files, file)
		}
	}
	if len(files) > 0 {
		description, _ := messages.AttachmentParts(files)
		result.Content = append(core.Text(description), result.Content...)
	}
	return execution, callErr
}

func (r *runtime) finishInterruptedTools(ctx context.Context) error {
	session := r.core.Load()
	if snapshot := session.Snapshot(); snapshot.Running || snapshot.Maintenance {
		return nil
	}
	for _, record := range session.UnresolvedTools() {
		result := core.ToolResult{Content: core.Text("The tool request was interrupted; its effects are unconfirmed."), Outcome: core.OutcomeFailed, IsError: true}
		if record.Result != nil {
			result = *record.Result
			result.Content = append(core.Text("The tool response was interrupted; its effects are unconfirmed."), result.Content...)
			result.Outcome, result.IsError = core.OutcomeFailed, true
		}
		if record.Invocation.Call.Name == "send_message" {
			result.Content = core.Text(`{"sent":null,"error":"message send attempt was interrupted"}`)
		}
		if err := session.ResolveTool(ctx, record.Invocation.ExecutionID, core.ToolExecution{Result: &result}); err != nil {
			return err
		}
	}
	return nil
}

func (r *runtime) registerHandle(ctx context.Context, internalID int, b domain.ExecutionBinding) (processHandle, error) {
	handle := processHandle{RuntimeID: r.service.RuntimeID, AgentID: r.agentID, SessionID: r.id, InternalID: internalID, Binding: b}
	err := r.service.Store.Update(ctx, func(tx *persistence.Tx) error {
		var counter struct {
			domain.Meta
			Value int `json:"value"`
		}
		err := tx.Get(ctx, "counters", "process", &counter)
		if err != nil && !domain.IsMissing(err) {
			return err
		}
		counter.ID = "process"
		if counter.Value >= 1<<53-1 {
			return domain.Conflict("process handle space exhausted")
		}
		counter.Value++
		if err = tx.Save(ctx, "counters", &counter.Meta, &counter); err != nil {
			return err
		}
		handle.ID = strconv.Itoa(counter.Value)
		return tx.Save(ctx, "process_handles", &handle.Meta, &handle)
	})
	return handle, err
}

type trackedBackend struct {
	runtime *runtime
	backend standardtools.Backend
}

func (b *trackedBackend) FileSystem(ctx context.Context, inv core.Invocation) (standardtools.FileSystem, error) {
	return b.backend.FileSystem(ctx, inv)
}
func (b *trackedBackend) StartProcess(ctx context.Context, inv core.Invocation, request standardtools.ProcessRequest) (standardtools.Process, error) {
	process, err := b.backend.StartProcess(ctx, inv, request)
	if err != nil {
		return nil, err
	}
	b.runtime.processMu.Lock()
	b.runtime.processes[inv.ExecutionID] = process
	b.runtime.processMu.Unlock()
	return &trackedProcess{Process: process, runtime: b.runtime, execution: inv.ExecutionID}, nil
}

type trackedProcess struct {
	standardtools.Process
	runtime   *runtime
	execution string
}

func (p *trackedProcess) Wait() (int, error) {
	defer func() {
		p.runtime.processMu.Lock()
		delete(p.runtime.processes, p.execution)
		p.runtime.processMu.Unlock()
	}()
	return p.Process.Wait()
}
