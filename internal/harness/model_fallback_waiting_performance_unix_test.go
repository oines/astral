//go:build darwin || linux

package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
)

type fallbackWaitingTransport struct {
	mu      sync.Mutex
	host    string
	initial []time.Time
	probes  []time.Time
	errors  []string
}

func fallbackWaitingText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	for _, part := range parts {
		if part.Type == "text" {
			text += part.Text
		}
	}
	return text
}

func (r *fallbackWaitingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	defer request.Body.Close()
	raw, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	var body struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Tools []json.RawMessage `json:"tools"`
	}
	err = json.Unmarshal(raw, &body)
	var firstText, secondText string
	if len(body.Messages) > 0 {
		firstText = fallbackWaitingText(body.Messages[0].Content)
	}
	if len(body.Messages) > 1 {
		secondText = fallbackWaitingText(body.Messages[1].Content)
	}
	probe := len(body.Messages) == 2 && body.Messages[0].Role == "system" && firstText == "Reply with OK." && secondText == "Check that generation is available."
	r.mu.Lock()
	if request.Method != http.MethodPost || request.URL.Host != r.host || !strings.HasSuffix(request.URL.Path, "/chat/completions") || body.Model != "fixture" || err != nil {
		r.errors = append(r.errors, "unexpected offline request shape or endpoint")
	}
	if probe {
		r.probes = append(r.probes, time.Now())
		if len(body.Tools) != 0 {
			r.errors = append(r.errors, "recovery detection included tools")
		}
	} else {
		r.initial = append(r.initial, time.Now())
		if len(body.Messages) == 0 || !strings.Contains(firstText, hostPromptHeading) {
			r.errors = append(r.errors, "non-probe request lost the normal Host prompt")
		}
	}
	r.mu.Unlock()
	// No dial or fallback RoundTripper exists. The complete request body is
	// consumed, but only fixed-size timestamps and counters are retained.
	return modelFallbackTestResponse(request, http.StatusPaymentRequired, `{"error":{"message":"synthetic quota exhausted","code":"insufficient_quota"}}`), nil
}

func (r *fallbackWaitingTransport) snapshot() ([]time.Time, []time.Time, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]time.Time(nil), r.initial...), append([]time.Time(nil), r.probes...), append([]string(nil), r.errors...)
}

func fallbackWaitingChoice(t *testing.T, name string, fallback int, choices ...int) int {
	t.Helper()
	value := fallback
	if raw := os.Getenv(name); raw != "" {
		var err error
		value, err = strconv.Atoi(raw)
		if err != nil {
			t.Fatalf("%s must be an integer", name)
		}
	}
	for _, allowed := range choices {
		if value == allowed {
			return value
		}
	}
	t.Fatalf("%s must be one of %v", name, choices)
	return 0
}

