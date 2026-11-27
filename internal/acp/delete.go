package acp

import (
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func (c *connection) deleteSession(request rpcRequest) *rpcError {
	var params struct {
		SessionID string `json:"sessionId"`
	}
	if err := decodeParams(request.Params, &params); err != nil {
		return err
	}
	if params.SessionID == "" {
		return invalidParams("sessionId is required.")
	}

	// A session outside the token's view is indistinguishable from one that
	// does not exist. Pass the Bot scope through to the archive permission check.
	_, err := c.allowedSession(c.ctx, params.SessionID)
	if err == nil {
		archived := true
		ctx := persistence.WithSessionScope(c.ctx, c.botID, "")
		err = c.service.harness.SaveAgentSessionPresentation(ctx, c.botID, params.SessionID, nil, &archived)
	}
	if err != nil && !domain.IsMissing(err) {
		return toRPCError(err)
	}
	c.reply(request.ID, struct{}{}, false)
	return nil
}
