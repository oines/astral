package harness

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
)

func titleRequest(request map[string]any) bool {
	return strings.Contains(string(encode(request)), "Generate a short title for this conversation")
}

func TestSessionTitleUsesSelectedModelWithoutBlockingOrChangingHistory(t *testing.T) {
	started := make(chan map[string]any, 1)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var calls atomic.Int32
	f := newFixture(t, 0, func(ctx context.Context, _ int, request map[string]any) any {
		if !titleRequest(request) {
			return textReply("chat reply")
		}
		calls.Add(1)
		started <- request
		select {
		case <-ctx.Done():
			return nil
		case <-release:
			return textReply(`"K8s滚动更新分析"`)
		}
	})
	var selected domain.ModelProfile
	if err := f.store.Get(f.ctx, "models", f.agent.DefaultModelID, &selected); err != nil {
		t.Fatal(err)
	}
	selected.Meta, selected.ModelID, selected.Name = domain.Meta{}, "selected-current-model", "Current model"
	declareFixtureModelRecommendations(t, f, &selected)
	if err := f.service.Models.SaveProfile(f.ctx, &selected); err != nil {
		t.Fatal(err)
	}
	session, err := f.service.CreateSession(f.ctx, f.agent.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.service.SelectModel(f.ctx, session.ID, selected.ID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	input := InputRequest{ID: "first", Text: "## 为什么 Kubernetes 的滚动更新这么慢\n日志在这里"}
	if _, err = f.service.Submit(ctx, session.ID, input); err != nil {
		t.Fatal(err)
	}
	cancel() // Losing the HTTP connection cannot lose the independent title.
	var request map[string]any
	select {
	case request = <-started:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	tools, _ := request["tools"].([]any)
	if request["model"] != selected.ModelID || len(request["messages"].([]any)) != 1 || len(tools) != 0 {
		t.Fatalf("title did not use an isolated request on the selected model: %s", encode(request))
	}
	if body := string(encode(request)); strings.Contains(body, "Current Astral environment") || strings.Contains(body, "<message") {
		t.Fatalf("title request contained host context: %s", body)
	}
	view := f.idle(t, session.ID) // Chat can finish while title generation is blocked.
	if view.Title != "为什么 Kubernetes 的滚动更新这么慢" || view.TitleSource != domain.TitlePrompt {
		t.Fatalf("missing durable fallback: %+v", view.Session)
	}
	if _, err = f.service.Submit(f.ctx, session.ID, input); err != nil {
		t.Fatal(err)
	}
	if err = f.service.SelectModel(f.ctx, session.ID, f.agent.DefaultModelID); err != nil {
		t.Fatal(err)
	}
	pin := true
	if err = f.service.SaveSessionPresentation(f.ctx, session.ID, nil, &pin, nil, nil); err != nil {
		t.Fatal(err)
	}
	unblock()
	eventually(t, f.ctx, func() bool {
		return f.store.Get(f.ctx, "sessions", session.ID, &session) == nil && session.TitleSource == domain.TitleGenerated
	})
	if session.Title != "K8s滚动更新分析" || !session.Pinned || sessionSelection(t, f, session.ID).ID != f.agent.DefaultModelID {
		t.Fatalf("generated title lost unrelated edits: %+v", session)
	}
	contextView, err := f.service.InspectContext(f.ctx, session.ID)
	if err != nil || strings.Contains(string(encode(contextView)), "Generate a short title") || strings.Contains(string(encode(contextView)), "K8s滚动更新分析") {
		t.Fatalf("title polluted model history: %v %s", err, encode(contextView))
	}
	f.restart(t)
	if _, err = f.service.Submit(f.ctx, session.ID, InputRequest{ID: "later", Text: "接下来分析别的问题"}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, session.ID)
	if calls.Load() != 1 {
		t.Fatalf("title was generated %d times", calls.Load())
	}
}

func TestManualSessionTitleWinsDuringGeneration(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	f := newFixture(t, 0, func(ctx context.Context, _ int, request map[string]any) any {
		if !titleRequest(request) {
			return textReply("chat reply")
		}
		close(started)
		select {
		case <-ctx.Done():
			return nil
		case <-release:
			return textReply("模型生成标题")
		}
	})
	session, err := f.service.CreateSession(f.ctx, f.agent.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.Submit(f.ctx, session.ID, InputRequest{ID: "first", Text: "手动保留的标题"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	// Even renaming to the same fallback text must count as an Owner choice.
	manual := "手动保留的标题"
	if err = f.service.SaveSessionPresentation(f.ctx, session.ID, &manual, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	unblock()
	f.service.workers.Wait()
	if err = f.store.Get(f.ctx, "sessions", session.ID, &session); err != nil {
		t.Fatal(err)
	}
	if session.Title != manual || session.TitleSource != domain.TitleManual {
		t.Fatalf("manual rename was lost: %+v", session)
	}
}

func TestFailedTitleGenerationKeepsFallbackWithoutRetry(t *testing.T) {
	for _, result := range []string{"empty", "invalid response", "multiline", "oversized"} {
		t.Run(result, func(t *testing.T) {
			var calls atomic.Int32
			f := newFixture(t, 0, func(_ context.Context, _ int, request map[string]any) any {
				if !titleRequest(request) {
					return textReply("chat reply")
				}
				calls.Add(1)
				switch result {
				case "invalid response":
					return map[string]any{"error": map[string]any{"message": "failed"}}
				case "multiline":
					return textReply("标题\n额外解释")
				case "oversized":
					return textReply(strings.Repeat("长", 100))
				default:
					return textReply("")
				}
			})
			session, err := f.service.CreateSession(f.ctx, f.agent.ID, "New session")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.service.Submit(f.ctx, session.ID, InputRequest{ID: "first", Text: "检查 `fmt.Println` 的用法"}); err != nil {
				t.Fatal(err)
			}
			f.idle(t, session.ID)
			eventually(t, f.ctx, func() bool { return calls.Load() == 1 })
			f.service.workers.Wait()
			f.restart(t) // Joins the attempt and verifies the claim survives reopen.
			if _, err = f.service.Submit(f.ctx, session.ID, InputRequest{ID: "second", Text: "后续消息"}); err != nil {
				t.Fatal(err)
			}
			view := f.idle(t, session.ID)
			if view.Title != "检查 fmt.Println 的用法" || view.TitleSource != domain.TitlePrompt || calls.Load() != 1 {
				t.Fatalf("failed generation changed the fallback or retried: %+v, calls %d", view.Session, calls.Load())
			}
		})
	}
}

func TestExplicitTitleDoesNotGenerate(t *testing.T) {
	f := newFixture(t, 0, func(_ context.Context, _ int, request map[string]any) any {
		if titleRequest(request) {
			t.Error("an explicitly named Session must not generate a title")
		}
		return textReply("chat reply")
	})
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "first", Text: "请求内容"}); err != nil {
		t.Fatal(err)
	}
	view := f.idle(t, f.session.ID)
	if view.Title != f.session.Title || view.TitleSource != domain.TitleManual {
		t.Fatalf("explicit title changed: %+v", view.Session)
	}
}

func TestFallbackSessionTitle(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"## 标题\n第二行", "标题"},
		{"**检查** `fmt.Println` 和 [链接](https://example.com)", "检查 fmt.Println 和 链接"},
		{"```go\nvar x = 1\n```\n真正的主题", "真正的主题"},
		{"```go\nvar x = 1\n```", ""},
		{"  \n ", ""},
		{strings.Repeat("中", 70), strings.Repeat("中", 50) + "…"},
		{"- a   short\ttitle", "a short title"},
	} {
		if got := fallbackSessionTitle(tc.input); got != tc.want {
			t.Errorf("fallbackSessionTitle(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestBoundTitleInput(t *testing.T) {
	input := strings.Repeat("头", 10000) + strings.Repeat("尾", 10000)
	spec := core.ModelSpec{ContextWindow: 8192, MaxInputTokens: 4096}
	got := boundTitleInput(input, spec, titleGenerateMaxTokens)
	if len([]rune(got)) != 4096-titlePromptOverheadTokens || !strings.HasPrefix(got, "头") || !strings.HasSuffix(got, "尾") || !strings.Contains(got, "\n…\n") {
		t.Fatal("long input did not fit the model input limit with a head/tail sample")
	}
	spec.ContextWindow = 1024
	if boundTitleInput(input, spec, titleGenerateMaxTokens) != "" {
		t.Fatal("an impossible title budget must skip generation")
	}
}
