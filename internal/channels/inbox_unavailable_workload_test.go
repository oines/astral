package channels

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral-core/models"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/testkit"
)

// Share the real Core/SQLite/offline HTTP fixture with pause recovery tests.
func unavailableFixture(t *testing.T) (*testkit.Platform, *Service, domain.ConversationBinding, *atomic.Bool, *atomic.Int64) {
	return unavailableFixtureWithPending(t, 64)
}

func TestIMUnavailableWorkloadMetrics(t *testing.T) {
	p, s, b, _, calls := unavailableFixture(t)
	first := fmt.Sprint(firstPendingBatch(t, p, b.SessionID))
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	started := time.Now()
	var firstError error
	for i := range 3 {
		event, _ := activeDeliveryArchive(t, p, b, fmt.Sprintf("continuous-%d", i), "")
		err := s.deliver(p.Context, b, event)
		if err == nil {
			t.Fatal("quota failure was hidden")
		}
		if i == 0 {
			firstError = err
		}
	}
	elapsed := time.Since(started)
	runtime.ReadMemStats(&after)
	if fmt.Sprint(firstPendingBatch(t, p, b.SessionID)) != first {
		t.Fatal("failed maintenance changed the original oldest64 inputs")
	}
	_, fixed := any(s).(interface {
		ResumeIMSession(context.Context, string) error
	})
	want := int64(3)
	if fixed {
		want = 1
	}
	if calls.Load() != want {
		t.Fatalf("maintenance calls=%d want%d", calls.Load(), want)
	}
	state, err := p.CoreStore.LoadState(p.Context, b.SessionID)
	if err != nil || state.PendingInputs[core.InputContext] != uint64(64+want) {
		t.Fatalf("pending identities changed: pending=%v err=%v", state.PendingInputs, err)
	}
	if !fixed {
		t.Log("baseline: release17 per-event maintenance without persisted pause")
	}
	t.Logf("IM_UNAVAILABLE_METRICS fixed=%t events=3 oldest_pending=64 model_calls=%d elapsed_ms=%.3f alloc_bytes=%d mallocs=%d", fixed, calls.Load(), float64(elapsed)/float64(time.Millisecond), after.TotalAlloc-before.TotalAlloc, after.Mallocs-before.Mallocs)
	if fixed {
		var native *models.APIError
		// The first actual SDK/modelbridge failure must retain its structured
		// cause all the way through Core and Host; never parse its display text.
		if !errors.As(firstError, &native) || native.Kind != models.KindQuotaExhausted || native.StatusCode != 402 {
			t.Fatalf("real Core/modelbridge error lost API classification: %T", firstError)
		}
	}
}
