package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oines/astral/internal/channels"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/harness"
	"github.com/oines/astral/internal/testkit"
)

type invalidationFrame struct {
	kind     string
	snapshot *Snapshot
	event    Event
	err      error
}
type invalidationStream struct {
	cancel   context.CancelFunc
	response *http.Response
	frames   chan invalidationFrame
}

func readInvalidationStream(t *testing.T, ctx context.Context, response *http.Response, cancel context.CancelFunc) *invalidationStream {
	t.Helper()
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		t.Fatalf("SSE HTTP %d", response.StatusCode)
	}
	s := &invalidationStream{cancel: cancel, response: response, frames: make(chan invalidationFrame, 256)}
	t.Cleanup(func() { s.cancel(); _ = s.response.Body.Close() })
	go func() {
		defer close(s.frames)
		scanner := bufio.NewScanner(response.Body)
		scanner.Buffer(make([]byte, 4096), 8<<20)
		kind := ""
		send := func(frame invalidationFrame) bool {
			select {
			case s.frames <- frame:
				return true
			case <-ctx.Done():
				return false
			}
		}
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "event: ") {
				kind = strings.TrimPrefix(line, "event: ")
			}
			if strings.HasPrefix(line, ":") {
				if !send(invalidationFrame{kind: "keepalive"}) {
					return
				}
			}
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			raw := []byte(strings.TrimPrefix(line, "data: "))
			frame := invalidationFrame{kind: kind}
			if kind == "snapshot" {
				frame.snapshot = &Snapshot{}
				frame.err = json.Unmarshal(raw, frame.snapshot)
			} else {
				frame.err = json.Unmarshal(raw, &frame.event)
			}
			if !send(frame) {
				return
			}
		}
		if err := scanner.Err(); err != nil && ctx.Err() == nil {
			send(invalidationFrame{err: err})
		}
	}()
	return s
}
func openInvalidationStream(t *testing.T, p *testkit.Platform, server *httptest.Server) (*invalidationStream, Snapshot) {
	t.Helper()
	ctx, cancel := context.WithCancel(p.Context)
	response := request(t, ctx, server, "GET", "/api/v1/sessions/"+p.Session.ID+"/events", nil, true)
	stream := readInvalidationStream(t, ctx, response, cancel)
	select {
	case frame, ok := <-stream.frames:
		if !ok || frame.err != nil || frame.kind != "snapshot" || frame.snapshot == nil {
			t.Fatalf("missing initial SSE snapshot: %+v", frame)
		}
		return stream, *frame.snapshot
	case <-p.Context.Done():
		t.Fatal(p.Context.Err())
	}
	return nil, Snapshot{}
}
func (s *invalidationStream) view(t *testing.T, ctx context.Context, why string, predicate func(harness.View) bool) harness.View {
	t.Helper()
	deadline, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	for {
		select {
		case frame, ok := <-s.frames:
			if !ok || frame.err != nil {
				t.Fatalf("SSE ended waiting for %s: %+v", why, frame)
			}
			if frame.kind == "resync" {
				t.Fatalf("unexpected resync waiting for %s", why)
			}
			if frame.event.View != nil && predicate(*frame.event.View) {
				return *frame.event.View
			}
		case <-deadline.Done():
			t.Fatalf("SSE did not show %s: %v", why, deadline.Err())
		}
	}
}
func invalidationOwnerPost(t *testing.T, p *testkit.Platform, server *httptest.Server, suffix string, value any) {
	t.Helper()
	var body io.Reader
	if value != nil {
		body = bytes.NewReader(testkit.JSON(value))
	}
	response := request(t, p.Context, server, "POST", "/api/v1/sessions/"+p.Session.ID+suffix, body, true)
	raw, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("Owner %s: HTTP%d %v %s", suffix, response.StatusCode, err, raw)
	}
}

// Cancellation is observed but deliberately not returned until release. Unlike
// a blocked httptest handler, this leaves Core Stream in progress without
// holding Core/Host locks, so Stopping must be visible before RunEnd exists.
type invalidationHeldModel struct {
	entered, canceled, release         chan struct{}
	enterOnce, cancelOnce, releaseOnce sync.Once
	calls                              atomic.Int64
}

