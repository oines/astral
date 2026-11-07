package channels

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"syscall"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral-core/models"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/messages"
	"github.com/oines/astral/internal/testkit"
)

func runPausedInbox(t *testing.T, p *testkit.Platform, s *Service, b domain.ConversationBinding) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(p.Context)
	done := make(chan struct{})
	go func() { defer close(done); s.inbox(ctx, b) }()
	testkit.Await(t, p.Context, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.inboxWake[b.ID] != nil })
	return func() {
		cancel()
		select {
		case <-done:
		case <-p.Context.Done():
			t.Fatal(p.Context.Err())
		}
	}
}

func TestInboxQuotaPauseSurvivesRestartAndReplaysEveryObservationOnce(t *testing.T) {
	// The separate failure workload preserves the oldest 64 queued inputs.
	// Keep full replay smaller: under -race, preparing every recovered Context
	// is expensive even with an offline provider, and is not the failure storm.
	inboxQuotaRecovery(t, 8, true)
}

// Opt in without -race to measure the complete old-64 plus new-30 recovery:
// ASTRAL_IM_RECOVERY_WORKLOAD=1 go test ./internal/channels -run '^TestInboxLargeBacklogRecoveryWorkload$' -count=1 -v
func TestInboxLargeBacklogRecoveryWorkload(t *testing.T) {
	if os.Getenv("ASTRAL_IM_RECOVERY_WORKLOAD") != "1" {
		t.Skip("explicit large-backlog recovery workload")
	}
	inboxQuotaRecovery(t, 64, false)
}

