// Package mcpclient connects to remote MCP tool servers over HTTP or legacy SSE.
package mcpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

const (
	MaxTools              = 64
	MaxSchemaBytes        = 32 << 10
	MaxResultBytes        = 256 << 10
	maxArgumentBytes      = 256 << 10
	maxDescriptionBytes   = 4 << 10
	maxListBytes          = MaxTools*(MaxSchemaBytes+maxDescriptionBytes+256) + 4096
	maxToolPages          = 32
	maxWireOverhead       = 4096
	maxInFlight           = 16
	defaultConnectTimeout = 12 * time.Second
	defaultCallTimeout    = 30 * time.Second
	streamableVersion     = "2025-11-25"
	legacyVersion         = "2024-11-05"
)

var (
	ErrTransport   = errors.New("MCP transport failed")
	ErrDestination = errors.New("MCP destination is not allowed")
	ErrProtocol    = errors.New("invalid MCP protocol response")
	ErrLimit       = errors.New("MCP size or count limit exceeded")
	ErrClosed      = errors.New("MCP connection is closed")
	ErrRemote      = errors.New("MCP server rejected the request")
)

type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type ServerSpec struct {
	Type    string   `json:"type"`
	Name    string   `json:"name"`
	URL     string   `json:"url"`
	Headers []Header `json:"headers,omitempty"`
}

type Options struct {
	ConnectTimeout time.Duration
	CallTimeout    time.Duration
}

type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type Content struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	Data     string          `json:"data,omitempty"`
	MIMEType string          `json:"mimeType,omitempty"`
	URI      string          `json:"uri,omitempty"`
	Name     string          `json:"name,omitempty"`
	Resource json.RawMessage `json:"resource,omitempty"`
}

type Result struct {
	Content           []Content       `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	IsError           bool            `json:"isError"`
}

type Client struct {
	ctx         context.Context
	cancel      context.CancelFunc
	http        *http.Client
	transport   *http.Transport
	callTimeout time.Duration
	legacy      bool
	sequence    atomic.Uint64
	inFlight    chan struct{}
	cancelling  chan struct{}
	tools       []Tool

	mu        sync.Mutex
	endpoint  string
	postURL   string
	headers   http.Header
	version   string
	sessionID string
	pending   map[uint64]chan rpcMessage
}

// Connect initializes the remote session and discovers its initial tool set.
// The caller context bounds setup. The successful client lives until Close.
func Connect(ctx context.Context, spec ServerSpec, opts Options) (*Client, error) {
	if spec.Type != "http" && spec.Type != "sse" {
		return nil, errors.New("unsupported MCP transport; use http or sse")
	}
	endpoint, err := parseEndpoint(spec.URL)
	if err != nil {
		return nil, err
	}
	headers, err := connectionHeaders(spec.Headers)
	if err != nil {
		return nil, err
	}
	connectTimeout, callTimeout := opts.ConnectTimeout, opts.CallTimeout
	if connectTimeout <= 0 {
		connectTimeout = defaultConnectTimeout
	}
	if callTimeout <= 0 {
		callTimeout = defaultCallTimeout
	}
	clientCtx, clientCancel := context.WithCancel(context.Background())
	transport := newTransport()
	c := &Client{
		ctx: clientCtx, cancel: clientCancel, transport: transport,
		http:        &http.Client{Transport: transport, CheckRedirect: denyRedirect},
		callTimeout: callTimeout, legacy: spec.Type == "sse", inFlight: make(chan struct{}, maxInFlight), cancelling: make(chan struct{}, 4),
		endpoint: endpoint.String(), postURL: endpoint.String(), headers: headers,
	}
	setup, finish := context.WithTimeout(ctx, connectTimeout)
	defer finish()
	stop := context.AfterFunc(setup, func() { _ = c.Close() })
	defer stop()
	if c.legacy {
		c.pending = make(map[uint64]chan rpcMessage)
		if err = c.openLegacy(setup, endpoint); err != nil {
			_ = c.Close()
			return nil, err
		}
	}
	if err = c.initialize(setup); err == nil {
		c.tools, err = c.discover(setup)
	}
	if err == nil && !stop() {
		err = setup.Err()
	}
	if err == nil && c.ctx.Err() != nil {
		err = ErrClosed
	}
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

func (c *Client) initialize(ctx context.Context) error {
	offer := streamableVersion
	if c.legacy {
		offer = legacyVersion
	}
	params := struct {
		Version      string            `json:"protocolVersion"`
		Capabilities struct{}          `json:"capabilities"`
		ClientInfo   map[string]string `json:"clientInfo"`
	}{Version: offer, ClientInfo: map[string]string{"name": "Astral", "version": "1"}}
	var initialized struct {
		Version      string `json:"protocolVersion"`
		Capabilities struct {
			Tools json.RawMessage `json:"tools"`
		} `json:"capabilities"`
	}
	if err := c.request(ctx, "initialize", params, 32<<10, &initialized); err != nil {
		return err
	}
	switch initialized.Version {
	case streamableVersion, "2025-06-18", "2025-03-26", legacyVersion:
	default:
		return errors.New("unsupported MCP protocol version")
	}
	if !jsonObject(initialized.Capabilities.Tools) {
		return errors.New("MCP server did not advertise tools")
	}
	c.mu.Lock()
	c.version = initialized.Version
	c.mu.Unlock()
	return c.notify(ctx, "notifications/initialized", nil)
}

func (c *Client) discover(ctx context.Context) ([]Tool, error) {
	type toolQuery struct {
		Cursor string `json:"cursor,omitempty"`
	}
	var tools []Tool
	query := toolQuery{}
	seenCursor := make(map[string]struct{})
	seenName := make(map[string]struct{})
	for page := 0; page < maxToolPages; page++ {
		var list struct {
			Tools      []Tool `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := c.request(ctx, "tools/list", query, maxListBytes, &list); err != nil {
			return nil, err
		}
		if list.Tools == nil {
			return nil, ErrProtocol
		}
		if len(list.Tools) > MaxTools-len(tools) || len(list.NextCursor) > 1024 {
			return nil, ErrLimit
		}
		for _, tool := range list.Tools {
			if len(tool.Description) > maxDescriptionBytes || len(tool.InputSchema) > MaxSchemaBytes {
				return nil, ErrLimit
			}
			_, duplicate := seenName[tool.Name]
			if duplicate || !validToolName(tool.Name) || !jsonObject(tool.InputSchema) {
				return nil, ErrProtocol
			}
			seenName[tool.Name] = struct{}{}
			tools = append(tools, tool)
		}
		if list.NextCursor == "" {
			return tools, nil
		}
		if _, duplicate := seenCursor[list.NextCursor]; duplicate {
			return nil, ErrProtocol
		}
		seenCursor[list.NextCursor] = struct{}{}
		query.Cursor = list.NextCursor
	}
	return nil, ErrLimit
}

