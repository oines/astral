package messages

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestScheduledInputKeepsProvenanceAndIMReplyReminder(t *testing.T) {
	at := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	for _, bound := range []bool{false, true} {
		message := Scheduled("input", "Inspect <project>", ScheduleMetadata{ID: "schedule_1", Title: `Daily "check"`, ScheduledAt: at}, at.Add(time.Second), bound)
		var metadata Metadata
		if err := json.Unmarshal(message.Metadata, &metadata); err != nil {
			t.Fatal(err)
		}
		if metadata.Source != "schedule" || metadata.DisplayText != "Inspect <project>" || metadata.Schedule == nil || metadata.Schedule.ID != "schedule_1" {
			t.Fatalf("schedule metadata was lost: %+v", metadata)
		}
		if metadata.AcceptedAt == nil || !metadata.AcceptedAt.Equal(at.Add(time.Second)) {
			t.Fatalf("actual input acceptance time was lost: %+v", metadata)
		}
		body := message.Content[0].Text
		if !strings.Contains(body, `source="schedule"`) || !strings.Contains(body, "Inspect &lt;project&gt;") || strings.Contains(body, `source="web"`) {
			t.Fatalf("incorrect model source: %s", body)
		}
		if strings.Contains(body, IMReplyReminder) != bound {
			t.Fatalf("IM reply reminder does not match binding: %s", body)
		}
	}
}

func TestScheduledInputRetainsProvidedTimezone(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC).In(location)
	message := Scheduled("input", "Review the project", ScheduleMetadata{ID: "plan", Title: "Check", ScheduledAt: at}, at.Add(time.Second), false)
	if !strings.Contains(message.Content[0].Text, `scheduled-at="2026-07-01T08:00:00-04:00"`) || !strings.Contains(message.Content[0].Text, `t="2026-07-01T08:00:01-04:00"`) {
		t.Fatal("scheduled model input discarded its global timezone:", message.Content[0].Text)
	}
}
