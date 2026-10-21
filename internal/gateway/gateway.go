// Package gateway exposes the authenticated execution binding's Agent view.
// It is reachable only through a Daemon's bound localhost listeners.
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"

	sessions "github.com/oines/astral-core"
	"github.com/oines/astral/internal/channels"
	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/harness"
	"github.com/oines/astral/internal/httpcontract"
	"github.com/oines/astral/internal/persistence"
)

type Service struct {
	Harness  *harness.Service
	Channels *channels.Service
	Contract *httpcontract.Contract
	mux      *http.ServeMux
}
type bindingKey struct{}

func binding(r *http.Request) domain.ExecutionBinding {
	return r.Context().Value(bindingKey{}).(domain.ExecutionBinding)
}

type Context struct {
	Agent                 domain.Agent   `json:"agent"`
	Session               domain.Session `json:"session"`
	SourceComputerID      string         `json:"source_computer_id"`
	CWD                   string         `json:"cwd"`
	AgentHome             string         `json:"agent_home"`
	DefaultConversationID string         `json:"default_conversation_id,omitempty"`
	CapabilitiesURL       string         `json:"capabilities_url"`
}
type MessagePage struct {
	Items        []persistence.StoredObservation `json:"items"`
	NextSequence int64                           `json:"next_sequence"`
}
type HistoryPage struct {
	Entries    []sessions.Entry `json:"entries"`
	NextCursor string           `json:"next_cursor,omitempty"`
	HeadID     string           `json:"head_id"`
}
type SendText struct {
	ConversationID string `json:"conversation_id,omitempty"`
	Text           string `json:"text"`
}
type SendFile struct {
	ConversationID string `json:"conversation_id,omitempty"`
	FileURI        string `json:"file_uri,omitempty"`
	Path           string `json:"path,omitempty"`
	Text           string `json:"text,omitempty"`
}
type ReferenceRequest struct {
	Path     string `json:"path"`
	Snapshot bool   `json:"snapshot,omitempty"`
}
type ReferenceResponse struct {
	File domain.FileRef `json:"file"`
	URI  string         `json:"uri"`
}
type DownloadRequest struct {
	ConversationID string `json:"conversation_id"`
	EventID        string `json:"event_id"`
	AttachmentRef  string `json:"attachment_ref"`
	Path           string `json:"path"`
}
type Capabilities struct {
	Identity    string            `json:"identity"`
	Idempotency string            `json:"idempotency"`
	Files       string            `json:"files"`
	Endpoints   map[string]string `json:"endpoints"`
}

