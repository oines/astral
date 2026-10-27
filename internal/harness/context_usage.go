package harness

import (
	"context"
	core "github.com/oines/astral-core"
)

// ContextUsage describes the last successful generation's reported input and
// frozen model window. It never resolves current configuration or estimates.
type ContextUsage struct {
	EntryID       string `json:"entry_id"`
	ModelRef      string `json:"model_ref,omitempty"`
	InputTokens   int    `json:"input_tokens"`
	ContextWindow int    `json:"context_window,omitempty"`
}

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
			if entry.ID == r.usageHead {
				report = r.contextUsage
				goto done
			}
			if entry.Kind != core.EntryMessage || entry.Message == nil || entry.Message.Role != core.RoleAssistant || entry.Message.Failed() || !entry.Usage.Available || entry.Usage.InputMissing {
				continue
			}
			report = &ContextUsage{EntryID: entry.ID, InputTokens: entry.Usage.InputTokens}
			if entry.Generation != nil {
				report.ModelRef = entry.Generation.Model.ID
				report.ContextWindow = entry.Generation.Model.ContextWindow
			}
			goto done
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
