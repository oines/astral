package acp

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcReply struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

func decodeRequest(raw []byte) (rpcRequest, *rpcError) {
	var request rpcRequest
	if !json.Valid(raw) {
		return request, &rpcError{Code: -32700, Message: "Parse error."}
	}
	if err := json.Unmarshal(raw, &request); err != nil || request.JSONRPC != "2.0" || request.Method == "" || !validRequestID(request.ID) {
		return rpcRequest{}, &rpcError{Code: -32600, Message: "Invalid request."}
	}
	return request, nil
}

func validRequestID(id json.RawMessage) bool {
	return len(id) == 0 || len(id) <= 1024 && (id[0] == '"' || id[0] == '-' || id[0] >= '0' && id[0] <= '9')
}

func decodeParams(raw json.RawMessage, dest any) *rpcError {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' || json.Unmarshal(raw, dest) != nil {
		return invalidParams("Expected an object with valid parameters.")
	}
	return nil
}

func invalidParams(message string) *rpcError {
	return &rpcError{Code: -32602, Message: message}
}

func toRPCError(err error) *rpcError {
	var failure *domain.Error
	if errors.As(err, &failure) {
		switch failure.Status {
		case 400:
			return invalidParams(failure.Message)
		case 403, 404:
			return &rpcError{Code: -32002, Message: "Session or resource is unavailable."}
		case 409:
			return &rpcError{Code: -32000, Message: failure.Message}
		}
	}
	if errors.Is(err, core.ErrBusy) {
		return &rpcError{Code: -32000, Message: "The session is busy."}
	}
	if errors.Is(err, core.ErrNotFound) || errors.Is(err, core.ErrClosed) {
		return &rpcError{Code: -32002, Message: "Session is unavailable."}
	}
	return &rpcError{Code: -32603, Message: "Astral could not complete the request."}
}

func (c *connection) fail(id json.RawMessage, err *rpcError) bool {
	return c.send(rpcReply{JSONRPC: "2.0", ID: id, Error: err}, false)
}

func (c *connection) reply(id json.RawMessage, result any, replay bool) bool {
	return c.send(rpcReply{JSONRPC: "2.0", ID: id, Result: result}, replay)
}

func (c *connection) notify(method string, params any, replay bool) bool {
	return c.send(rpcNotification{JSONRPC: "2.0", Method: method, Params: params}, replay)
}

func (c *connection) initialize(request rpcRequest) bool {
	var params struct {
		ProtocolVersion *uint16 `json:"protocolVersion"`
	}
	if err := decodeParams(request.Params, &params); err != nil || params.ProtocolVersion == nil {
		c.fail(request.ID, invalidParams("protocolVersion is required."))
		return false
	}
	return c.reply(request.ID, map[string]any{
		"protocolVersion": 1,
		"agentInfo":       map[string]any{"name": "astral", "title": "Astral", "version": "1"},
		"agentCapabilities": map[string]any{
			"loadSession":     true,
			"mcpCapabilities": map[string]bool{"http": true, "sse": true},
			"_meta":           map[string]any{"dev.oines.astral/websocket": 1, "dev.oines.astral/activityStatus": true},
		},
		"authMethods": []any{},
	}, false)
}

// Reserve a prompt or mark its cancellation in WebSocket receive order.
// Database, replay, and model work run after admission on bounded workers.
func (c *connection) prepare(request rpcRequest) (func() *rpcError, *rpcError) {
	switch request.Method {
	case "session/new", "session/load":
		return func() *rpcError { return c.setup(request) }, nil
	case "session/prompt":
		return c.preparePrompt(request)
	case "session/cancel":
		return c.prepareCancellation(request)
	case "initialize":
		return nil, &rpcError{Code: -32600, Message: "The connection is already initialized."}
	default:
		return nil, &rpcError{Code: -32601, Message: "Method not found."}
	}
}

type contentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Name     string `json:"name,omitempty"`
	URI      string `json:"uri,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

func promptContent(prompt []contentBlock) (string, []string, *rpcError) {
	var text strings.Builder
	var files []string
	for _, part := range prompt {
		switch part.Type {
		case "text":
			text.WriteString(part.Text)
		case "resource_link":
			if part.URI == "" {
				return "", nil, invalidParams("Resource links require a URI.")
			}
			if strings.HasPrefix(part.URI, "astral-file://") {
				files = append(files, part.URI)
			} else {
				text.WriteString("\n" + part.Name + " (" + part.URI + ")\n")
			}
		default:
			return "", nil, invalidParams("Prompt supports text and resource links.")
		}
	}
	if strings.TrimSpace(text.String()) == "" && len(files) == 0 {
		return "", nil, invalidParams("Prompt is empty.")
	}
	return text.String(), files, nil
}
