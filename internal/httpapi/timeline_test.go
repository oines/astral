package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/harness"
	"github.com/oines/astral/internal/testkit"
)

func TestTimelineProjectionIncludesCompleteToolDetails(t *testing.T) {
	original := core.Entry{ID: "tool", Kind: core.EntryToolResult, Tool: &core.ToolRecord{Invocation: core.Invocation{Call: core.ToolCall{Name: "exec_command", Arguments: json.RawMessage(`{"cmd":"echo ok","large":"` + strings.Repeat("x", 10000) + `"}`)}}, Result: &core.ToolResult{Outcome: core.OutcomeSucceeded, Content: core.Text(strings.Repeat("output", 10000)), Details: json.RawMessage(`{"output":"large"}`)}}}
	projected := timelineEntry(original)
	if string(projected.Tool.Invocation.Call.Arguments) != string(original.Tool.Invocation.Call.Arguments) || projected.Tool.Result.Content[0].Text != original.Tool.Result.Content[0].Text || string(projected.Tool.Result.Details) != string(original.Tool.Result.Details) {
		t.Fatal("execution details were truncated or omitted")
	}

	if len(original.Tool.Result.Content) == 0 || len(original.Tool.Result.Details) == 0 || !strings.Contains(string(original.Tool.Invocation.Call.Arguments), "large") {
		t.Fatal("presentation changed persisted entry")
	}
}

func TestTimelineProjectionPreservesImageGeometryAndDisplayTextWithoutModelEnvelope(t *testing.T) {
	original := core.Entry{Kind: core.EntryMessage, Message: &core.Message{Role: core.RoleUser, Metadata: json.RawMessage(`{"source":"schedule","display_text":"visible"}`), Content: []core.Content{{Type: "text", Text: "model envelope"}, {Type: "image", Data: json.RawMessage(`"base64"`), Metadata: json.RawMessage(`{"width":1200,"height":800}`)}}}}
	projected := timelineEntry(original)
	if projected.Message.Content[0].Text != "" || len(projected.Message.Content[1].Data) != 0 || string(projected.Message.Content[1].Metadata) != `{"width":1200,"height":800}` {
		t.Fatalf("bad projection: %+v", projected.Message)
	}
	if original.Message.Content[0].Text != "model envelope" || len(original.Message.Content[1].Data) == 0 {
		t.Fatal("original content changed")
	}
	original.Kind = core.EntryContext
	if timelineEntry(original).Message != nil {
		t.Fatal("passive context leaked into timeline transfer")
	}
}

func TestTimelineProjectionRemovesOpaquePayloadWithoutMutatingSource(t *testing.T) {
	opaque := json.RawMessage(`{"provider":"opaque"}`)
	original := core.Entry{Kind: core.EntryToolResult, Tool: &core.ToolRecord{
		Invocation: core.Invocation{Binding: opaque, Call: core.ToolCall{Arguments: json.RawMessage(`{"x":1}`), Metadata: opaque}},
		Result: &core.ToolResult{Content: []core.Content{
			{Type: "text", Text: "complete output", Metadata: opaque},
			{Type: "image", URL: "data:image/png;base64,raw", Data: json.RawMessage(`"raw"`), Metadata: json.RawMessage(`{"width":10,"height":20,"provider":"opaque"}`)},
		}},
	}}
	projected := timelineEntry(original)
	if projected.Tool.Invocation.Binding != nil || projected.Tool.Invocation.Call.Metadata != nil || projected.Tool.Result.Content[0].Metadata != nil || projected.Tool.Result.Content[1].Data != nil || projected.Tool.Result.Content[1].URL != "" {
		t.Fatal("opaque or inline image bytes leaked into timeline")
	}
	if string(projected.Tool.Result.Content[1].Metadata) != `{"width":10,"height":20}` {
		t.Fatal("image projection lost geometry or retained provider metadata")
	}
	if original.Tool.Invocation.Binding == nil || original.Tool.Invocation.Call.Metadata == nil || original.Tool.Result.Content[0].Metadata == nil || original.Tool.Result.Content[1].Data == nil || original.Tool.Result.Content[1].URL == "" {
		t.Fatal("timeline projection mutated the stored record")
	}
	file := timelineEntry(core.Entry{ID: "file", Kind: core.EntryMessage, Message: &core.Message{Role: core.RoleUser, Metadata: json.RawMessage(`{"display_text":"","files":[{"id":"file"}]}`), Content: core.Text("model envelope")}})
	if timelineUnit(file) != "message:file" {
		t.Fatal("file-only input disappeared from the visible timeline budget")
	}
}

