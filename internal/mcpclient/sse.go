package mcpclient

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type sseEvent struct {
	kind  string
	data  []byte
	id    *string
	retry *time.Duration
}

type eventReader struct {
	reader *bufio.Reader
	limit  int
}

func newEventReader(body io.Reader, limit int) *eventReader {
	return &eventReader{reader: bufio.NewReader(body), limit: limit}
}

func (r *eventReader) next() (sseEvent, error) {
	var event sseEvent
	for {
		line, err := r.line()
		if err != nil {
			return sseEvent{}, err
		}
		if len(line) == 0 {
			if event.data != nil || event.id != nil || event.retry != nil {
				event.data = bytes.TrimSuffix(event.data, []byte{'\n'})
				return event, nil
			}
			event.kind = ""
			continue
		}
		name, value, _ := bytes.Cut(line, []byte{':'})
		value = bytes.TrimPrefix(value, []byte{' '})
		switch string(name) {
		case "event":
			if len(value) <= 64 {
				event.kind = string(value)
			}
		case "data":
			if len(value)+1 > r.limit-len(event.data) {
				return sseEvent{}, ErrLimit
			}
			event.data = append(event.data, value...)
			event.data = append(event.data, '\n')
		case "id":
			if len(value) == 0 || len(value) <= 1024 && validHeaderValue(string(value)) {
				id := string(value)
				event.id = &id
			}
		case "retry":
			if millis, err := strconv.ParseUint(string(value), 10, 32); err == nil {
				duration := time.Duration(millis) * time.Millisecond
				event.retry = &duration
			}
		}
	}
}

func (r *eventReader) line() ([]byte, error) {
	var line []byte
	for {
		part, err := r.reader.ReadSlice('\n')
		if len(part) > r.limit-len(line) {
			return nil, ErrLimit
		}
		switch {
		case err == nil:
			if len(line) != 0 {
				part = append(line, part...)
			}
			return bytes.TrimRight(part, "\r\n"), nil
		case errors.Is(err, bufio.ErrBufferFull):
			line = append(line, part...)
		default:
			return nil, err
		}
	}
}

func (c *Client) openLegacy(ctx context.Context, endpoint *url.URL) error {
	// The persistent GET belongs to the client, while Connect's setup context
	// can still cancel the entire client during initialization.
	response, err := c.send(c.ctx, http.MethodGet, nil, "")
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	contentType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if contentType != "text/event-stream" {
		response.Body.Close()
		return ErrProtocol
	}
	reader := newEventReader(response.Body, maxURLBytes+128)
	var event sseEvent
	for len(event.data) == 0 {
		event, err = reader.next()
		if err != nil {
			response.Body.Close()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, ErrLimit) {
				return err
			}
			return ErrTransport
		}
	}
	if event.kind != "endpoint" {
		response.Body.Close()
		return ErrProtocol
	}
	target, err := endpoint.Parse(strings.TrimSpace(string(event.data)))
	if err != nil || !validEndpoint(target) || !sameOrigin(endpoint, target) || len(target.String()) > maxURLBytes {
		response.Body.Close()
		return ErrDestination
	}
	c.mu.Lock()
	if c.ctx.Err() != nil {
		c.mu.Unlock()
		response.Body.Close()
		return ErrClosed
	}
	c.postURL = target.String()
	c.mu.Unlock()
	reader.limit = maxListBytes + maxWireOverhead
	go c.readLegacy(response.Body, reader)
	return nil
}

func (c *Client) readLegacy(body io.Closer, reader *eventReader) {
	defer c.Close()
	defer body.Close()
	for {
		event, err := reader.next()
		if err != nil {
			return
		}
		if len(event.data) == 0 || event.kind != "message" && event.kind != "" {
			continue
		}
		message, err := decodeRPC(event.data)
		if err != nil {
			return
		}
		if message.Method != "" {
			c.replyToServer(c.ctx, message)
			continue
		}
		id, err := strconv.ParseUint(string(message.ID), 10, 64)
		if err != nil {
			continue
		}
		c.mu.Lock()
		reply := c.pending[id]
		c.mu.Unlock()
		if reply != nil {
			select {
			case reply <- message:
			default:
			}
		}
	}
}

func (c *Client) legacyRequest(ctx context.Context, id uint64, body []byte) (rpcMessage, error) {
	reply := make(chan rpcMessage, 1)
	c.mu.Lock()
	if c.ctx.Err() != nil {
		c.mu.Unlock()
		return rpcMessage{}, ErrClosed
	}
	c.pending[id] = reply
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	if err := c.sendAck(ctx, body); err != nil {
		return rpcMessage{}, err
	}
	select {
	case message := <-reply:
		return message, nil
	case <-ctx.Done():
		return rpcMessage{}, ctx.Err()
	case <-c.ctx.Done():
		return rpcMessage{}, ErrClosed
	}
}
