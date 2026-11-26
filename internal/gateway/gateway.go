// Package gateway exposes the authenticated execution binding's Agent view.
// It is reachable only through a Daemon's bound localhost listeners.
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	sessions "github.com/oines/astral-core"
	"github.com/oines/astral/internal/channels"
	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/harness"
	"github.com/oines/astral/internal/httpcontract"
	"github.com/oines/astral/internal/persistence"
	"github.com/oines/astral/internal/timezone"
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
	Timezone              timezone.View  `json:"timezone"`
}
type MessagePage struct {
	Items        []StoredMessageObservationView `json:"items"`
	NextSequence int64                          `json:"next_sequence"`
}
type HistoryPage struct {
	Entries                    []sessions.Entry          `json:"entries"`
	NextCursor                 string                    `json:"next_cursor,omitempty"`
	HeadID                     string                    `json:"head_id"`
	SubagentActivity           []domain.SubagentActivity `json:"subagent_activity,omitempty"`
	SubagentActivityCursor     string                    `json:"subagent_activity_cursor,omitempty"`
	SubagentActivityNextCursor string                    `json:"subagent_activity_next_cursor,omitempty"`
}
type ResolveConversation struct {
	ExternalID string `json:"external_id"`
	TopicID    string `json:"topic_id,omitempty"`
}
type SendText struct {
	domain.MessageOptions
	ConversationID string                `json:"conversation_id,omitempty"`
	Text           string                `json:"text,omitempty"`
	Parts          []domain.OutboundPart `json:"parts,omitempty"`
	ReplyTo        string                `json:"reply_to,omitempty"`
}

