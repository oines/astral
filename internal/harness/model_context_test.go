package harness

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
)

func modelEntries(t *testing.T, f *fixture) []core.Entry {
	t.Helper()
	session, err := f.service.Core(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	page, err := session.History().Scan(f.ctx, core.Query{Kind: core.EntryContext, Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	var entries []core.Entry
	for _, entry := range page.Entries {
		if entry.Message.ContextKind == modelContextKind {
			entries = append(entries, entry)
		}
	}
	return entries
}

func anotherModel(t *testing.T, f *fixture) domain.ModelProfile {
	t.Helper()
	var profile domain.ModelProfile
	if err := f.store.Get(f.ctx, "models", f.agent.DefaultModelID, &profile); err != nil {
		t.Fatal(err)
	}
	profile.Meta, profile.Name, profile.ModelID = domain.Meta{}, "Another model", "fixture-next"
	profile.Overrides = map[string]json.RawMessage{"context_window": encode(64000), "images": encode("unsupported")}
	if err := f.service.Models.SaveProfile(f.ctx, &profile); err != nil {
		t.Fatal(err)
	}
	return profile
}

func TestModelChangesInjectOnlyModelStateAndSurviveReopen(t *testing.T) {
	f := newFixture(t, 1, func(context.Context, int, map[string]any) any { return textReply("unused") })
	initial := environmentEntries(t, f, f.session.ID)
	var metadata environmentMetadata
	if len(initial) != 1 || json.Unmarshal(initial[0].Message.Metadata, &metadata) != nil || metadata.Model.ID != "fixture" || metadata.Model.ContextWindow != 128000 || metadata.Model.Vision != "supported" {
		t.Fatalf("initial environment lacks actual model state: %+v", metadata)
	}
	next := anotherModel(t, f)
	selectModel := func(id string) {
		t.Helper()
		if err := f.service.SelectModel(f.ctx, f.session.ID, id); err != nil {
			t.Fatal(err)
		}
	}
	selectModel(next.ID)
	notices := modelEntries(t, f)
	if len(notices) != 1 || len(environmentEntries(t, f, f.session.ID)) != 1 || f.count() != 0 {
		t.Fatal("model selection repeated the environment, omitted the notice, or woke the model")
	}
	text := notices[0].Message.Content[0].Text
	if !strings.Contains(text, "test (fixture) to Another model (fixture-next)") || !strings.Contains(text, `"context_window":64000`) || !strings.Contains(text, `"vision":"unsupported"`) || strings.Contains(text, "current_computer") || strings.Contains(text, "discovery") {
		t.Fatalf("model notice contains wrong or unrelated state: %s", text)
	}
	selectModel(next.ID)
	if len(modelEntries(t, f)) != 1 {
		t.Fatal("selecting unchanged state repeated the notice")
	}
	next.Overrides["images"] = encode("unknown")
	next.Overrides["context_window"] = encode(96000)
	if err := f.service.Models.SaveProfile(f.ctx, &next); err != nil {
		t.Fatal(err)
	}
	selectModel(next.ID)
	selectModel(f.agent.DefaultModelID)
	selectModel(next.ID)
	if len(modelEntries(t, f)) != 4 {
		t.Fatal("model capability changes or returning selections were lost")
	}
	if err := f.service.SelectModel(f.ctx, f.session.ID, "missing-model"); err == nil || len(modelEntries(t, f)) != 4 {
		t.Fatal("failed selection announced a model change")
	}
	f.restart(t)
	if err := f.service.InitializeSession(f.ctx, f.session.ID); err != nil {
		t.Fatal(err)
	}
	view, err := f.service.InspectContext(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, current := environmentState(view.Request.Messages)
	if current.ID != next.ModelID || current.ContextWindow != 96000 || current.Vision != "unknown" || len(environmentEntries(t, f, f.session.ID)) != 1 || len(modelEntries(t, f)) != 4 {
		t.Fatalf("reopening lost the latest state or repeated context: %+v", current)
	}
}

func TestActiveModelSwitchIsVisibleBeforeItsFirstRequest(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	f := newFixture(t, 1, func(ctx context.Context, n int, request map[string]any) any {
		if n == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return nil
			}
			return toolReply("work", "exec_command", map[string]any{"command": "true"})
		}
		body := string(encode(request))
		if request["model"] != "fixture-next" || !strings.Contains(body, "Host model switched") || len(wireEnvironments(request)) != 1 {
			t.Errorf("new model's first request lacks its isolated change notice: %s", body)
		}
		return textReply("done")
	})
	next := anotherModel(t, f)
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "work", Text: "use a tool"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	if err := f.service.SelectModel(f.ctx, f.session.ID, next.ID); err != nil {
		t.Fatal(err)
	}
	close(release)
	f.idle(t, f.session.ID)
	if f.count() != 2 || len(modelEntries(t, f)) != 1 || len(environmentEntries(t, f, f.session.ID)) != 1 {
		t.Fatal("active selection duplicated context or interrupted the run")
	}
}

func TestCompactionRebuildsCurrentModelAfterChangeNotice(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("A short history summary.") })
	next := anotherModel(t, f)
	if err := f.service.SelectModel(f.ctx, f.session.ID, next.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "long", Text: strings.Repeat("previous conversation details ", 3000)}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	if err := f.service.Compact(f.ctx, f.session.ID, ""); err != nil {
		t.Fatal(err)
	}
	view, err := f.service.InspectContext(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	latest, current := environmentState(view.Request.Messages)
	if latest.CompactionID == "" || latest.Model != current || current.ID != next.ModelID || current.Vision != "unsupported" || len(environmentEntries(t, f, f.session.ID)) != 2 {
		t.Fatalf("compaction restored stale model state: %+v", latest)
	}
}