func fallbackWaitingStates(t *testing.T, f *fixture, ids []string) (map[string]modelFallbackState, map[string]uint64) {
	t.Helper()
	states, versions := make(map[string]modelFallbackState, len(ids)), make(map[string]uint64, len(ids))
	f.service.modelFallbackMu.Lock()
	for _, id := range ids {
		var state modelFallbackState
		if err := f.store.Maintenance(f.ctx, modelFallbackKind, id, &state); err != nil {
			f.service.modelFallbackMu.Unlock()
			t.Fatal(err)
		}
		states[id] = state
	}
	f.service.modelFallbackMu.Unlock()
	for _, id := range ids {
		state, err := f.coreStore.LoadState(f.ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		versions[id] = state.Version
	}
	return states, versions
}

// Measures real exhausted-quota waiting, including the production 60-second
// detector schedule. Its hot window contains only a timer: no View polling,
// forced collection, artificial due time or external model connection.
func TestModelFallbackWaitingPerformance(t *testing.T) {
	if os.Getenv("ASTRAL_FALLBACK_WAIT") != "1" {
		t.Skip("set ASTRAL_FALLBACK_WAIT=1 and ASTRAL_PERFORMANCE_OUTPUT for quota-waiting measurement")
	}
	sessions := fallbackWaitingChoice(t, "ASTRAL_FALLBACK_WAIT_SESSIONS", 1, 1, 10, 50)
	seconds := fallbackWaitingChoice(t, "ASTRAL_FALLBACK_WAIT_SECONDS", 10, 10, 65)
	output := os.Getenv("ASTRAL_PERFORMANCE_OUTPUT")
	if !filepath.IsAbs(output) {
		t.Fatal("ASTRAL_PERFORMANCE_OUTPUT must name the new absolute directory prepared by TestMain")
	}
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any {
		t.Error("request escaped the offline waiting transport")
		return nil
	})
	f.agent.ModelPriority = []core.ModelSelection{{ID: f.agent.DefaultModelID}}
	f.agent.ModelAutoRestore = true
	if err := f.service.SaveAgent(f.ctx, &f.agent); err != nil {
		t.Fatal(err)
	}
	var profile domain.ModelProfile
	var connection domain.Provider
	if err := f.store.Get(f.ctx, "models", f.agent.DefaultModelID, &profile); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Get(f.ctx, "providers", profile.ProviderID, &connection); err != nil {
		t.Fatal(err)
	}
	endpoint, err := url.Parse(connection.BaseURL)
	if err != nil {
		t.Fatal(err)
	}
	transport := &fallbackWaitingTransport{host: endpoint.Host}
	f.service.Models.HTTPClient.Transport = transport
	ids := []string{f.session.ID}
	var stopped bool
	t.Cleanup(func() {
		if !stopped {
			for _, id := range ids {
				if err := f.service.Stop(f.ctx, id); err != nil {
					t.Errorf("stop quota-waiting fixture: %v", err)
				}
			}
		}
	})
	for i := 1; i < sessions; i++ {
		session, err := f.service.CreateSession(f.ctx, f.agent.ID, fmt.Sprintf("Synthetic quota waiting %02d", i))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, session.ID)
	}
	for i, id := range ids {
		if _, err := f.service.Submit(f.ctx, id, InputRequest{ID: fmt.Sprintf("synthetic-quota-wait-%02d", i), Text: "Continue this synthetic task when its model can generate again."}); err != nil {
			t.Fatal(err)
		}
	}
	// Poll setup only. Start the measured interval after every task has
	// durably entered waiting and the initial failure notifications settle.
	eventually(t, f.ctx, func() bool {
		f.service.modelFallbackMu.Lock()
		defer f.service.modelFallbackMu.Unlock()
		for _, id := range ids {
			var state modelFallbackState
			if f.store.Maintenance(f.ctx, modelFallbackKind, id, &state) != nil || state.Phase != "waiting" || !state.Resumable {
				return false
			}
		}
		return true
	})
	select {
	case <-time.After(750 * time.Millisecond):
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	statesBefore, versionsBefore := fallbackWaitingStates(t, f, ids)
	initialBefore, probesBefore, errorsBefore := transport.snapshot()
	if len(initialBefore) != sessions || len(probesBefore) != 0 || len(errorsBefore) != 0 {
		t.Fatalf("waiting setup requests: initial=%d probes=%d errors=%v", len(initialBefore), len(probesBefore), errorsBefore)
	}
	filesBefore := performanceFixtureSnapshot(t, f)
	before := performanceResources(t)
	timer := time.NewTimer(time.Duration(seconds) * time.Second)
	select {
	case <-timer.C:
	case <-f.ctx.Done():
		timer.Stop()
		t.Fatal(f.ctx.Err())
	}
	after := performanceResources(t)
	filesAfter := performanceFixtureSnapshot(t, f)
	statesAfter, versionsAfter := fallbackWaitingStates(t, f, ids)
	initialAfter, probesAfter, requestErrors := transport.snapshot()
	stillWaiting, scheduledCorrectly := true, true
	for _, id := range ids {
		state := statesAfter[id]
		stillWaiting = stillWaiting && state.Phase == "waiting" && state.Resumable && state.Preferred.ID == f.agent.DefaultModelID
		if seconds == 10 {
			scheduledCorrectly = scheduledCorrectly && state.NextProbeAt.Equal(statesBefore[id].NextProbeAt)
		} else {
			scheduledCorrectly = scheduledCorrectly && state.NextProbeAt.After(statesBefore[id].NextProbeAt)
		}
	}
	wantProbes := 0
	if seconds == 65 {
		wantProbes = sessions
	}
	requestsCorrect := len(initialAfter) == sessions && len(probesAfter) == wantProbes && len(requestErrors) == 0 && f.count() == 0
	versionsStable := reflect.DeepEqual(versionsBefore, versionsAfter)
	var intervals []int64
	probeIntervalsCorrect := true
	// Requests and their first probes each occur once per Session. Pair their
	// chronological order to verify the observed minimum interval without
	// adding test headers or changing production request preparation.
	for i, at := range probesAfter {
		if i >= len(initialAfter) {
			probeIntervalsCorrect = false
			break
		}
		interval := at.Sub(initialAfter[i])
		intervals = append(intervals, interval.Nanoseconds())
		probeIntervalsCorrect = probeIntervalsCorrect && interval >= 60*time.Second
	}
	toolsAbsent := true
	for _, id := range ids {
		session, err := f.service.Core(f.ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		tools, err := session.History().Scan(f.ctx, core.Query{Kind: core.EntryToolStarted, Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		toolsAbsent = toolsAbsent && len(tools.Entries) == 0
	}
	noHotGC := after.NumForcedGC == before.NumForcedGC
	noShortWindowWALGrowth := seconds != 10 || filesAfter.WALBytes <= filesBefore.WALBytes
	// Deliberate diagnostic collection is outside the reported hot window and
	// occurs while the exhausted tasks still retain their recovery state.
	goruntime.GC()
	postWindowGC := performanceResources(t)
	for _, id := range ids {
		if err := f.service.Stop(f.ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	stopped = true
	stoppedStates, _ := fallbackWaitingStates(t, f, ids)
	stopCancelled := true
	for _, state := range stoppedStates {
		stopCancelled = stopCancelled && !fallbackArmed(state)
	}
	select {
	case <-time.After(750 * time.Millisecond):
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	stoppedInitial, stoppedProbes, _ := transport.snapshot()
	stopStable := len(stoppedInitial) == len(initialAfter) && len(stoppedProbes) == len(probesAfter)
	success := requestsCorrect && versionsStable && stillWaiting && scheduledCorrectly && probeIntervalsCorrect && toolsAbsent && noHotGC && noShortWindowWALGrowth && stopCancelled && stopStable
	result := map[string]any{
		"success": success, "mode": "exhausted_single_model_waiting", "sessions": sessions, "requested_seconds": seconds,
		"scope":            "Host/Core/SDK/SQLite waiting after synthetic quota exhaustion; unchanged real 60-second recovery detection; offline transport; no hot-window polling or forced GC",
		"profiles_enabled": os.Getenv("ASTRAL_PERFORMANCE_PROFILE") == "1",
		"resource_before":  before, "resource_after": after, "resources": performanceDifference(before, after),
		"files_before": filesBefore, "files_after": filesAfter, "core_versions_before": versionsBefore, "core_versions_after": versionsAfter,
		"initial_requests_before": len(initialBefore), "initial_requests_after": len(initialAfter), "probe_requests_before": len(probesBefore), "probe_requests_after": len(probesAfter),
		"total_requests": len(initialAfter) + len(probesAfter), "initial_request_times": initialAfter, "probe_request_times": probesAfter,
		"ordered_initial_to_probe_intervals_ns": intervals, "states_before": statesBefore, "states_after": statesAfter,
		"post_window_gc_while_waiting": postWindowGC, "post_window_gc_scope": "one diagnostic GC after timing and before Stop; excluded from resources hot interval",
		"invariants": map[string]bool{"expected_model_requests": requestsCorrect, "core_versions_unchanged": versionsStable, "all_sessions_still_waiting": stillWaiting, "probe_schedule_correct": scheduledCorrectly,
			"probe_intervals_at_least_60s": probeIntervalsCorrect, "no_tools": toolsAbsent, "no_hot_forced_gc": noHotGC, "no_short_window_wal_growth": noShortWindowWALGrowth,
			"stop_cancelled_recovery": stopCancelled, "no_requests_after_stop": stopStable},
	}
	if err := performanceWriteJSON(filepath.Join(output, "warm-idle.json"), result); err != nil {
		t.Fatal(err)
	}
	if !success {
		t.Fatal("quota-waiting invariants failed; inspect warm-idle.json")
	}
}
