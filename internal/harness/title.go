package harness

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/modelbridge"
	"github.com/oines/astral/internal/persistence"
)

const (
	titleGenerateTimeout = 60 * time.Second
	// Reasoning models need room for thinking as well as the short title.
	titleGenerateMaxTokens    = 2048
	titlePromptOverheadTokens = 512
	fallbackTitleMaxRunes     = 50
)

// Keep Memoh's rules and worked examples: a topic noun phrase makes a more
// useful sidebar title than a shortened copy of the user's request.
const titleGenerationPrompt = `Generate a short title for this conversation, in the same language as the user's message.

Rules:
- A noun phrase naming the TOPIC, not a summary of the request.
- Chinese titles: 6-10 characters preferred; never shorter than 4 or longer than 12. English titles: 2-6 words.
- Keep concrete proper nouns (products, technologies, places).
- Prefer concrete specifics over abstract categories: name the actual thing (the damage, the tool, the symptom), not the class of problem.
- Vivid wording from the user's own message may be kept as-is; don't sand casual rants down into officialese.
- For two-entity topics in Chinese, use the "X与Y" form; in English, use "X and Y".
- For Chinese titles, allowed suffixes when one is needed: 分析/解析/介绍/建议/疑问/困惑/误解/修复/控制/设计/差异 — pick by topic nature, or use no suffix. English titles use plain noun phrases.
- No ending punctuation, no quotes.
- Return ONLY the title text.

Examples of the rules (do not copy their content):
- "烤箱预热要多久" → "烤箱预热时长" (concrete noun, no suffix)
- "为什么 Kubernetes 的滚动更新这么慢" → "K8s滚动更新缓慢分析" (technical, suffix 分析)
- "我到底是感冒还是过敏" → "感冒还是过敏性鼻炎" (keep the user's own contrast)
- "无糖可乐里的糖醇是不是更健康" → "无糖与糖醇区别" (two-entity X与Y form)
- "楼下的装修电钻吵了一整周" → "装修电钻噪声困扰" (casual complaint, vivid word kept)

User: `

func defaultSessionTitle(title string) bool {
	switch strings.ToLower(strings.TrimSpace(title)) {
	case "", "new session", "新建会话", "untitled session":
		return true
	default:
		return false
	}
}

