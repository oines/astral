package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral-core/store/sqlite"
	"github.com/oines/astral/internal/channels"
	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/harness"
	"github.com/oines/astral/internal/persistence"
	"github.com/oines/astral/internal/testkit"
)

type rolloutFixture struct {
	p       *testkit.Platform
	api     *Service
	binding domain.ExecutionBinding
}

func newRolloutFixture(t *testing.T) *rolloutFixture {
	t.Helper()
	p := testkit.New(t, 1, func(context.Context, int, map[string]any) any { return testkit.Text("MODEL_MUST_NOT_BE_INVOKED") })
	// Every test writes committed Core records directly. Closing the Harness also
	// stops its recovery worker, so it cannot initialize a Host-only child or
	// activate any of the deliberately failed/raw execution records below.
	if err := p.Harness.Close(p.Context); err != nil {
		t.Fatal(err)
	}
	api := New(p.Harness, channels.New(p.Store, p.Harness))
	f := &rolloutFixture{p: p, api: api}
	// testkit's initial Session already has Core state. Use a fresh Host-only
	// root so the exact committed branch and all its identifiers are controlled.
	p.Session = f.hostSession(t, "", false)
	binding, err := p.Hub.Bind(p.Context, p.Agent.ID, p.Session.ID, p.Agent.DefaultComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	f.binding = binding
	f.commit(t, p.Session.ID,
		core.Entry{ID: "rollout-created", Kind: core.EntryCreated, Created: &core.CreationRecord{FormatVersion: core.FormatVersion, SessionID: p.Session.ID, RolloutID: "gateway-rollout"}},
		core.Entry{ID: "rollout-config", Kind: core.EntryConfiguration, Configuration: &core.Configuration{SystemPrompt: "RAW_SYSTEM_PREFIX", Selection: core.ModelSelection{ID: p.Profile.ID}}},
	)
	return f
}

func (f *rolloutFixture) commit(t *testing.T, sessionID string, entries ...core.Entry) core.RuntimeState {
	t.Helper()
	state, err := f.p.CoreStore.LoadState(f.p.Context, sessionID)
	if err != nil && !errors.Is(err, core.ErrNotFound) {
		t.Fatal(err)
	}
	for i := range entries {
		entries[i].SessionID, entries[i].Version = sessionID, state.Version+1
		entries[i].Time = time.Now().UTC()
	}
	if _, err := f.p.CoreStore.Commit(f.p.Context, sessionID, core.Commit{ID: domain.ID("rollout-commit"), ExpectedVersion: state.Version, Entries: entries}); err != nil {
		t.Fatal(err)
	}
	state, err = f.p.CoreStore.LoadState(f.p.Context, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func rolloutMessage(id, parent, text string) core.Entry {
	message := core.User(text)
	return core.Entry{ID: id, ParentID: parent, Kind: core.EntryMessage, Message: &message}
}

func (f *rolloutFixture) hostSession(t *testing.T, parentID string, private bool) domain.Session {
	t.Helper()
	record := domain.Session{Meta: domain.Meta{ID: domain.ID("session")}, AgentID: f.p.Agent.ID, Title: "Raw rollout fixture", ComputerID: f.p.Agent.DefaultComputerID, SubagentParentID: parentID, Private: private}
	if err := f.p.Store.Save(f.p.Context, "sessions", &record.Meta, &record); err != nil {
		t.Fatal(err)
	}
	return record
}

func (f *rolloutFixture) fork(t *testing.T, parentID, head string, private bool) domain.Session {
	t.Helper()
	child := f.hostSession(t, parentID, private)
	state, err := f.p.CoreStore.LoadState(f.p.Context, parentID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.p.CoreStore.Fork(f.p.Context, core.ForkRequest{SourceID: parentID, SourceRolloutID: state.RolloutID, TargetID: child.ID, HeadID: head, Mode: core.ForkShared}); err != nil {
		t.Fatal(err)
	}
	return child
}

func (f *rolloutFixture) read(t *testing.T, binding domain.ExecutionBinding, id string, query url.Values, status int) (HistoryPage, []byte) {
	t.Helper()
	reply, err := f.api.Handle(f.p.Context, computers.GatewayRequest{Binding: binding, Method: http.MethodGet, Path: "/v1/sessions/" + id + "/rollout", Query: query.Encode()})
	if err != nil || reply.Status != status {
		t.Fatalf("rollout %s?%s: status=%d body=%s error=%v, want %d", id, query.Encode(), reply.Status, reply.Body, err, status)
	}
	if !json.Valid(reply.Body) {
		t.Fatalf("rollout returned non-JSON body: %s", reply.Body)
	}
	var page HistoryPage
	if status == http.StatusOK {
		if err := json.Unmarshal(reply.Body, &page); err != nil {
			t.Fatal(err)
		}
	} else if bytes.Contains(reply.Body, []byte("PRIVATE_ROLLOUT_BODY")) {
		t.Fatalf("rejected request leaked a history body: %s", reply.Body)
	}
	return page, reply.Body
}

func rolloutIDs(entries []core.Entry) []string {
	ids := make([]string, len(entries))
	for i, entry := range entries {
		ids[i] = entry.ID
	}
	return ids
}

func TestAgentRolloutReadsRawSharedBranchAndWriterTraceWithoutRuntime(t *testing.T) {
	f := newRolloutFixture(t)
	p := f.p
	ancestor := rolloutMessage("ancestor-message", "rollout-config", "PRIVATE_ROLLOUT_BODY ancestor")
	ancestor.Message.Metadata = testkit.JSON(map[string]string{"raw": "ancestor metadata"})
	ancestor.Message.Content = append(ancestor.Message.Content, core.Content{Type: "image", URL: "astral-file://source/image.png", MediaType: "image/png", Data: json.RawMessage(`"RAW_IMAGE_BYTES"`)})
	f.commit(t, p.Session.ID, ancestor)
	child := f.fork(t, p.Session.ID, ancestor.ID, true)
	sibling := f.fork(t, p.Session.ID, ancestor.ID, false)
	f.commit(t, sibling.ID, rolloutMessage("sibling-message", ancestor.ID, "SIBLING_ONLY_BODY"))
	f.commit(t, p.Session.ID, rolloutMessage("parent-later", ancestor.ID, "PARENT_ONLY_BODY"))
	childState := f.commit(t, child.ID, rolloutMessage("child-message", ancestor.ID, "PRIVATE_ROLLOUT_BODY child"),
		core.Entry{ID: "failed-run", Kind: core.EntryRun, RunID: "failed-run-id", Run: &core.RunRecord{ID: "failed-run-id", Status: "failed", Error: "RAW_PROVIDER_FAILURE_DETAIL"}},
	)
	// A removed/disabled model must not prevent a persisted raw read. These
	// children intentionally have no subagent runtime settings to open either.
	if err := p.Store.Get(p.Context, "models", p.Profile.ID, &p.Profile); err != nil {
		t.Fatal(err)
	}
	p.Profile.Enabled = new(bool)
	if err := p.Store.Save(p.Context, "models", &p.Profile.Meta, &p.Profile); err != nil {
		t.Fatal(err)
	}
	if available, _ := p.Harness.Models.Availability(p.Context, p.Profile.ID); available {
		t.Fatal("fixture model must be unavailable")
	}
	page, raw := f.read(t, f.binding, child.ID, nil, http.StatusOK)
	if page.HeadID != childState.Branch.HeadID || !reflect.DeepEqual(rolloutIDs(page.Entries), []string{"child-message", "ancestor-message", "rollout-config"}) {
		t.Fatalf("raw branch omitted inherited history, changed order, or included another branch: %+v", page)
	}
	if bytes.Contains(raw, []byte("RAW_IMAGE_BYTES")) || bytes.Contains(raw, []byte("SIBLING_ONLY_BODY")) || bytes.Contains(raw, []byte("PARENT_ONLY_BODY")) || bytes.Contains(raw, []byte("RAW_PROVIDER_FAILURE_DETAIL")) {
		t.Fatalf("default rollout leaked media, another branch, or operational trace: %s", raw)
	}
	for _, entry := range page.Entries {
		if !entry.IsHistory() {
			t.Fatalf("non-history entry in branch page: %+v", entry)
		}
		if entry.ID == ancestor.ID && (!bytes.Equal(entry.Message.Metadata, ancestor.Message.Metadata) || entry.Message.Content[1].URL != ancestor.Message.Content[1].URL || len(entry.Message.Content[1].Data) != 0) {
			t.Fatalf("raw inherited message metadata or image identity changed: %+v", entry)
		}
	}
	trace, raw := f.read(t, f.binding, child.ID, url.Values{"trace": {"true"}}, http.StatusOK)
	if len(trace.Entries) < 3 || trace.Entries[0].ID != "failed-run" || trace.Entries[0].Run == nil || trace.Entries[0].Run.Status != "failed" || trace.Entries[0].Run.Error != "RAW_PROVIDER_FAILURE_DETAIL" {
		t.Fatalf("writer trace lost the raw failed Run and error: %+v", trace)
	}
	for _, entry := range trace.Entries {
		if entry.SessionID != child.ID {
			t.Fatalf("writer trace included inherited or sibling records: %s", raw)
		}
	}
	stored, err := p.CoreStore.Get(p.Context, childState.RolloutID, ancestor.ID)
	if err != nil || !bytes.Equal(stored.Entry.Message.Content[1].Data, ancestor.Message.Content[1].Data) {
		t.Fatalf("filtering mutated the stored image: %+v %v", stored, err)
	}
	after, err := p.CoreStore.LoadState(p.Context, child.ID)
	if err != nil || !reflect.DeepEqual(after, childState) || p.Requests.Load() != 0 {
		t.Fatalf("read changed execution state or called a model: requests=%d error=%v", p.Requests.Load(), err)
	}
}

func TestAgentRolloutPrivacyFreshChildrenAndBotScope(t *testing.T) {
	f := newRolloutFixture(t)
	p := f.p
	p.Session.Private = true
	if err := p.Store.Save(p.Context, "sessions", &p.Session.Meta, &p.Session); err != nil {
		t.Fatal(err)
	}
	f.commit(t, p.Session.ID, rolloutMessage("private-ancestor", "rollout-config", "PRIVATE_ROLLOUT_BODY root"))
	child := f.fork(t, p.Session.ID, "private-ancestor", true)
	f.commit(t, child.ID, rolloutMessage("private-child", "private-ancestor", "PRIVATE_ROLLOUT_BODY child"))
	grandchild := f.fork(t, child.ID, "private-child", true)
	childBinding, err := p.Hub.Bind(p.Context, p.Agent.ID, child.ID, p.Agent.DefaultComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	unrelated := f.hostSession(t, "", false)
	unrelatedBinding, err := p.Hub.Bind(p.Context, p.Agent.ID, unrelated.ID, p.Agent.DefaultComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	f.read(t, f.binding, child.ID, nil, http.StatusOK)
	f.read(t, childBinding, child.ID, nil, http.StatusOK)
	f.read(t, f.binding, unrelated.ID, nil, http.StatusOK)
	f.read(t, unrelatedBinding, child.ID, nil, http.StatusNotFound)
	f.read(t, f.binding, grandchild.ID, nil, http.StatusOK)
	f.read(t, childBinding, grandchild.ID, nil, http.StatusOK)
	independentGrandchild := f.hostSession(t, child.ID, true)
	f.commit(t, independentGrandchild.ID,
		core.Entry{ID: "independent-created", Kind: core.EntryCreated, Created: &core.CreationRecord{FormatVersion: core.FormatVersion, SessionID: independentGrandchild.ID, RolloutID: "independent-grandchild-rollout"}},
		core.Entry{ID: "independent-config", Kind: core.EntryConfiguration, Configuration: &core.Configuration{Selection: core.ModelSelection{ID: p.Profile.ID}}},
		rolloutMessage("independent-message", "independent-config", "PRIVATE_ROLLOUT_BODY independent grandchild"),
	)
	f.read(t, f.binding, independentGrandchild.ID, nil, http.StatusOK)
	f.read(t, unrelatedBinding, independentGrandchild.ID, nil, http.StatusNotFound)
	fresh := f.hostSession(t, p.Session.ID, true)
	page, raw := f.read(t, f.binding, fresh.ID, nil, http.StatusOK)
	if page.Entries == nil || len(page.Entries) != 0 || page.HeadID != "" || page.NextCursor != "" {
		t.Fatalf("uninitialized Host child must return an empty rollout: %s", raw)
	}
	f.read(t, f.binding, fresh.ID, url.Values{"trace": {"true"}}, http.StatusOK)
	if _, err := p.CoreStore.LoadState(p.Context, fresh.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("reading fresh child initialized Core state: %v", err)
	}
	foreignAgent := p.Agent
	foreignAgent.Meta = domain.Meta{ID: domain.ID("agent")}
	if err := p.Store.Save(p.Context, "agents", &foreignAgent.Meta, &foreignAgent); err != nil {
		t.Fatal(err)
	}
	foreign := f.hostSession(t, "", false)
	foreign.AgentID = foreignAgent.ID
	if err := p.Store.Save(p.Context, "sessions", &foreign.Meta, &foreign); err != nil {
		t.Fatal(err)
	}
	f.read(t, f.binding, foreign.ID, nil, http.StatusForbidden)
	f.read(t, f.binding, "missing-session", nil, http.StatusNotFound)
	if p.Requests.Load() != 0 {
		t.Fatal("authorization or empty history read invoked a model")
	}
}

func TestAgentRolloutCursorIsScopedStableAndSurvivesGatewayRestart(t *testing.T) {
	f := newRolloutFixture(t)
	p := f.p
	f.commit(t, p.Session.ID, rolloutMessage("cursor-ancestor", "rollout-config", "PRIVATE_ROLLOUT_BODY cursor ancestor"))
	child := f.fork(t, p.Session.ID, "cursor-ancestor", true)
	head := "cursor-ancestor"
	for i := range 7 {
		id := fmt.Sprintf("cursor-child-%d", i)
		f.commit(t, child.ID, rolloutMessage(id, head, "PRIVATE_ROLLOUT_BODY "+id))
		head = id
	}
	f.commit(t, child.ID, core.Entry{ID: "cursor-failed", Kind: core.EntryRun, RunID: "cursor-run", Run: &core.RunRecord{ID: "cursor-run", Status: "failed", Error: "earlier failure"}})
	sibling := f.fork(t, p.Session.ID, "cursor-ancestor", false)
	f.commit(t, sibling.ID, rolloutMessage("cursor-sibling", "cursor-ancestor", "SIBLING_ONLY_BODY"))
	before, _ := f.read(t, f.binding, child.ID, url.Values{"limit": {"200"}}, http.StatusOK)
	beforeTrace, _ := f.read(t, f.binding, child.ID, url.Values{"trace": {"true"}, "limit": {"200"}}, http.StatusOK)
	first, _ := f.read(t, f.binding, child.ID, url.Values{"limit": {"2"}}, http.StatusOK)
	firstTrace, _ := f.read(t, f.binding, child.ID, url.Values{"trace": {"true"}, "limit": {"2"}}, http.StatusOK)
	if len(first.Entries) != 2 || first.NextCursor == "" || len(firstTrace.Entries) != 2 || firstTrace.NextCursor == "" {
		t.Fatalf("fixture did not obtain both opaque page cursors: branch=%+v trace=%+v", first, firstTrace)
	}
	f.read(t, f.binding, sibling.ID, url.Values{"cursor": {first.NextCursor}}, http.StatusBadRequest)
	f.read(t, f.binding, child.ID, url.Values{"cursor": {first.NextCursor}, "trace": {"true"}}, http.StatusBadRequest)
	f.read(t, f.binding, child.ID, url.Values{"cursor": {firstTrace.NextCursor}}, http.StatusBadRequest)
	forged := []byte(first.NextCursor)
	forged[len(forged)/2] ^= 1
	f.read(t, f.binding, child.ID, url.Values{"cursor": {string(forged)}}, http.StatusBadRequest)
	f.read(t, f.binding, child.ID, url.Values{"cursor": {"not-a-signed-cursor"}}, http.StatusBadRequest)
	unrelated := f.hostSession(t, "", false)
	unrelatedBinding, err := p.Hub.Bind(p.Context, p.Agent.ID, unrelated.ID, p.Agent.DefaultComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	f.read(t, unrelatedBinding, child.ID, url.Values{"cursor": {first.NextCursor}}, http.StatusNotFound)
	// Capture both modes before appending. Neither a new history node nor a new
	// operational Run record may enter continuation pages of the older snapshot.
	f.commit(t, child.ID, rolloutMessage("cursor-after-capture", head, "NEW_HISTORY_BODY"),
		core.Entry{ID: "cursor-run-after-capture", Kind: core.EntryRun, RunID: "new-run", Run: &core.RunRecord{ID: "new-run", Status: "failed", Error: "NEW_OPERATION_BODY"}},
	)
	// New Store handles and a new Gateway simulate restart without borrowing an
	// in-memory cursor key or opening any execution runtime.
	reopened, err := persistence.Open(filepath.Join(p.Directory, "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	coreReopened, err := sqlite.Open(filepath.Join(p.Directory, "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer coreReopened.Close()
	restarted := harness.New(reopened, coreReopened, p.Harness.Models, p.Hub)
	defer restarted.Close(p.Context)
	if err := restarted.Close(p.Context); err != nil {
		t.Fatal(err)
	}
	f.api = New(restarted, channels.New(reopened, restarted))
	collect := func(first HistoryPage, trace bool) []string {
		t.Helper()
		ids := rolloutIDs(first.Entries)
		cursor := first.NextCursor
		for pages := 0; cursor != ""; pages++ {
			if pages > 20 {
				t.Fatal("rollout cursor did not terminate")
			}
			query := url.Values{"cursor": {cursor}, "limit": {"3"}}
			if trace {
				query.Set("trace", "true")
			}
			page, _ := f.read(t, f.binding, child.ID, query, http.StatusOK)
			if page.HeadID != first.HeadID {
				t.Fatalf("continuation changed captured head: got %q, want %q", page.HeadID, first.HeadID)
			}
			ids = append(ids, rolloutIDs(page.Entries)...)
			cursor = page.NextCursor
		}
		return ids
	}
	if ids := collect(first, false); !reflect.DeepEqual(ids, rolloutIDs(before.Entries)) {
		t.Fatalf("branch cursor changed snapshot/order after growth and restart: got %v want %v", ids, rolloutIDs(before.Entries))
	}
	if ids := collect(firstTrace, true); !reflect.DeepEqual(ids, rolloutIDs(beforeTrace.Entries)) {
		t.Fatalf("trace cursor changed snapshot/order after growth and restart: got %v want %v", ids, rolloutIDs(beforeTrace.Entries))
	}
	current, _ := f.read(t, f.binding, child.ID, nil, http.StatusOK)
	currentTrace, _ := f.read(t, f.binding, child.ID, url.Values{"trace": {"true"}}, http.StatusOK)
	if current.HeadID != "cursor-after-capture" || !slices.Contains(rolloutIDs(currentTrace.Entries), "cursor-run-after-capture") {
		t.Fatal("fresh query did not observe newly committed records")
	}
	if p.Requests.Load() != 0 {
		t.Fatal("pagination or restart inspection called a model")
	}
}

func TestAgentRolloutPaginationLimits(t *testing.T) {
	f := newRolloutFixture(t)
	head := "rollout-config"
	for i := range 60 {
		id := fmt.Sprintf("limit-message-%d", i)
		f.commit(t, f.p.Session.ID, rolloutMessage(id, head, id))
		head = id
	}
	page, _ := f.read(t, f.binding, f.p.Session.ID, nil, http.StatusOK)
	if len(page.Entries) != 50 || page.NextCursor == "" {
		t.Fatalf("default limit must be 50: entries=%d cursor=%q", len(page.Entries), page.NextCursor)
	}
	page, _ = f.read(t, f.binding, f.p.Session.ID, url.Values{"limit": {"1"}}, http.StatusOK)
	if len(page.Entries) != 1 || page.NextCursor == "" {
		t.Fatal("minimum page limit was not applied")
	}
	page, _ = f.read(t, f.binding, f.p.Session.ID, url.Values{"limit": {"200"}}, http.StatusOK)
	if len(page.Entries) != 61 || page.NextCursor != "" {
		t.Fatalf("upper page limit must allow the complete history: entries=%d cursor=%q", len(page.Entries), page.NextCursor)
	}
	for _, limit := range []string{"0", "201", "-1", "bad"} {
		f.read(t, f.binding, f.p.Session.ID, url.Values{"limit": {limit}}, http.StatusBadRequest)
	}
	for _, query := range []url.Values{{"limit": {"1", "2"}}, {"unknown": {"value"}}, {"trace": {"1"}}, {"trace": {"false", "true"}}} {
		f.read(t, f.binding, f.p.Session.ID, query, http.StatusBadRequest)
	}
}
