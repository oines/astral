package harness

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
)

func TestModelFallbackWaitsForFiveFrozenNetworkAttemptsBeforeNextCandidate(t *testing.T) {
	f := newFixture(t, 0, func(_ context.Context, _ int, request map[string]any) any {
		if request["model"] != "fixture-next" {
			t.Errorf("unexpected successful model request: %v", request["model"])
		}
		return textReply("The fallback model completed the original task.")
	})
	backup := anotherModel(t, f)
	f.agent.ModelPriority = []core.ModelSelection{{ID: f.agent.DefaultModelID}, {ID: backup.ID}}
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
	original := f.service.Models.HTTPClient.Transport
	if original == nil {
		original = http.DefaultTransport
	}
	var mu sync.Mutex
	var payloads, authorizations []string
	var attemptsAt []time.Time
	var backupAt time.Time
	var backupAuthorization string
	f.service.Models.HTTPClient.Transport = modelRecoveryTransport(func(request *http.Request) (*http.Response, error) {
		decoded, raw := modelFallbackTestRequest(t, request)
		if decoded["model"] != "fixture" {
			mu.Lock()
			attempts := len(payloads)
			backupAt, backupAuthorization = time.Now(), request.Header.Get("Authorization")
			mu.Unlock()
			if attempts != 5 {
				t.Errorf("fallback started after %d primary attempts, want 5", attempts)
			}
			return original.RoundTrip(request)
		}
		mu.Lock()
		payloads = append(payloads, raw)
		authorizations = append(authorizations, request.Header.Get("Authorization"))
		attemptsAt = append(attemptsAt, time.Now())
		first := len(payloads) == 1
		mu.Unlock()
		if first {
			// Change live defaults and credentials after the request is frozen.
			// All five retries must still use the original payload and key;
			// only the next candidate resolves the latest connection settings.
			temperature := 0.25
			profile.Overrides = map[string]json.RawMessage{"defaults": encode(core.GenerationOptions{Temperature: &temperature})}
			if err := f.service.Models.SaveProfile(f.ctx, &profile); err != nil {
				t.Error(err)
			}
			updatedKey := "fallback-after-network-retry-key"
			if err := f.service.Models.SaveProvider(f.ctx, &connection, &updatedKey, nil); err != nil {
				t.Error(err)
			}
		}
		return nil, io.ErrUnexpectedEOF
	})
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "five-network-attempts", Text: "Complete this task even if the primary model has a network problem."}); err != nil {
		t.Fatal(err)
	}
	view := f.idle(t, f.session.ID)
	if view.LastRun == nil || view.LastRun.Status != "completed" {
		t.Fatalf("fallback did not finish the original task: %+v", view.LastRun)
	}
	selection, err := f.service.ModelSelection(f.ctx, f.session.ID)
	if err != nil || selection.ID != backup.ID || f.count() != 1 {
		t.Fatalf("next candidate did not complete exactly once: selection=%+v successful=%d err=%v", selection, f.count(), err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(payloads) != 5 || len(authorizations) != 5 || backupAt.IsZero() {
		t.Fatalf("unexpected dispatch counts: primary=%d credentials=%d backup=%v", len(payloads), len(authorizations), backupAt)
	}
	for i := range payloads {
		if payloads[i] != payloads[0] || authorizations[i] != authorizations[0] {
			t.Fatalf("attempt %d changed the frozen payload or credentials", i+1)
		}
	}
	if authorizations[0] != "Bearer fixture-key" || backupAuthorization != "Bearer fallback-after-network-retry-key" {
		t.Fatalf("frozen/new credential boundary was lost: primary=%q backup=%q", authorizations[0], backupAuthorization)
	}
	for i, backoff := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 5 * time.Second} {
		if elapsed := attemptsAt[i+1].Sub(attemptsAt[i]); elapsed < backoff-100*time.Millisecond {
			t.Errorf("retry %d did not preserve backoff: elapsed=%v want at least %v", i+1, elapsed, backoff)
		}
	}
	if elapsed := backupAt.Sub(attemptsAt[0]); elapsed < 12*time.Second-100*time.Millisecond {
		t.Errorf("fallback skipped the primary model's retry budget: elapsed=%v", elapsed)
	}
}