// maybeGenerateSessionTitle runs under r.mu after durable input acceptance.
// Persist the fallback and claim the single attempt before starting any network
// work. A failed call or a restart keeps the fallback without retrying.
func (r *runtime) maybeGenerateSessionTitle(userQuery string) {
	userQuery = strings.TrimSpace(userQuery)
	if userQuery == "" {
		return
	}
	s := r.service
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.workers.Add(1)
	s.mu.Unlock()
	// Owner disconnect/Stop does not cancel the independent metadata request;
	// server shutdown does, and Close waits for this worker.
	ctx, cancel := context.WithTimeout(s.ctx, titleGenerateTimeout)
	finish := func() { cancel(); s.workers.Done() }
	var claimed domain.Session
	err := s.Store.Update(ctx, func(tx *persistence.Tx) error {
		var record domain.Session
		if err := tx.Get(ctx, "sessions", r.id, &record); err != nil {
			return err
		}
		if record.BindingID != "" {
			return nil
		}
		if record.TitleSource != domain.TitleDefault && (record.TitleSource != "" || !defaultSessionTitle(record.Title)) {
			return nil
		}
		if fallback := fallbackSessionTitle(userQuery); fallback != "" {
			record.Title = fallback
		}
		record.TitleSource = domain.TitlePrompt
		if err := tx.Save(ctx, "sessions", &record.Meta, &record); err != nil {
			return err
		}
		claimed = record
		return nil
	})
	if err != nil || claimed.ID == "" {
		finish()
		return
	}
	s.notify(r.id)
	go func() {
		defer finish()
		title := r.generateSessionTitle(ctx, claimed, userQuery)
		if title == "" {
			return
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		updated := false
		err := s.Store.Update(ctx, func(tx *persistence.Tx) error {
			var current domain.Session
			if err := tx.Get(ctx, "sessions", r.id, &current); err != nil {
				return err
			}
			// Manual renames (even to the same text) win. Unrelated changes such
			// as pinning or model selection are preserved by updating a fresh row.
			if current.TitleSource != domain.TitlePrompt || current.Title != claimed.Title {
				return nil
			}
			current.Title, current.TitleSource = title, domain.TitleGenerated
			if err := tx.Save(ctx, "sessions", &current.Meta, &current); err != nil {
				return err
			}
			updated = true
			return nil
		})
		if err != nil {
			slog.DebugContext(ctx, "session title update failed", "session_id", r.id, "error", err)
		} else if updated {
			s.notify(r.id)
		}
	}()
}

func (r *runtime) generateSessionTitle(ctx context.Context, record domain.Session, userQuery string) string {
	ctx = modelbridge.WithSessionID(ctx, r.id)
	model, err := r.service.Models.Resolve(ctx, record.ModelRef, nil)
	if err != nil {
		return ""
	}
	spec := model.Spec()
	outputTokens := min(titleGenerateMaxTokens, spec.MaxOutputTokens)
	input := boundTitleInput(userQuery, spec, outputTokens)
	if input == "" || outputTokens <= 0 {
		return ""
	}
	options := record.Options
	options.ToolChoice = core.ToolChoice{Mode: "none"}
	options.ResponseFormat, options.Stop = nil, nil
	// A separate model call has no tools, environment, history or Core rollout.
	// Use the exact model selected at acceptance, without Core's retry loop.
	reply, err := model.Stream(ctx, core.Request{
		ModelID: spec.ID, ModelRevision: spec.Revision,
		Messages:        []core.Message{core.User(titleGenerationPrompt + input)},
		MaxOutputTokens: outputTokens, Options: options,
	}, func(core.ModelEvent) {})
	if err != nil || reply.Message.IsError || reply.Message.FinishReason != core.FinishStop {
		return ""
	}
	var text strings.Builder
	for _, part := range reply.Message.Content {
		if part.Type == "text" {
			text.WriteString(part.Text)
		}
	}
	title := strings.Trim(strings.TrimSpace(text.String()), "\"'`“”‘’")
	title = strings.TrimSpace(title)
	if strings.ContainsAny(title, "\r\n") || len([]rune(title)) > 80 {
		return ""
	}
	return title
}

// Long pasted documents can name the subject at either end. Reserve room for
// output and the fixed prompt, then keep 2/3 head and 1/3 tail as in Memoh.
func boundTitleInput(input string, spec core.ModelSpec, outputTokens int) string {
	window := spec.ContextWindow
	if window <= 0 {
		window = 8192
	}
	budget := window - outputTokens - titlePromptOverheadTokens
	if spec.MaxInputTokens > 0 {
		budget = min(budget, spec.MaxInputTokens-titlePromptOverheadTokens)
	}
	if budget <= 0 {
		return ""
	}
	runes := []rune(input)
	if len(runes) <= budget {
		return input
	}
	// The elision itself also occupies input budget.
	if budget < 6 {
		return string(runes[:budget])
	}
	budget -= 3
	head := budget * 2 / 3
	return string(runes[:head]) + "\n…\n" + string(runes[len(runes)-(budget-head):])
}

var fallbackMarkdownPatterns = []struct {
	re      *regexp.Regexp
	replace string
}{
	{regexp.MustCompile("(?s)```.*?```"), " "},
	{regexp.MustCompile("`([^`]+)`"), "$1"},
	{regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`), "$1"},
	{regexp.MustCompile(`(?m)^\s{0,3}#{1,6}\s+`), ""},
	{regexp.MustCompile(`(?m)^\s*>+\s?`), ""},
	{regexp.MustCompile(`(?m)^\s*[-*+]\s+`), ""},
	{regexp.MustCompile(`(?m)^\s*\d+\.\s+`), ""},
	{regexp.MustCompile(`(\*\*|\*|__|~~)`), ""},
	{regexp.MustCompile(`\|`), " "},
}

func fallbackSessionTitle(input string) string {
	for _, pattern := range fallbackMarkdownPatterns {
		input = pattern.re.ReplaceAllString(input, pattern.replace)
	}
	line := strings.SplitN(strings.TrimSpace(input), "\n", 2)[0]
	line = strings.Join(strings.Fields(line), " ")
	runes := []rune(line)
	if len(runes) > fallbackTitleMaxRunes {
		return string(runes[:fallbackTitleMaxRunes]) + "…"
	}
	return line
}
