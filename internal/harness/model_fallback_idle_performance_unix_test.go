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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
)

// Keep this fixture independent of the fallback implementation: the same file
// and resource helpers also compile at the pre-fallback Host baseline.
type fallbackIdleTransport struct {
	host    string
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int64
	waiting atomic.Int64
}

func (r *fallbackIdleTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	defer request.Body.Close()
	if request.URL.Host != r.host || !strings.HasSuffix(request.URL.Path, "/chat/completions") {
		return nil, fmt.Errorf("unexpected offline idle request: %s", request.URL)
	}
	if _, err := io.Copy(io.Discard, request.Body); err != nil {
		return nil, err
	}
	r.calls.Add(1)
	r.waiting.Add(1)
	defer r.waiting.Add(-1)
	select {
	case r.entered <- struct{}{}:
	default:
	}
	select {
	case <-request.Context().Done():
		return nil, request.Context().Err()
	case <-r.release:
		body, err := json.Marshal(textReply("Offline idle request released after measurement."))
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body))), Request: request}, nil
	}
}

func fallbackIdleChoice(t *testing.T, name string, defaultValue int, choices ...int) int {
	t.Helper()
	value := defaultValue
	if raw := os.Getenv(name); raw != "" {
		var err error
		value, err = strconv.Atoi(raw)
		if err != nil {
			t.Fatalf("%s must be an integer: %v", name, err)
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

func fallbackIdleVersions(t *testing.T, f *fixture, ids []string) map[string]uint64 {
	t.Helper()
	versions := make(map[string]uint64, len(ids))
	for _, id := range ids {
		state, err := f.coreStore.LoadState(f.ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		versions[id] = state.Version
	}
	return versions
}

// TestModelFallbackIdlePerformance observes natural waiting, not model latency.
// No explicit collection, View polling or request completion occurs in its hot
// interval. Optional profile runs are marked separately by the resource helper.
// The offline transport cannot contact an external service.
func TestModelFallbackIdlePerformance(t *testing.T) {
	if os.Getenv("ASTRAL_FALLBACK_IDLE") != "1" {
		t.Skip("set ASTRAL_FALLBACK_IDLE=1 and ASTRAL_PERFORMANCE_OUTPUT for an explicit idle measurement")
	}
	sessions := fallbackIdleChoice(t, "ASTRAL_FALLBACK_IDLE_SESSIONS", 0, 0, 10, 50)
	completed := fallbackIdleChoice(t, "ASTRAL_FALLBACK_IDLE_COMPLETE", 0, 0, 1000, 10000)
	children := fallbackIdleChoice(t, "ASTRAL_FALLBACK_IDLE_CHILDREN", 0, 0, 35)
	seconds := 10
	if raw := os.Getenv("ASTRAL_FALLBACK_IDLE_SECONDS"); raw != "" {
		var err error
		seconds, err = strconv.Atoi(raw)
		if err != nil || seconds < 1 || seconds > 60 {
			t.Fatal("ASTRAL_FALLBACK_IDLE_SECONDS must be between 1 and 60; 1 is a diagnostic smoke only")
		}
	}
	output := os.Getenv("ASTRAL_PERFORMANCE_OUTPUT")
	if !filepath.IsAbs(output) {
		t.Fatal("ASTRAL_PERFORMANCE_OUTPUT must name the new absolute directory prepared by TestMain")
	}
	if info, err := os.Stat(output); err != nil || !info.IsDir() {
		t.Fatal("ASTRAL_PERFORMANCE_OUTPUT must already be a directory prepared by TestMain")
	}

	f := newFixture(t, 0, func(context.Context, int, map[string]any) any {
		return textReply("Settled offline child work.")
	})
	var settledIDs []string
	if children > 0 {
		parent := subagentTestSeed(t, f)
		for i := range children {
			child := subagentTestSpawn(t, f, parent, fmt.Sprintf("idle-child-%02d", i), "Complete this bounded offline child instruction.", false)
			state := subagentTestFinished(t, f, child.SessionID)
			subagentWorkerCheckpoint(t, f, child.SessionID, state)
			settledIDs = append(settledIDs, child.SessionID)
		}
		f.idle(t, parent.id)
		settledIDs = append(settledIDs, parent.id)
	}
	for i := range completed {
		id := fmt.Sprintf("idle-complete-%05d", i)
		// Generic operation API and literal data deliberately avoid new private
		// fallback types. Both versions receive exactly the same completed rows.
		value := map[string]any{"session_id": id, "agent_id": f.agent.ID, "epoch": 1, "phase": "complete", "preferred": map[string]string{"id": f.agent.DefaultModelID}, "active": map[string]string{"id": f.agent.DefaultModelID}}
		if err := f.store.SaveMaintenance(f.ctx, "model_fallback", id, "complete", value); err != nil {
			t.Fatal(err)
		}
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
	transport := &fallbackIdleTransport{host: endpoint.Host, entered: make(chan struct{}, max(1, sessions)), release: make(chan struct{})}
	f.service.Models.HTTPClient.Transport = transport
	var runningIDs []string
	var release sync.Once
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, id := range runningIDs {
			if err := f.service.Stop(cleanup, id); err != nil {
				t.Errorf("stop blocked idle Session %s: %v", id, err)
			}
		}
		release.Do(func() { close(transport.release) })
		if err := f.service.Close(cleanup); err != nil {
			t.Errorf("close idle measurement Harness: %v", err)
		}
	})
	for i := range sessions {
		session, err := f.service.CreateSession(f.ctx, f.agent.ID, fmt.Sprintf("Idle performance %02d", i))
		if err != nil {
			t.Fatal(err)
		}
		runningIDs = append(runningIDs, session.ID)
		if _, err := f.service.Submit(f.ctx, session.ID, InputRequest{ID: fmt.Sprintf("idle-request-%02d", i), Text: "Wait for this offline request without generating another model request."}); err != nil {
			t.Fatal(err)
		}
	}
	for range sessions {
		select {
		case <-transport.entered:
		case <-f.ctx.Done():
			t.Fatal("not all model requests reached the offline wait", f.ctx.Err())
		}
	}
	for _, id := range runningIDs {
		view, err := f.service.View(f.ctx, id)
		if err != nil || view.Status != "running" {
			t.Fatalf("idle fixture did not establish a live Run for %s: status=%s err=%v", id, view.Status, err)
		}
	}
	// Drain setup notifications before sampling, without forcing a collection.
	select {
	case <-time.After(750 * time.Millisecond):
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	ids := append(append([]string{}, settledIDs...), runningIDs...)
	versionsBefore := fallbackIdleVersions(t, f, ids)
	filesBefore := performanceFixtureSnapshot(t, f)
	requestsBefore, waitingBefore := transport.calls.Load(), transport.waiting.Load()
	providerBefore := f.count()
	before := performanceResources(t)
	timer := time.NewTimer(time.Duration(seconds) * time.Second)
	select {
	case <-timer.C:
	case <-f.ctx.Done():
		timer.Stop()
		t.Fatal(f.ctx.Err())
	}
	after := performanceResources(t)
	requestsAfter, waitingAfter := transport.calls.Load(), transport.waiting.Load()
	providerAfter := f.count()
	filesAfter := performanceFixtureSnapshot(t, f)
	versionsAfter := fallbackIdleVersions(t, f, ids)
	noExtraRequests := requestsBefore == int64(sessions) && requestsAfter == requestsBefore && providerAfter == providerBefore
	allWaiting := waitingBefore == int64(sessions) && waitingAfter == waitingBefore
	versionsStable := reflect.DeepEqual(versionsBefore, versionsAfter)
	result := map[string]any{
		"scope":             "Harness/Core/SDK/SQLite natural idle with an offline blocking transport; no UI, IM connection, external model or forced GC",
		"profiles_enabled":  os.Getenv("ASTRAL_PERFORMANCE_PROFILE") == "1",
		"requested_seconds": seconds, "requested_waiting_sessions": sessions, "completed_maintenance_records": completed, "settled_children": children,
		"resources": performanceDifference(before, after), "files_before": filesBefore, "files_after": filesAfter,
		"resource_before": before, "resource_after": after,
		"model_calls_before": requestsBefore, "model_calls_after": requestsAfter, "model_waiting_before": waitingBefore, "model_waiting_after": waitingAfter,
		"fixture_provider_calls_before": providerBefore, "fixture_provider_calls_after": providerAfter,
		"core_versions_before": versionsBefore, "core_versions_after": versionsAfter,
		"invariants": map[string]bool{"no_extra_model_requests": noExtraRequests, "all_requested_models_still_waiting": allWaiting, "core_versions_unchanged": versionsStable, "no_forced_gc": after.NumForcedGC == before.NumForcedGC},
	}
	raw, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(filepath.Join(output, "warm-idle.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write(append(raw, '\n')); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	t.Logf("natural idle measurement: %s", filepath.Join(output, "warm-idle.json"))
	if !noExtraRequests || !allWaiting || !versionsStable || after.NumForcedGC != before.NumForcedGC {
		t.Fatal("idle measurement invariants failed; inspect warm-idle.json")
	}
}
