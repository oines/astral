package harness

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func TestUnavailableModelDoesNotBlockColdSessionInspectionOrRepair(t *testing.T) {
	for _, scenario := range []struct {
		name, reason string
		change       func(*fixture, *domain.ModelProfile, *domain.Provider) error
	}{
		{"missing model", "model_missing", func(f *fixture, profile *domain.ModelProfile, _ *domain.Provider) error {
			return f.store.Update(f.ctx, func(tx *persistence.Tx) error { return tx.Delete(f.ctx, "models", profile.ID, profile.Revision) })
		}},
		{"missing provider", "provider_missing", func(f *fixture, _ *domain.ModelProfile, provider *domain.Provider) error {
			return f.store.Update(f.ctx, func(tx *persistence.Tx) error { return tx.Delete(f.ctx, "providers", provider.ID, provider.Revision) })
		}},
		{"disabled model", "model_disabled", func(f *fixture, profile *domain.ModelProfile, _ *domain.Provider) error {
			enabled := false
			profile.Enabled = &enabled
			return f.service.Models.SaveProfile(f.ctx, profile)
		}},
		{"disabled provider", "provider_disabled", func(f *fixture, _ *domain.ModelProfile, provider *domain.Provider) error {
			enabled := false
			provider.Enabled = &enabled
			return f.service.Models.SaveProvider(f.ctx, provider, nil, nil)
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newFixture(t, 0, func(_ context.Context, _ int, request map[string]any) any {
				if request["stream"] != false {
					t.Errorf("mock model must retain its independently declared non-streaming API: %v", request["stream"])
				}
				return textReply("done")
			})
			if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "original", Text: "original conversation"}); err != nil {
				t.Fatal(err)
			}
			f.idle(t, f.session.ID)
			var profile domain.ModelProfile
			var provider domain.Provider
			if err := f.store.Get(f.ctx, "models", f.agent.DefaultModelID, &profile); err != nil {
				t.Fatal(err)
			}
			if err := f.store.Get(f.ctx, "providers", profile.ProviderID, &provider); err != nil {
				t.Fatal(err)
			}
			// A same-named replacement is a distinct connection. Even making it
			// the Bot default must not silently replace this Session's choice.
			replacementProvider := provider
			replacementProvider.Meta = domain.Meta{}
			key := "replacement-key"
			if err := f.service.Models.SaveProvider(f.ctx, &replacementProvider, &key, nil); err != nil {
				t.Fatal(err)
			}
			replacement := profile
			replacement.Meta, replacement.ProviderID = domain.Meta{}, replacementProvider.ID
			declareFixtureModelRecommendations(t, f, &replacement)
			if err := f.service.Models.SaveProfile(f.ctx, &replacement); err != nil {
				t.Fatal(err)
			}
			f.agent.DefaultModelID = replacement.ID
			if err := f.service.SaveAgent(f.ctx, &f.agent); err != nil {
				t.Fatal(err)
			}
			before, err := f.service.View(f.ctx, f.session.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err = scenario.change(f, &profile, &provider); err != nil {
				t.Fatal(err)
			}
			f.restart(t)
			view, err := f.service.View(f.ctx, f.session.ID)
			if err != nil {
				t.Fatalf("unavailable model blocked cold View: %v", err)
			}
			if view.ModelAvailable || view.ModelUnavailableReason != scenario.reason || view.Status != "needs_attention" || view.CanContinue {
				t.Fatalf("unavailable model was not described separately: %+v", view)
			}
			if !jsonEqual(view.Selection, before.Selection) || view.ProfileID != profile.ID || view.HeadID != before.HeadID || view.Revision != before.Revision {
				t.Fatal("inspection changed the historical configuration or rebound to the Bot default")
			}
			inspected, err := f.service.ReadContext(f.ctx, f.session.ID)
			if err != nil || inspected.Selection.ID != before.Selection.ID {
				t.Fatalf("context inspection lost unavailable model snapshot: %v", err)
			}
			session, err := f.service.Core(f.ctx, f.session.ID)
			if err != nil {
				t.Fatal(err)
			}
			attempts, err := session.Attempts().Scan(f.ctx, core.AttemptQuery{Purpose: "generation", Limit: 20})
			if err != nil || len(attempts.Attempts) != 1 {
				t.Fatalf("unavailable model blocked attempt history: %v", err)
			}
			frozen, err := session.Attempts().Request(f.ctx, attempts.Attempts[0].ID)
			if err != nil || frozen.ModelID != attempts.Attempts[0].Input.Model.ID {
				t.Fatalf("unavailable model blocked frozen request reconstruction: %v", err)
			}
			if err = f.service.Continue(f.ctx, f.session.ID); err == nil {
				t.Fatal("Continue accepted an unavailable selection")
			}
			if err = f.service.Compact(f.ctx, f.session.ID, ""); err == nil {
				t.Fatal("Compact accepted an unavailable selection")
			}
			if f.count() != 1 {
				t.Fatal("inspection or blocked execution dispatched a request")
			}
			if err = f.service.SelectModel(f.ctx, f.session.ID, replacement.ID); err != nil {
				t.Fatalf("unavailable selection blocked model repair: %v", err)
			}
			view, err = f.service.View(f.ctx, f.session.ID)
			if err != nil || !view.ModelAvailable || view.ModelUnavailableReason != "" || view.ProfileID != replacement.ID {
				t.Fatalf("repair did not clear model unavailability: %+v %v", view, err)
			}
			if _, err = f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "repaired", Text: "continue after changing the model"}); err != nil {
				t.Fatal(err)
			}
			f.idle(t, f.session.ID)
			if f.count() != 2 {
				t.Fatal("repaired Session did not execute exactly once")
			}
		})
	}
}