func newInvalidationHeldModel(t *testing.T, p *testkit.Platform) *invalidationHeldModel {
	t.Helper()
	m := &invalidationHeldModel{entered: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{})}
	p.Harness.Models.HTTPClient.Transport = m
	t.Cleanup(m.finish)
	return m
}
func (m *invalidationHeldModel) finish() { m.releaseOnce.Do(func() { close(m.release) }) }
func (m *invalidationHeldModel) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/chat/completions") {
		return nil, fmt.Errorf("unexpected local model request %s", r.URL.Path)
	}
	_, err := io.Copy(io.Discard, r.Body)
	closeErr := r.Body.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	m.calls.Add(1)
	m.enterOnce.Do(func() { close(m.entered) })
	select {
	case <-r.Context().Done():
		m.cancelOnce.Do(func() { close(m.canceled) })
		<-m.release
		return nil, r.Context().Err()
	case <-m.release:
	}
	raw, _ := json.Marshal(testkit.Text("Released bounded local request."))
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(raw)), Request: r}, nil
}
func invalidationWait(t *testing.T, ctx context.Context, c <-chan struct{}, why string) {
	t.Helper()
	select {
	case <-c:
	case <-ctx.Done():
		t.Fatalf("%s: %v", why, ctx.Err())
	}
}

func TestSessionEventsBroadcastStopToTwoReadersBeforeRunEnds(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any {
		t.Error("unexpected original provider request")
		return nil
	})
	model := newInvalidationHeldModel(t, p)
	_, server := apiFixture(t, p)
	one, _ := openInvalidationStream(t, p, server)
	two, _ := openInvalidationStream(t, p, server)
	invalidationOwnerPost(t, p, server, "/inputs", harness.InputRequest{ID: "broadcast-stop", Text: "Wait in this bounded local request."})
	invalidationWait(t, p.Context, model.entered, "model start")
	for _, stream := range []*invalidationStream{one, two} {
		stream.view(t, p.Context, "running", func(v harness.View) bool { return v.Status == "running" })
	}
	invalidationOwnerPost(t, p, server, "/stop", map[string]any{})
	invalidationWait(t, p.Context, model.canceled, "model cancellation")
	for _, stream := range []*invalidationStream{one, two} {
		stream.view(t, p.Context, "stopping before model returns", func(v harness.View) bool { return v.Status == "stopping" })
	}
	state, err := p.CoreStore.LoadState(p.Context, p.Session.ID)
	if err != nil || state.LastRun == nil || state.LastRun.Status != "started" {
		t.Fatalf("fixture already ended before stopping reached both readers: %+v %v", state, err)
	}
	model.finish()
	for _, stream := range []*invalidationStream{one, two} {
		stream.view(t, p.Context, "durable terminal run", func(v harness.View) bool {
			return v.LastRun != nil && v.LastRun.Status != "started" && v.Status != "running" && v.Status != "stopping"
		})
	}
	if model.calls.Load() != 1 {
		t.Fatalf("Stop started extra model work: %d", model.calls.Load())
	}
}

func TestSessionEventsBroadcastQueuedInputsAndClear(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any {
		t.Error("unexpected original provider request")
		return nil
	})
	model := newInvalidationHeldModel(t, p)
	_, server := apiFixture(t, p)
	one, _ := openInvalidationStream(t, p, server)
	two, _ := openInvalidationStream(t, p, server)
	invalidationOwnerPost(t, p, server, "/inputs", harness.InputRequest{ID: "held-current", Text: "Hold this local request."})
	invalidationWait(t, p.Context, model.entered, "model start")
	invalidationOwnerPost(t, p, server, "/inputs", harness.InputRequest{ID: "queued-next", Text: "Keep this queued follow-up."})
	for _, stream := range []*invalidationStream{one, two} {
		stream.view(t, p.Context, "queued follow-up", func(v harness.View) bool {
			for _, q := range v.Pending {
				if q.ID == "queued-next" && q.Kind == "follow_up" {
					return true
				}
			}
			return false
		})
	}
	invalidationOwnerPost(t, p, server, "/pending/clear", map[string]string{"kind": "follow_up"})
	for _, stream := range []*invalidationStream{one, two} {
		stream.view(t, p.Context, "cleared follow-up while still running", func(v harness.View) bool { return v.Status == "running" && len(v.Pending) == 0 })
	}
	if model.calls.Load() != 1 {
		t.Fatalf("queued work unexpectedly generated: %d", model.calls.Load())
	}
	invalidationOwnerPost(t, p, server, "/stop", map[string]any{})
	model.finish()
}

