package messages

import (
	"encoding/json"
	"fmt"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
)

type ScheduleMetadata struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	ScheduledAt time.Time `json:"scheduled_at"`
}

// Scheduled is an actionable input with its own provenance. The prompt is
// deliberately distinct from an Owner's manually entered Web message.
func Scheduled(id, prompt string, plan ScheduleMetadata, at time.Time, im bool) core.Message {
	body := fmt.Sprintf("<message id=\"%s\" source=\"schedule\" schedule-id=\"%s\" schedule-title=\"%s\" scheduled-at=\"%s\" t=\"%s\">\n%s\n</message>",
		Escape(id), Escape(plan.ID), Escape(plan.Title), plan.ScheduledAt.Format(time.RFC3339Nano), at.Format(time.RFC3339Nano), Escape(prompt))
	if im {
		body += "\n" + IMReplyReminder
	}
	metadata, _ := json.Marshal(Metadata{Source: "schedule", DeliveryID: id, DisplayText: prompt, AcceptedAt: &at, Sender: domain.Sender{ID: plan.ID, Name: plan.Title}, Schedule: &plan})
	return core.Message{Role: core.RoleUser, Content: core.Text(body), Metadata: metadata}
}