type modelRecoveryTransport func(*http.Request) (*http.Response, error)

func (f modelRecoveryTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestModelUpdatesPreserveFrozenNetworkRetryAndApplyAtNextActivation(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("done") })
	var profile domain.ModelProfile
	var provider domain.Provider
	if err := f.store.Get(f.ctx, "models", f.agent.DefaultModelID, &profile); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Get(f.ctx, "providers", profile.ProviderID, &provider); err != nil {
		t.Fatal(err)
	}
	original := f.service.Models.HTTPClient.Transport
	if original == nil {
		original = http.DefaultTransport
	}
	var mu sync.Mutex
	var payloads []string
	var authorizations []string
	f.service.Models.HTTPClient.Transport = modelRecoveryTransport(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		request.Body = io.NopCloser(strings.NewReader(string(body)))
		mu.Lock()
		payloads = append(payloads, string(body))
		authorizations = append(authorizations, request.Header.Get("Authorization"))
		first := len(payloads) == 1
		mu.Unlock()
		if first {
			// Fail after freezing, then edit both resource snapshots before
			// Core's retry. This must not rebuild the in-flight request.
			temperature := 0.25
			profile.Overrides = map[string]json.RawMessage{"defaults": encode(core.GenerationOptions{Temperature: &temperature})}
			if err = f.service.Models.SaveProfile(f.ctx, &profile); err != nil {
				t.Error(err)
			}
			key := "updated-retry-key"
			if err = f.service.Models.SaveProvider(f.ctx, &provider, &key, nil); err != nil {
				t.Error(err)
			}
			return nil, io.ErrUnexpectedEOF
		}
		return original.RoundTrip(request)
	})
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "retry", Text: "retry this request"}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	mu.Lock()
	if len(payloads) != 2 || payloads[0] != payloads[1] || authorizations[0] != authorizations[1] {
		mu.Unlock()
		t.Fatal("network retry changed its frozen request or credentials")
	}
	mu.Unlock()
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "updated", Text: "use the new model settings"}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	mu.Lock()
	defer mu.Unlock()
	if len(payloads) != 3 || authorizations[2] == authorizations[1] {
		t.Fatal("new activation did not adopt the latest Provider credentials")
	}
	var latest map[string]any
	if json.Unmarshal([]byte(payloads[2]), &latest) != nil || latest["temperature"] != 0.25 {
		t.Fatal("new activation did not adopt the latest Profile defaults")
	}
}

