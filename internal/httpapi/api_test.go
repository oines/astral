package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	sessions "github.com/oines/astral-core"
	"github.com/oines/astral/internal/channels"
	"github.com/oines/astral/internal/gateway"
	"github.com/oines/astral/internal/harness"
	"github.com/oines/astral/internal/testkit"
)

func apiFixture(t *testing.T, p *testkit.Platform) (*API, *httptest.Server) {
	t.Helper()
	a := New(Config{Harness: p.Harness, Channels: channels.New(p.Store, p.Harness), OwnerToken: "owner-test-key", DataDirectory: p.Directory, Version: "test"})
	server := httptest.NewServer(a)
	t.Cleanup(server.Close)
	return a, server
}
func request(t *testing.T, ctx context.Context, server *httptest.Server, method, path string, body io.Reader, auth bool) *http.Response {
	t.Helper()
	r, err := http.NewRequestWithContext(ctx, method, server.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if auth {
		r.Header.Set("Authorization", "Bearer owner-test-key")
	}
	response, err := server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
func TestOwnerAuthAndIndependentHistoricalImageAndOriginalDownload(t *testing.T) {
	p := testkit.New(t, 1, func(context.Context, int, map[string]any) any { return testkit.Text("image received") })
	_, server := apiFixture(t, p)
	response := request(t, p.Context, server, "GET", "/api/v1/bootstrap", nil, false)
	if response.StatusCode != 401 {
		t.Fatal("Owner API accepted anonymous access")
	}
	response.Body.Close()
	image, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aK7kAAAAASUVORK5CYII=")
	response = request(t, p.Context, server, "POST", "/api/v1/sessions/"+p.Session.ID+"/attachments?name=image.png", bytes.NewReader(image), true)
	var file gateway.ReferenceResponse
	err := json.NewDecoder(response.Body).Decode(&file)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 {
		t.Fatalf("stream upload failed: %d %v", response.StatusCode, err)
	}
	r, _ := http.NewRequestWithContext(p.Context, "GET", server.URL+"/api/v1/files/"+file.File.ID+"/content?session_id="+p.Session.ID, nil)
	r.Header.Set("Authorization", "Bearer owner-test-key")
	r.Header.Set("Range", "bytes=8-15")
	response, err = server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 206 || !bytes.Equal(raw, image[8:16]) {
		t.Fatalf("source range failed: %d %v %x", response.StatusCode, err, raw)
	}
	input := harness.InputRequest{ID: "image", Text: "Look", Files: []string{file.URI}}
	response = request(t, p.Context, server, "POST", "/api/v1/sessions/"+p.Session.ID+"/inputs", bytes.NewReader(testkit.JSON(input)), true)
	if response.StatusCode != 200 {
		raw, _ = io.ReadAll(response.Body)
		t.Fatalf("input failed: %s", raw)
	}
	response.Body.Close()
	p.Idle(t, p.Session.ID)
	session, _ := p.Harness.Core(p.Context, p.Session.ID)
	page, err := session.History().Scan(p.Context, sessions.Query{Kind: sessions.EntryMessage, MaxBytes: 64 << 20})
	if err != nil {
		t.Fatal(err)
	}
	entryID := ""
	for _, entry := range page.Entries {
		if entry.Message.Role == "user" {
			entryID = entry.ID
			break
		}
	}
	if entryID == "" {
		t.Fatal("input was not persisted")
	}
	if err = os.Remove(file.File.Path); err != nil {
		t.Fatal(err)
	}
	_ = p.Daemons[0].Close(p.Context)
	response = request(t, p.Context, server, "GET", "/api/v1/sessions/"+p.Session.ID+"/entries/"+entryID+"/images/0", nil, true)
	raw, err = io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || !bytes.Equal(raw, image) {
		t.Fatal("offline historical preview depended on original file")
	}
	response = request(t, p.Context, server, "GET", "/api/v1/files/"+file.File.ID+"/content?session_id="+p.Session.ID, nil, true)
	if response.StatusCode == 200 {
		t.Fatal("original file API silently substituted rollout payload")
	}
	response.Body.Close()
	response = request(t, p.Context, server, "GET", "/api/v1/sessions/"+p.Session.ID+"/rollout", nil, true)
	raw, err = io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || !strings.Contains(string(raw), base64.StdEncoding.EncodeToString(image)) || !strings.Contains(string(raw), `"type":"attempt"`) || strings.Contains(string(raw), `"model_request"`) {
		t.Fatal("rollout export lost the canonical image or separate attempt references")
	}
	response = request(t, p.Context, server, "GET", "/api/v1/sessions/"+p.Session.ID+"/attempts", nil, true)
	var attempts sessions.AttemptPage
	err = json.NewDecoder(response.Body).Decode(&attempts)
	response.Body.Close()
	if err != nil || len(attempts.Attempts) != 1 || attempts.Attempts[0].Outcome == nil || attempts.Attempts[0].Outcome.Reply != nil {
		t.Fatalf("separate attempt records missing: %v", err)
	}
	response = request(t, p.Context, server, "GET", "/api/v1/sessions/"+p.Session.ID+"/attempts/"+attempts.Attempts[0].ID+"/request", nil, true)
	raw, err = io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || !strings.Contains(string(raw), base64.StdEncoding.EncodeToString(image)) {
		t.Fatal("referenced request reconstruction lost offline image")
	}
	response = request(t, p.Context, server, "GET", "/api/v1/sessions/"+p.Session.ID+"/history", nil, true)
	raw, err = io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || strings.Contains(string(raw), base64.StdEncoding.EncodeToString(image)) {
		t.Fatal("ordinary history unnecessarily transferred frozen base64")
	}
}

func TestContextStatsStayOnFrozenRequestUntilRunFinishes(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	p := testkit.New(t, 0, func(ctx context.Context, _ int, _ map[string]any) any {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return testkit.Text("done")
	})
	_, server := apiFixture(t, p)
	if _, err := p.Harness.Submit(p.Context, p.Session.ID, harness.InputRequest{ID: "frozen-context", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-p.Context.Done():
		t.Fatal("model did not start")
	}
	if err := p.Store.Get(p.Context, "models", p.Profile.ID, &p.Profile); err != nil {
		t.Fatal(err)
	}
	p.Profile.Overrides = map[string]json.RawMessage{"context_window": testkit.JSON(64000)}
	if err := p.Harness.Models.SaveProfile(p.Context, &p.Profile); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Harness.Models.Verify(p.Context, p.Profile.ID); err != nil {
		t.Fatal(err)
	}
	if err := p.Harness.SelectModel(p.Context, p.Session.ID, p.Profile.ID); err != nil {
		t.Fatal(err)
	}
	read := func() ContextStats {
		t.Helper()
		response := request(t, p.Context, server, "GET", "/api/v1/sessions/"+p.Session.ID+"/context", nil, true)
		defer response.Body.Close()
		var stats ContextStats
		if err := json.NewDecoder(response.Body).Decode(&stats); err != nil || response.StatusCode != 200 {
			t.Fatalf("context response failed: status %d, %v", response.StatusCode, err)
		}
		return stats
	}
	if stats := read(); stats.ContextWindow != 128000 || stats.EstimatedInput <= 0 {
		t.Fatalf("active request changed with the next model selection: %+v", stats)
	}
	close(release)
	p.Idle(t, p.Session.ID)
	if stats := read(); stats.ContextWindow != 64000 {
		t.Fatalf("completed run did not expose the next request projection: %+v", stats)
	}
}

func TestIdleContextUsesCurrentVerifiedProfileWithoutStartingRun(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any {
		t.Error("context inspection started a model request")
		return testkit.Text("unexpected")
	})
	_, server := apiFixture(t, p)
	if err := p.Store.Get(p.Context, "models", p.Profile.ID, &p.Profile); err != nil {
		t.Fatal(err)
	}
	p.Profile.Overrides = map[string]json.RawMessage{"context_window": testkit.JSON(64000)}
	if err := p.Harness.Models.SaveProfile(p.Context, &p.Profile); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Harness.Models.Verify(p.Context, p.Profile.ID); err != nil {
		t.Fatal(err)
	}
	coreSession, err := p.Harness.Core(p.Context, p.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	before := testkit.JSON(coreSession.State())
	response := request(t, p.Context, server, "GET", "/api/v1/sessions/"+p.Session.ID+"/context", nil, true)
	defer response.Body.Close()
	var stats ContextStats
	if err := json.NewDecoder(response.Body).Decode(&stats); err != nil || response.StatusCode != 200 || stats.ContextWindow != 64000 {
		t.Fatalf("idle profile revision did not refresh: %+v, HTTP %d, %v", stats, response.StatusCode, err)
	}
	view, err := p.Harness.View(p.Context, p.Session.ID)
	if err != nil || view.RunID != "" || view.Status != "idle" {
		t.Fatal("context inspection activated the Session")
	}
	if string(before) != string(testkit.JSON(coreSession.State())) {
		t.Fatal("context inspection changed persisted Session state")
	}
}

func TestEventSubscriptionPrecedesSnapshotAndReconcilesCommit(t *testing.T) {
	release := make(chan struct{})
	p := testkit.New(t, 0, func(ctx context.Context, _ int, _ map[string]any) any {
		select {
		case <-release:
		case <-ctx.Done():
			return nil
		}
		return testkit.Text("committed answer")
	})
	_, server := apiFixture(t, p)
	streamCtx, stop := context.WithCancel(p.Context)
	defer stop()
	response := request(t, streamCtx, server, "GET", "/api/v1/sessions/"+p.Session.ID+"/events", nil, true)
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 4096), 8<<20)
	if !scanner.Scan() || !strings.HasPrefix(scanner.Text(), "id: ") {
		t.Fatal("missing server epoch")
	}
	if !scanner.Scan() || scanner.Text() != "event: snapshot" {
		t.Fatal("stream did not begin with authoritative snapshot")
	}
	if !scanner.Scan() {
		t.Fatal("missing snapshot")
	}
	var snapshot Snapshot
	if err := json.Unmarshal([]byte(strings.TrimPrefix(scanner.Text(), "data: ")), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.View.ID != p.Session.ID {
		t.Fatal("snapshot Session differs")
	}
	if snapshot.View.ContextUsage != nil {
		t.Fatal("snapshot invented usage before the first response")
	}
	if _, err := p.Harness.Submit(p.Context, p.Session.ID, harness.InputRequest{ID: "stream-input", Text: "run"}); err != nil {
		t.Fatal(err)
	}
	close(release)
	entries := map[string]bool{}
	assistantID := ""
	for scanner.Scan() {
		if !strings.HasPrefix(scanner.Text(), "data: ") {
			continue
		}
		var event Event
		if json.Unmarshal([]byte(strings.TrimPrefix(scanner.Text(), "data: ")), &event) != nil {
			continue
		}
		if event.Epoch != p.Harness.RuntimeID {
			t.Fatal("mixed server event epoch")
		}
		if event.Entry != nil {
			entries[event.Entry.ID] = true
			if event.Entry.Message != nil && event.Entry.Message.Role == "assistant" {
				assistantID = event.Entry.ID
				if event.View == nil || event.View.ContextUsage == nil || event.View.ContextUsage.InputTokens != 20 || event.View.ContextUsage.EntryID != assistantID || event.View.ContextUsage.ContextWindow != 128000 {
					t.Fatalf("committed model usage did not reach the subscription: %+v", event.View)
				}
			}
		}
		if event.Core != nil && event.Core.Type == "run_end" {
			break
		}
	}
	stop()
	if assistantID == "" || len(entries) < 3 {
		t.Fatal("commit disappeared between snapshot and subscription")
	}
	snapshotResponse := request(t, p.Context, server, "GET", "/api/v1/sessions/"+p.Session.ID+"/snapshot", nil, true)
	defer snapshotResponse.Body.Close()
	var after Snapshot
	if err := json.NewDecoder(snapshotResponse.Body).Decode(&after); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range after.History.Entries {
		if entry.ID == assistantID {
			found = true
		}
	}
	if !found {
		t.Fatal("streaming commit has no matching durable Entry ID on reconnect")
	}
	if after.View.ContextUsage == nil || after.View.ContextUsage.InputTokens != 20 || after.View.ContextUsage.EntryID != assistantID {
		t.Fatal("reconnection snapshot lost the last reported usage")
	}
}

func TestContractHasRuntimeRoutesAndStreamSchemas(t *testing.T) {
	doc := Contract().Document()
	paths := doc["paths"].(map[string]any)
	for _, path := range []string{"/api/v1/sessions/{id}/inputs", "/api/v1/sessions/{id}/events", "/v1/im/files", "/api/v1/computers/containers"} {
		if paths[path] == nil {
			t.Fatalf("missing runtime contract %s", path)
		}
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "text/event-stream") || !strings.Contains(string(raw), "ReasoningOptions") {
		t.Fatal("stream or native Reasoning contract missing")
	}
}

func TestRetrySnapshotSurvivesEventStreamReconnectWithoutResubmission(t *testing.T) {
	p := testkit.New(t, 0, func(_ context.Context, n int, _ map[string]any) any {
		if n == 1 {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", "2")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = io.WriteString(w, `{"error":{"type":"rate_limit_error","message":"fixture throttled"}}`)
			})
		}
		return testkit.Text("one committed answer")
	})
	_, server := apiFixture(t, p)
	path := "/api/v1/sessions/" + p.Session.ID
	connect := func() (*http.Response, *bufio.Scanner, Snapshot) {
		t.Helper()
		response := request(t, p.Context, server, "GET", path+"/events", nil, true)
		scanner := bufio.NewScanner(response.Body)
		scanner.Buffer(make([]byte, 4096), 8<<20)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "data: ") {
				var snapshot Snapshot
				if err := json.Unmarshal([]byte(strings.TrimPrefix(scanner.Text(), "data: ")), &snapshot); err != nil {
					t.Fatal(err)
				}
				return response, scanner, snapshot
			}
		}
		t.Fatalf("stream ended before snapshot: %v", scanner.Err())
		return nil, nil, Snapshot{}
	}
	response, scanner, _ := connect()
	defer response.Body.Close()
	submission := request(t, p.Context, server, "POST", path+"/inputs", bytes.NewReader(testkit.JSON(harness.InputRequest{ID: "reconnect-input", Text: "hello"})), true)
	_ = submission.Body.Close()
	if submission.StatusCode != http.StatusOK {
		t.Fatalf("input HTTP %d", submission.StatusCode)
	}
	var retry *sessions.AttemptStatus
	for scanner.Scan() {
		if !strings.HasPrefix(scanner.Text(), "data: ") {
			continue
		}
		var event Event
		if json.Unmarshal([]byte(strings.TrimPrefix(scanner.Text(), "data: ")), &event) != nil || event.Core == nil || event.Core.Type != sessions.EventRetry {
			continue
		}
		retry = event.Core.Attempt
		if event.View == nil || event.View.Attempt == nil || event.View.Attempt.State != "retrying" {
			t.Fatal("retry event did not carry the current execution snapshot")
		}
		break
	}
	if retry == nil || retry.Attempt != 1 || retry.MaxAttempts != 3 || retry.Purpose != "generation" || !retry.NextRetryAt.After(time.Now()) {
		t.Fatalf("missing retry progress: %+v", retry)
	}
	_ = response.Body.Close()
	response, scanner, snapshot := connect()
	defer response.Body.Close()
	if snapshot.View.Attempt == nil || snapshot.View.Attempt.AttemptID != retry.AttemptID || snapshot.View.Attempt.State != "retrying" || snapshot.View.RunID != retry.RunID || !snapshot.View.Attempt.NextRetryAt.Equal(retry.NextRetryAt) {
		t.Fatalf("reconnect lost active retry: %+v", snapshot.View)
	}
	if p.Requests.Load() != 1 {
		t.Fatal("reconnect started an extra model request")
	}
	completed := false
	for scanner.Scan() {
		if !strings.HasPrefix(scanner.Text(), "data: ") {
			continue
		}
		var event Event
		if json.Unmarshal([]byte(strings.TrimPrefix(scanner.Text(), "data: ")), &event) != nil || event.Core == nil {
			continue
		}
		if event.Core.Type == sessions.EventRunEnd {
			completed = event.Core.Error == ""
			break
		}
	}
	if !completed || p.Requests.Load() != 2 {
		t.Fatalf("expected one retry and one completion, got requests=%d completed=%v", p.Requests.Load(), completed)
	}
	p.Idle(t, p.Session.ID)
	coreSession, err := p.Harness.Core(p.Context, p.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	page, err := coreSession.History().Scan(p.Context, sessions.Query{Kind: sessions.EntryInputQueued, MaxBytes: 1 << 20})
	if err != nil || len(page.Entries) != 1 {
		t.Fatalf("reconnect duplicated input: %d %v", len(page.Entries), err)
	}
}