// Deliberately raw-record pagination: this catches regressions where hidden
// context records count as visible conversation units.
type timelineFixtureStore struct {
	core.Store
	records []core.Record
	calls   int
}

func (s *timelineFixtureStore) LoadState(context.Context, string) (core.RuntimeState, error) {
	return core.RuntimeState{RolloutID: "rollout", Branch: core.Projection{HeadID: s.records[0].Entry.ID}}, nil
}
func (s *timelineFixtureStore) Scan(_ context.Context, _ string, q core.Query) (core.Page, error) {
	s.calls++
	start := 0
	if q.Cursor != "" {
		sequence, _ := strconv.ParseUint(q.Cursor, 10, 64)
		for start < len(s.records) {
			e := s.records[start].Entry
			if q.PathHead != "" && e.ID == q.Cursor || q.PathHead == "" && e.Sequence < sequence {
				break
			}
			start++
		}
	}
	end := min(start+q.Limit, len(s.records))
	bytes := 0
	for index := start; index < end; index++ {
		raw, _ := json.Marshal(s.records[index])
		if bytes+len(raw) > q.MaxBytes {
			if index == start {
				return core.Page{}, core.ErrBudget
			}
			end = index
			break
		}
		bytes += len(raw)
	}
	page := core.Page{Records: s.records[start:end]}
	if end < len(s.records) {
		if q.PathHead != "" {
			page.Next = s.records[end].Entry.ID
		} else {
			page.Next = strconv.FormatUint(s.records[end-1].Entry.Sequence, 10)
		}
	}
	return page, nil
}
func timelineRecords(n int) []core.Record {
	result := make([]core.Record, n)
	for i := range result {
		seq := n - i
		result[i].Entry = core.Entry{ID: fmt.Sprint(seq), Sequence: uint64(seq), SessionID: "s", Kind: core.EntryContext}
		if seq > 1 {
			result[i].Entry.ParentID = fmt.Sprint(seq - 1)
		}
	}
	return result
}
func TestTimelinePagesCountVisibleContentAndPreserveRawCursor(t *testing.T) {
	store := &timelineFixtureStore{records: timelineRecords(180)}
	for _, index := range []int{0, 80, 160} {
		m := core.User(fmt.Sprint(index))
		store.records[index].Entry.Kind = core.EntryMessage
		store.records[index].Entry.Message = &m
	}
	page, err := timelineHistory(context.Background(), store, "s", "", 2, false)
	if err != nil || len(page.Entries) != 2 || page.NextCursor != "99" || store.calls != 2 {
		t.Fatalf("visible page: entries=%d cursor=%s calls=%d err=%v", len(page.Entries), page.NextCursor, store.calls, err)
	}
	next, err := timelineHistory(context.Background(), store, "s", page.NextCursor, 2, false)
	if err != nil || len(next.Entries) != 1 || next.Entries[0].ID != "20" || next.NextCursor != "" {
		t.Fatalf("continuation: %+v %v", next, err)
	}
}
func TestTimelineInvisibleBacklogIsBoundedAndStillAdvances(t *testing.T) {
	store := &timelineFixtureStore{records: timelineRecords(2000)}
	page, err := timelineHistory(context.Background(), store, "s", "", 60, false)
	if err != nil || len(page.Entries) != 0 || page.NextCursor != "976" || store.calls != 16 {
		t.Fatalf("unbounded scan: %+v calls=%d err=%v", page, store.calls, err)
	}
}

