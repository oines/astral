package recall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	core "github.com/oines/astral-core"
	"github.com/oines/astral-core/store/sqlite"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/messages"
	"github.com/oines/astral/internal/persistence"
)

type fixture struct {
	ctx    context.Context
	store  *persistence.Store
	core   *sqlite.Store
	recall *Service
	clock  time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	dir := t.TempDir()
	store, err := persistence.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	history, err := sqlite.Open(filepath.Join(dir, "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{ctx: ctx, store: store, core: history, recall: New(store, history), clock: time.Now().UTC()}
	t.Cleanup(func() { _ = f.recall.Close(); _ = history.Close(); _ = store.Close(); cancel() })
	return f
}

func (f *fixture) session(t *testing.T, id, agent string) {
	t.Helper()
	record := domain.Session{Meta: domain.Meta{ID: id}, AgentID: agent, Title: id, Source: "web", ModelRef: "missing-model~1"}
	if err := f.store.Save(f.ctx, "sessions", &record.Meta, &record); err != nil {
		t.Fatal(err)
	}
	f.append(t, id,
		core.Entry{Kind: core.EntryCreated, Created: &core.CreationRecord{FormatVersion: core.FormatVersion, SessionID: id, RolloutID: id}},
		core.Entry{Kind: core.EntryConfiguration, Configuration: &core.Configuration{ModelID: "missing-model~1"}},
	)
}

func (f *fixture) append(t *testing.T, id string, entries ...core.Entry) []core.Entry {
	t.Helper()
	state, err := f.core.LoadState(f.ctx, id)
	if err != nil && !errors.Is(err, core.ErrNotFound) {
		t.Fatal(err)
	}
	head := state.Branch.HeadID
	for i := range entries {
		e := &entries[i]
		if e.ID == "" {
			e.ID = domain.ID("entry")
		}
		e.SessionID, e.Version = id, state.Version+1
		f.clock = f.clock.Add(time.Second)
		e.Time = f.clock
		if e.IsHistory() {
			e.ParentID, head = head, e.ID
		}
	}
	_, err = f.core.Commit(f.ctx, id, core.Commit{ID: domain.ID("commit"), ExpectedVersion: state.Version, Entries: entries})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func message(role core.Role, text string) core.Entry {
	m := core.Message{Role: role, Content: core.Text(text)}
	if role == core.RoleAssistant {
		m.FinishReason = core.FinishStop
	}
	return core.Entry{Kind: core.EntryMessage, Message: &m}
}

func (f *fixture) fork(t *testing.T, parent, child, head string) {
	t.Helper()
	state, err := f.core.LoadState(f.ctx, parent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.core.Fork(f.ctx, core.ForkRequest{SourceID: parent, SourceRolloutID: state.RolloutID, TargetID: child, HeadID: head, Mode: core.ForkShared}); err != nil {
		t.Fatal(err)
	}
	var record domain.Session
	if err = f.store.Get(f.ctx, "sessions", parent, &record); err != nil {
		t.Fatal(err)
	}
	record.Meta, record.Title, record.ParentSessionID, record.ForkEntryID = domain.Meta{ID: child}, child, parent, head
	if err = f.store.Save(f.ctx, "sessions", &record.Meta, &record); err != nil {
		t.Fatal(err)
	}
}

func search(t *testing.T, f *fixture, request SearchRequest) SearchPage {
	t.Helper()
	page, err := f.recall.Search(f.ctx, "agent", request)
	if err != nil {
		t.Fatal(err)
	}
	return page
}

func TestSearchFindsDeepUnicodePassagesAndPagesEveryHit(t *testing.T) {
	f := newFixture(t)
	f.session(t, "one", "agent")
	text := strings.Repeat("这是一段与查询无关的背景🙂", 400) + "电脑权限：第一个决定。" + strings.Repeat("无关背景", 150) + "电脑权限：第二个决定。" + strings.Repeat("结束背景", 150) + "电脑权限：最后的结论。"
	entry := f.append(t, "one", message(core.RoleUser, "开头没有关键词"), message(core.RoleAssistant, text))[1]
	f.session(t, "two", "agent")
	f.append(t, "two", message(core.RoleAssistant, "另一个 session 的电脑权限。"))
	f.session(t, "private", "other")
	f.append(t, "private", message(core.RoleAssistant, "电脑权限：另一位 Agent 的秘密。"))
	page := search(t, f, SearchRequest{Query: "电脑", ContextChars: 80, Limit: 1})
	if len(page.Hits) != 1 || page.Hits[0].SessionID != "two" || page.NextCursor == "" {
		t.Fatalf("first page: %+v", page)
	}
	// Appending to existing Sessions and adding a Session cannot change a
	// search snapshot that has already been returned to a script.
	f.append(t, "one", message(core.RoleAssistant, "新追加的电脑权限，旧查询不可见。"))
	f.session(t, "three", "agent")
	f.append(t, "three", message(core.RoleAssistant, "新 session 的电脑权限。"))
	all := append([]Hit{}, page.Hits...)
	for page.NextCursor != "" {
		page = search(t, f, SearchRequest{Cursor: page.NextCursor, Limit: 1})
		all = append(all, page.Hits...)
		if len(all) > 5 {
			t.Fatal("cursor repeated hits")
		}
	}
	if len(all) != 4 {
		t.Fatalf("lost passages: %+v", all)
	}
	previous := -1
	for _, hit := range all[1:] {
		if hit.SessionID != "one" || hit.EntryID != entry.ID || hit.Start <= previous || !strings.Contains(hit.Text, "电脑") || utf8.RuneCountInString(hit.Text) > 80 {
			t.Fatalf("wrong snippet or provenance: %+v", hit)
		}
		if hit.Text != string([]rune(text)[hit.Start:hit.End]) || hit.Start < 4000 {
			t.Fatal("snippet offsets are not original Unicode positions")
		}
		previous = hit.Start
	}
	if hits := search(t, f, SearchRequest{Query: "电脑"}).Hits; len(hits) != 6 {
		t.Fatalf("new snapshot did not catch up: %d", len(hits))
	}
	if _, err := f.recall.Search(f.ctx, "agent", SearchRequest{Query: "电脑", SessionIDs: []string{"private"}}); err == nil {
		t.Fatal("foreign explicit scope accepted")
	}
}

func TestSearchLiteralModesShortWordsAndCodeIdentifiers(t *testing.T) {
	f := newFixture(t)
	f.session(t, "one", "agent")
	f.append(t, "one", message(core.RoleAssistant, "电脑 权限 Foo_bar/路径/a_b%[c] KELVIN 🙂 中文连续字符串"))
	for _, tc := range []struct {
		query, mode string
		sensitive   bool
		want        int
	}{
		{"电脑", "", false, 1}, {"电", "", false, 1}, {"🙂", "", false, 1}, {"连续字", "", false, 1},
		{"foo_bar/路径/a_b%[c]", "phrase", false, 1}, {"foo_bar", "", true, 0}, {"Foo_bar", "", true, 1},
		{"kelvin", "", false, 1}, {"电脑 权限", "all", false, 1}, {"电脑 不存在", "all", false, 0},
		{"不存在 权限", "any", false, 1}, {"权限 电脑", "phrase", false, 0},
	} {
		t.Run(tc.query+tc.mode+fmt.Sprint(tc.sensitive), func(t *testing.T) {
			page := search(t, f, SearchRequest{Query: tc.query, Match: tc.mode, CaseSensitive: tc.sensitive})
			if len(page.Hits) != tc.want {
				t.Fatalf("query %q: %+v", tc.query, page)
			}
		})
	}
	f.append(t, "one", message(core.RoleAssistant, strings.Repeat("a", 2000)))
	page := search(t, f, SearchRequest{Query: "aaa", ContextChars: 32, Limit: 200})
	if len(page.Hits) < 50 {
		t.Fatal("dense matches collapsed into one giant excerpt")
	}
	for _, hit := range page.Hits {
		if len([]rune(hit.Text)) > 32 {
			t.Fatal("dense excerpt exceeded requested width")
		}
	}
}

func TestLegacySharedForkScopeDedupCompactionAndForgedReferences(t *testing.T) {
	f := newFixture(t)
	f.session(t, "parent", "agent")
	ancestor := f.append(t, "parent", message(core.RoleUser, "needle shared ancestor"))[0]
	f.fork(t, "parent", "child", ancestor.ID)
	parentOnly := f.append(t, "parent", message(core.RoleAssistant, "needle parent-only later"))[0]
	f.append(t, "child", message(core.RoleAssistant, "needle child-only later"))
	f.append(t, "child", core.Entry{Kind: core.EntryCompaction, Compaction: &core.CompactionRecord{ThroughID: ancestor.ID, Summary: "summary-only compacted context"}})
	all := search(t, f, SearchRequest{Query: "needle"})
	if len(all.Hits) != 3 {
		t.Fatalf("shared history duplicated: %+v", all)
	}
	child := search(t, f, SearchRequest{Query: "needle", SessionIDs: []string{"child"}, Order: "oldest"})
	if len(child.Hits) != 2 || child.Hits[0].EntryID != ancestor.ID || child.Hits[0].SourceSessionID != "parent" || child.Hits[0].SessionID != "child" {
		t.Fatalf("child ancestry: %+v", child)
	}
	if hits := search(t, f, SearchRequest{Query: "parent-only", SessionIDs: []string{"child"}}).Hits; len(hits) != 0 {
		t.Fatal("sibling leaked")
	}
	if hits := search(t, f, SearchRequest{Query: "summary-only"}).Hits; len(hits) != 0 {
		t.Fatal("summary entered default search")
	}
	if hits := search(t, f, SearchRequest{Query: "summary-only", Kinds: []string{"summary"}}).Hits; len(hits) != 1 {
		t.Fatal("explicit summary search failed")
	}
	var ref reference
	if err := decode(child.Hits[0].Ref, &ref); err != nil {
		t.Fatal(err)
	}
	ref.EntryID, ref.Start, ref.End = parentOnly.ID, 0, 1
	if _, err := f.recall.Read(f.ctx, "agent", "child", ReadRequest{Around: encode(ref)}); err == nil {
		t.Fatal("forged sibling entry accepted")
	}
	ref.HeadID = parentOnly.ID
	if _, err := f.recall.Read(f.ctx, "agent", "child", ReadRequest{Around: encode(ref)}); err == nil {
		t.Fatal("forged sibling head accepted")
	}
	if _, err := f.recall.Read(f.ctx, "other", "child", ReadRequest{}); err == nil {
		t.Fatal("foreign read accepted")
	}
}

func TestReadAroundRangesNeighborsPaginationAndRebuild(t *testing.T) {
	f := newFixture(t)
	f.session(t, "one", "agent")
	long := strings.Repeat("🙂中文背景", 1500) + "deep-target" + strings.Repeat("结尾", 100)
	entries := f.append(t, "one", message(core.RoleUser, "before"), message(core.RoleAssistant, long), message(core.RoleUser, "after"))
	hit := search(t, f, SearchRequest{Query: "deep-target"}).Hits[0]
	page, err := f.recall.Read(f.ctx, "agent", "one", ReadRequest{Around: hit.Ref, Length: 80})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 3 || page.Items[0].Text != "before" || !strings.Contains(page.Items[1].Text, "deep-target") || page.Items[2].Text != "after" {
		t.Fatalf("wrong context: %+v", page)
	}
	if page.Items[1].Start < 7000 || utf8.RuneCountInString(page.Items[1].Text) > 80 {
		t.Fatal("read restarted at the head of a long entry")
	}
	zero, offset := 0, page.Items[1].End
	continued, err := f.recall.Read(f.ctx, "agent", "one", ReadRequest{Around: hit.Ref, Before: &zero, After: &zero, Offset: &offset, Length: 19})
	if err != nil || len(continued.Items) != 1 || continued.Items[0].Text != string([]rune(long)[offset:offset+19]) {
		t.Fatalf("range continuation: %+v %v", continued, err)
	}
	recent, err := f.recall.Read(f.ctx, "agent", "one", ReadRequest{Limit: 1})
	if err != nil || len(recent.Items) != 1 || recent.Items[0].Text != "after" || recent.OlderCursor == "" || recent.NewerCursor != "" {
		t.Fatalf("recent: %+v %v", recent, err)
	}
	f.append(t, "one", message(core.RoleUser, "future"))
	older, err := f.recall.Read(f.ctx, "agent", "one", ReadRequest{Cursor: recent.OlderCursor, Limit: 1})
	if err != nil || older.Items[0].EntryID != entries[1].ID || older.NewerCursor == "" {
		t.Fatalf("older: %+v %v", older, err)
	}
	newer, err := f.recall.Read(f.ctx, "agent", "one", ReadRequest{Cursor: older.NewerCursor, Limit: 20})
	if err != nil || len(newer.Items) != 1 || newer.Items[0].Text != "after" {
		t.Fatalf("read snapshot drifted: %+v %v", newer, err)
	}
	before, _ := f.core.LoadState(f.ctx, "one")
	if err = f.recall.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(f.store.Directory, "recall.db")); err != nil {
		t.Fatal(err)
	}
	f.recall = New(f.store, f.core)
	rebuilt, err := f.recall.Read(f.ctx, "agent", "one", ReadRequest{Around: hit.Ref, Length: 80})
	if err != nil || !reflect.DeepEqual(page, rebuilt) {
		t.Fatalf("source ref changed after rebuilding: %+v %v", rebuilt, err)
	}
	after, _ := f.core.LoadState(f.ctx, "one")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("recall changed durable execution state")
	}
}

func TestCatalogActivitySnapshotsAndIncrementalIndexing(t *testing.T) {
	f := newFixture(t)
	f.session(t, "older", "agent")
	f.append(t, "older", message(core.RoleUser, "needle old"))
	f.session(t, "newer", "agent")
	f.append(t, "newer", message(core.RoleUser, "needle new"))
	f.session(t, "foreign", "other")
	var old domain.Session
	if err := f.store.Get(f.ctx, "sessions", "older", &old); err != nil {
		t.Fatal(err)
	}
	old.Archived, old.Title = true, "renamed old"
	if err := f.store.Save(f.ctx, "sessions", &old.Meta, &old); err != nil {
		t.Fatal(err)
	}
	page, err := f.recall.List(f.ctx, "agent", ListRequest{Limit: 1})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != "newer" || page.NextCursor == "" {
		t.Fatalf("catalog: %+v %v", page, err)
	}
	f.append(t, "older", message(core.RoleUser, "needle new activity"))
	next, err := f.recall.List(f.ctx, "agent", ListRequest{Cursor: page.NextCursor, Limit: 1})
	if err != nil || len(next.Items) != 1 || next.Items[0].ID != "older" || !next.Items[0].Archived || next.NextCursor != "" {
		t.Fatalf("catalog cursor: %+v %v", next, err)
	}
	latest, err := f.recall.List(f.ctx, "agent", ListRequest{})
	if err != nil || latest.Items[0].ID != "older" {
		t.Fatal("new snapshot missed activity")
	}
	search(t, f, SearchRequest{Query: "needle"})
	stats := f.core.Stats()
	search(t, f, SearchRequest{Query: "needle"})
	reads := f.core.Stats().RowsRead - stats.RowsRead
	if reads > 2 {
		t.Fatalf("unchanged history was reread: %d source rows", reads)
	}
	if _, err = f.recall.List(f.ctx, "other", ListRequest{Cursor: page.NextCursor}); err == nil {
		t.Fatal("cross-Agent catalog cursor accepted")
	}
	if _, err = f.recall.List(f.ctx, "agent", ListRequest{Cursor: page.NextCursor, Source: "im"}); err == nil {
		t.Fatal("cursor filters changed silently")
	}
	if _, err = f.recall.Search(f.ctx, "agent", SearchRequest{Cursor: page.NextCursor}); err == nil {
		t.Fatal("wrong cursor type accepted")
	}
	if _, err = f.recall.db.ExecContext(f.ctx, "UPDATE snapshots SET expires_at=0"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.recall.List(f.ctx, "agent", ListRequest{Cursor: page.NextCursor}); err == nil {
		t.Fatal("expired cursor silently restarted")
	}
}

func TestCatalogDoesNotBuildFullTextIndex(t *testing.T) {
	f := newFixture(t)
	f.session(t, "long", "agent")
	entries := make([]core.Entry, 600)
	for i := range entries {
		entries[i] = message(core.RoleUser, fmt.Sprintf("history %d", i))
	}
	last := f.append(t, "long", entries...)[len(entries)-1]
	env := core.User("environment refresh")
	env.ContextKind = "astral.environment"
	f.append(t, "long", core.Entry{Kind: core.EntryContext, Message: &env})
	stats := f.core.Stats()
	page, err := f.recall.List(f.ctx, "agent", ListRequest{})
	if err != nil || len(page.Items) != 1 || !page.Items[0].LastActivityAt.Equal(last.Time) {
		t.Fatalf("catalog activity: %+v %v", page, err)
	}
	if reads := f.core.Stats().RowsRead - stats.RowsRead; reads > 3 {
		t.Fatalf("catalog read entire history: %d rows", reads)
	}
	var count int
	if err = f.recall.db.QueryRowContext(f.ctx, "SELECT count(*) FROM blocks").Scan(&count); err != nil || count != 0 {
		t.Fatalf("catalog built a text index: %d %v", count, err)
	}
	stats = f.core.Stats()
	if _, err = f.recall.List(f.ctx, "agent", ListRequest{}); err != nil {
		t.Fatal(err)
	}
	if reads := f.core.Stats().RowsRead - stats.RowsRead; reads > 1 {
		t.Fatalf("cached catalog reread source content: %d rows", reads)
	}
}

func TestDetachedCopiesRemainIndependentSearchResults(t *testing.T) {
	f := newFixture(t)
	f.session(t, "source", "agent")
	entry := f.append(t, "source", message(core.RoleUser, "copied needle"))[0]
	state, err := f.core.LoadState(f.ctx, "source")
	if err != nil {
		t.Fatal(err)
	}
	copy, err := f.core.Fork(f.ctx, core.ForkRequest{SourceID: "source", SourceRolloutID: state.RolloutID, TargetID: "copy", HeadID: entry.ID, Mode: core.ForkDetached})
	if err != nil {
		t.Fatal(err)
	}
	record := domain.Session{Meta: domain.Meta{ID: "copy"}, AgentID: "agent", Source: "web", ParentSessionID: "source", ForkEntryID: entry.ID}
	if err = f.store.Save(f.ctx, "sessions", &record.Meta, &record); err != nil {
		t.Fatal(err)
	}
	f.append(t, "source", message(core.RoleAssistant, "source-only needle"))
	f.append(t, "copy", message(core.RoleAssistant, "copy-only needle"))
	all := search(t, f, SearchRequest{Query: "needle"})
	if len(all.Hits) != 4 {
		t.Fatalf("independent copies were merged: %+v", all)
	}
	child := search(t, f, SearchRequest{Query: "copied", SessionIDs: []string{"copy"}})
	if len(child.Hits) != 1 || child.Hits[0].SourceSessionID != "source" {
		t.Fatalf("copy lost provenance: %+v", child)
	}
	var ref reference
	if err = decode(child.Hits[0].Ref, &ref); err != nil || ref.RolloutID != copy.RolloutID {
		t.Fatalf("copy points at source rollout: %+v %v", ref, err)
	}
	if hits := search(t, f, SearchRequest{Query: "source-only", SessionIDs: []string{"copy"}}).Hits; len(hits) != 0 {
		t.Fatal("source's later history leaked into copy")
	}
	if hits := search(t, f, SearchRequest{Query: "copy-only", SessionIDs: []string{"source"}}).Hits; len(hits) != 0 {
		t.Fatal("copy's history leaked into source")
	}
}

func TestToolEvidenceAndContextAreIndexedOnce(t *testing.T) {
	f := newFixture(t)
	f.session(t, "one", "agent")
	user := messages.Web("web", "needle actual request", "owner", time.Now(), nil)
	env := core.User("environment-only needle")
	env.ContextKind = "astral.environment"
	f.append(t, "one", core.Entry{Kind: core.EntryMessage, Message: &user}, core.Entry{Kind: core.EntryContext, Message: &env})
	call := core.ToolCall{ID: "call", Name: "exec_command", Arguments: json.RawMessage(`{"cmd":"needle-command"}`)}
	assistant := core.Message{Role: core.RoleAssistant, Content: []core.Content{{Type: "tool_call", ToolCall: &call}}, FinishReason: core.FinishToolCalls}
	invocation := core.Invocation{SessionID: "one", AssistantID: "assistant", ExecutionID: "execution", Call: call}
	result := &core.ToolResult{Content: core.Text("needle error output"), Outcome: core.OutcomeSucceeded}
	f.append(t, "one", core.Entry{ID: "assistant", Kind: core.EntryMessage, Message: &assistant},
		core.Entry{Kind: core.EntryToolResult, Tool: &core.ToolRecord{Invocation: invocation, Result: result}},
		core.Entry{Kind: core.EntryModelRequest, ModelRequest: &core.ModelRequestRecord{AttemptID: "request", Purpose: "generation", Request: core.Request{ModelID: "model", Messages: []core.Message{user, env, assistant}}}},
	)
	page := search(t, f, SearchRequest{Query: "needle"})
	if len(page.Hits) != 3 {
		t.Fatalf("duplicate request/lifecycle/environment content: %+v", page)
	}
	if hits := search(t, f, SearchRequest{Query: "environment-only"}).Hits; len(hits) != 0 {
		t.Fatal("environment indexed")
	}
	if hits := search(t, f, SearchRequest{Query: "needle", Roles: []string{"tool"}}).Hits; len(hits) != 1 || hits[0].ToolName != "exec_command" {
		t.Fatalf("tool filter: %+v", hits)
	}
}

func TestConcurrentScriptsAndSourceFailures(t *testing.T) {
	f := newFixture(t)
	f.session(t, "one", "agent")
	for i := 0; i < 50; i++ {
		f.append(t, "one", message(core.RoleUser, fmt.Sprintf("needle %d", i)))
	}
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			page, err := f.recall.Search(f.ctx, "agent", SearchRequest{Query: "needle", Limit: 1})
			if err != nil {
				t.Error(err)
				return
			}
			if _, err = f.recall.Read(f.ctx, "agent", "one", ReadRequest{Around: page.Hits[0].Ref}); err != nil {
				t.Error(err)
			}
		}()
	}
	workers.Wait()
	ctx, cancel := context.WithCancel(f.ctx)
	cancel()
	if _, err := f.recall.Search(ctx, "agent", SearchRequest{Query: "needle"}); err == nil {
		t.Fatal("cancelled query returned a misleading result")
	}
}
