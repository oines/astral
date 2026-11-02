package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
)

func TestSubagentReportTimezoneUsesSourceTimeAndReplayPreservesAcceptedBytes(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("Reviewed report.") })
	root := subagentTestSeed(t, f)
	// Only the transport identity is needed; no child model or worker generates
	// unrelated reports while exact source timestamps are under test.
	child := domain.Session{Meta: domain.Meta{ID: "timezone-source-child"}, AgentID: f.agent.ID, SubagentParentID: root.id, Title: "Time source"}
	if err := f.store.Save(f.ctx, "sessions", &child.Meta, &child); err != nil {
		t.Fatal(err)
	}
	reports := func() map[string]core.Entry {
		t.Helper()
		result := map[string]core.Entry{}
		for _, entry := range subagentTestEntries(t, f, root.id) {
			if entry.Message == nil || entry.Message.ContextKind != subagentUpdateKind {
				continue
			}
			var metadata map[string]string
			if json.Unmarshal(entry.Message.Metadata, &metadata) == nil && metadata["child_session_id"] == child.ID {
				if _, exists := result[metadata["message_id"]]; exists {
					t.Fatalf("report %s duplicated", metadata["message_id"])
				}
				result[metadata["message_id"]] = entry
			}
		}
		return result
	}
	deliver := func(entry core.Entry) core.Entry {
		t.Helper()
		source := encode(entry)
		eventually(t, f.ctx, func() bool {
			err := f.service.deliverSubagentEntry(f.ctx, child, entry)
			if err != nil && !errors.Is(err, errSubagentReportsPending) {
				t.Fatal(err)
			}
			return err == nil
		})
		if !bytes.Equal(source, encode(entry)) {
			t.Fatal("presentation timezone mutated the source entry")
		}
		var received core.Entry
		eventually(t, f.ctx, func() bool { var ok bool; received, ok = reports()[entry.ID]; return ok })
		f.idle(t, root.id)
		return received
	}
	check := func(source, received core.Entry, zone string) {
		t.Helper()
		location, err := time.LoadLocation(zone)
		if err != nil {
			t.Fatal(err)
		}
		want := source.Time.In(location).Format(time.RFC3339Nano)
		text := ""
		for _, part := range received.Message.Content {
			if part.Type == "text" {
				text += part.Text
			}
		}
		header := strings.SplitN(text, "\n", 2)[0]
		if !strings.Contains(header, want) || strings.Count(text, want) != 1 {
			t.Fatalf("report must have exactly one source timestamp in its header: want %q, got %q", want, text)
		}
		if source.Message != nil {
			body := assistantText(source.Message)
			if !strings.HasSuffix(text, body) {
				t.Fatalf("multi-part report body was changed: want suffix %q got %q", body, text)
			}
		}
	}
	makeProgress := func(id string, at time.Time) core.Entry {
		message := core.Message{Role: core.RoleAssistant, Content: append(core.Text("FIRST_BODY_PART\n"), core.Text("SECOND_BODY_PART")...)}
		return core.Entry{ID: id, RunID: "run-" + id, Kind: core.EntryMessage, Time: at, Message: &message}
	}
	summer := time.Date(2026, time.July, 7, 15, 4, 5, 123456789, time.UTC)
	winter := time.Date(2026, time.January, 7, 15, 4, 5, 987654321, time.UTC)
	setFixtureTimezone(t, f, "UTC")
	initial := makeProgress("utc-progress", summer)
	check(initial, deliver(initial), "UTC")
	setFixtureTimezone(t, f, "America/New_York")
	sources := []core.Entry{initial}
	for _, season := range []struct {
		name   string
		at     time.Time
		offset string
	}{{"summer", summer, "-04:00"}, {"winter", winter, "-05:00"}} {
		progress := makeProgress(season.name+"-progress", season.at)
		ended := core.Entry{ID: season.name + "-ended", RunID: "run-" + season.name, Kind: core.EntryRun, Time: season.at, Run: &core.RunRecord{ID: "run-" + season.name, Status: "completed"}}
		for _, source := range []core.Entry{progress, ended} {
			received := deliver(source)
			check(source, received, "America/New_York")
			if !strings.Contains(string(encode(received.Message)), season.offset) {
				t.Fatalf("%s did not use DST offset %s", source.ID, season.offset)
			}
			sources = append(sources, source)
		}
	}
	before := reports()
	setFixtureTimezone(t, f, "Asia/Singapore")
	for _, source := range sources {
		deliver(source)
	}
	after := reports()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("timezone change and replay rewrote accepted messages or added duplicate reports")
	}
	newSource := makeProgress("singapore-new-progress", winter)
	check(newSource, deliver(newSource), "Asia/Singapore")
	for id, old := range before {
		if got := reports()[id]; !bytes.Equal(encode(old), encode(got)) {
			t.Fatalf("new report changed old report %s", id)
		}
	}
}