func TestSessionEventsBroadcastModelAndAvailabilityChanges(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any {
		t.Error("model selection generated a reply")
		return nil
	})
	_, server := apiFixture(t, p)
	one, _ := openInvalidationStream(t, p, server)
	two, _ := openInvalidationStream(t, p, server)
	other := p.Profile
	other.Meta = domain.Meta{}
	other.Name = "Other local fixture model"
	other.ModelID = "other-local-fixture"
	other.Overrides = p.Profile.Recommendations
	if err := p.Harness.Models.SaveProfile(p.Context, &other); err != nil {
		t.Fatal(err)
	}
	invalidationOwnerPost(t, p, server, "/model", SelectModel{ProfileID: other.ID})
	for _, stream := range []*invalidationStream{one, two} {
		stream.view(t, p.Context, "new model selection", func(v harness.View) bool { return v.ProfileID == other.ID && v.ModelAvailable })
	}
	for _, enabled := range []bool{false, true} {
		if err := p.Store.Get(p.Context, "models", other.ID, &other); err != nil {
			t.Fatal(err)
		}
		other.Enabled = &enabled
		if err := p.Harness.Models.SaveProfile(p.Context, &other); err != nil {
			t.Fatal(err)
		}
		for _, stream := range []*invalidationStream{one, two} {
			stream.view(t, p.Context, "Host-only model availability", func(v harness.View) bool {
				return v.ProfileID == other.ID && v.ModelAvailable == enabled && (enabled || v.Status == "needs_attention")
			})
		}
	}
	if p.Requests.Load() != 0 {
		t.Fatalf("resource edits started a model request: %d", p.Requests.Load())
	}
}

func invalidationBindChat(t *testing.T, p *testkit.Platform, api *API) domain.ConversationBinding {
	t.Helper()
	bot := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var value any
		switch {
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			value = map[string]any{"id": 7, "is_bot": true, "username": "event_fixture_bot", "first_name": "Fixture"}
		case strings.HasSuffix(r.URL.Path, "/getWebhookInfo"):
			value = map[string]any{"url": ""}
		case strings.HasSuffix(r.URL.Path, "/getChat"):
			value = map[string]any{"id": -100, "type": "group", "title": "Local event fixture"}
		default:
			t.Errorf("unexpected IM operation %s", r.URL.Path)
			http.Error(w, "forbidden fixture operation", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": value})
	}))
	t.Cleanup(bot.Close)
	c := domain.Connection{Name: "Local event fixture Bot", AgentID: p.Agent.ID, Adapter: "telegram.bot", Enabled: true, Config: testkit.JSON(channels.TelegramConfig{BaseURL: bot.URL})}
	token := "event-fixture-token"
	if err := api.Config.Channels.SaveConnection(p.Context, &c, &token); err != nil {
		t.Fatal(err)
	}
	conversation, err := api.Config.Channels.ResolveConversation(p.Context, c.ID, "-100", "")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := api.Config.Channels.Bind(p.Context, conversation.ID, p.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	return binding
}
func TestSessionEventsBroadcastIMPauseAndRecovery(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { t.Error("pause projection called a model"); return nil })
	api, server := apiFixture(t, p)
	binding := invalidationBindChat(t, p, api)
	one, _ := openInvalidationStream(t, p, server)
	two, _ := openInvalidationStream(t, p, server)
	// The real binding supplies identity; persist the actual maintenance record
	// shape consumed by Channels.IMDeliveryError, without starting IM workers.
	pause := map[string]any{"agent_id": p.Agent.ID, "session_id": p.Session.ID, "binding_id": binding.ID, "error": "Local fixture provider quota is exhausted."}
	if err := p.Store.SaveMaintenance(p.Context, "im_inbox_pause", binding.ID, "blocked", pause); err != nil {
		t.Fatal(err)
	}
	for _, stream := range []*invalidationStream{one, two} {
		stream.view(t, p.Context, "paused IM delivery", func(v harness.View) bool { return v.Status == "needs_attention" && v.Error == pause["error"] })
	}
	if err := p.Store.DeleteMaintenance(p.Context, "im_inbox_pause", binding.ID); err != nil {
		t.Fatal(err)
	}
	for _, stream := range []*invalidationStream{one, two} {
		stream.view(t, p.Context, "cleared IM failure", func(v harness.View) bool { return v.Status == "idle" && v.Error == "" })
	}
	if p.Requests.Load() != 0 {
		t.Fatalf("pause recovery generated: %d", p.Requests.Load())
	}
}

