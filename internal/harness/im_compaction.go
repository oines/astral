package harness

import (
	_ "embed"

	"github.com/oines/astral/internal/domain"
)

//go:embed prompts/im-compaction.md
var imCompactionPrompt string

func compactionPromptFor(record domain.Session) string {
	if record.BindingID != "" {
		return imCompactionPrompt
	}
	// An unset Host override leaves the Core default in effect.
	return ""
}
