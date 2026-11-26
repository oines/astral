package gateway

import (
	"encoding/json"
	"errors"
	"net/http"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/httpcontract"
	"github.com/oines/astral/internal/mcpclient"
)

type MCPToolCall struct {
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

func (s *Service) mcpRoutes() {
	register[httpcontract.Empty, []core.ToolDefinition](s, "GET", "/v1/mcp/tools", "listMountedMCPTools", func(r *http.Request, _ httpcontract.Empty) ([]core.ToolDefinition, error) {
		return s.Harness.MCPTools(r.Context(), binding(r).SessionID)
	})
	register[MCPToolCall, mcpclient.Result](s, "POST", "/v1/mcp/tools/{name}/call", "callMountedMCPTool", func(r *http.Request, input MCPToolCall) (mcpclient.Result, error) {
		result, err := s.Harness.CallMCPTool(r.Context(), binding(r).SessionID, r.PathValue("name"), input.Arguments)
		if errors.Is(err, mcpclient.ErrLimit) {
			return mcpclient.Result{}, domain.Invalid("the MCP response exceeded the result limit")
		}
		if errors.Is(err, mcpclient.ErrRemote) {
			return mcpclient.Result{}, domain.Offline("the remote MCP call failed")
		}
		return result, err
	})
}