func New(h *harness.Service, c *channels.Service) *Service {
	s := &Service{Harness: h, Channels: c, Contract: httpcontract.New(), mux: http.NewServeMux()}
	register[httpcontract.Empty, Context](s, "GET", "/v1/context", "agentContext", func(r *http.Request, _ httpcontract.Empty) (Context, error) {
		b := binding(r)
		value := Context{SourceComputerID: b.ComputerID, CWD: b.CWD, AgentHome: b.AgentHome, CapabilitiesURL: "/v1/capabilities"}
		if err := h.Store.Get(r.Context(), "agents", b.AgentID, &value.Agent); err != nil {
			return value, err
		}
		if err := h.Store.Get(r.Context(), "sessions", b.SessionID, &value.Session); err != nil {
			return value, err
		}
		if value.Session.BindingID != "" {
			var bound domain.ConversationBinding
			if h.Store.Get(r.Context(), "bindings", value.Session.BindingID, &bound) == nil && bound.Enabled {
				value.DefaultConversationID = bound.ConversationID
			}
		}
		return value, nil
	})
	register[httpcontract.Empty, []domain.Conversation](s, "GET", "/v1/conversations", "agentConversations", func(r *http.Request, _ httpcontract.Empty) ([]domain.Conversation, error) {
		return c.VisibleConversations(r.Context(), binding(r).AgentID)
	})
	register[httpcontract.Empty, MessagePage](s, "GET", "/v1/conversations/{id}/messages", "agentMessages", func(r *http.Request, _ httpcontract.Empty) (MessagePage, error) {
		if err := s.visibleConversation(r.Context(), binding(r).AgentID, r.PathValue("id")); err != nil {
			return MessagePage{}, err
		}
		after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		limit := queryLimit(r, 100)
		items, err := h.Store.Observations(r.Context(), r.PathValue("id"), after, limit)
		if err != nil {
			return MessagePage{}, err
		}
		page := MessagePage{Items: []persistence.StoredObservation{}, NextSequence: after}
		for _, item := range items {
			page.NextSequence = item.Sequence
			if query := r.URL.Query().Get("q"); query == "" || strings.Contains(strings.ToLower(item.Observation.Text), strings.ToLower(query)) {
				page.Items = append(page.Items, item)
			}
		}
		return page, nil
	}, httpcontract.Parameter{Name: "after", Type: "integer"}, httpcontract.Parameter{Name: "limit", Type: "integer"}, httpcontract.Parameter{Name: "q"})
	register[httpcontract.Empty, HistoryPage](s, "GET", "/v1/sessions/{id}/history", "agentHistory", func(r *http.Request, _ httpcontract.Empty) (HistoryPage, error) {
		if err := s.visibleSession(r.Context(), binding(r).AgentID, r.PathValue("id")); err != nil {
			return HistoryPage{}, err
		}
		return History(r.Context(), h, r.PathValue("id"), r.URL.Query().Get("cursor"), queryLimit(r, 50), false, false)
	}, httpcontract.Parameter{Name: "cursor"}, httpcontract.Parameter{Name: "limit", Type: "integer"})
	register[httpcontract.Empty, []domain.ComputerStatus](s, "GET", "/v1/computers", "agentComputers", func(r *http.Request, _ httpcontract.Empty) ([]domain.ComputerStatus, error) {
		var agent domain.Agent
		if err := h.Store.Get(r.Context(), "agents", binding(r).AgentID, &agent); err != nil {
			return nil, err
		}
		result := []domain.ComputerStatus{}
		for _, id := range agent.AllowedComputers {
			var computer domain.Computer
			if err := h.Store.Get(r.Context(), "computers", id, &computer); err != nil {
				continue
			}
			computer.TokenID = ""
			result = append(result, h.Hub.Status(computer))
		}
		return result, nil
	})
	register[SendText, []domain.Outbox](s, "POST", "/v1/im/messages", "agentSendText", func(r *http.Request, request SendText) ([]domain.Outbox, error) {
		b := binding(r)
		return c.Enqueue(r.Context(), b.AgentID, b.SessionID, channels.SendRequest{IntentID: r.Header.Get("Idempotency-Key"), ConversationID: request.ConversationID, Text: request.Text})
	})
	register[SendFile, []domain.Outbox](s, "POST", "/v1/im/files", "agentSendFile", func(r *http.Request, request SendFile) ([]domain.Outbox, error) {
		b := binding(r)
		if request.FileURI == "" && request.Path == "" {
			return nil, domain.Invalid("provide a source file URI or Computer path")
		}
		return c.Enqueue(r.Context(), b.AgentID, b.SessionID, channels.SendRequest{IntentID: r.Header.Get("Idempotency-Key"), ConversationID: request.ConversationID, Text: request.Text, FileURI: request.FileURI, Path: request.Path, SourceComputerID: b.ComputerID, SourceBinding: &b})
	})
	register[httpcontract.Empty, domain.Outbox](s, "GET", "/v1/im/receipts/{id}", "agentSendReceipt", func(r *http.Request, _ httpcontract.Empty) (domain.Outbox, error) {
		var value domain.Outbox
		err := h.Store.Get(r.Context(), "outbox", r.PathValue("id"), &value)
		if err == nil && value.AgentID != binding(r).AgentID {
			err = domain.Forbidden("receipt is outside the current Agent view")
		}
		return value, err
	})
	register[DownloadRequest, ReferenceResponse](s, "POST", "/v1/attachments/download", "agentDownloadAttachment", func(r *http.Request, request DownloadRequest) (ReferenceResponse, error) {
		ref, err := c.DownloadAttachment(r.Context(), binding(r), request.ConversationID, request.EventID, request.AttachmentRef, request.Path)
		return ReferenceResponse{File: ref, URI: fileURI(ref)}, err
	})
	register[ReferenceRequest, ReferenceResponse](s, "POST", "/v1/files/reference", "agentFileReference", func(r *http.Request, request ReferenceRequest) (ReferenceResponse, error) {
		if request.Path == "" {
			return ReferenceResponse{}, domain.Invalid("source path is required")
		}
		ref, err := h.Files.Reference(r.Context(), binding(r), request.Path, request.Snapshot)
		return ReferenceResponse{File: ref, URI: fileURI(ref)}, err
	})
	register[httpcontract.Empty, Capabilities](s, "GET", "/v1/capabilities", "agentCapabilities", func(r *http.Request, _ httpcontract.Empty) (Capabilities, error) {
		return Capabilities{
			Identity:    "Agent, Session and source Computer come from this listener. Request bodies cannot change them.",
			Idempotency: "POST /im/messages and /im/files require a stable Idempotency-Key. They return queued receipts; query /im/receipts/{id}. Accepted means platform acceptance, not final delivery. Unknown sends must be inspected before retry.",
			Files:       "Paths resolve on this listener's source Computer. File URIs retain their source. Ordinary assistant links do not upload files to IM.",
			Endpoints:   map[string]string{"context": "GET /v1/context", "conversations": "GET /v1/conversations", "messages": "GET /v1/conversations/{id}/messages?after=0&limit=100&q=", "history": "GET /v1/sessions/{id}/history?cursor=&limit=50", "computers": "GET /v1/computers", "send_text": "POST /v1/im/messages {conversation_id?,text}", "send_file": "POST /v1/im/files {conversation_id?,file_uri?|path?,text?}", "receipt": "GET /v1/im/receipts/{id}", "download_attachment": "POST /v1/attachments/download {conversation_id,event_id,attachment_ref,path}", "reference": "POST /v1/files/reference {path,snapshot?}", "openapi": "GET /v1/openapi.json"},
		}, nil
	})
	s.mux.HandleFunc("GET /v1/openapi.json", func(w http.ResponseWriter, r *http.Request) { httpcontract.JSON(w, 200, s.Contract.Document()) })
	return s
}
func register[Request, Response any](s *Service, method, path, id string, handler func(*http.Request, Request) (Response, error), query ...httpcontract.Parameter) {
	httpcontract.Register(s.Contract, s.mux, httpcontract.Operation{Method: method, Path: path, ID: id, Tag: "Agent Gateway", Query: query}, func(w http.ResponseWriter, r *http.Request, request Request) (Response, error) {
		return handler(r, request)
	})
}
func fileURI(ref domain.FileRef) string { return "astral-file://" + ref.ComputerID + "/" + ref.ID }
func queryLimit(r *http.Request, fallback int) int {
	n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if n < 1 {
		return fallback
	}
	return min(n, 100)
}
func (s *Service) visibleConversation(ctx context.Context, agentID, id string) error {
	all, err := s.Channels.VisibleConversations(ctx, agentID)
	if err != nil {
		return err
	}
	for _, c := range all {
		if c.ID == id {
			return nil
		}
	}
	return domain.Forbidden("Conversation is outside the current Agent view")
}
func (s *Service) visibleSession(ctx context.Context, agentID, id string) error {
	var session domain.Session
	if err := s.Harness.Store.Get(ctx, "sessions", id, &session); err != nil {
		return err
	}
	if session.AgentID != agentID {
		return domain.Forbidden("Session is outside the current Agent view")
	}
	return nil
}

