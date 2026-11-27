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
	Data    any    `json:"data,omitempty"`
}

const (
	rpcConflict   = 1001
	rpcOverloaded = 1002
)

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
	return len(id) == 0 || len(id) <= 1024 && (id[0] == '"' || id[0] == '-' || id[0] >= '0' && id[0] <= '9' || bytes.Equal(id, []byte("null")))
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
			return &rpcError{Code: rpcConflict, Message: failure.Message}
		}
	}
	if errors.Is(err, core.ErrBusy) {
		return &rpcError{Code: rpcConflict, Message: "The session is busy."}
	}
	if errors.Is(err, core.ErrInvalid) {
		return invalidParams("Astral could not apply the supplied parameters.")
	}
	if errors.Is(err, core.ErrNotFound) || errors.Is(err, core.ErrClosed) {
		return &rpcError{Code: -32002, Message: "Session is unavailable."}
	}
	return &rpcError{Code: -32603, Message: "Astral could not complete the request."}
}

func (c *connection) fail(id json.RawMessage, err *rpcError) bool {
	return c.send(rpcReply{JSONRPC: "2.0", ID: id, Error: err}, false)
}

func (c *connection) failRequest(request rpcRequest, err *rpcError) {
	if err.Code == -32002 && err.Data == nil {
		var params struct {
			SessionID string `json:"sessionId"`
		}
		if decodeParams(request.Params, &params) == nil && params.SessionID != "" {
			withSession := *err
			withSession.Data = params
			err = &withSession
		}
	}
	c.fail(request.ID, err)
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
		Capabilities    struct {
			Subagents *struct{} `json:"subagents"`
			Session   struct {
				Compaction *struct{} `json:"compaction"`
			} `json:"session"`
		} `json:"clientCapabilities"`
	}
	if err := decodeParams(request.Params, &params); err != nil {
		c.fail(request.ID, err)
		return false
	}
	if params.ProtocolVersion == nil {
		c.fail(request.ID, invalidParams("protocolVersion is required."))
		return false
	}
	c.sessionCompaction = params.Capabilities.Session.Compaction != nil
	c.clientSubagents = params.Capabilities.Subagents != nil
	return c.reply(request.ID, map[string]any{
		"protocolVersion": 1,
		"agentInfo":       map[string]any{"name": "astral", "title": "Astral", "version": "1"},
		"agentCapabilities": map[string]any{
			"loadSession":         true,
			"promptCapabilities":  map[string]bool{"image": true},
			"mcpCapabilities":     map[string]bool{"http": true, "sse": true},
			"sessionCapabilities": map[string]struct{}{"list": {}, "resume": {}, "close": {}, "delete": {}},
			"_meta":               map[string]any{"dev.oines.astral/websocket": 1, "dev.oines.astral/activityStatus": true},
		},
		"authMethods": []any{},
	}, false)
}

// Reserve a prompt or mark its cancellation in WebSocket receive order.
// Database, replay, and model work run after admission on bounded workers.
func (c *connection) prepare(request rpcRequest) (func() *rpcError, *rpcError) {
	switch request.Method {
	case "session/new", "session/load", "session/resume":
		return func() *rpcError { return c.setup(request) }, nil
	case "session/list":
		return func() *rpcError { return c.listSessions(request) }, nil
	case "session/close":
		return c.prepareClose(request)
	case "session/delete":
		return func() *rpcError { return c.deleteSession(request) }, nil
	case "session/set_config_option":
		return func() *rpcError { return c.setConfigOption(request) }, nil
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
	Data     string `json:"data,omitempty"`
}

type promptInput struct {
	text   string
	files  []string
	images []contentBlock
}

func promptContent(prompt []contentBlock) (promptInput, *rpcError) {
	var text strings.Builder
	var input promptInput
	for _, part := range prompt {
		switch part.Type {
		case "text":
			text.WriteString(part.Text)
		case "resource_link":
			if part.URI == "" {
				return promptInput{}, invalidParams("Resource links require a URI.")
			}
			if strings.HasPrefix(part.URI, "astral-file://") {
				input.files = append(input.files, part.URI)
			} else {
				text.WriteString("\n" + part.Name + " (" + part.URI + ")\n")
			}
		case "image":
			if len(input.images) == maxPromptImages {
				return promptInput{}, invalidParams("A prompt may contain at most four inline images.")
			}
			if part.Data == "" || part.MimeType == "" {
				return promptInput{}, invalidParams("Inline images require base64 data and a MIME type.")
			}
			input.images = append(input.images, part)
		default:
			return promptInput{}, invalidParams("Prompt supports text, inline images and resource links.")
		}
	}
	input.text = text.String()
	if strings.TrimSpace(input.text) == "" && len(input.files) == 0 && len(input.images) == 0 {
		return promptInput{}, invalidParams("Prompt is empty.")
	}
	return input, nil
}
