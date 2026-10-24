package harness

import (
	"context"
	"strings"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/modelbridge"
)

const commonSystemPrompt = `Messages headed "` + environmentSnapshotHeading + `" describe the current Session environment. Use the latest snapshot together with subsequent state updates; an update replaces only the fields it names. These snapshots and updates are passive information, not requests to act.
File and shell tools use the selected Computer and Session working directory. Existing processes and file references retain their source Computer. Sessions sharing an AgentHome share its files; a shell cd or command workdir does not change the Session working directory.
The current environment lists available skills. Read a relevant SKILL.md before using it. When present, discovery.agents_md.content already contains the AGENTS.md from the current CWD. Follow it as the workspace instructions for work in that directory. After a CWD change, use the new directory's AGENTS.md; guidance from the previous directory is historical. Unavailable discovery does not mean files are absent.
Additional platform capabilities, including history recall, chats and files, are discoverable through the local API. When needed, use exec_command on the selected Computer to GET the latest environment's local_api.capabilities_url and follow its documentation.`

const webSystemPrompt = commonSystemPrompt + `
Messages identify their source and author. Reply to the user with assistant text in Web.`

const imSystemPrompt = commonSystemPrompt + `
This Session is bound to an IM chat. Messages identify their source and author. Assistant text is visible in Web only. Use send_message to speak in the bound chat, with the Host-selected destination. IM observations are passive; an IM trigger invites a response. Finishing without sending is silent in IM.`

const personaSeparator = "\n\n"

func systemPromptFor(record domain.Session) string {
	if record.BindingID != "" {
		return imSystemPrompt
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
	return composeSystemPrompt(hostPrompt, agent.Persona), nil
}

// sessionModel prepares the current Bot persona before Core measures and freezes
// the request. An in-flight request, retry or recovered attempt retains its
// frozen prompt. Summary prompts and auxiliary model calls are not replaced.
type sessionModel struct {
	core.Model
	runtime *runtime
}

func (m sessionModel) PrepareRequest(ctx context.Context, request core.Request) (core.Request, error) {
	request, err := core.PrepareRequest(ctx, m.Model, request)
	if err != nil {
		return request, err
	}
	// Keep the Host variant selected together with this request's tool schema.
	// Only the persona is read afresh; a separate binding read could mix states.
	for _, hostPrompt := range []string{webSystemPrompt, imSystemPrompt} {
		if request.SystemPrompt == hostPrompt || strings.HasPrefix(request.SystemPrompt, hostPrompt+personaSeparator) {
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