type responseWriter struct {
	header http.Header
	code   int
	body   bytes.Buffer
}

func (w *responseWriter) Header() http.Header  { return w.header }
func (w *responseWriter) WriteHeader(code int) { w.code = code }
func (w *responseWriter) Write(body []byte) (int, error) {
	if w.code == 0 {
		w.code = 200
	}
	return w.body.Write(body)
}
func (s *Service) Handle(ctx context.Context, request computers.GatewayRequest) (computers.GatewayResponse, error) {
	// Verify at the service boundary as well as at the Hub. Unit/in-process
	// callers cannot bypass the current grant, boot identity or allowed set.
	if err := s.Harness.Hub.Validate(ctx, request.Binding); err != nil {
		return computers.GatewayResponse{}, err
	}
	path, err := url.ParseRequestURI(request.Path)
	if err != nil || path.RawQuery != "" || path.Host != "" || !strings.HasPrefix(path.Path, "/v1/") {
		return computers.GatewayResponse{}, domain.Invalid("invalid Gateway path")
	}
	path.RawQuery = request.Query
	r, err := http.NewRequestWithContext(context.WithValue(ctx, bindingKey{}, request.Binding), request.Method, path.String(), bytes.NewReader(request.Body))
	if err != nil {
		return computers.GatewayResponse{}, err
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", request.Intent)
	w := &responseWriter{header: http.Header{}}
	s.mux.ServeHTTP(w, r)
	if !json.Valid(w.body.Bytes()) {
		w.body.Reset()
		_ = json.NewEncoder(&w.body).Encode(httpcontract.Error{Code: "not_found", Message: "This Agent Gateway endpoint is unavailable."})
	}
	return computers.GatewayResponse{Status: w.code, Body: w.body.Bytes()}, nil
}

// History returns branch history by default; a trace is the current writer's
// operational facts. Rollout export is a separate full-fidelity stream.
func History(ctx context.Context, h *harness.Service, id, cursor string, limit int, trace, media bool) (HistoryPage, error) {
	session, err := h.Core(ctx, id)
	if err != nil {
		return HistoryPage{}, err
	}
	query := sessions.Query{Cursor: cursor, Limit: limit, MaxBytes: 128 << 20}
	head := session.Head()
	if trace {
		query.SessionID = id
		query.Reverse = true
	} else {
		query.PathHead = head
		query.HistoryOnly = true
	}
	page, err := session.History().Scan(ctx, query)
	if err != nil {
		return HistoryPage{}, err
	}
	if !media {
		for i := range page.Entries {
			RemoveImageData(reflect.ValueOf(&page.Entries[i]))
		}
	}
	return HistoryPage{Entries: page.Entries, NextCursor: page.NextCursor, HeadID: head}, nil
}

// RemoveImageData operates only on owned history/event copies. The payload
// remains available through the authenticated history image and export APIs.
func RemoveImageData(value reflect.Value) {
	if !value.IsValid() {
		return
	}
	if value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface {
		if !value.IsNil() {
			RemoveImageData(value.Elem())
		}
		return
	}
	if value.Kind() == reflect.Struct {
		field := value.FieldByName("Type")
		if field.IsValid() && field.Kind() == reflect.String && field.String() == "image" {
			data := value.FieldByName("Data")
			if data.IsValid() && data.CanSet() {
				data.SetZero()
			}
		}
		for i := 0; i < value.NumField(); i++ {
			if value.Type().Field(i).IsExported() {
				RemoveImageData(value.Field(i))
			}
		}
	} else if value.Kind() == reflect.Slice || value.Kind() == reflect.Array {
		if value.Type().Elem().Kind() == reflect.Uint8 {
			return
		}
		for i := 0; i < value.Len(); i++ {
			RemoveImageData(value.Index(i))
		}
	}
}
