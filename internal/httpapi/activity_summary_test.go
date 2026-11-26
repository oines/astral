package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/gateway"
	"github.com/oines/astral/internal/harness"
	"github.com/oines/astral/internal/messages"
	"github.com/oines/astral/internal/testkit"
)

func TestActivitySummaryUsesOneBoundedCallForTwoStreams(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		mode     string
		watchers int
		want     int64
		acp      bool
	}{
		{name: "unobserved"},
		{name: "disabled", mode: "disabled", watchers: 2},
		{name: "shared", watchers: 2, want: 1},
		{name: "ACP user goal", watchers: 1, want: 1, acp: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			const clientName = `Editor "Desk" & Lab`
			const goalHead = "Compare password rules at http://example.test/docs."
			goal := goalHead + " Preserve <scope> & original text.\n" + strings.Repeat("More background. ", 20) + "fixture-only-opaque-value"
			var calls atomic.Int64
			mainMayFinish := make(chan struct{})
			p := testkit.New(t, 0, func(_ context.Context, _ int, body map[string]any) any {
				if body["max_tokens"] == float64(40) || body["max_completion_tokens"] == float64(40) {
					calls.Add(1)
					wire := string(testkit.JSON(body["messages"]))
					if !strings.Contains(wire, goalHead) ||
						!strings.Contains(wire, "Checking secret handling.") ||
						strings.Contains(wire, "fixture-only-opaque-value") || len(wire) > 3000 {
						t.Error("summary request did not keep the bounded display input")
					}
					return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprint(w, "data: {\"id\":\"summary\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Checking results\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
					})
				}
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					writeDelta := func(delta map[string]string, finish any) {
						raw, _ := json.Marshal(map[string]any{"id": "activity-fixture", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
						fmt.Fprintf(w, "data: %s\n\n", raw)
						w.(http.Flusher).Flush()
					}
					writeDelta(map[string]string{"role": "assistant", "reasoning_content": strings.Repeat("Checking secret handling. ", 15)}, nil)
					select {
					case <-r.Context().Done():
						return
					case <-mainMayFinish:
					}
					writeDelta(map[string]string{"content": "The output is ready."}, "stop")
					fmt.Fprint(w, "data: [DONE]\n\n")
				})
			})
			if err := p.Store.Get(p.Context, "models", p.Profile.ID, &p.Profile); err != nil {
				t.Fatal(err)
			}
			p.Profile.Overrides = map[string]json.RawMessage{"streaming": testkit.JSON(true)}
			if err := p.Harness.Models.SaveProfile(p.Context, &p.Profile); err != nil {
				t.Fatal(err)
			}
			p.Agent.ActivitySummaryMode = scenario.mode
			if err := p.Harness.SaveAgent(p.Context, &p.Agent); err != nil {
				t.Fatal(err)
			}
			title := "Activity preview"
			if err := p.Harness.SaveSessionPresentation(p.Context, p.Session.ID, &title, nil, nil, nil); err != nil {
				t.Fatal(err)
			}
			_, server := apiFixture(t, p)
			var streams []*invalidationStream
			for range scenario.watchers {
				stream, _ := openInvalidationStream(t, p, server)
				streams = append(streams, stream)
			}
			input := harness.InputRequest{ID: "activity-input", Text: goal}
			if scenario.acp {
				if _, err := p.Harness.SubmitACP(p.Context, p.Session.ID, input, clientName); err != nil {
					t.Fatal(err)
				}
			} else {
				invalidationOwnerPost(t, p, server, "/inputs", input)
			}
			if scenario.want > 0 {
				for _, stream := range streams {
					waitActivityStatus(t, p.Context, stream, "summarizer")
				}
			}
			close(mainMayFinish)
			for _, stream := range streams {
				waitActivityStatus(t, p.Context, stream, "idle")
			}
			testkit.Await(t, p.Context, func() bool {
				snapshot, err := p.Harness.Snapshot(p.Context, p.Session.ID)
				return err == nil && snapshot.State.LastRun != nil && !snapshot.Running
			})
			if got := calls.Load(); got != scenario.want {
				t.Fatalf("summary calls = %d, want %d", got, scenario.want)
			}
			if !scenario.acp {
				return
			}
			response := request(t, p.Context, server, http.MethodGet, "/api/v1/sessions/"+p.Session.ID+"/timeline", nil, true)
			defer response.Body.Close()
			var page gateway.HistoryPage
			if err := json.NewDecoder(response.Body).Decode(&page); response.StatusCode != http.StatusOK || err != nil {
				t.Fatalf("timeline: status %d, decode error %v", response.StatusCode, err)
			}
			for _, entry := range page.Entries {
				if entry.InputID != input.ID || entry.Message == nil || entry.Message.Role != core.RoleUser {
					continue
				}
				var metadata messages.Metadata
				if err := json.Unmarshal(entry.Message.Metadata, &metadata); err != nil {
					t.Fatal(err)
				}
				if metadata.Source != "acp" || metadata.DisplayText != goal || metadata.Sender.ID != "owner" || metadata.Sender.Name != "Owner" {
					t.Fatalf("unexpected ACP timeline metadata: %+v", metadata)
				}
				for _, part := range entry.Message.Content {
					if part.Type == "text" && part.Text != "" {
						t.Fatal("timeline leaked the model input text")
					}
				}
				return
			}
			t.Fatal("the Owner timeline is missing the ACP user message")
		})
	}
}

func waitActivityStatus(t *testing.T, ctx context.Context, stream *invalidationStream, want string) {
	t.Helper()
	deadline, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	for {
		select {
		case frame, ok := <-stream.frames:
			if !ok || frame.err != nil || frame.kind == "resync" {
				t.Fatalf("activity stream ended waiting for %s: %+v", want, frame)
			}
			status := frame.event.ActivityStatus
			if status != nil && (status.State == want || status.Source == want) {
				return
			}
		case <-deadline.Done():
			t.Fatalf("missing activity status %s: %v", want, deadline.Err())
		}
	}
}
