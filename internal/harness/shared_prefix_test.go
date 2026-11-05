package harness

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/messages"
	"github.com/oines/astral/internal/persistence"
)

type sharedPrefixTransport func(*http.Request) (*http.Response, error)

func (f sharedPrefixTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestSharedPrefixChildAdmission(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("done") })
	root := subagentTestSeed(t, f)
	child := subagentTestSpawn(t, f, root, "shared-prefix", "Complete this task.", false)
	var record domain.Session
	if err := f.store.Get(f.ctx, "sessions", child.SessionID, &record); err != nil {
		t.Fatal(err)
	}
	if err := f.service.reconcileSubagent(f.ctx, record); err != nil {
		t.Fatal(err)
	}
	subagentTestFinished(t, f, child.SessionID)
	var saved map[string]any
	if err := f.store.Get(f.ctx, "subagent_settings", child.SessionID, &saved); err != nil {
		t.Fatal(err)
	}
	configuration := saved["configuration"].(map[string]any)
	if len(configuration) != 1 || configuration["selection"] == nil || saved["message_tool"] != nil {
		t.Fatal("new child settings still store a copied prefix")
	}
}

func TestSharedPrefixPersonaRefreshesDuringChildRunWithoutChangingFrozenRequest(t *testing.T) {
	const beforePersona = "PERSONA_BEFORE_SHARED_UPDATE"
	const afterPersona = "PERSONA_AFTER_SHARED_UPDATE"
	entered, release := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	f := newFixture(t, 0, func(ctx context.Context, _ int, request map[string]any) any {
		body := string(encode(request))
		if strings.Contains(body, "PERSONA_NEXT_STEP") && strings.Contains(body, "You are a subagent of Session") && !first.Swap(true) {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return nil
			}
			return toolReply("next-persona", "agent_control", map[string]any{})
		}
		return textReply("Finished the delegated task.")
	})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	savePersona(t, f, beforePersona)
	root := subagentTestSeed(t, f)
	child := subagentTestSpawn(t, f, root, "fresh-persona", "PERSONA_NEXT_STEP", false)
	select {
	case <-entered:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	childCore, err := f.service.Core(f.ctx, child.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	page, err := childCore.Attempts().Scan(f.ctx, core.AttemptQuery{Purpose: "generation", Reverse: true, Limit: 1})
	if err != nil || len(page.Attempts) != 1 {
		t.Fatalf("missing frozen child request: %v", err)
	}
	frozen, err := childCore.Attempts().Request(f.ctx, page.Attempts[0].ID)
	if err != nil || !strings.Contains(frozen.SystemPrompt, beforePersona) {
		t.Fatalf("initial child request did not use the shared Persona: %v", err)
	}
	version, requests := childCore.State().Version, f.count()
	savePersona(t, f, afterPersona)
	if childCore.State().Version != version || f.count() != requests {
		t.Fatal("applying Persona wrote child context or woke another model")
	}
	once.Do(func() { close(release) })
	subagentTestFinished(t, f, child.SessionID)
	if _, err := f.service.Submit(f.ctx, root.id, InputRequest{ID: "root-current-persona", Text: "Use the current Persona."}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, root.id)
	latest := subagentTestRequest(t, f, child.SessionID)
	if !strings.Contains(latest.SystemPrompt, afterPersona) || strings.Contains(latest.SystemPrompt, beforePersona) {
		t.Fatal("the next request in the same child Run retained old Persona")
	}
	rootRequest := subagentTestRequest(t, f, root.id)
	if latest.SystemPrompt != rootRequest.SystemPrompt || !reflect.DeepEqual(latest.Tools, rootRequest.Tools) {
		t.Fatal("root and existing child did not use the same current prefix")
	}
	after, err := childCore.Attempts().Request(f.ctx, page.Attempts[0].ID)
	if err != nil || !reflect.DeepEqual(after, frozen) {
		t.Fatal("applying Persona changed the frozen first request")
	}
}

type sharedPrefixMessenger struct{}

func (sharedPrefixMessenger) MessageTool(_ context.Context, _ string, chat string) (core.ToolDefinition, error) {
	definition := DefaultMessageTool()
	definition.Version, definition.Description = "shared-"+chat, "Platform capability for "+chat
	return definition, nil
}
func (sharedPrefixMessenger) SendMessage(context.Context, string, string, string, MessageTarget, MessageArgs) ([]domain.Outbox, error) {
	return nil, errors.New("subagent must never reach the IM sender")
}

func TestSharedPrefixIMVariantChangesAtChildRunBoundaryAndKeepsPermissions(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	f := newFixture(t, 0, func(ctx context.Context, _ int, request map[string]any) any {
		body := string(encode(request))
		if strings.Contains(body, "PIN_CURRENT_IM_VARIANT") && strings.Contains(body, "You are a subagent of Session") && !first.Swap(true) {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return nil
			}
			return toolReply("continue-pinned", "agent_control", map[string]any{})
		}
		return textReply("Completed.")
	})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	f.service.IM = sharedPrefixMessenger{}
	chatA, chatB := promptFixtureChat(t, f, "platform-A"), promptFixtureChat(t, f, "platform-B")
	bindPromptFixture(t, f, chatA)
	root := subagentTestSeed(t, f)
	child := subagentTestSpawn(t, f, root, "shared-im", "PIN_CURRENT_IM_VARIANT", true)
	select {
	case <-entered:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	r, err := f.service.open(f.ctx, child.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	before := subagentTestRequest(t, f, child.SessionID)
	if before.SystemPrompt != subagentTestRequest(t, f, root.id).SystemPrompt || (sendMessageTool{runtime: r}).Definition().Version != "shared-"+chatA.ID {
		t.Fatal("child did not use the root's IM prefix and capability variant")
	}
	if _, err := (sendMessageTool{runtime: r}).Bind(f.ctx, core.Invocation{SessionID: child.SessionID}); err == nil {
		t.Fatal("a shared tool declaration granted the child IM sending permission")
	}
	if _, err := (sendMessageTool{runtime: r}).Execute(f.ctx, core.Invocation{Call: core.ToolCall{Arguments: encode(MessageArgs{Text: "forbidden"})}, Binding: encode(MessageTarget{})}, nil); err == nil {
		t.Fatal("a direct execution bypassed child IM sending permission")
	}
	runID := r.core.Load().State().LastRun.ID
	bindPromptFixture(t, f, chatB)
	if snapshot, err := r.snapshot(); err != nil || !snapshot.Running || snapshot.State.LastRun.ID != runID {
		t.Fatal("root rebind cancelled or restarted the active child")
	}
	once.Do(func() { close(release) })
	subagentTestFinished(t, f, child.SessionID)
	pinned := subagentTestRequest(t, f, child.SessionID)
	if !strings.Contains(pinned.SystemPrompt, `conversation-id: "`+chatA.ID+`"`) || !reflect.DeepEqual(pinned.Tools, before.Tools) {
		t.Fatal("a new request mixed the old tool variant with the root's new chat")
	}
	selected := subagentTreeModel(t, f)
	if err := f.service.selectDirectSubagentModel(f.ctx, root.id, child.SessionID, core.ModelSelection{ID: selected.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.controlSubagent(f.ctx, root.id, "new-shared-im", AgentControlInput{AgentID: child.SessionID, Input: "Use the current shared IM capability variant."}); err != nil {
		t.Fatal(err)
	}
	subagentRecoveryReceipt(t, f, child.SessionID, "subagent_instruction_new-shared-im")
	subagentTestFinished(t, f, child.SessionID)
	current := subagentTestRequest(t, f, child.SessionID)
	if !strings.Contains(current.SystemPrompt, `conversation-id: "`+chatB.ID+`"`) || (sendMessageTool{runtime: r}).Definition().Version != "shared-"+chatB.ID {
		t.Fatal("the child's next Run did not resolve the current root variant")
	}
	resolved, err := f.service.Models.ResolveSelection(f.ctx, core.ModelSelection{ID: selected.ID})
	if err != nil || current.ModelID != resolved.Model.Spec().ID || r.core.Load().Configuration().Selection.ID != selected.ID {
		t.Fatal("refreshing the shared IM variant reverted the child's model choice")
	}
}

func TestSharedPrefixMissingRootDoesNotFallBackToCopiedSettings(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("done") })
	root := subagentTestSeed(t, f)
	child := subagentTestSpawn(t, f, root, "missing-prefix", "Complete this task.", false)
	subagentTestFinished(t, f, child.SessionID)
	r, err := f.service.open(f.ctx, child.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.Update(f.ctx, func(tx *persistence.Tx) error {
		var record domain.Session
		if err := tx.Get(f.ctx, "sessions", root.id, &record); err != nil {
			return err
		}
		return tx.Delete(f.ctx, "sessions", root.id, record.Revision)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.currentSystemPrompt(f.ctx, webSystemPrompt); err == nil {
		t.Fatal("missing root fell back to an independent child prefix")
	}
}

func TestSharedPrefixChildRetryKeepsFrozenPersonaAndNextRunRefreshes(t *testing.T) {
	const oldPersona, newPersona = "FROZEN_CHILD_PERSONA", "FRESH_CHILD_PERSONA"
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var first atomic.Bool
	requests := make(chan string, 4)
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("done") })
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	base := f.service.Models.HTTPClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	f.service.Models.HTTPClient = &http.Client{Transport: sharedPrefixTransport(func(request *http.Request) (*http.Response, error) {
		raw, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		request.Body = io.NopCloser(bytes.NewReader(raw))
		if bytes.Contains(raw, []byte("CHILD_RETRY_PREFIX")) && bytes.Contains(raw, []byte("You are a subagent of Session")) {
			requests <- string(raw)
			if !first.Swap(true) {
				close(entered)
				select {
				case <-release:
				case <-request.Context().Done():
					return nil, request.Context().Err()
				}
				return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"temporary","type":"server_error"}}`)), Request: request}, nil
			}
		}
		return base.RoundTrip(request)
	})}
	savePersona(t, f, oldPersona)
	root := subagentTestSeed(t, f)
	child := subagentTestSpawn(t, f, root, "frozen-retry", "CHILD_RETRY_PREFIX", false)
	select {
	case <-entered:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	savePersona(t, f, newPersona)
	once.Do(func() { close(release) })
	subagentTestFinished(t, f, child.SessionID)
	firstBody, retryBody := <-requests, <-requests
	if firstBody != retryBody || !strings.Contains(retryBody, oldPersona) || strings.Contains(retryBody, newPersona) {
		t.Fatal("a provider retry re-resolved the shared prefix instead of using frozen bytes")
	}
	if _, err := f.service.controlSubagent(f.ctx, root.id, "after-frozen-retry", AgentControlInput{AgentID: child.SessionID, Input: "Use the latest Persona for this next task."}); err != nil {
		t.Fatal(err)
	}
	subagentRecoveryReceipt(t, f, child.SessionID, "subagent_instruction_after-frozen-retry")
	subagentTestFinished(t, f, child.SessionID)
	if request := subagentTestRequest(t, f, child.SessionID); !strings.Contains(request.SystemPrompt, newPersona) || strings.Contains(request.SystemPrompt, oldPersona) {
		t.Fatal("the next Run retained the frozen retry's Persona")
	}
}

func TestSharedPrefixPassiveMutationRepairsOldToolsAfterReadOnlyOpen(t *testing.T) {
	for _, operation := range []string{"deliver", "environment"} {
		t.Run(operation, func(t *testing.T) {
			f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("done") })
			r := subagentTestSeed(t, f)
			legacy := r.core.Load().Configuration()
			for i := range legacy.Tools {
				if legacy.Tools[i].Name == "read" || legacy.Tools[i].Name == "write" || legacy.Tools[i].Name == "edit" {
					legacy.Tools[i].Version = "astral-2/2"
				}
			}
			if err := f.service.Close(f.ctx); err != nil {
				t.Fatal(err)
			}
			state, err := f.coreStore.LoadState(f.ctx, r.id)
			if err != nil {
				t.Fatal(err)
			}
			old := core.Entry{ID: domain.ID("old-config"), Kind: core.EntryConfiguration, ParentID: state.Branch.HeadID, SessionID: r.id, Version: state.Version + 1, Configuration: &legacy}
			if _, err := f.coreStore.Commit(f.ctx, r.id, core.Commit{ID: "old-prefix", ExpectedVersion: state.Version, Entries: []core.Entry{old}}); err != nil {
				t.Fatal(err)
			}
			f.restart(t)
			reopened, err := f.service.open(f.ctx, r.id)
			if err != nil || reopened.core.Load().State().Branch.ConfigurationID != old.ID {
				t.Fatalf("read-only open changed old declarations: %v", err)
			}
			count, run := f.count(), reopened.core.Load().State().LastRun.ID
			mutate := func(id string) {
				t.Helper()
				if operation == "environment" {
					if err := f.service.InitializeSession(f.ctx, r.id); err != nil {
						t.Fatal("old declarations blocked environment refresh:", err)
					}
				} else {
					observation := messages.Observation(domain.Observation{ID: id, MessageID: id, Text: "New passive observation."}, domain.Connection{}, domain.Conversation{Meta: domain.Meta{ID: "source-chat"}}, nil, "")
					if _, err := f.service.Deliver(f.ctx, r.id, observation, nil); err != nil {
						t.Fatal("old declarations blocked passive delivery:", err)
					}
				}
			}
			mutate("first-new-observation")
			configuration := reopened.core.Load().State().Branch.ConfigurationID
			if configuration == old.ID || f.count() != count || reopened.core.Load().State().LastRun.ID != run {
				t.Fatal("passive mutation did not refresh declarations or started model work")
			}
			mutate("second-new-observation")
			if reopened.core.Load().State().Branch.ConfigurationID != configuration {
				t.Fatal("unchanged passive input wrote another Configuration")
			}
		})
	}
}
