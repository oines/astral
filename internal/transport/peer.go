// Package transport multiplexes authenticated Server/Daemon calls over one
// outbound WebSocket. File bodies use bounded chunks and per-stream credits.
package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/oines/astral/internal/domain"
)

const chunkSize = 32 << 10
const window = 8
const maxCalls = 128

var ErrDisconnected = errors.New("computer connection closed")

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Outcome string `json:"outcome,omitempty"`
}

func (e *Error) Error() string { return e.Message }

type frame struct {
	ID     string          `json:"id"`
	Kind   string          `json:"kind"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Meta   json.RawMessage `json:"meta,omitempty"`
	Data   []byte          `json:"data,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

type Request struct {
	ID     string
	Method string
	Params json.RawMessage
	Body   io.Reader
}
type Handler func(context.Context, Request, *Responder) error

type Peer struct {
	conn    *websocket.Conn
	handler Handler
	ctx     context.Context
	cancel  context.CancelCauseFunc
	write   sync.Mutex
	mu      sync.Mutex
	streams map[string]*stream
	done    chan struct{}
}

type stream struct {
	peer     *Peer
	id       string
	incoming bool
	ctx      context.Context
	cancel   context.CancelCauseFunc
	data     chan piece
	credits  chan struct{}
	headers  chan header
	mu       sync.Mutex
	ended    bool
}
type piece struct {
	data []byte
	end  bool
	err  error
}
type header struct {
	meta json.RawMessage
	err  error
}

func New(conn *websocket.Conn, handler Handler) *Peer {
	ctx, cancel := context.WithCancelCause(context.Background())
	p := &Peer{conn: conn, handler: handler, ctx: ctx, cancel: cancel, streams: map[string]*stream{}, done: make(chan struct{})}
	conn.SetReadLimit(128 << 10)
	go p.read()
	return p
}
func (p *Peer) Done() <-chan struct{} { return p.done }
func (p *Peer) Close() error {
	p.cancel(ErrDisconnected)
	err := p.conn.Close(websocket.StatusGoingAway, "shutdown")
	// The peer may have closed its socket after our read loop was cancelled.
	// EOF during this explicit shutdown is an already-closed connection.
	if errors.Is(err, net.ErrClosed) || errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) ||
		websocket.CloseStatus(err) == websocket.StatusNormalClosure || websocket.CloseStatus(err) == websocket.StatusGoingAway {
		return nil
	}
	return err
}
func (p *Peer) send(value frame) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	p.write.Lock()
	defer p.write.Unlock()
	ctx, cancel := context.WithTimeout(p.ctx, 15*time.Second)
	defer cancel()
	return p.conn.Write(ctx, websocket.MessageText, raw)
}
func (p *Peer) makeStream(id string, incoming bool) (*stream, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ctx.Err() != nil {
		return nil, ErrDisconnected
	}
	if len(p.streams) >= maxCalls {
		return nil, &Error{Code: "busy", Message: "computer connection has too many active calls", Outcome: "not_executed"}
	}
	if _, exists := p.streams[id]; exists {
		return nil, errors.New("duplicate stream ID")
	}
	ctx, cancel := context.WithCancelCause(p.ctx)
	s := &stream{peer: p, id: id, incoming: incoming, ctx: ctx, cancel: cancel, data: make(chan piece, window+1), credits: make(chan struct{}, window), headers: make(chan header, 1)}
	for range window {
		s.credits <- struct{}{}
	}
	p.streams[id] = s
	return s, nil
}
func (p *Peer) remove(s *stream, err error) {
	p.mu.Lock()
	if p.streams[s.id] == s {
		delete(p.streams, s.id)
	}
	p.mu.Unlock()
	s.cancel(err)
}
func (p *Peer) find(id string) *stream { p.mu.Lock(); defer p.mu.Unlock(); return p.streams[id] }

