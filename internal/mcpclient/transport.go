package mcpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"time"
)

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      uint64 `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code *int64 `json:"code"`
	} `json:"error"`
}

func (c *Client) request(ctx context.Context, method string, params any, limit int, result any) error {
	id := c.sequence.Add(1)
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		return ErrProtocol
	}
	var response rpcMessage
	if c.legacy {
		response, err = c.legacyRequest(ctx, id, body)
	} else {
		response, err = c.httpRequest(ctx, id, body, method == "initialize", limit)
	}
	if err != nil {
		if ctx.Err() != nil {
			if method == "tools/call" && c.ctx.Err() == nil {
				c.cancelRequest(id)
			}
			return ctx.Err()
		}
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if c.ctx.Err() != nil {
		return ErrClosed
	}
	if response.Error != nil {
		if response.Error.Code == nil || response.Result != nil {
			return ErrProtocol
		}
		return ErrRemote
	}
	if len(response.Result) > limit {
		return ErrLimit
	}
	if !jsonObject(response.Result) {
		return ErrProtocol
	}
	if err = json.Unmarshal(response.Result, result); err != nil {
		return ErrProtocol
	}
	if c.ctx.Err() != nil {
		return ErrClosed
	}
	return ctx.Err()
}

func (c *Client) httpRequest(ctx context.Context, id uint64, body []byte, initialize bool, limit int) (rpcMessage, error) {
	response, err := c.send(ctx, http.MethodPost, body, "")
	if err != nil {
		return rpcMessage{}, err
	}
	defer response.Body.Close()
	if initialize {
		sessionIDs := response.Header.Values("Mcp-Session-Id")
		if len(sessionIDs) > 1 {
			return rpcMessage{}, ErrProtocol
		}
		if len(sessionIDs) == 1 {
			if !validSessionID(sessionIDs[0]) {
				return rpcMessage{}, ErrProtocol
			}
			c.mu.Lock()
			if c.ctx.Err() == nil {
				c.sessionID = sessionIDs[0]
			}
			c.mu.Unlock()
		}
	}
	contentType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	switch contentType {
	case "application/json":
		raw, err := readBounded(response.Body, limit+maxWireOverhead)
		if err != nil {
			return rpcMessage{}, err
		}
		message, err := decodeRPC(raw)
		if err != nil || message.Method != "" || !sameRequestID(message.ID, id) {
			return rpcMessage{}, ErrProtocol
		}
		return message, nil
	case "text/event-stream":
		return c.readStreamableResponse(ctx, response.Body, id, limit+maxWireOverhead)
	default:
		return rpcMessage{}, ErrProtocol
	}
}

func (c *Client) readStreamableResponse(ctx context.Context, body io.ReadCloser, id uint64, limit int) (rpcMessage, error) {
	reader := newEventReader(body, limit)
	retryAfter := 250 * time.Millisecond
	var lastEventID string
	for resumes := 0; ; {
		event, err := reader.next()
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) || lastEventID == "" || resumes == 2 || ctx.Err() != nil {
				if errors.Is(err, ErrLimit) {
					return rpcMessage{}, err
				}
				return rpcMessage{}, ErrTransport
			}
			_ = body.Close()
			timer := time.NewTimer(retryAfter)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return rpcMessage{}, ctx.Err()
			}
			response, err := c.send(ctx, http.MethodGet, nil, lastEventID)
			if err != nil {
				return rpcMessage{}, err
			}
			body = response.Body
			defer response.Body.Close()
			contentType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
			if contentType != "text/event-stream" {
				return rpcMessage{}, ErrProtocol
			}
			reader = newEventReader(body, limit)
			resumes++
			continue
		}
		if event.id != nil {
			lastEventID = *event.id
		}
		if event.retry != nil {
			retryAfter = *event.retry
		}
		if len(event.data) == 0 {
			continue
		}
		message, err := decodeRPC(event.data)
		if err != nil {
			return rpcMessage{}, err
		}
		if message.Method != "" {
			c.replyToServer(ctx, message)
			continue
		}
		if !sameRequestID(message.ID, id) {
			return rpcMessage{}, ErrProtocol
		}
		return message, nil
	}
}

func (c *Client) notify(ctx context.Context, method string, params any) error {
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", Method: method, Params: params})
	if err != nil {
		return ErrProtocol
	}
	return c.sendAck(ctx, body)
}

func (c *Client) cancelRequest(id uint64) {
	select {
	case c.cancelling <- struct{}{}:
	default:
		return
	}
	go func() {
		defer func() { <-c.cancelling }()
		ctx, cancel := context.WithTimeout(c.ctx, time.Second)
		defer cancel()
		_ = c.notify(ctx, "notifications/cancelled", map[string]uint64{"requestId": id})
	}()
}

func (c *Client) replyToServer(ctx context.Context, message rpcMessage) {
	if len(message.ID) == 0 || message.Result != nil || message.Error != nil {
		return
	}
	if first := message.ID[0]; first != '"' && first != '-' && (first < '0' || first > '9') {
		return
	}
	response := map[string]any{"jsonrpc": "2.0", "id": message.ID}
	if message.Method == "ping" {
		response["result"] = struct{}{}
	} else {
		response["error"] = map[string]any{"code": -32601, "message": "Method not found"}
	}
	if body, err := json.Marshal(response); err == nil {
		ctx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		_ = c.sendAck(ctx, body)
	}
}

func (c *Client) sendAck(ctx context.Context, body []byte) error {
	response, err := c.send(ctx, http.MethodPost, body, "")
	if err == nil {
		_ = response.Body.Close()
	}
	return err
}

func (c *Client) send(ctx context.Context, method string, body []byte, lastEventID string) (*http.Response, error) {
	c.mu.Lock()
	if c.ctx.Err() != nil {
		c.mu.Unlock()
		return nil, ErrClosed
	}
	address := c.endpoint
	if method == http.MethodPost {
		address = c.postURL
	}
	headers, sessionID, version := c.headers.Clone(), c.sessionID, c.version
	c.mu.Unlock()
	request, err := http.NewRequestWithContext(ctx, method, address, bytes.NewReader(body))
	if err != nil {
		return nil, ErrDestination
	}
	request.Header = headers
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json, text/event-stream")
	} else {
		request.Header.Set("Accept", "text/event-stream")
	}
	if sessionID != "" {
		request.Header.Set("Mcp-Session-Id", sessionID)
	}
	if version != "" {
		request.Header.Set("Mcp-Protocol-Version", version)
	}
	if lastEventID != "" {
		request.Header.Set("Last-Event-Id", lastEventID)
	}
	response, err := c.http.Do(request)
	switch {
	case errors.Is(err, ErrDestination):
		return nil, ErrDestination
	case err != nil && ctx.Err() != nil:
		return nil, ctx.Err()
	case err != nil:
		return nil, ErrTransport
	case response.StatusCode < 200 || response.StatusCode >= 300:
		_ = response.Body.Close()
		return nil, ErrRemote
	default:
		return response, nil
	}
}

func validSessionID(value string) bool {
	if len(value) == 0 || len(value) > 1024 {
		return false
	}
	for i := range len(value) {
		if value[i] < 0x21 || value[i] > 0x7e {
			return false
		}
	}
	return true
}

func readBounded(reader io.Reader, limit int) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	if err != nil {
		return nil, ErrTransport
	}
	if len(data) > limit {
		return nil, ErrLimit
	}
	return data, nil
}

func decodeRPC(data []byte) (rpcMessage, error) {
	var message rpcMessage
	if json.Unmarshal(data, &message) != nil || message.JSONRPC != "2.0" {
		return rpcMessage{}, ErrProtocol
	}
	return message, nil
}

func sameRequestID(raw json.RawMessage, id uint64) bool {
	return string(raw) == strconv.FormatUint(id, 10)
}