func TestTimelineTraceCursorAndExecutionBudget(t *testing.T) {
	store := &timelineFixtureStore{records: timelineRecords(180)}
	for _, index := range []int{0, 1, 80, 81, 160} {
		entry := &store.records[index].Entry
		entry.Kind = core.EntryToolResult
		entry.Tool = &core.ToolRecord{Invocation: core.Invocation{ExecutionID: fmt.Sprint(index / 80)}}
	}
	page, err := timelineHistory(context.Background(), store, "s", "", 2, true)
	if err != nil || len(page.Entries) != 2 || page.Entries[0].ID != "180" || page.Entries[1].ID != "100" || page.NextCursor != "100" {
		t.Fatalf("trace visible units: %+v %v", page, err)
	}
	next, err := timelineHistory(context.Background(), store, "s", page.NextCursor, 2, true)
	if err != nil || len(next.Entries) != 2 || next.Entries[0].ID != "99" || next.Entries[1].ID != "20" || next.NextCursor != "20" {
		t.Fatalf("trace continuation: %+v %v", next, err)
	}
	last, err := timelineHistory(context.Background(), store, "s", next.NextCursor, 2, true)
	if err != nil || len(last.Entries) != 0 || last.NextCursor != "" {
		t.Fatalf("trace end: %+v %v", last, err)
	}
}
func TestTimelineLargeTextIsNotCutAtByteBoundary(t *testing.T) {
	store := &timelineFixtureStore{records: timelineRecords(3)}
	for i := range store.records {
		m := core.User("small")
		if i == 1 {
			m = core.User(strings.Repeat("x", timelinePageBytes+1))
		}
		store.records[i].Entry.Kind = core.EntryMessage
		store.records[i].Entry.Message = &m
	}
	first, err := timelineHistory(context.Background(), store, "s", "", 60, false)
	if err != nil || len(first.Entries) != 1 || first.NextCursor != "2" {
		t.Fatalf("byte boundary: %+v %v", first, err)
	}
	second, err := timelineHistory(context.Background(), store, "s", first.NextCursor, 60, false)
	if err != nil || len(second.Entries) != 1 || len(second.Entries[0].Message.Content[0].Text) != timelinePageBytes+1 || second.NextCursor != "1" {
		t.Fatalf("large record omitted: entries=%d cursor=%s err=%v", len(second.Entries), second.NextCursor, err)
	}
}
func TestSnapshotWatermarkDoesNotSkipCommitsObservedByLaterView(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("answer") })
	api, _ := apiFixture(t, p)
	session, err := p.Harness.Core(p.Context, p.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	subscription := session.Subscribe(256)
	defer subscription.Close()
	before := session.Snapshot().EventSequence
	if _, err = p.Harness.Submit(p.Context, p.Session.ID, harness.InputRequest{ID: "watermark", Text: "run"}); err != nil {
		t.Fatal(err)
	}
	p.Idle(t, p.Session.ID)
	after := session.Snapshot().EventSequence
	if after <= before {
		t.Fatal("fixture did not commit after snapshot watermark")
	}
	req := httptest.NewRequest("GET", "/snapshot", nil).WithContext(p.Context)
	req.SetPathValue("id", p.Session.ID)
	snapshot, err := api.snapshotAt(req, before)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.View.EventSequence != before {
		t.Fatalf("later View advanced snapshot watermark: %d > %d", snapshot.View.EventSequence, before)
	}
	found := false
	for !found {
		select {
		case event := <-subscription.C:
			if event.Type == core.EventCommitted && event.Sequence > snapshot.View.EventSequence {
				found = true
			}
		default:
			t.Fatal("subscribed commit disappeared")
		}
	}
}

// Only the API reads this wrapper. The already-open Core session retains the
// real Store, so it can commit while both snapshot pages are held at old data.
type heldTimelineStore struct {
	core.Store
	read    chan struct{}
	release <-chan struct{}
}

func (s *heldTimelineStore) Scan(ctx context.Context, id string, query core.Query) (core.Page, error) {
	page, err := s.Store.Scan(ctx, id, query)
	if err != nil {
		return page, err
	}
	select {
	case s.read <- struct{}{}:
	case <-ctx.Done():
		return core.Page{}, ctx.Err()
	}
	select {
	case <-s.release:
		return page, nil
	case <-ctx.Done():
		return core.Page{}, ctx.Err()
	}
}

