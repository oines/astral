package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/messages"
	"github.com/oines/astral/internal/modelbridge"
)

const hostPromptHeading = "Use spawn_agent proactively for long-running work, independent investigations and work that can proceed in parallel."

const commonSystemPrompt = hostPromptHeading + `
You may delegate a task at any stage, including after you have already made substantial progress. You are not required to personally finish work you have started.
You may delegate unfinished work partway through by spawning with inherited context and a clear handoff, keeping the user-facing conversation responsive. Subagents are persistent Sessions: reuse them through agent_control. Do not poll or block waiting; a subagent's natural Run end triggers its direct parent. Progress text is delivered without waking the parent.
Determine your role from the latest Host delegation User message for this Session. A user-facing root Session serves the user. A subagent serves only its direct parent: output progress, findings, blockers and results directly; the system automatically delivers these messages to that parent. Only the user-facing root can spawn subagents or use send_message; all subagents are refused. Copied identities and delegation messages describe earlier Sessions; the latest delegation User message establishes the current subagent identity and takes precedence over copied parent identity. The user-facing root manages only its direct subagents.
Messages headed "` + environmentSnapshotHeading + `" describe the current Session environment. Use the latest snapshot together with subsequent state updates; an update replaces only the fields it names. These snapshots and updates are passive information, not requests to act.
The latest Host identity User message identifies the current Session and its role; use it for API targets. It takes precedence over identities appearing in copied System text or historical context. System also provides the Bot identity.
File and shell tools use the selected Computer and Session working directory. Existing processes and file references retain their source Computer. Sessions sharing an AgentHome share its files; a shell cd or command workdir does not change the Session working directory.
The current environment lists available skills. Read a relevant SKILL.md before using it. When present, discovery.agents_md.content already contains the AGENTS.md from the current CWD. Follow it as the workspace instructions for work in that directory. After a CWD change, use the new directory's AGENTS.md; guidance from the previous directory is historical. Unavailable discovery does not mean files are absent.
When the latest environment provides local_api.capabilities_url, use exec_command on the selected Computer to GET it for capabilities such as history recall, chats and files. If local_api is null, use the tools available for the selected Computer. Command API requests, including discovery, require an Authorization: Bearer header using that command's ASTRAL_API_TOKEN environment variable. Reference the token variable in commands without printing, saving or placing its expanded value in process arguments. A credential belongs to one command, including while it runs asynchronously, and the Server can revoke it.`

const webSystemPrompt = commonSystemPrompt + `
Messages identify their source and author. A user-facing root Session outputs replies directly to the user on the web page.`

const imPromptHeading = "For a user-facing root Session bound to an IM chat,"

const imSystemPrompt = commonSystemPrompt + "\n" + imPromptHeading + ` text you output directly is shown only on the web page; the user in the IM chat cannot see it. Use send_message to send replies to that chat. The system supplies the bound destination. Messages identify their source and author. IM observations are passive context; an IM trigger asks you to handle the referenced message. Finishing without send_message sends nothing to the IM chat.
Each IM observation's conversation-id identifies its historical source; the current binding does not change the source of earlier observations.
` + messages.ChatMetadataNotice + "\n" + messages.ReactionContextNotice

const personaSeparator = "\n\n"
const identityHeading = "Current Astral identity:\n"