func (p *Peer) read() {
	defer close(p.done)
	defer func() { p.cancel(ErrDisconnected); _ = p.conn.CloseNow() }()
	for {
		_, raw, err := p.conn.Read(p.ctx)
		if err != nil {
			return
		}
		var f frame
		if json.Unmarshal(raw, &f) != nil || f.ID == "" {
			return
		}
		if f.Kind == "open" {
			if p.handler == nil {
				_ = p.send(frame{ID: f.ID, Kind: "end", Error: &Error{Code: "unsupported", Message: "calls are unavailable on this peer", Outcome: "not_executed"}})
				continue
			}
			s, err := p.makeStream(f.ID, true)
			if err != nil {
				_ = p.send(frame{ID: f.ID, Kind: "end", Error: wireError(err)})
				continue
			}
			go p.serve(s, f)
			continue
		}
		s := p.find(f.ID)
		// Acknowledgments and cancellation can cross a terminal response.
		if s == nil {
			continue
		}
		switch f.Kind {
		case "credit":
			select {
			case s.credits <- struct{}{}:
			default:
				return
			}
		case "headers":
			if s.incoming {
				return
			}
			select {
			case s.headers <- header{meta: f.Meta}:
			default:
				return
			}
		case "data":
			if len(f.Data) == 0 || len(f.Data) > chunkSize {
				return
			}
			if !s.push(piece{data: f.Data}) {
				return
			}
		case "input_end":
			if !s.incoming {
				return
			}
			if !s.push(piece{end: true}) {
				return
			}
		case "end":
			if s.incoming {
				return
			}
			var endErr error
			if f.Error != nil {
				endErr = f.Error
			}
			select {
			case s.headers <- header{err: endErr}:
			default:
			}
			if !s.push(piece{end: true, err: endErr}) {
				return
			}
		case "cancel":
			s.cancel(context.Canceled)
		default:
			return
		}
	}
}

func (s *stream) push(value piece) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return false
	}
	if value.end {
		s.ended = true
	}
	select {
	case s.data <- value:
		return true
	default:
		return false
	}
}
func (p *Peer) serve(s *stream, f frame) {
	reader := &body{s: s}
	out := &Responder{s: s}
	var err error
	func() {
		defer func() {
			if recover() != nil {
				err = &Error{Code: "internal", Message: "daemon handler failed", Outcome: "unknown"}
			}
		}()
		err = p.handler(s.ctx, Request{ID: f.ID, Method: f.Method, Params: f.Params, Body: reader}, out)
	}()
	if !out.sentHeaders && err == nil {
		err = out.Headers(struct{}{})
	}
	_ = p.send(frame{ID: s.id, Kind: "end", Error: wireError(err)})
	p.remove(s, err)
}
func wireError(err error) *Error {
	if err == nil {
		return nil
	}
	var value *Error
	if errors.As(err, &value) {
		return value
	}
	var known *domain.Error
	if errors.As(err, &known) {
		return &Error{Code: known.Code, Message: known.Message, Outcome: "not_executed"}
	}
	return &Error{Code: "operation_failed", Message: err.Error(), Outcome: "unknown"}
}

type Responder struct {
	s           *stream
	sentHeaders bool
}

