package channels

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/harness"
	"github.com/oines/astral/internal/testkit"
)

func TestPausedInboxWakesForActualComposerModelChange(t *testing.T) {
	for _, child := range []bool{false, true} {
		name := "root-only"
		if child {
			name = "with-settled-child"
		}
		t.Run(name, func(t *testing.T) {
			var spawned atomic.Bool
			p := testkit.New(t, 0, func(_ context.Context, _ int, request map[string]any) any {
				if child && strings.Contains(string(encoded(request["messages"])), "COMPOSER_CHILD_FIXTURE") && !spawned.Swap(true) {
					return testkit.Tool("spawn_agent", map[string]any{"task": "Complete this bounded offline fixture and finish."})
				}
				return testkit.Text("Offline fixture completed.")
			})
			s := New(p.Store, p.Harness)
			bot := newBot(t)
			connection := bot.connection(t, s, p.Agent.ID)
			binding := createBinding(t, p, s, connection, "-100", "")
			if child {
				if _, err := p.Harness.Submit(p.Context, binding.SessionID, harness.InputRequest{ID: "seed-child", Text: "COMPOSER_CHILD_FIXTURE: create the one offline fixture child."}); err != nil {
					t.Fatal(err)
				}
				testkit.Await(t, p.Context, func() bool {
					rows, err := p.Store.List(p.Context, "sessions")
					if err != nil {
						return false
					}
					found := false
					for _, row := range rows {
						var record domain.Session
						if p.Store.Get(p.Context, "sessions", row.ID, &record) != nil {
							return false
						}
						if record.SubagentParentID == binding.SessionID {
							found = true
							state, e := p.CoreStore.LoadState(p.Context, record.ID)
							if e != nil || state.LastRun == nil || state.LastRun.Status != "completed" || state.CurrentAttemptID != "" {
								return false
							}
							// A completed child can still deliver its terminal report and
							// start another parent Run. Wait for durable delivery and
							// application before checking the parent's idle boundary.
							var checkpoint struct {
								Protocol int    `json:"protocol"`
								Version  uint64 `json:"version"`
								Sequence uint64 `json:"sequence"`
							}
							if p.Store.Maintenance(p.Context, "subagent_delivery", record.ID, &checkpoint) != nil || checkpoint.Protocol != 1 || checkpoint.Version != state.Version {
								return false
							}
							page, e := p.CoreStore.Scan(p.Context, state.RolloutID, core.Query{SessionID: record.ID, Kind: core.EntryRun, Reverse: true, Limit: 1, MaxBytes: 1 << 20})
							if e != nil || len(page.Records) != 1 {
								return false
							}
							terminal := page.Records[0].Entry
							if terminal.Run == nil || terminal.Run.ID != state.LastRun.ID || terminal.Run.Status != "completed" || checkpoint.Sequence < terminal.Sequence {
								return false
							}
							receipt, e := p.CoreStore.LookupInput(p.Context, binding.SessionID, "subagent_run_end_"+record.ID+"_"+state.LastRun.ID)
							if e != nil || receipt.State != core.InputApplied {
								return false
							}
						}
					}
					return found && !p.Harness.StorageBusy()
				})
				p.Idle(t, binding.SessionID)
			}
			other := p.Profile
			other.Meta, other.Name, other.ModelID = domain.Meta{}, "repaired model", "offline-repaired"
			// A new raw model ID cannot inherit the old model's verified limits.
			// This offline profile supplies its known fixture limits explicitly.
			other.Overrides = p.Profile.Recommendations
			if err := p.Harness.Models.SaveProfile(p.Context, &other); err != nil {
				t.Fatal(err)
			}
			boundary, err := s.inboxBoundary(p.Context, binding)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.pauseInboxFailure(p.Context, binding, boundary, core.ErrCompactionInputTooLarge); err != nil {
				t.Fatal(err)
			}
			startChannels(t, s, p.Context)
			testkit.Await(t, p.Context, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.inboxWake[binding.ID] != nil })
			if !child {
				if err = p.Harness.SelectModel(p.Context, binding.SessionID, p.Profile.ID); err != nil {
					t.Fatal(err)
				}
				time.Sleep(100 * time.Millisecond)
				var pause inboxPause
				if err = p.Store.Maintenance(p.Context, inboxPauseKind, binding.ID, &pause); err != nil || !pause.RetryAt.IsZero() {
					t.Fatalf("same selection unexpectedly released permanent pause: %+v error=%v", pause, err)
				}
			}
			if err = p.Harness.SelectModel(p.Context, binding.SessionID, other.ID); err != nil {
				t.Fatal(err)
			}
			testkit.Await(t, p.Context, func() bool {
				var pause inboxPause
				return domain.IsMissing(p.Store.Maintenance(p.Context, inboxPauseKind, binding.ID, &pause))
			})
			selected, err := p.Harness.ModelSelection(p.Context, binding.SessionID)
			if err != nil || selected.ID != other.ID || bot.sends.Load() != 0 {
				t.Fatalf("model recovery changed identity or sent a message: %+v error=%v sends=%d", selected, err, bot.sends.Load())
			}
		})
	}
}
