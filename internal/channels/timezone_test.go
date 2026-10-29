package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/messages"
	"github.com/oines/astral/internal/testkit"
)

func setChannelTimezone(t *testing.T, p *testkit.Platform, name string) {
	t.Helper()
	var value struct {
		domain.Meta
		domain.Preferences
	}
	if err := p.Store.Get(p.Context, "preferences", "owner", &value); err != nil && !domain.IsMissing(err) {
		t.Fatal(err)
	}
	value.ID, value.Timezone = "owner", name
	if err := p.Store.Save(p.Context, "preferences", &value.Meta, &value); err != nil {
		t.Fatal(err)
	}
}

func TestIMAndProbeUseGlobalTimezoneWithoutRewritingArchivedOrPriorContext(t *testing.T) {
	requests := make(chan map[string]any, 4)
	p := testkit.New(t, 0, func(_ context.Context, _ int, request map[string]any) any {
		requests <- request
		return testkit.Tool("decide", map[string]bool{"participate": false})
	})
	s := New(p.Store, p.Harness)
	bot := newBot(t)
	connection := bot.connection(t, s, p.Agent.ID)
	binding := createBinding(t, p, s, connection, "-100", "")
	at := time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC)
	edited := at.Add(time.Minute)
	first := domain.Observation{SchemaVersion: 1, ID: "utc-event", ExternalEventID: "utc-event", MessageID: "10", Kind: "message.edited", ConnectionID: connection.ID, ConversationID: binding.ConversationID,
		Sender: domain.Sender{ID: "7", Name: "Ada"}, Text: "First immutable observation.", SentAt: at, ReceivedAt: at, EditedAt: &edited}
	setChannelTimezone(t, p, "UTC")
	if _, err := p.Store.Archive(p.Context, first); err != nil {
		t.Fatal(err)
	}
	archived, err := p.Store.Observation(p.Context, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	originalArchive := encoded(archived)
	if err := s.deliver(p.Context, binding, first); err != nil {
		t.Fatal(err)
	}
	p.Idle(t, binding.SessionID)
	find := func(view core.Context, id string) core.Message {
		t.Helper()
		for _, message := range view.Request.Messages {
			var metadata messages.Metadata
			if json.Unmarshal(message.Metadata, &metadata) == nil && metadata.EventID == id && message.ContextKind == "im.observation" {
				return message
			}
		}
		t.Fatalf("missing observation %q", id)
		return core.Message{}
	}
	view, err := p.Harness.InspectContext(p.Context, binding.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	prior := find(view, first.ID)
	if !strings.Contains(prior.Content[0].Text, `t="2026-07-01T12:00:00Z"`) || !strings.Contains(prior.Content[0].Text, `edited="2026-07-01T12:01:00Z"`) {
		t.Fatal("initial IM observation did not use UTC")
	}
	setChannelTimezone(t, p, "America/New_York")
	second := first
	second.ID, second.ExternalEventID, second.MessageID, second.Text = "localized-event", "localized-event", "11", "New localized observation."
	if _, err := p.Store.Archive(p.Context, second); err != nil {
		t.Fatal(err)
	}
	if err := s.deliver(p.Context, binding, second); err != nil {
		t.Fatal(err)
	}
	p.Idle(t, binding.SessionID)
	view, err = p.Harness.InspectContext(p.Context, binding.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	localized := find(view, second.ID)
	if !strings.Contains(localized.Content[0].Text, `t="2026-07-01T08:00:00-04:00"`) || !strings.Contains(localized.Content[0].Text, `edited="2026-07-01T08:01:00-04:00"`) {
		t.Fatal("new IM observation did not localize original and edited timestamps:", localized.Content[0].Text)
	}
	if !bytes.Equal(encoded(prior), encoded(find(view, first.ID))) || first.EditedAt.Location() != time.UTC {
		t.Fatal("new IM construction mutated prior context or the input's edited timestamp")
	}
	archived, err = p.Store.Observation(p.Context, first.ID)
	if err != nil || !bytes.Equal(originalArchive, encoded(archived)) {
		t.Fatal("global timezone change rewrote the original archived platform event")
	}
	for _, zone := range []string{"America/New_York", "Asia/Shanghai"} {
		setChannelTimezone(t, p, zone)
		if participate, err := s.shouldParticipate(p.Context, binding, view); err != nil || participate {
			t.Fatalf("localized Probe request failed: %v %v", participate, err)
		}
		request := <-requests
		wire := request["messages"].([]any)
		last := wire[len(wire)-1].(map[string]any)
		text := reminderWireText(last)
		line := strings.SplitN(text, "\n", 2)[0]
		current, err := time.Parse(time.RFC3339, strings.TrimPrefix(line, "Current time: "))
		if err != nil {
			t.Fatalf("Probe current time is malformed: %q", line)
		}
		location, err := time.LoadLocation(zone)
		if err != nil {
			t.Fatal(err)
		}
		if current.Format(time.RFC3339) != current.In(location).Format(time.RFC3339) {
			t.Fatalf("Probe current time did not adopt %s: %s", zone, line)
		}
	}
	if err := s.deliver(p.Context, binding, first); err != nil {
		t.Fatal(err)
	}
	after, err := p.Harness.InspectContext(p.Context, binding.SessionID)
	if err != nil || !bytes.Equal(encoded(prior), encoded(find(after, first.ID))) || !bytes.Equal(encoded(localized), encoded(find(after, second.ID))) {
		t.Fatal("delivery replay or Probe reformatted existing observations")
	}
}