func (r *Responder) Headers(meta any) error {
	if r.sentHeaders {
		return errors.New("response headers already sent")
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	if err = r.s.peer.send(frame{ID: r.s.id, Kind: "headers", Meta: raw}); err != nil {
		return err
	}
	r.sentHeaders = true
	return nil
}
func (r *Responder) Write(data []byte) (int, error) {
	if !r.sentHeaders {
		if err := r.Headers(struct{}{}); err != nil {
			return 0, err
		}
	}
	return r.s.write(data)
}
func (r *Responder) JSON(value any) error {
	if err := r.Headers(map[string]string{"content_type": "application/json"}); err != nil {
		return err
	}
	return json.NewEncoder(r).Encode(value)
}
func (s *stream) write(data []byte) (int, error) {
	total := 0
	for len(data) > 0 {
		n := min(len(data), chunkSize)
		select {
		case <-s.ctx.Done():
			return total, context.Cause(s.ctx)
		case <-s.credits:
		}
		if err := s.peer.send(frame{ID: s.id, Kind: "data", Data: data[:n]}); err != nil {
			return total, err
		}
		total += n
		data = data[n:]
	}
	return total, nil
}

type body struct {
	s         *stream
	current   []byte
	terminal  error
	closed    atomic.Bool
	readEnded atomic.Bool
}

func (b *body) Read(target []byte) (int, error) {
	if len(target) == 0 {
		return 0, nil
	}
	if b.closed.Load() {
		return 0, io.ErrClosedPipe
	}
	if len(b.current) > 0 {
		n := copy(target, b.current)
		b.current = b.current[n:]
		return n, nil
	}
	if b.terminal != nil {
		return 0, b.terminal
	}
	select {
	case <-b.s.ctx.Done():
		return 0, context.Cause(b.s.ctx)
	case part := <-b.s.data:
		if part.end {
			b.readEnded.Store(true)
			b.terminal = part.err
			if b.terminal == nil {
				b.terminal = io.EOF
			}
			return 0, b.terminal
		}
		// Credit on consumption, not receipt: a slow file consumer cannot
		// accumulate an unbounded response or block unrelated stream frames.
		if err := b.s.peer.send(frame{ID: b.s.id, Kind: "credit"}); err != nil {
			return 0, err
		}
		b.current = part.data
		n := copy(target, b.current)
		b.current = b.current[n:]
		return n, nil
	}
}
func (b *body) Close() error {
	if !b.closed.CompareAndSwap(false, true) {
		return nil
	}
	if !b.readEnded.Load() {
		_ = b.s.peer.send(frame{ID: b.s.id, Kind: "cancel"})
	}
	b.s.peer.remove(b.s, context.Canceled)
	return nil
}

func (p *Peer) Call(ctx context.Context, method string, params any, input io.Reader) (json.RawMessage, io.ReadCloser, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, nil, err
	}
	s, err := p.makeStream(domain.ID("call"), false)
	if err != nil {
		return nil, nil, err
	}
	context.AfterFunc(s.ctx, func() {
		if closer, ok := input.(io.Closer); ok {
			_ = closer.Close()
		}
	})
	stop := context.AfterFunc(ctx, func() {
		s.cancel(ctx.Err())
		_ = p.send(frame{ID: s.id, Kind: "cancel"})
		if closer, ok := input.(io.Closer); ok {
			_ = closer.Close()
		}
	})
	if err = p.send(frame{ID: s.id, Kind: "open", Method: method, Params: raw}); err != nil {
		stop()
		p.remove(s, err)
		return nil, nil, err
	}
	go func() {
		defer func() {
			if s.ctx.Err() != nil {
				stop()
			}
		}()
		if input != nil {
			buffer := make([]byte, chunkSize)
			for {
				n, readErr := input.Read(buffer)
				if n > 0 {
					if _, err := s.write(buffer[:n]); err != nil {
						s.cancel(err)
						return
					}
				}
				if readErr != nil {
					if readErr != io.EOF {
						s.cancel(readErr)
						_ = p.send(frame{ID: s.id, Kind: "cancel"})
						return
					}
					break
				}
			}
		}
		if err := p.send(frame{ID: s.id, Kind: "input_end"}); err != nil {
			s.cancel(err)
		}
	}()
	select {
	case <-s.ctx.Done():
		stop()
		p.remove(s, context.Cause(s.ctx))
		return nil, nil, context.Cause(s.ctx)
	case h := <-s.headers:
		if h.err != nil {
			stop()
			p.remove(s, h.err)
			return nil, nil, h.err
		}
		return h.meta, &responseBody{body: &body{s: s}, stop: stop}, nil
	}
}

type responseBody struct {
	*body
	stop func() bool
}

func (b *responseBody) Close() error { b.stop(); return b.body.Close() }

func (p *Peer) JSON(ctx context.Context, method string, params, result any) error {
	_, body, err := p.Call(ctx, method, params, nil)
	if err != nil {
		return err
	}
	defer body.Close()
	raw, err := io.ReadAll(io.LimitReader(body, (32<<20)+1))
	if err != nil {
		return err
	}
	if len(raw) > 32<<20 {
		return fmt.Errorf("RPC JSON response exceeds 32 MiB")
	}
	if result == nil {
		return nil
	}
	return json.Unmarshal(raw, result)
}