func TestEventsRetainCommitWhileSnapshotPagesAreBeingRead(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("during snapshot") })
	_, server := apiFixture(t, p)
	// Open Core before replacing the API's Store reference.
	if _, err := p.Harness.Core(p.Context, p.Session.ID); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	held := &heldTimelineStore{Store: p.CoreStore, read: make(chan struct{}, 2), release: release}
	p.Harness.CoreStore = held
	ctx, cancel := context.WithCancel(p.Context)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/v1/sessions/"+p.Session.ID+"/events", nil)
	req.Header.Set("Authorization", "Desktop desktop-test-key")
	type responseResult struct {
		response *http.Response
		err      error
	}
	responses := make(chan responseResult, 1)
	go func() {
		response, err := server.Client().Do(req)
		responses <- responseResult{response, err}
	}()
	for range 2 {
		select {
		case <-held.read:
		case <-ctx.Done():
			t.Fatal("snapshot did not read both timeline pages:", ctx.Err())
		}
	}
	if _, err := p.Harness.Submit(ctx, p.Session.ID, harness.InputRequest{ID: "mid-snapshot", Text: "run during snapshot"}); err != nil {
		t.Fatal(err)
	}
	p.Idle(t, p.Session.ID)
	close(release)
	response := <-responses
	if response.err != nil {
		t.Fatal(response.err)
	}
	defer response.response.Body.Close()
	scanner := bufio.NewScanner(response.response.Body)
	scanner.Buffer(make([]byte, 4096), 8<<20)
	var snapshot Snapshot
	var committed *Event
	kind := ""
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			kind = strings.TrimPrefix(line, "event: ")
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		raw := []byte(strings.TrimPrefix(line, "data: "))
		if kind == "snapshot" {
			if err := json.Unmarshal(raw, &snapshot); err != nil {
				t.Fatal(err)
			}
			continue
		}
		var event Event
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Fatal(err)
		}
		if event.Entry != nil && event.Entry.Message != nil && event.Entry.Message.Role == core.RoleAssistant {
			committed = &event
			break
		}
	}
	if committed == nil || committed.Core == nil || committed.Core.Type != core.EventCommitted || committed.Core.Sequence <= snapshot.View.EventSequence {
		t.Fatalf("snapshot watermark discarded its concurrent commit: snapshot sequence=%d event=%+v scanner=%v", snapshot.View.EventSequence, committed, scanner.Err())
	}
	for _, page := range [][]core.Entry{snapshot.History.Entries, snapshot.Trace.Entries} {
		for _, entry := range page {
			if entry.ID == committed.Entry.ID {
				t.Fatal("fixture failed to hold a page before the concurrent commit")
			}
		}
	}
}

func TestTimelineForkKeepsInheritedHistoryAndCurrentWriterTrace(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("answer") })
	submit := func(id, text string) harness.View {
		t.Helper()
		if _, err := p.Harness.Submit(p.Context, id, harness.InputRequest{ID: domain.ID("input"), Text: text}); err != nil {
			t.Fatal(err)
		}
		return p.Idle(t, id)
	}
	boundary := submit(p.Session.ID, "before fork")
	submit(p.Session.ID, "parent later")
	child, err := p.Harness.Fork(p.Context, p.Session.ID, boundary.HeadID, "child", domain.ID("session"))
	if err != nil {
		t.Fatal(err)
	}
	submit(child.ID, "child later")
	for _, trace := range []bool{false, true} {
		cursor := ""
		texts := map[string]bool{}
		seen := map[string]bool{}
		for {
			page, err := timelineHistory(p.Context, p.CoreStore, child.ID, cursor, 1, trace)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range page.Entries {
				if seen[entry.ID] || trace && entry.SessionID != child.ID {
					t.Fatalf("timeline repeated entry or leaked another writer: trace=%v entry=%+v", trace, entry)
				}
				seen[entry.ID] = true
				if entry.Message != nil && entry.Message.Role == core.RoleUser {
					var metadata struct {
						Text string `json:"display_text"`
					}
					_ = json.Unmarshal(entry.Message.Metadata, &metadata)
					texts[metadata.Text] = true
				}
			}
			if page.NextCursor == "" {
				break
			}
			if page.NextCursor == cursor {
				t.Fatal("timeline cursor did not advance")
			}
			cursor = page.NextCursor
		}
		if !texts["child later"] || texts["parent later"] || texts["before fork"] == trace {
			t.Fatalf("fork scope changed: trace=%v texts=%v", trace, texts)
		}
	}
}
