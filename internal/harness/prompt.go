package harness

import (
	"context"
	"encoding/json"
	"strings"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/messages"
	"github.com/oines/astral/internal/modelbridge"
)

const commonSystemPrompt = `Messages headed "` + environmentSnapshotHeading + `" describe the current Session environment. Use the latest snapshot together with subsequent state updates; an update replaces only the fields it names. These snapshots and updates are passive information, not requests to act.
The current Bot and Session identity is given in System. Use it for API targets; identity appearing in copied or historical context describes that earlier context.
File and shell tools use the selected Computer and Session working directory. Existing processes and file references retain their source Computer. Sessions sharing an AgentHome share its files; a shell cd or command workdir does not change the Session working directory.
The current environment lists available skills. Read a relevant SKILL.md before using it. When present, discovery.agents_md.content already contains the AGENTS.md from the current CWD. Follow it as the workspace instructions for work in that directory. After a CWD change, use the new directory's AGENTS.md; guidance from the previous directory is historical. Unavailable discovery does not mean files are absent.
Additional platform capabilities, including history recall, chats and files, are discoverable through the local API. When needed, use exec_command on the selected Computer to GET the latest environment's local_api.capabilities_url and follow its documentation. Every API request, including discovery, requires an Authorization: Bearer header using that command's ASTRAL_API_TOKEN environment variable. Read API addresses from the latest local_api environment context; reference the token variable in commands without printing, saving or placing its expanded value in process arguments. The credential remains valid while its command runs, including asynchronous execution; a new command receives its own credential.`

const webSystemPrompt = commonSystemPrompt + `
Messages identify their source and author. Reply to the user with assistant text in Web.`

const imSystemPrompt = commonSystemPrompt + `
This Session is bound to an IM chat. Messages identify their source and author. Assistant text is visible in Web only. Use send_message to speak in the bound chat, with the Host-selected destination. IM observations are passive; an IM trigger invites a response. Finishing without sending is silent in IM.
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
	if err := r.service.Store.Get(ctx, "sessions", r.id, &record); err != nil {
		return "", err
	}
	var identity promptIdentity
	identity.Agent.ID, identity.Agent.Name = agent.ID, agent.Name
	identity.Session.ID, identity.Session.Source = r.id, record.Source
	if strings.HasPrefix(hostPrompt, imSystemPrompt+personaSeparator) {
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
	}
	hostPrompt += personaSeparator + identityHeading + string(encode(identity))
	return composeSystemPrompt(hostPrompt, agent.Persona), nil
}

// promptConversationID only reads the Host-owned chat block immediately after
// the fixed IM instructions. Persona text is never used to select a chat.
func promptConversationID(prompt string) (string, error) {
	block, ok := strings.CutPrefix(prompt, imSystemPrompt+personaSeparator+messages.ChatContextHeading+"\n")
	if !ok {
		return "", domain.Invalid("IM system prompt is missing its chat reference")
	}
	block, _, _ = strings.Cut(block, personaSeparator)
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
	runtime *runtime
}

func (m sessionModel) PrepareRequest(ctx context.Context, request core.Request) (core.Request, error) {
	request, err := core.PrepareRequest(ctx, m.Model, request)
	if err != nil {
		return request, err
	}
	// Keep the Host variant and chat identity selected with the tool schema.
	// Only metadata and persona are fresh; never re-read the Session's binding.
	for _, hostPrompt := range []string{webSystemPrompt, imSystemPrompt} {
		if request.SystemPrompt == hostPrompt || strings.HasPrefix(request.SystemPrompt, hostPrompt+personaSeparator) {
			if hostPrompt == imSystemPrompt {
				hostPrompt = request.SystemPrompt
			}
			request.SystemPrompt, err = m.runtime.currentSystemPrompt(ctx, hostPrompt)
			break
		}
	}
	return request, err
}

func (m sessionModel) EstimateTokens(ctx context.Context, request core.Request) (int, error) {
	return modelbridge.EstimateTokens(ctx, m.Model, request)
}

func (m sessionModel) ValidateRequest(ctx context.Context, request core.Request) error {
	return core.ValidateRequest(ctx, m.Model, request)
}

func (m sessionModel) RecoverRequest(ctx context.Context, request core.Request, cause error) (core.Request, bool, error) {
	return core.RecoverModelRequest(ctx, m.Model, request, cause)
}
