package harness_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/oines/astral/internal/harness"
	"github.com/oines/astral/internal/testkit"
)

func TestReportedContextUsageKeepsPreviousUntilNewUsageArrives(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	p := testkit.New(t, 0, func(ctx context.Context, n int, _ map[string]any) any {
		if n == 2 {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return nil
			}
		}
		reply := testkit.Text("done").(map[string]any)
		switch n {
		case 1:
			reply["usage"] = map[string]any{"prompt_tokens": 27707, "completion_tokens": 5, "prompt_tokens_details": map[string]any{"cached_tokens": 27136}}
		case 2:
			reply["usage"] = map[string]any{"prompt_tokens": 29000, "completion_tokens": 5}
		case 3:
			delete(reply, "usage")
		case 4:
			reply["usage"] = map[string]any{"prompt_tokens": 0, "completion_tokens": 5}
		default:
			reply["usage"] = map[string]any{"prompt_tokens": 99000, "completion_tokens": 5}
		}
		return reply
	})
	read := func() harness.View {
		t.Helper()
		view, err := p.Harness.View(p.Context, p.Session.ID)
		if err != nil {
			t.Fatal(err)
		}
		return view
	}
	prompt := func(id, text string) harness.View {
		t.Helper()
		if _, err := p.Harness.Submit(p.Context, p.Session.ID, harness.InputRequest{ID: id, Text: text}); err != nil {
			t.Fatal(err)
		}
		return p.Idle(t, p.Session.ID)
	}
	if read().ContextUsage != nil {
		t.Fatal("a fresh Session invented model usage")
	}
	first := prompt("first", strings.Repeat("Retained history. ", 2000)).ContextUsage
	if first == nil || first.InputTokens != 27707 || first.ContextWindow != 128000 {
		t.Fatalf("first real usage, including cached input, was not preserved: %+v", first)
	}
	if _, err := p.Harness.Submit(p.Context, p.Session.ID, harness.InputRequest{ID: "second", Text: "next"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-p.Context.Done():
		t.Fatal("second model request did not start")
	}
	if current := read().ContextUsage; current == nil || *current != *first {
		t.Fatalf("an in-flight request replaced the last report: %+v", current)
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
	if current := read().ContextUsage; current == nil || *current != *first {
		t.Fatalf("a new model selection changed an existing report: %+v", current)
	}
	unblock()
	second := p.Idle(t, p.Session.ID).ContextUsage
	if second == nil || second.InputTokens != 29000 || second.ContextWindow != 128000 || second.EntryID == first.EntryID {
		t.Fatalf("the response lost its frozen request's model window: %+v", second)
	}
	if current := prompt("missing", "no usage this time").ContextUsage; current == nil || *current != *second {
		t.Fatalf("missing provider usage overwrote the previous report: %+v", current)
	}
	zero := prompt("zero", "an explicit zero").ContextUsage
	if zero == nil || zero.InputTokens != 0 || zero.ContextWindow != 64000 {
		t.Fatalf("reported zero was treated as missing: %+v", zero)
	}
	if err := p.Harness.Compact(p.Context, p.Session.ID, "Summarize older history."); err != nil {
		t.Fatal(err)
	}
	if current := read().ContextUsage; current == nil || *current != *zero {
		t.Fatalf("compaction usage overwrote generation usage: %+v", current)
	}
	if err := p.Harness.Close(p.Context); err != nil {
		t.Fatal(err)
	}
	p.Harness = harness.New(p.Store, p.CoreStore, p.Harness.Models, p.Hub)
	if current := read().ContextUsage; current == nil || *current != *zero {
		t.Fatalf("reopening lost the last durable report: %+v", current)
	}
}

func TestReportedContextUsageFollowsBranchBeyondRecentHistory(t *testing.T) {
	p := testkit.New(t, 0, func(_ context.Context, n int, _ map[string]any) any {
		reply := testkit.Text("done").(map[string]any)
		if n <= 2 {
			reply["usage"] = map[string]any{"prompt_tokens": n * 100, "completion_tokens": 5}
		} else {
			delete(reply, "usage")
		}
		return reply
	})
	prompt := func(n int) harness.View {
		t.Helper()
		if _, err := p.Harness.Submit(p.Context, p.Session.ID, harness.InputRequest{ID: fmt.Sprint(n), Text: "hello"}); err != nil {
			t.Fatal(err)
		}
		return p.Idle(t, p.Session.ID)
	}
	first := prompt(1).ContextUsage
	second := prompt(2).ContextUsage
	if first == nil || second == nil || second.InputTokens != 200 {
		t.Fatal("initial provider reports are missing")
	}
	child, err := p.Harness.Fork(p.Context, p.Session.ID, first.EntryID, "Earlier branch", "usage-fork")
	if err != nil {
		t.Fatal(err)
	}
	view, err := p.Harness.View(p.Context, child.ID)
	if err != nil || view.ContextUsage == nil || *view.ContextUsage != *first {
		t.Fatalf("fork included a report beyond its selected head: %+v, %v", view.ContextUsage, err)
	}
	for n := 3; n <= 29; n++ {
		prompt(n)
	}
	if err := p.Harness.Close(p.Context); err != nil {
		t.Fatal(err)
	}
	p.Harness = harness.New(p.Store, p.CoreStore, p.Harness.Models, p.Hub)
	view, err = p.Harness.View(p.Context, p.Session.ID)
	if err != nil || view.ContextUsage == nil || *view.ContextUsage != *second {
		t.Fatalf("last report was lost outside the recent UI pages: %+v, %v", view.ContextUsage, err)
	}
}