type invalidationViewCounter struct {
	harness.Messenger
	calls            atomic.Int64
	first            atomic.Bool
	entered, release chan struct{}
}

func (c *invalidationViewCounter) IMDeliveryError(ctx context.Context, id string) (string, error) {
	c.calls.Add(1)
	if c.entered != nil && c.first.CompareAndSwap(false, true) {
		close(c.entered)
		select {
		case <-c.release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return c.Messenger.(interface {
		IMDeliveryError(context.Context, string) (string, error)
	}).IMDeliveryError(ctx, id)
}
func TestSessionEventsUnchangedIdleDoesNotReadViewEverySecond(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { t.Error("idle SSE called a model"); return nil })
	_, server := apiFixture(t, p)
	counted := &invalidationViewCounter{Messenger: p.Harness.IM}
	p.Harness.IM = counted
	one, _ := openInvalidationStream(t, p, server)
	two, _ := openInvalidationStream(t, p, server)
	before := counted.calls.Load()
	if before != 2 {
		t.Fatalf("expected one real idle View per initial snapshot, got %d", before)
	}
	// Cross more than two periods of the removed one-second View ticker. The
	// production keepalive is 15 seconds; the separate natural replay reads it.
	time.Sleep(2250 * time.Millisecond)
	if after := counted.calls.Load(); after != before {
		t.Fatalf("unchanged SSE repeatedly rebuilt View: %d -> %d", before, after)
	}
	for _, stream := range []*invalidationStream{one, two} {
		select {
		case frame, ok := <-stream.frames:
			if !ok || frame.err != nil || frame.kind != "keepalive" {
				t.Fatalf("idle SSE changed or closed: %+v", frame)
			}
		default:
		}
	}
	if p.Requests.Load() != 0 {
		t.Fatal("idle subscription generated work")
	}
}

func TestSessionEventsRetainHostChangeDuringInitialSnapshot(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any {
		t.Error("snapshot resource edit called a model")
		return nil
	})
	_, server := apiFixture(t, p)
	gate := &invalidationViewCounter{Messenger: p.Harness.IM, entered: make(chan struct{}), release: make(chan struct{})}
	p.Harness.IM = gate
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(gate.release) }) })
	ctx, cancel := context.WithCancel(p.Context)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/v1/sessions/"+p.Session.ID+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Desktop desktop-test-key")
	type responseResult struct {
		response *http.Response
		err      error
	}
	responses := make(chan responseResult, 1)
	go func() { response, err := server.Client().Do(req); responses <- responseResult{response, err} }()
	invalidationWait(t, p.Context, gate.entered, "View read old availability")
	if err := p.Store.Get(p.Context, "models", p.Profile.ID, &p.Profile); err != nil {
		t.Fatal(err)
	}
	enabled := false
	p.Profile.Enabled = &enabled
	if err := p.Harness.Models.SaveProfile(p.Context, &p.Profile); err != nil {
		t.Fatal(err)
	}
	release.Do(func() { close(gate.release) })
	var response responseResult
	select {
	case response = <-responses:
	case <-p.Context.Done():
		t.Fatal(p.Context.Err())
	}
	if response.err != nil {
		t.Fatal(response.err)
	}
	stream := readInvalidationStream(t, ctx, response.response, cancel)
	first := <-stream.frames
	if first.snapshot == nil || !first.snapshot.View.ModelAvailable {
		t.Fatalf("barrier did not preserve old initial view: %+v", first)
	}
	stream.view(t, p.Context, "Host change committed during initial snapshot", func(v harness.View) bool { return !v.ModelAvailable && v.Status == "needs_attention" })
	if p.Requests.Load() != 0 {
		t.Fatal("snapshot reconciliation generated work")
	}
}