func inboxQuotaRecovery(t *testing.T, oldPending int, normalReplayWindow bool) {
	t.Helper()
	p, s, b, failed, calls := unavailableFixtureWithPending(t, oldPending)
	first := fmt.Sprint(firstPendingBatch(t, p, b.SessionID))
	event, through := activeDeliveryArchive(t, p, b, "quota-failure", "")
	err := s.deliver(p.Context, b, event)
	var api *models.APIError
	if !errors.As(err, &api) || api.Kind != models.KindQuotaExhausted {
		t.Fatalf("wrapped actual error=%T", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("first failure called model%d times", calls.Load())
	}
	for i := range 30 {
		next, seq := activeDeliveryArchive(t, p, b, fmt.Sprintf("archived-during-quota-%02d", i), "")
		through = seq
		if err = s.deliver(p.Context, b, next); err == nil {
			t.Fatal("paused observation was silently accepted")
		}
	}
	state, err := p.CoreStore.LoadState(p.Context, b.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	pendingIDs := firstPendingBatch(t, p, b.SessionID)
	if calls.Load() != 1 || state.PendingInputs[core.InputContext] != uint64(oldPending+1) || len(pendingIDs) < oldPending || fmt.Sprint(pendingIDs[:oldPending]) != first {
		t.Fatalf("quota storm or altered queue: calls=%d pending=%v", calls.Load(), state.PendingInputs)
	}
	if delivery, err := p.Store.Delivery(p.Context, event.ID); err != nil || delivery.State != "blocked" || delivery.Error == "" {
		t.Fatalf("persistent cause disappeared: %+v %v", delivery, err)
	}
	if reason, err := s.IMDeliveryError(p.Context, b.SessionID); err != nil || reason == "" {
		t.Fatalf("quota pause invisible: %q %v", reason, err)
	}

	activeDeliveryReopen(t, p)
	s = New(p.Store, p.Harness)
	stop := runPausedInbox(t, p, s, b)
	if paused, err := s.pausedInbox(p.Context, b); err != nil || !paused {
		t.Fatalf("restart lost pause: %v %v", paused, err)
	}
	stop()
	if calls.Load() != 1 {
		t.Fatalf("restart retried unavailable model: %d", calls.Load())
	}
	// A top-up changes no revision. The existing explicit Session retry is the
	// recovery boundary, not another random group message or a timed 402 retry.
	failed.Store(false)
	beforeRecovery := calls.Load()
	startedRecovery := time.Now()
	if err = s.ResumeIMSession(p.Context, b.SessionID); err != nil {
		t.Fatal(err)
	}
	if err = p.Harness.ResumeContext(p.Context, b.SessionID); err != nil {
		t.Fatal(err)
	}
	state, err = p.CoreStore.LoadState(p.Context, b.SessionID)
	if err != nil || state.PendingInputs[core.InputContext] != 0 || calls.Load() == beforeRecovery {
		t.Fatalf("top-up did not recover the queued compact with unchanged settings: pending=%v calls=%d err=%v", state.PendingInputs, calls.Load()-beforeRecovery, err)
	}
	initialRecoveryCalls := calls.Load() - beforeRecovery
	// The deliberately small failure window must cause an actual compact, but
	// is not a throughput benchmark. After proving top-up recovery without a
	// settings change, restore the fixture's normal window for backlog replay.
	if normalReplayWindow {
		if err = p.Store.Get(p.Context, "models", p.Profile.ID, &p.Profile); err != nil {
			t.Fatal(err)
		}
		p.Profile.Recommendations["context_window"] = testkit.JSON(128000)
		if err = p.Harness.Models.SaveProfile(p.Context, &p.Profile); err != nil {
			t.Fatal(err)
		}
	}
	stop = runPausedInbox(t, p, s, b)
	defer stop()
	testkit.Await(t, p.Context, func() bool {
		var at cursor
		if p.Store.Get(p.Context, "inbox_cursors", b.ID, &at) != nil {
			return false
		}
		if at.Sequence != through {
			return false
		}
		state, e := p.CoreStore.LoadState(p.Context, b.SessionID)
		return e == nil && state.PendingInputs[core.InputContext] == 0
	})
	session, err := p.Harness.Core(p.Context, b.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	page, err := session.History().Scan(p.Context, core.Query{Kind: core.EntryContext, Limit: 1000, MaxBytes: 8 << 20})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, entry := range page.Entries {
		if entry.Message == nil {
			continue
		}
		var m messages.Metadata
		if json.Unmarshal(entry.Message.Metadata, &m) == nil && m.Source == "im" {
			seen[m.EventID]++
		}
	}
	if want := oldPending + 1 + 30; len(seen) != want {
		t.Fatalf("recovery history has %d IM observations, want %d", len(seen), want)
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("observation %s repeated%d times", id, n)
		}
	}
	if reason, err := s.IMDeliveryError(p.Context, b.SessionID); err != nil || reason != "" {
		t.Fatalf("recovery kept stale failure: %q %v", reason, err)
	}
	compactions, err := session.History().Scan(p.Context, core.Query{Kind: core.EntryCompaction, Limit: 1000, MaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("IM_RECOVERY_METRICS old_pending=%d new_during_pause=30 replayed=%d normal_replay_window=%t unchanged_settings_recovery_model_calls=%d later_replay_model_calls=%d committed_compactions=%d elapsed_ms=%.3f", oldPending, len(seen), normalReplayWindow, initialRecoveryCalls, calls.Load()-beforeRecovery-initialRecoveryCalls, len(compactions.Entries), float64(time.Since(startedRecovery))/float64(time.Millisecond))
}

func TestInboxFailureClassificationAndRetrySchedule(t *testing.T) {
	for _, test := range []struct {
		name      string
		err       error
		permanent bool
		minimum   time.Duration
	}{
		{"compaction-input", fmt.Errorf("passive recovery: %w", core.ErrCompactionInputTooLarge), true, 0},
		{"credentials", models.SafeError(&models.APIError{StatusCode: 401, Kind: models.KindAuthentication}), true, 0},
		{"permission", models.SafeError(&models.APIError{StatusCode: 403, Kind: models.KindPermissionDenied}), true, 0},
		{"quota-in-429", models.SafeError(&models.APIError{StatusCode: 429, Kind: models.KindQuotaExhausted}), true, 0},
		{"rate-retry-after", models.SafeError(&models.APIError{StatusCode: 429, Kind: models.KindRateLimited, Header: http.Header{"Retry-After": {"120"}}}), false, 120 * time.Second},
		{"server", models.SafeError(&models.APIError{StatusCode: 503, Kind: models.KindServerError}), false, 30 * time.Second},
		{"network", models.SafeError(&url.Error{Op: "POST", URL: "https://fixture.invalid", Err: syscall.ECONNRESET}), false, 30 * time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := testkit.New(t, 0, func(context.Context, int, map[string]any) any {
				t.Error("schedule test made a model request")
				return nil
			})
			s := New(p.Store, p.Harness)
			b := activeDeliveryChat(t, p, s, false, false)
			boundary, err := s.inboxBoundary(p.Context, b)
			if err != nil {
				t.Fatal(err)
			}
			at := time.Now()
			if err = s.pauseInboxFailure(p.Context, b, boundary, test.err); err != nil {
				t.Fatal(err)
			}
			var pause inboxPause
			if err = p.Store.Maintenance(p.Context, inboxPauseKind, b.ID, &pause); err != nil {
				t.Fatal(err)
			}
			if test.permanent != pause.RetryAt.IsZero() {
				t.Fatalf("wrong retry kind: %+v", pause)
			}
			if !test.permanent && pause.RetryAt.Before(at.Add(test.minimum)) {
				t.Fatalf("ignored SDK retry delay: %v", pause.RetryAt.Sub(at))
			}
			for i := range 5 {
				event, _ := activeDeliveryArchive(t, p, b, fmt.Sprintf("wait-%d", i), "")
				_ = s.deliver(p.Context, b, event)
			}
			if p.Requests.Load() != 0 {
				t.Fatal("new IM messages bypassed retry time")
			}
			if !test.permanent {
				pause.RetryAt = time.Now().Add(-time.Second)
				if err = p.Store.SaveMaintenance(p.Context, inboxPauseKind, b.ID, "blocked", pause); err != nil {
					t.Fatal(err)
				}
				if paused, err := s.pausedInbox(p.Context, b); err != nil || paused {
					t.Fatalf("due retry stayed blocked: %v %v", paused, err)
				}
				if err = s.pauseInboxFailure(p.Context, b, boundary, test.err); err != nil {
					t.Fatal(err)
				}
				if err = p.Store.Maintenance(p.Context, inboxPauseKind, b.ID, &pause); err != nil || pause.Failures != 2 || pause.RetryAt.Before(time.Now().Add(59*time.Second)) {
					t.Fatalf("retry did not back off: %+v %v", pause, err)
				}
			}
		})
	}
}

func TestInboxConfigEditAndExplicitRetryRejectLateOldFailure(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return nil })
	s := New(p.Store, p.Harness)
	b := activeDeliveryChat(t, p, s, false, false)
	boundary, err := s.inboxBoundary(p.Context, b)
	if err != nil {
		t.Fatal(err)
	}
	cause := models.SafeError(&models.APIError{StatusCode: 402, Kind: models.KindQuotaExhausted})
	if err = s.pauseInboxFailure(p.Context, b, boundary, cause); err != nil {
		t.Fatal(err)
	}
	if err = p.Store.Get(p.Context, "models", p.Profile.ID, &p.Profile); err != nil {
		t.Fatal(err)
	}
	p.Profile.Name += " corrected"
	if err = p.Harness.Models.SaveProfile(p.Context, &p.Profile); err != nil {
		t.Fatal(err)
	}
	if paused, err := s.pausedInbox(p.Context, b); err != nil || paused {
		t.Fatalf("configuration edit did not recover: %v %v", paused, err)
	}
	boundary, err = s.inboxBoundary(p.Context, b)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ResumeIMSession(p.Context, b.SessionID); err != nil {
		t.Fatal(err)
	}
	if err = s.pauseInboxFailure(p.Context, b, boundary, cause); err != nil {
		t.Fatal(err)
	}
	if paused, err := s.pausedInbox(p.Context, b); err != nil || paused {
		t.Fatalf("late failure undid explicit recovery: %v %v", paused, err)
	}
}