func TestDisablingProviderDoesNotChangeFrozenRetryAndBlocksNextRequest(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("completed frozen request") })
	var profile domain.ModelProfile
	var provider domain.Provider
	if err := f.store.Get(f.ctx, "models", f.agent.DefaultModelID, &profile); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Get(f.ctx, "providers", profile.ProviderID, &provider); err != nil {
		t.Fatal(err)
	}
	original := f.service.Models.HTTPClient.Transport
	if original == nil {
		original = http.DefaultTransport
	}
	var dispatches atomic.Int64
	f.service.Models.HTTPClient.Transport = modelRecoveryTransport(func(request *http.Request) (*http.Response, error) {
		if dispatches.Add(1) == 1 {
			enabled := false
			provider.Enabled = &enabled
			if err := f.service.Models.SaveProvider(f.ctx, &provider, nil, nil); err != nil {
				t.Error(err)
			}
			return nil, io.ErrUnexpectedEOF
		}
		return original.RoundTrip(request)
	})
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "disabled-before-retry", Text: "try a request"}); err != nil {
		t.Fatal(err)
	}
	var view View
	eventually(t, f.ctx, func() bool {
		var err error
		view, err = f.service.View(f.ctx, f.session.ID)
		if err != nil {
			t.Error(err)
			return false
		}
		return view.LastRun != nil && view.LastRun.Status == "completed"
	})
	if dispatches.Load() != 2 || view.ModelAvailable || view.ModelUnavailableReason != "provider_disabled" {
		t.Fatalf("frozen retry changed target or availability was lost: dispatches=%d view=%+v", dispatches.Load(), view)
	}
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "next-disabled", Text: "new request"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, f.ctx, func() bool {
		view, err := f.service.View(f.ctx, f.session.ID)
		return err == nil && view.LastRun != nil && view.LastRun.Status == "failed"
	})
	if dispatches.Load() != 2 {
		t.Fatal("new request dispatched through a disabled Provider")
	}
}

