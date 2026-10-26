package harness

import (
	"context"
	"encoding/json"

	core "github.com/oines/astral-core"
)

// ContextUsage describes the latest successful generation with provider-reported
// usage on this branch. It is not an estimate of the next request or a sum.
type ContextUsage struct {
	EntryID       string `json:"entry_id"`
	ModelRef      string `json:"model_ref,omitempty"`
	InputTokens   int    `json:"input_tokens"`
	ContextWindow int    `json:"context_window,omitempty"`
}

// Called under runtime.mu. The cache follows the selected history head, so
// ordinary state updates do not rescan history and forks keep their own lineage.
func (r *runtime) reportedContextUsage(ctx context.Context, head string) (*ContextUsage, error) {
	if head == r.usageHead {
		return r.contextUsage, nil
	}
	var report *ContextUsage
	cursor := ""
	for {
		page, err := r.core.Load().History().Path(ctx, head, core.Query{Cursor: cursor, Limit: 32, MaxBytes: 128 << 20})
		if err != nil {
			return nil, err
		}
		for _, entry := range page.Entries {
			if report == nil {
				if entry.ID == r.usageHead {
					report = r.contextUsage
					goto done
				}
				if entry.Kind != core.EntryMessage || entry.Message == nil || entry.Message.Role != core.RoleAssistant || entry.Message.Failed() || !entry.Usage.Available || entry.Usage.InputMissing {
					continue
				}
				report = &ContextUsage{EntryID: entry.ID, InputTokens: entry.Usage.InputTokens}
				if entry.Generation != nil && entry.Generation.Model.ContextWindow > 0 {
					report.ModelRef = entry.Generation.Model.ID
					report.ContextWindow = entry.Generation.Model.ContextWindow
					goto done
				}
				var metadata struct {
					ModelID string `json:"model_id"`
				}
				_ = json.Unmarshal(entry.Message.Metadata, &metadata)
				report.ModelRef = metadata.ModelID
			}
			// Astral adapters record their frozen profile reference on the message.
			// Older or custom messages can recover it from branch configuration.
			if report.ModelRef == "" && entry.Configuration != nil {
				report.ModelRef = entry.Configuration.ModelID
			}
			if report.ModelRef != "" {
				if model, err := r.service.Models.Resolve(ctx, report.ModelRef, nil); err == nil {
					report.ContextWindow = model.Spec().ContextWindow
				}
				goto done
			}
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
done:
	r.usageHead, r.contextUsage = head, report
	return report, nil
}