func (c *Client) Tools() []Tool {
	tools := make([]Tool, len(c.tools))
	for i, tool := range c.tools {
		tool.InputSchema = bytes.Clone(tool.InputSchema)
		tools[i] = tool
	}
	return tools
}

func (c *Client) Call(ctx context.Context, name string, args json.RawMessage) (Result, error) {
	if c.ctx.Err() != nil {
		return Result{}, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	select {
	case c.inFlight <- struct{}{}:
		defer func() { <-c.inFlight }()
	default:
		return Result{}, ErrLimit
	}
	found := false
	for _, tool := range c.tools {
		if tool.Name == name {
			found = true
			break
		}
	}
	if !found {
		return Result{}, errors.New("MCP tool is not declared")
	}
	if len(args) > maxArgumentBytes {
		return Result{}, ErrLimit
	}
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	if !jsonObject(args) || !json.Valid(args) {
		return Result{}, errors.New("MCP tool arguments must be a JSON object")
	}
	ctx, cancel := context.WithTimeout(ctx, c.callTimeout)
	defer cancel()
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	params := struct {
		Name string          `json:"name"`
		Args json.RawMessage `json:"arguments"`
	}{Name: name, Args: args}
	var result Result
	err := c.request(ctx, "tools/call", params, MaxResultBytes, &result)
	if err != nil {
		return Result{}, err
	}
	if result.Content == nil && len(result.StructuredContent) == 0 || len(result.StructuredContent) != 0 && !jsonObject(result.StructuredContent) {
		return Result{}, ErrProtocol
	}
	return result, nil
}

// Close revokes this client's credentials and cancels its requests and streams.
func (c *Client) Close() error {
	c.mu.Lock()
	c.cancel()
	c.headers = nil
	c.endpoint, c.postURL, c.sessionID = "", "", ""
	c.pending = nil
	c.mu.Unlock()
	c.transport.CloseIdleConnections()
	return nil
}

func jsonObject(value []byte) bool {
	value = bytes.TrimSpace(value)
	return len(value) > 0 && value[0] == '{'
}

func validToolName(name string) bool {
	if len(name) == 0 || len(name) > 128 {
		return false
	}
	for _, ch := range name {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-' || ch == '.') {
			return false
		}
	}
	return true
}
