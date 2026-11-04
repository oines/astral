package channels

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"syscall"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral-core/models"
	"github.com/oines/astral/internal/domain"
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
