package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/messages"
	"github.com/oines/astral/internal/schedule"
)

func setFixtureTimezone(t *testing.T, f *fixture, name string) {
	t.Helper()
	var value struct {
		domain.Meta
		domain.Preferences
	}
	if err := f.store.Get(f.ctx, "preferences", "owner", &value); err != nil && !domain.IsMissing(err) {
		t.Fatal(err)
	}
	value.ID, value.Timezone = "owner", name
	if err := f.store.Save(f.ctx, "preferences", &value.Meta, &value); err != nil {
		t.Fatal(err)
	}
}

func timezoneHistory(t *testing.T, f *fixture) map[string][]byte {
	t.Helper()
	session, err := f.service.Core(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	page, err := session.History().Scan(f.ctx, core.Query{Limit: 1000, MaxBytes: 64 << 20})
	if err != nil {
		t.Fatal(err)
	}
	entries := map[string][]byte{}
	for _, entry := range page.Entries {
		entries[entry.ID] = encode(entry)
	}
	return entries
}

func timezoneInput(t *testing.T, f *fixture, id string) core.Message {
	t.Helper()
	view, err := f.service.InspectContext(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range view.Request.Messages {
		var metadata messages.Metadata
		if json.Unmarshal(message.Metadata, &metadata) == nil && metadata.DeliveryID == id {
			return message
		}
	}
	t.Fatalf("model context is missing input %q", id)
	return core.Message{}
}

func TestGlobalTimezoneOnlyChangesNewInputsAndAppendsStableEnvironment(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("done") })
	setFixtureTimezone(t, f, "UTC")
	first := InputRequest{ID: "utc-input", Text: "This input uses UTC."}
	if _, err := f.service.Submit(f.ctx, f.session.ID, first); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	oldInput := timezoneInput(t, f, first.ID)
	inputTime := func(message core.Message) time.Time {
		t.Helper()
		var envelope struct {
			Time string `xml:"t,attr"`
		}
		if err := xml.Unmarshal([]byte(message.Content[0].Text), &envelope); err != nil {
			t.Fatal(err)
		}
		at, err := time.Parse(time.RFC3339Nano, envelope.Time)
		if err != nil {
			t.Fatal(err)
		}
		return at
	}
	if !strings.HasSuffix(inputTime(oldInput).Format(time.RFC3339), "Z") {
		t.Fatal("initial Web input did not use UTC")
	}
	before := timezoneHistory(t, f)
	setFixtureTimezone(t, f, "America/New_York")
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "local-input", Text: "This input uses the configured timezone."}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	newInput := timezoneInput(t, f, "local-input")
	newAt := inputTime(newInput)
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	if newAt.Format(time.RFC3339Nano) != newAt.In(location).Format(time.RFC3339Nano) {
		t.Fatal("new Web input did not adopt the global timezone")
	}
	after := timezoneHistory(t, f)
	for id, original := range before {
		if !bytes.Equal(original, after[id]) {
			t.Fatalf("timezone change rewrote prior history/context entry %q", id)
		}
	}
	if !bytes.Equal(encode(oldInput), encode(timezoneInput(t, f, first.ID))) {
		t.Fatal("current model view reformatted an old input")
	}
	environments := environmentEntries(t, f, f.session.ID)
	if !strings.Contains(environments[len(environments)-1].Message.Content[0].Text, `"timezone":"America/New_York"`) {
		t.Fatal("latest environment does not identify the global timezone")
	}
	if err := f.service.InitializeSession(f.ctx, f.session.ID); err != nil {
		t.Fatal(err)
	}
	if count := len(environmentEntries(t, f, f.session.ID)); count != len(environments) {
		t.Fatal("unchanged timezone generated another environment snapshot")
	}
	// Schedule timestamps use the same global setting while the stable
	// occurrence and its already-admitted input remain immutable on replay.
	occurrence := schedule.Occurrence{ScheduleID: "timezone-plan", AgentID: f.agent.ID, SessionID: f.session.ID, Title: "Local schedule", Prompt: "Scheduled instruction", ScheduledAt: time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC)}
	if err := f.service.deliverSchedule(f.ctx, occurrence); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	scheduled := timezoneInput(t, f, occurrence.InputID())
	if !strings.Contains(scheduled.Content[0].Text, `scheduled-at="2026-07-01T08:00:00-04:00"`) {
		t.Fatal("scheduled instruction did not localize its original occurrence time")
	}
	setFixtureTimezone(t, f, "UTC")
	if err := f.service.deliverSchedule(f.ctx, occurrence); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encode(scheduled), encode(timezoneInput(t, f, occurrence.InputID()))) {
		t.Fatal("schedule replay changed the timezone of an accepted instruction")
	}
	if _, err := f.service.Submit(f.ctx, f.session.ID, first); err != nil {
		t.Fatal(err)
	}
	if f.count() != 3 {
		t.Fatal("idempotent replay created an additional activation")
	}
}

func TestGlobalTimezoneChangeDoesNotRebuildFrozenNetworkRetry(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("done") })
	setFixtureTimezone(t, f, "UTC")
	original := f.service.Models.HTTPClient.Transport
	if original == nil {
		original = http.DefaultTransport
	}
	var mu sync.Mutex
	var payloads []string
	f.service.Models.HTTPClient.Transport = modelRecoveryTransport(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		request.Body = io.NopCloser(bytes.NewReader(body))
		mu.Lock()
		payloads = append(payloads, string(body))
		first := len(payloads) == 1
		mu.Unlock()
		if first {
			setFixtureTimezone(t, f, "America/New_York")
			return nil, io.ErrUnexpectedEOF
		}
		return original.RoundTrip(request)
	})
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "frozen-utc", Text: "Freeze this request before changing timezone."}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	mu.Lock()
	if len(payloads) != 2 || payloads[0] != payloads[1] {
		mu.Unlock()
		t.Fatal("global timezone change rebuilt the frozen network retry")
	}
	mu.Unlock()
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "new-zone", Text: "Use the updated timezone now."}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	mu.Lock()
	defer mu.Unlock()
	if len(payloads) != 3 || !strings.Contains(payloads[2], "America/New_York") {
		t.Fatal("the next new request did not receive the updated timezone snapshot")
	}
}
