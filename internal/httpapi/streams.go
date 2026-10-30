package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	core "github.com/oines/astral-core"

	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/filestream"
	"github.com/oines/astral/internal/gateway"
	"github.com/oines/astral/internal/harness"
	"github.com/oines/astral/internal/httpcontract"
)

type Snapshot struct {
	Epoch   string              `json:"epoch"`
	View    harness.View        `json:"view"`
	History gateway.HistoryPage `json:"history"`
	Trace   gateway.HistoryPage `json:"trace"`
}
type Event struct {
	Epoch string        `json:"epoch"`
	Core  *core.Event   `json:"core,omitempty"`
	Entry *core.Entry   `json:"entry,omitempty"`
	View  *harness.View `json:"view,omitempty"`
}

func (a *API) streamRoutes() {
	for _, spec := range []struct {
		path, id, content string
		handler           http.HandlerFunc
	}{
		{"/sessions/{id}/events", "sessionEvents", "text/event-stream", a.events},
		{"/sessions/{id}/rollout", "exportRollout", "application/x-ndjson", a.exportRollout},
		{"/sessions/{id}/entries/{entry}/images/{index}", "historyImage", "application/octet-stream", a.historyImage},
		{"/files/{id}/content", "fileContent", "application/octet-stream", a.fileContent},
	} {
		path := "/api/v1" + spec.path
		a.Contract.Describe(httpcontract.Operation{Method: "GET", Path: path, ID: spec.id, Tag: "Streams", Auth: true, ContentType: spec.content, Query: []httpcontract.Parameter{{Name: "session_id"}, {Name: "download", Type: "boolean"}}})
		a.mux.HandleFunc("GET "+path, spec.handler)
	}
	route[httpcontract.Empty, gateway.HistoryPage](a, "GET", "/sessions/{id}/timeline", "sessionTimeline", "Sessions", func(r *http.Request, _ httpcontract.Empty) (gateway.HistoryPage, error) {
		page, err := gateway.History(r.Context(), a.Config.Harness, r.PathValue("id"), r.URL.Query().Get("cursor"), limit(r, 60), r.URL.Query().Get("trace") == "true", false)
		return timelinePage(page), err
	}, httpcontract.Parameter{Name: "cursor"}, httpcontract.Parameter{Name: "limit", Type: "integer"}, httpcontract.Parameter{Name: "trace", Type: "boolean"})

	a.Contract.Describe(httpcontract.Operation{Method: "POST", Path: "/api/v1/sessions/{id}/attachments", ID: "uploadAttachment", Tag: "Files", Auth: true, Response: reflect.TypeFor[gateway.ReferenceResponse](),
		Query: []httpcontract.Parameter{{Name: "name", Required: true}, {Name: "computer_id"}}})
	upload := a.Contract.Paths["/api/v1/sessions/{id}/attachments"].(map[string]any)["post"].(map[string]any)
	upload["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/octet-stream": map[string]any{"schema": map[string]any{"type": "string", "format": "binary"}}}}
	a.mux.HandleFunc("POST /api/v1/sessions/{id}/attachments", a.upload)
	// Event schemas are part of the contract even though EventSource has its own
	// lifecycle and must not be represented as a normal JSON mutation.
	a.Contract.Describe(httpcontract.Operation{Method: "GET", Path: "/api/v1/sessions/{id}/snapshot", ID: "sessionSnapshot", Tag: "Sessions", Auth: true, Response: reflect.TypeFor[Snapshot]()})
	a.mux.HandleFunc("GET /api/v1/sessions/{id}/snapshot", func(w http.ResponseWriter, r *http.Request) {
		snapshot, err := a.snapshot(r)
		if err != nil {
			httpcontract.Failure(w, r, err)
			return
		}
		httpcontract.JSON(w, 200, snapshot)
	})
	a.Contract.Describe(httpcontract.Operation{Method: "GET", Path: "/api/v1/event-schema", ID: "eventSchema", Tag: "System", Auth: true, Response: reflect.TypeFor[Event]()})
	a.mux.HandleFunc("GET /api/v1/event-schema", func(w http.ResponseWriter, r *http.Request) {
		httpcontract.JSON(w, 200, Event{Epoch: a.Config.Harness.RuntimeID})
	})
}
func (a *API) snapshot(r *http.Request) (Snapshot, error) {
	h := a.Config.Harness
	snapshot := Snapshot{Epoch: h.RuntimeID}
	var err error
	if snapshot.View, err = h.View(r.Context(), r.PathValue("id")); err != nil {
		return snapshot, err
	}
	var historyErr, traceErr error
	var reads sync.WaitGroup
	reads.Add(2)
	go func() {
		defer reads.Done()
		snapshot.History, historyErr = gateway.History(r.Context(), h, r.PathValue("id"), "", 60, false, false)
	}()
	go func() {
		defer reads.Done()
		snapshot.Trace, traceErr = gateway.History(r.Context(), h, r.PathValue("id"), "", 60, true, false)
	}()
	reads.Wait()
	if historyErr != nil {
		return snapshot, historyErr
	}
	if traceErr != nil {
		return snapshot, traceErr
	}
	snapshot.History = timelinePage(snapshot.History)
	snapshot.Trace = timelinePage(snapshot.Trace)
	// History already includes accepted messages and tool results. Avoid sending
	// those bodies a second time in the operational tail.
	seen := map[string]bool{}
	for _, entry := range snapshot.History.Entries {
		seen[entry.ID] = true
	}
	trace := snapshot.Trace.Entries[:0]
	for _, entry := range snapshot.Trace.Entries {
		if !seen[entry.ID] {
			trace = append(trace, entry)
		}
	}
	snapshot.Trace.Entries = trace
	return snapshot, nil
}
func (a *API) events(w http.ResponseWriter, r *http.Request) {
	h := a.Config.Harness
	session, err := h.Core(r.Context(), r.PathValue("id"))
	if err != nil {
		httpcontract.Failure(w, r, err)
		return
	}
	// Subscribe first: a commit during snapshot reads is queued, then reconciled
	// by its persistent Entry ID. A reconnect always receives a fresh snapshot.
	subscription := session.Subscribe(256)
	defer subscription.Close()
	snapshot, err := a.snapshot(r)
	if err != nil {
		httpcontract.Failure(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	send := func(kind, id string, value any) bool {
		raw, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			return false
		}
		_ = controller.SetWriteDeadline(time.Now().Add(15 * time.Second))
		if id != "" {
			if _, err = fmt.Fprintf(w, "id: %s\n", id); err != nil {
				return false
			}
		}
		if _, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, raw); err != nil {
			return false
		}
		return controller.Flush() == nil
	}
	if !send("snapshot", h.RuntimeID, snapshot) {
		return
	}
	stateTicker := time.NewTicker(time.Second)
	defer stateTicker.Stop()
	lastView, _ := json.Marshal(snapshot.View)
	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-subscription.C:
			if !ok {
				_ = send("resync", "", map[string]string{"reason": "event subscription ended; reconnect to reconcile durable history"})
				return
			}
			// Durable content is sent once in Entry. Model events only need their
			// ordered text deltas; opaque provider payloads are not UI data.
			event.Message = nil
			if event.Progress != nil {
				progress := *event.Progress
				progress.Details = nil
				event.Progress = &progress
			}
			if event.Model != nil {
				model := *event.Model
				model.Data = nil
				model.Metadata = nil
				event.Model = &model
			}
			envelope := Event{Epoch: h.RuntimeID, Core: &event}
			if event.Type == core.EventRunStart || event.Type == core.EventRunEnd || event.Type == core.EventApproval || event.Type == core.EventContextApplied || event.Attempt != nil || event.Type == core.EventMaintenanceStart || event.Type == core.EventMaintenanceEnd {
				if view, viewErr := h.View(r.Context(), r.PathValue("id")); viewErr == nil {
					envelope.View = &view
					lastView, _ = json.Marshal(view)
				}
			}
			if event.EntryID != "" {
				if entry, readErr := session.History().Get(r.Context(), event.EntryID); readErr == nil {
					entry = timelineEntry(entry)
					envelope.Entry = &entry
					if entry.Kind == core.EntryMessage && entry.Message != nil && entry.Message.Role == core.RoleAssistant && entry.Usage.Available {
						if view, viewErr := h.View(r.Context(), r.PathValue("id")); viewErr == nil {
							envelope.View = &view
							lastView, _ = json.Marshal(view)
						}
					}
				}
			}
			if !send("event", fmt.Sprintf("%s:%d", h.RuntimeID, event.Sequence), envelope) {
				return
			}
		case <-stateTicker.C:
			view, viewErr := h.View(r.Context(), r.PathValue("id"))
			if viewErr != nil {
				_ = send("resync", "", map[string]string{"reason": "Session view changed"})
				return
			}
			raw, _ := json.Marshal(view)
			if string(raw) != string(lastView) {
				lastView = raw
				if !send("state", "", Event{Epoch: h.RuntimeID, View: &view}) {
					return
				}
			} else {
				_ = controller.SetWriteDeadline(time.Now().Add(15 * time.Second))
				if _, err = io.WriteString(w, ": keepalive\n\n"); err != nil {
					return
				}
				if controller.Flush() != nil {
					return
				}
			}
		}
	}
}
func (a *API) exportRollout(w http.ResponseWriter, r *http.Request) {
	session, err := a.Config.Harness.Core(r.Context(), r.PathValue("id"))
	if err != nil {
		httpcontract.Failure(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": session.ID() + ".jsonl"}))
	w.Header().Set("Cache-Control", "no-store")
	encoder := json.NewEncoder(w)
	state := session.State()
	if err = encoder.Encode(map[string]any{"format": "astral-rollout-export", "version": 2, "session": state}); err != nil {
		return
	}
	// Capture finite attempt membership first. Each observed result already has
	// a durable history node, so scanning history afterwards includes every
	// referenced body even if the Session continues during the export.
	cursor := ""
	attemptsDone := false
	for {
		page, scanErr := session.Attempts().Scan(r.Context(), core.AttemptQuery{Cursor: cursor, Limit: 20, MaxBytes: 128 << 20})
		if scanErr != nil {
			panic(http.ErrAbortHandler)
		}
		for _, attempt := range page.Attempts {
			if attempt.Version > state.Version {
				attemptsDone = true
				break
			}
			if encoder.Encode(map[string]any{"type": "attempt", "attempt": attempt}) != nil {
				return
			}
		}
		if attemptsDone || page.Next == "" {
			break
		}
		cursor = page.Next
	}
	latest, err := a.Config.Harness.CoreStore.Scan(r.Context(), session.RolloutID(), core.Query{Reverse: true, Limit: 1, MaxBytes: 128 << 20})
	if err != nil {
		panic(http.ErrAbortHandler)
	}
	if len(latest.Records) == 0 {
		return
	}
	lastSequence := latest.Records[0].Entry.Sequence
	cursor = ""
	for {
		page, scanErr := a.Config.Harness.CoreStore.Scan(r.Context(), session.RolloutID(), core.Query{Cursor: cursor, Limit: 20, MaxBytes: 128 << 20})
		if scanErr != nil {
			panic(http.ErrAbortHandler)
		}
		for _, record := range page.Records {
			if record.Entry.Sequence > lastSequence {
				return
			}
			if encoder.Encode(map[string]any{"type": "entry", "record": record}) != nil {
				return
			}
		}
		if page.Next == "" {
			return
		}
		cursor = page.Next
	}
}
func (a *API) upload(w http.ResponseWriter, r *http.Request) {
	h := a.Config.Harness
	var session domain.Session
	if err := h.Store.Get(r.Context(), "sessions", r.PathValue("id"), &session); err != nil {
		httpcontract.Failure(w, r, err)
		return
	}
	computerID := r.URL.Query().Get("computer_id")
	if computerID == "" {
		computerID = session.ComputerID
	}
	b, err := h.Hub.Bind(r.Context(), session.AgentID, session.ID, computerID, "")
	if err != nil {
		httpcontract.Failure(w, r, err)
		return
	}
	name := r.URL.Query().Get("name")
	if name == "" {
		httpcontract.Failure(w, r, domain.Invalid("attachment filename is required"))
		return
	}
	ref, err := h.Files.Upload(r.Context(), b, name, http.MaxBytesReader(w, r.Body, 512<<20))
	if err != nil {
		httpcontract.Failure(w, r, err)
		return
	}
	httpcontract.JSON(w, 200, gateway.ReferenceResponse{File: ref, URI: filestream.URL(ref)})
}
func (a *API) fileContent(w http.ResponseWriter, r *http.Request) {
	h := a.Config.Harness
	var ref domain.FileRef
	if err := h.Store.Get(r.Context(), "files", r.PathValue("id"), &ref); err != nil {
		httpcontract.Failure(w, r, err)
		return
	}
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		httpcontract.Failure(w, r, domain.Invalid("Session is required for source Computer access"))
		return
	}
	b, err := h.Hub.Bind(r.Context(), ref.AgentID, sessionID, ref.ComputerID, "")
	if err != nil {
		httpcontract.Failure(w, r, err)
		return
	}
	var info computers.FileInfo
	if err = h.Hub.JSON(r.Context(), "file.stat", computers.Call{Binding: b, Path: ref.Path, Version: ref.Version}, &info); err != nil {
		httpcontract.Failure(w, r, err)
		return
	}
	offset, length, partial, err := parseRange(r.Header.Get("Range"), info.Size)
	if err != nil {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", info.Size))
		httpcontract.JSON(w, 416, httpcontract.Error{Code: "invalid_range", Message: err.Error()})
		return
	}
	_, _, body, err := h.Files.Read(r.Context(), ref.AgentID, sessionID, filestream.URL(ref), offset, &length)
	if err != nil {
		httpcontract.Failure(w, r, err)
		return
	}
	defer body.Close()
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	w.Header().Set("Content-Type", info.MediaType)
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	w.Header().Set("Cache-Control", "no-store")
	disposition := "inline"
	if r.URL.Query().Get("download") == "true" {
		disposition = "attachment"
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": ref.DisplayName}))
	if partial {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, offset+length-1, info.Size))
		w.WriteHeader(206)
	}
	if _, err = io.CopyBuffer(w, body, make([]byte, 32<<10)); err != nil {
		panic(http.ErrAbortHandler)
	}
}
func parseRange(raw string, size int64) (offset, length int64, partial bool, err error) {
	length = size
	if raw == "" {
		return
	}
	partial = true
	if !strings.HasPrefix(raw, "bytes=") || strings.Contains(raw, ",") {
		err = domain.Invalid("only one bytes range is supported")
		return
	}
	parts := strings.Split(strings.TrimPrefix(raw, "bytes="), "-")
	if len(parts) != 2 || size == 0 {
		err = domain.Invalid("range is outside the file")
		return
	}
	if parts[0] == "" {
		var suffix int64
		suffix, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil || suffix <= 0 {
			err = domain.Invalid("invalid byte suffix")
			return
		}
		length = min(size, suffix)
		offset = size - length
		return
	}
	offset, err = strconv.ParseInt(parts[0], 10, 64)
	if err != nil || offset < 0 || offset >= size {
		err = domain.Invalid("range is outside the file")
		return
	}
	end := size - 1
	if parts[1] != "" {
		end, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil || end < offset {
			err = domain.Invalid("invalid range end")
			return
		}
		end = min(end, size-1)
	}
	length = end - offset + 1
	return
}
func images(value reflect.Value, out *[]core.Content) {
	if !value.IsValid() {
		return
	}
	if value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface {
		if !value.IsNil() {
			images(value.Elem(), out)
		}
		return
	}
	if value.Type() == reflect.TypeFor[core.Content]() {
		part := value.Interface().(core.Content)
		if part.Type == "image" {
			*out = append(*out, part)
		}
		return
	}
	if value.Kind() == reflect.Struct {
		for i := 0; i < value.NumField(); i++ {
			if value.Type().Field(i).IsExported() {
				images(value.Field(i), out)
			}
		}
	}
	if value.Kind() == reflect.Slice || value.Kind() == reflect.Array {
		if value.Type().Elem().Kind() == reflect.Uint8 {
			return
		}
		for i := 0; i < value.Len(); i++ {
			images(value.Index(i), out)
		}
	}
}
func (a *API) historyImage(w http.ResponseWriter, r *http.Request) {
	session, err := a.Config.Harness.Core(r.Context(), r.PathValue("id"))
	if err != nil {
		httpcontract.Failure(w, r, err)
		return
	}
	entry, err := session.History().Get(r.Context(), r.PathValue("entry"))
	if err != nil {
		httpcontract.Failure(w, r, err)
		return
	}
	all := []core.Content{}
	images(reflect.ValueOf(entry), &all)
	index, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || index < 0 || index >= len(all) {
		httpcontract.Failure(w, r, domain.Missing("history image", r.PathValue("index")))
		return
	}
	part := all[index]
	if !strings.HasPrefix(part.MediaType, "image/") {
		httpcontract.Failure(w, r, domain.Invalid("history item is not an image"))
		return
	}
	var encoded string
	if json.Unmarshal(part.Data, &encoded) != nil {
		httpcontract.Failure(w, r, domain.Missing("frozen image payload", entry.ID))
		return
	}
	w.Header().Set("Content-Type", part.MediaType)
	w.Header().Set("Cache-Control", "private, max-age=86400, immutable")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("X-Astral-Media-Source", "rollout")
	if _, err = io.CopyBuffer(w, base64.NewDecoder(base64.StdEncoding, strings.NewReader(encoded)), make([]byte, 32<<10)); err != nil {
		panic(http.ErrAbortHandler)
	}
}