type promptIdentity struct {
	Agent struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"agent"`
	Session struct {
		ID             string `json:"id"`
		Source         string `json:"source"`
		ConversationID string `json:"conversation_id,omitempty"`
	} `json:"session"`
}

func systemPromptFor(record domain.Session) string {
	if record.BindingID != "" {
		// Keep the request's chat identity together with its selected tool schema.
		// Preparation expands this reference with fresh metadata for that chat.
		return imSystemPrompt + personaSeparator + messages.ChatContextHeading + "\nconversation-id: " + string(encode(record.ConversationID))
	}
	return webSystemPrompt
}

func composeSystemPrompt(hostPrompt, persona string) string {
	if strings.TrimSpace(persona) == "" {
		return hostPrompt
	}
	return hostPrompt + personaSeparator + persona
}

func (r *runtime) currentSystemPrompt(ctx context.Context, hostPrompt string) (string, error) {
	var agent domain.Agent
	if err := r.service.Store.Get(ctx, "agents", r.agentID, &agent); err != nil {
		return "", err
	}
	var record domain.Session
	if err := r.service.Store.Get(ctx, "sessions", r.prefixSourceID(), &record); err != nil {
		return "", err
	}
	if record.Deletion != nil || record.AgentID != r.agentID {
		return "", domain.Forbidden("Session prefix source is unavailable")
	}
	var identity promptIdentity
	identity.Agent.ID, identity.Agent.Name = agent.ID, agent.Name
	identity.Session.ID, identity.Session.Source = record.ID, record.Source
	if hostInstructions, _, _ := strings.Cut(hostPrompt, personaSeparator); strings.Contains(hostInstructions, "\n"+imPromptHeading) {
		conversationID, err := promptConversationID(hostPrompt)
		if err != nil {
			return "", err
		}
		identity.Session.ConversationID = conversationID
		var conversation domain.Conversation
		if err := r.service.Store.Get(ctx, "conversations", conversationID, &conversation); err != nil {
			return "", err
		}
		var connection domain.Connection
		if err := r.service.Store.Get(ctx, "connections", conversation.ConnectionID, &connection); err != nil {
			return "", err
		}
		if connection.AgentID != r.agentID {
			return "", domain.Forbidden("chat account belongs to another Bot")
		}
		hostPrompt = imSystemPrompt + personaSeparator + messages.ChatContext(connection, conversation)
	} else {
		hostPrompt = webSystemPrompt
	}
	hostPrompt += personaSeparator + identityHeading + string(encode(identity))
	return composeSystemPrompt(hostPrompt, agent.Persona), nil
}

func (r *runtime) prefixSourceID() string {
	if r.prefixRootID != "" {
		return r.prefixRootID
	}
	return r.id
}

// Fixed Host instructions contain no blank paragraphs. Their first metadata
// block therefore identifies the selected variant even after wording changes.
// Later blocks, including a persona's examples, cannot select another chat.
func hostPromptMetadata(prompt string) (string, bool) {
	if !strings.HasPrefix(prompt, hostPromptHeading+"\n") {
		return "", false
	}
	_, tail, found := strings.Cut(prompt, personaSeparator)
	if !found {
		return "", true
	}
	block, _, _ := strings.Cut(tail, personaSeparator)
	return block, true
}

// promptConversationID only reads the first Host-owned metadata block.
func promptConversationID(prompt string) (string, error) {
	metadata, host := hostPromptMetadata(prompt)
	block, ok := strings.CutPrefix(metadata, messages.ChatContextHeading+"\n")
	if !host || !ok {
		return "", domain.Invalid("IM system prompt is missing its chat reference")
	}
	for line := range strings.SplitSeq(block, "\n") {
		if value, ok := strings.CutPrefix(line, "conversation-id: "); ok {
			var id string
			if err := json.Unmarshal([]byte(value), &id); err != nil || id == "" {
				return "", domain.Invalid("IM system prompt has an invalid chat reference")
			}
			return id, nil
		}
	}
	return "", domain.Invalid("IM system prompt is missing its chat reference")
}

// sessionModel prepares current identity, Bot persona and IM chat metadata before
// Core measures and freezes the request. An in-flight request, retry or recovered
// attempt retains its frozen prompt. Auxiliary calls are not replaced.
type sessionModel struct {
	core.Model
	runtime   *runtime
	selection core.ModelSelection
}

func (m sessionModel) Stream(ctx context.Context, request core.Request, emit func(core.ModelEvent)) (core.Reply, error) {
	return m.stream(ctx, request, emit, func(ctx context.Context, emit func(core.ModelEvent)) (core.Reply, error) {
		return m.Model.Stream(ctx, request, emit)
	})
}

func (m sessionModel) stream(ctx context.Context, request core.Request, emit func(core.ModelEvent), invoke func(context.Context, func(core.ModelEvent)) (core.Reply, error)) (core.Reply, error) {
	if err := m.runtime.requireRunnable(ctx); err != nil {
		return core.Reply{}, err
	}
	if _, host := hostPromptMetadata(request.SystemPrompt); m.runtime.subagent != nil && host {
		// Preparation can temporarily refuse SelectModel. Once this request is
		// frozen, apply the saved selection for its successor before sending it.
		// Auxiliary compaction requests have their own System prompt and can
		// run inside tree-locked passive admission, so must not take this lock.
		s := m.runtime.service
		if err := lockSubagentContext(ctx, &s.subagentMu); err != nil {
			return core.Reply{}, err
		}
		err := m.runtime.syncSubagentModel(ctx)
		s.subagentMu.Unlock()
		if err != nil && !errors.Is(err, core.ErrBusy) {
			return core.Reply{}, err
		}

	}
	reply, err := invoke(ctx, emit)
	if err != nil {
		return reply, m.runtime.modelFallbackCallError(ctx, m.selection, err)
	}
	if err := m.runtime.recordModelFallbackSuccess(ctx); err != nil {
		return reply, err
	}
	return reply, nil
}

func (m sessionModel) PrepareExecution(ctx context.Context, request core.Request) (core.RequestExecution, error) {
	preparer, ok := m.Model.(core.RequestExecutionPreparer)
	if !ok {
		return nil, nil
	}
	execution, err := preparer.PrepareExecution(ctx, request)
	if err != nil {
		return execution, m.runtime.modelFallbackCallError(ctx, m.selection, err)
	}
	if execution == nil {
		return nil, nil
	}
	return core.RequestExecutionFunc(func(ctx context.Context, emit func(core.ModelEvent)) (core.Reply, error) {
		return m.stream(ctx, request, emit, execution.Stream)
	}), nil
}

func (m sessionModel) PrepareRequest(ctx context.Context, request core.Request) (core.Request, error) {
	var err error
	if preparer, ok := m.Model.(core.RequestPreparer); ok {
		request, err = preparer.PrepareRequest(ctx, request)
		if err != nil {
			return request, err
		}
	}
	return m.preparePrompt(ctx, request)
}

func (m sessionModel) ProjectRequest(ctx context.Context, request core.Request) (core.Request, error) {
	var projected core.Request
	var err error
	if projector, ok := m.Model.(core.RequestProjector); ok {
		projected, err = projector.ProjectRequest(ctx, request)
	} else {
		projected, err = core.PrepareRequest(ctx, m.Model, request)
	}
	if err != nil {
		return core.Request{}, err
	}
	return m.preparePrompt(ctx, projected)
}

func (m sessionModel) preparePrompt(ctx context.Context, request core.Request) (core.Request, error) {
	var err error
	// Keep the Host variant and chat identity selected with the tool schema.
	// Only metadata and persona are fresh; never re-read the Session's binding.
	if _, host := hostPromptMetadata(request.SystemPrompt); host {
		request.SystemPrompt, err = m.runtime.currentSystemPrompt(ctx, request.SystemPrompt)
	}
	return request, err
}

func (m sessionModel) EstimateTokens(ctx context.Context, request core.Request) (int, error) {
	return modelbridge.EstimateTokens(ctx, m.Model, request)
}

func (m sessionModel) ValidateRequest(ctx context.Context, request core.Request) error {
	if validator, ok := m.Model.(core.RequestValidator); ok {
		if err := validator.ValidateRequest(ctx, request); err != nil {
			return errors.Join(err, m.runtime.recordModelFallbackFailure(ctx, m.selection, err))
		}
	}
	return nil
}

func (m sessionModel) ValidateRequestReadOnly(ctx context.Context, request core.Request) error {
	return core.ValidateRequest(ctx, m.Model, request)
}

func (m sessionModel) RecoverRequest(ctx context.Context, request core.Request, cause error) (core.Request, bool, error) {
	// Core already isolated the request at its public recovery boundary.
	if recoverer, ok := m.Model.(core.ModelRequestRecoverer); ok {
		return recoverer.RecoverRequest(ctx, request, cause)
	}
	return core.Request{}, false, nil
}