func New(h *harness.Service, c *channels.Service) *Service {
	s := &Service{Harness: h, Channels: c, Contract: httpcontract.New(), mux: http.NewServeMux()}
	s.scheduleRoutes()
	s.wakeRoutes()
	s.skillRoutes()
	s.modelRoutes()
	s.mcpRoutes()
	s.rolloutRoutes()
	s.computerFileRoutes()
	register[httpcontract.Empty, Context](s, "GET", "/v1/context", "agentContext", func(r *http.Request, _ httpcontract.Empty) (Context, error) {
		b := binding(r)
		value := Context{SourceComputerID: b.ComputerID, CWD: b.CWD, AgentHome: b.AgentHome, CapabilitiesURL: "/v1/capabilities"}
		if err := h.Store.Get(r.Context(), "agents", b.AgentID, &value.Agent); err != nil {
			return value, err
		}
		if err := h.Store.Get(r.Context(), "sessions", b.SessionID, &value.Session); err != nil {
			return value, err
		}
		var err error
		if value.Timezone, err = h.Timezone.View(r.Context()); err != nil {
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
	register[httpcontract.Empty, []channels.ChatOperation](s, "GET", "/v1/accounts/{id}/operations", "agentAccountOperations", func(r *http.Request, _ httpcontract.Empty) ([]channels.ChatOperation, error) {
		return c.AccountOperations(r.Context(), binding(r).AgentID, r.PathValue("id"))
	})
	register[channels.ChatOperationRequest, channels.ChatOperationResult](s, "POST", "/v1/accounts/{id}/operations/{operation}", "agentAccountOperation", func(r *http.Request, request channels.ChatOperationRequest) (channels.ChatOperationResult, error) {
		return c.AccountOperation(r.Context(), binding(r), r.PathValue("id"), r.PathValue("operation"), request)
	})
	s.telegramStickerRoutes()
	s.messagePartRoutes()
	s.napcatEssenceRoutes()
	s.napcatAvatarRoutes()
	register[httpcontract.Empty, []domain.Connection](s, "GET", "/v1/accounts", "agentAccounts", func(r *http.Request, _ httpcontract.Empty) ([]domain.Connection, error) {
		return c.VisibleConnections(r.Context(), binding(r).AgentID)
	})
	register[ResolveConversation, domain.Conversation](s, "POST", "/v1/accounts/{id}/conversations/resolve", "agentResolveConversation", func(r *http.Request, request ResolveConversation) (domain.Conversation, error) {
		return c.ResolveForAgent(r.Context(), binding(r).AgentID, r.PathValue("id"), request.ExternalID, request.TopicID)
	})
	register[httpcontract.Empty, MessageObservationView](s, "GET", "/v1/conversations/{id}/messages/{message_id}", "agentMessage", func(r *http.Request, _ httpcontract.Empty) (MessageObservationView, error) {
		if err := s.visibleConversation(r.Context(), binding(r).AgentID, r.PathValue("id")); err != nil {
			return MessageObservationView{}, err
		}
		observation, err := h.Store.Message(r.Context(), r.PathValue("id"), r.PathValue("message_id"))
		return MessageObservation(observation), err
	})
	const contentPath = "/v1/conversations/{id}/messages/{message_id}/attachments/{ref}/content"
	s.describeContent(httpcontract.Operation{Method: "GET", Path: contentPath, ID: "agentAttachmentContent", Tag: "Agent Gateway", CommandAuth: true, ContentType: "application/octet-stream"})
	s.mux.HandleFunc("GET "+contentPath, func(w http.ResponseWriter, r *http.Request) {
		download, mediaType, err := c.AttachmentContent(r.Context(), binding(r), r.PathValue("id"), r.PathValue("message_id"), r.PathValue("ref"))
		if err != nil {
			httpcontract.Failure(w, r, err)
			return
		}
		download.MediaType = mediaType
		serveContent(w, r, download)
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
		items, err := h.Store.SearchObservations(r.Context(), r.PathValue("id"), after, limit, r.URL.Query().Get("q"))
		if err != nil {
			return MessagePage{}, err
		}
		page := MessagePage{Items: []StoredMessageObservationView{}, NextSequence: after}
		for _, item := range items {
			page.NextSequence = item.Sequence
			page.Items = append(page.Items, StoredMessageObservationView{Sequence: item.Sequence, Observation: MessageObservation(item.Observation)})
		}
		return page, nil
	}, httpcontract.Parameter{Name: "after", Type: "integer"}, httpcontract.Parameter{Name: "limit", Type: "integer"}, httpcontract.Parameter{Name: "q"})
	s.recallRoutes()
	register[httpcontract.Empty, []domain.ComputerStatus](s, "GET", "/v1/computers", "agentComputers", func(r *http.Request, _ httpcontract.Empty) ([]domain.ComputerStatus, error) {
		b := binding(r)
		var agent domain.Agent
		if err := h.Store.Get(r.Context(), "agents", b.AgentID, &agent); err != nil {
			return nil, err
		}
		var session domain.Session
		_ = h.Store.Get(r.Context(), "sessions", b.SessionID, &session)
		rootID := domain.RootSessionID(session)
		result := []domain.ComputerStatus{}
		seen := map[string]bool{}
		for _, id := range agent.AllowedComputers {
			var computer domain.Computer
			if err := h.Store.Get(r.Context(), "computers", id, &computer); err != nil {
				continue
			}
			computer.TokenID = ""
			seen[id] = true
			result = append(result, h.Hub.Status(computer))
		}
		if items, err := h.Store.List(r.Context(), "computers"); err == nil && rootID != "" {
			now := time.Now()
			for _, item := range items {
				var computer domain.Computer
				if json.Unmarshal(item.Data, &computer) != nil || !computer.Temporary || computer.Revoked || seen[computer.ID] {
					continue
				}
				if computer.ExpiresAt != nil && now.After(*computer.ExpiresAt) {
					continue
				}
				if computer.OwnerSessionID == rootID && computer.OwnerAgentID == b.AgentID {
					computer.TokenID = ""
					result = append(result, h.Hub.Status(computer))
				}
			}
		}
		return result, nil
	})
	register[SendText, []domain.Outbox](s, "POST", "/v1/im/messages", "agentSendText", func(r *http.Request, request SendText) ([]domain.Outbox, error) {
		b := binding(r)
		return c.Send(r.Context(), b.AgentID, b.SessionID, channels.SendRequest{MessageOptions: request.MessageOptions, IntentID: r.Header.Get("Idempotency-Key"), ConversationID: request.ConversationID, Text: request.Text, Parts: request.Parts, ReplyTo: request.ReplyTo, SourceComputerID: b.ComputerID, SourceBinding: &b})
	})
	register[httpcontract.Empty, sessions.ToolDefinition](s, "GET", "/v1/conversations/{id}/message-tool", "agentMessageTool", func(r *http.Request, _ httpcontract.Empty) (sessions.ToolDefinition, error) {
		if err := s.visibleConversation(r.Context(), binding(r).AgentID, r.PathValue("id")); err != nil {
			return sessions.ToolDefinition{}, err
		}
		return c.MessageTool(r.Context(), binding(r).AgentID, r.PathValue("id"))
	})
	register[httpcontract.Empty, domain.Outbox](s, "GET", "/v1/im/receipts/{id}", "agentSendReceipt", func(r *http.Request, _ httpcontract.Empty) (domain.Outbox, error) {
		var value domain.Outbox
		err := h.Store.Get(r.Context(), "outbox", r.PathValue("id"), &value)
		if err == nil && value.AgentID != binding(r).AgentID {
			err = domain.Forbidden("receipt is outside the current Agent view")
		}
		if err == nil {
			err = s.visibleConversation(r.Context(), binding(r).AgentID, value.ConversationID)
		}
		if err == nil && value.SessionID != "" {
			var session domain.Session
			err = h.Store.Get(r.Context(), "sessions", value.SessionID, &session)
			if err == nil {
				err = persistence.CheckSessionAccess(r.Context(), session)
			}
		}
		return value, err
	})
	s.discoveryRoutes()

	s.mux.HandleFunc("GET /v1/openapi.json", func(w http.ResponseWriter, r *http.Request) { httpcontract.JSON(w, 200, s.Contract.Document()) })
	return s
}
func register[Request, Response any](s *Service, method, path, id string, handler func(*http.Request, Request) (Response, error), query ...httpcontract.Parameter) {
	op := httpcontract.Operation{Method: method, Path: path, ID: id, Tag: "Agent Gateway", CommandAuth: true, Query: query}
	switch id {
	case "agentSendText", "agentPublishSharedSkill", "agentDeleteSharedSkill":
		op.Headers = []httpcontract.Parameter{{Name: "Idempotency-Key", Required: true, Description: "Stable identity of this operation. Reuse only for the same request."}}
	}
	httpcontract.Register(s.Contract, s.mux, op, func(w http.ResponseWriter, r *http.Request, request Request) (Response, error) {
		return handler(r, request)
	})
}
func queryLimit(r *http.Request, fallback int) int {
	n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if n < 1 {
		return fallback
	}
	return min(n, 100)
}
func (s *Service) visibleConversation(ctx context.Context, agentID, id string) error {
	return s.Channels.AuthorizeConversation(ctx, agentID, id)
}

type responseWriter struct {
	header http.Header
	code   int
	body   bytes.Buffer
	stream io.ReadCloser
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
	var source domain.Session
	if err := s.Harness.Store.Get(ctx, "sessions", request.Binding.SessionID, &source); err != nil {
		return computers.GatewayResponse{}, err
	}
	if source.AgentID != request.Binding.AgentID {
		return computers.GatewayResponse{}, domain.Forbidden("command Session belongs to another Bot")
	}
	if err := persistence.CheckSessionRunnable(ctx, source, s.Harness.Store); err != nil {
		// Keep this a normal HTTP conflict. The authenticated command must not
		// mutate another target while its own Session is being sealed.
		w := &responseWriter{header: http.Header{}}
		r, _ := http.NewRequestWithContext(ctx, http.MethodPost, "/v1/", nil)
		httpcontract.Failure(w, r, err)
		return computers.GatewayResponse{Status: w.code, Headers: w.header, Body: w.body.Bytes()}, nil
	}
	ctx = persistence.WithSessionScope(ctx, request.Binding.AgentID, request.Binding.SessionID)
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
	r.Header.Set("Range", request.Range)
	w := &responseWriter{header: http.Header{}}
	s.mux.ServeHTTP(w, r)
	if w.stream == nil && !json.Valid(w.body.Bytes()) {
		w.body.Reset()
		_ = json.NewEncoder(&w.body).Encode(httpcontract.Error{Code: "not_found", Message: "This Agent Gateway endpoint is unavailable."})
	}
	return computers.GatewayResponse{Status: w.code, Headers: w.header, Body: w.body.Bytes(), Stream: w.stream}, nil
}

// History returns branch history by default; a trace is the current writer's
// operational facts. Rollout export is a separate full-fidelity stream.
func History(ctx context.Context, h *harness.Service, id, cursor string, limit int, trace, media bool) (HistoryPage, error) {
	state, err := h.CoreStore.LoadState(ctx, id)
	if err != nil {
		return HistoryPage{}, err
	}
	query := sessions.Query{Cursor: cursor, Limit: limit, MaxBytes: 128 << 20}
	head := state.Branch.HeadID
	if trace {
		query.SessionID = id
		query.Reverse = true
	} else {
		query.PathHead = head
		query.HistoryOnly = true
	}
	if !trace && head == "" {
		return HistoryPage{Entries: []sessions.Entry{}}, nil
	}
	page, err := h.CoreStore.Scan(ctx, state.RolloutID, query)
	if err != nil {
		return HistoryPage{}, err
	}
	entries := make([]sessions.Entry, len(page.Records))
	for i, record := range page.Records {
		entries[i] = record.Entry
		if !media {
			RemoveImageData(&entries[i])
		}
	}
	return HistoryPage{Entries: entries, NextCursor: page.Next, HeadID: head}, nil
}

// RemoveImageData operates only on owned history/event copies. The payload
// remains available through the authenticated history image and export APIs.
func RemoveImageData(entry *sessions.Entry) {
	strip := func(parts []sessions.Content) {
		for i := range parts {
			if parts[i].Type == "image" {
				parts[i].Data = nil
			}
		}
	}
	if entry.Message != nil {
		strip(entry.Message.Content)
	}
	if entry.Tool != nil && entry.Tool.Result != nil {
		strip(entry.Tool.Result.Content)
	}
	if input := entry.Input; input != nil {
		if input.Message != nil {
			strip(input.Message.Content)
		}
		for _, context := range input.Contexts {
			strip(context.Content)
		}
	}
}