func TestColdInspectionDoesNotAdvanceModelRevision(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("done") })
	before, err := f.service.View(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	oldRef, _, err := f.service.Models.Selection(f.ctx, before.Selection.ID)
	if err != nil {
		t.Fatal(err)
	}
	var profile domain.ModelProfile
	if err = f.store.Get(f.ctx, "models", f.agent.DefaultModelID, &profile); err != nil {
		t.Fatal(err)
	}
	profile.Overrides = map[string]json.RawMessage{"context_window": encode(96000)}
	if err = f.service.Models.SaveProfile(f.ctx, &profile); err != nil {
		t.Fatal(err)
	}
	f.restart(t)
	view, err := f.service.View(f.ctx, f.session.ID)
	if err != nil || view.HeadID != before.HeadID || !jsonEqual(view.Selection, before.Selection) || view.Revision != before.Revision {
		t.Fatalf("cold inspection advanced the Session configuration: %+v %v", view, err)
	}
	if _, err = f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "next", Text: "use the updated configuration"}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	view, err = f.service.View(f.ctx, f.session.ID)
	if err != nil || !jsonEqual(view.Selection, before.Selection) {
		t.Fatalf("fresh execution changed the persistent choice: %+v %v", view, err)
	}
	session, err := f.service.Core(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	attempts, err := session.Attempts().Scan(f.ctx, core.AttemptQuery{Purpose: "generation", Limit: 20})
	if err != nil || len(attempts.Attempts) != 1 || attempts.Attempts[0].Input.Model.ID == oldRef {
		t.Fatalf("execution did not freeze the latest model revision: %+v %v", attempts, err)
	}
	context, err := f.service.InspectContext(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, model := environmentState(context.Request.Messages)
	if model.ContextWindow != 96000 {
		t.Fatalf("execution used stale model capabilities: %+v", model)
	}
}

func TestFrozenCrashCanContinueAfterLiveProviderRemovalButTerminalRunCannot(t *testing.T) {
	for _, terminal := range []string{"", "failed", "cancelled"} {
		name := terminal
		if name == "" {
			name = "unacknowledged crash"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("recovered frozen target") })
			resolved, err := f.service.Models.ResolveSelection(f.ctx, core.ModelSelection{ID: f.agent.DefaultModelID})
			if err != nil {
				t.Fatal(err)
			}
			if err = f.service.Close(f.ctx); err != nil {
				t.Fatal(err)
			}
			state, err := f.coreStore.LoadState(f.ctx, f.session.ID)
			if err != nil {
				t.Fatal(err)
			}
			commitEntries := func(commitID string, entries ...core.Entry) {
				t.Helper()
				current, readErr := f.coreStore.LoadState(f.ctx, f.session.ID)
				if readErr != nil {
					t.Fatal(readErr)
				}
				head := current.Branch.HeadID
				for i := range entries {
					entry := &entries[i]
					if entry.ID == "" {
						entry.ID = domain.ID("entry")
					}
					entry.SessionID = f.session.ID
					entry.Time = time.Now().UTC()
					entry.Version = current.Version + 1
					if entry.IsHistory() {
						entry.ParentID = head
						head = entry.ID
					}
				}
				if _, commitErr := f.coreStore.Commit(f.ctx, f.session.ID, core.Commit{ID: commitID, ExpectedVersion: current.Version, Entries: entries}); commitErr != nil {
					t.Fatal(commitErr)
				}
			}
			user := core.User("accepted before the process disappeared")
			commitEntries("crash-input", core.Entry{ID: "crash-user", Kind: core.EntryMessage, Message: &user}, core.Entry{ID: "crash-run-entry", Kind: core.EntryRun, RunID: "crash-run", Run: &core.RunRecord{ID: "crash-run", Status: "started"}})
			state, err = f.coreStore.LoadState(f.ctx, f.session.ID)
			if err != nil {
				t.Fatal(err)
			}
			base, err := core.ReadContext(f.ctx, f.coreStore, state.RolloutID, state.Branch.HeadID, 64<<20)
			if err != nil {
				t.Fatal(err)
			}
			request := base.Request
			request.ModelID, request.ModelRevision = resolved.Model.Spec().ID, resolved.Model.Spec().Revision
			request.MaxOutputTokens = 1024
			request.Options = resolved.Options
			request, err = core.PrepareRequest(f.ctx, resolved.Model, request)
			if err != nil {
				t.Fatal(err)
			}
			input, err := core.FreezeRequest(state.RolloutID, base, request, resolved.Model.Spec())
			if err != nil {
				t.Fatal(err)
			}
			input.Snapshot = resolved.Snapshot
			attempt := core.ModelAttempt{ID: "crash-attempt", SessionID: f.session.ID, RunID: "crash-run", TurnID: "crash-turn", Purpose: "generation", Number: 1, Version: state.Version + 1, StartedAt: time.Now().UTC(), Input: input}
			if _, err = f.coreStore.Commit(f.ctx, f.session.ID, core.Commit{ID: "crash-frozen", ExpectedVersion: state.Version, Attempts: []core.AttemptMutation{{ID: attempt.ID, Create: &attempt}}}); err != nil {
				t.Fatal(err)
			}
			if terminal != "" {
				state, err = f.coreStore.LoadState(f.ctx, f.session.ID)
				if err != nil {
					t.Fatal(err)
				}
				commitEntries("acknowledged-terminal", core.Entry{Kind: core.EntryRun, RunID: "crash-run", Run: &core.RunRecord{ID: "crash-run", Status: terminal, Error: "acknowledged stop or failure"}})
			}
			var profile domain.ModelProfile
			var connection domain.Provider
			if err = f.store.Get(f.ctx, "models", f.agent.DefaultModelID, &profile); err != nil {
				t.Fatal(err)
			}
			if err = f.store.Get(f.ctx, "providers", profile.ProviderID, &connection); err != nil {
				t.Fatal(err)
			}
			if err = f.service.Models.DeleteProvider(f.ctx, connection.ID, connection.Revision); err != nil {
				t.Fatal(err)
			}
			f.service = New(f.store, f.coreStore, f.service.Models, f.hub)
			view, err := f.service.View(f.ctx, f.session.ID)
			if err != nil {
				t.Fatal(err)
			}
			if view.ModelAvailable || view.CanContinue != (terminal == "") {
				t.Fatalf("frozen continuation availability is wrong: terminal=%q view=%+v", terminal, view)
			}
			if terminal != "" {
				if f.count() != 0 {
					t.Fatal("inspection dispatched terminal work")
				}
				return
			}
			if err = f.service.Continue(f.ctx, f.session.ID); err != nil {
				t.Fatal(err)
			}
			eventually(t, f.ctx, func() bool {
				view, err := f.service.View(f.ctx, f.session.ID)
				return err == nil && view.LastRun != nil && view.LastRun.Status == "completed"
			})
			if f.count() != 1 {
				t.Fatal("frozen target was not restored exactly once")
			}
		})
	}
}
