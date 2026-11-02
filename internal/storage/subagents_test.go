package storage

import (
	"context"
	"strings"
	"testing"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func TestSubagentStorageBelongsToOrdinaryRootAndDeduplicatesRollouts(t *testing.T) {
	s, store := fixture(t, &historyStore{})
	save(t, store, "agents", "bot", domain.Agent{Name: "Bot"})
	for id, record := range map[string]domain.Session{
		"root":          {AgentID: "bot", Title: "Root"},
		"shared-child":  {AgentID: "bot", Title: "Shared child", SubagentParentID: "root"},
		"fresh-child":   {AgentID: "bot", Title: "Fresh child", SubagentParentID: "root"},
		"shared-grand":  {AgentID: "bot", Title: "Shared grandchild", SubagentParentID: "fresh-child"},
		"fresh-grand":   {AgentID: "bot", Title: "Fresh grandchild", SubagentParentID: "shared-child"},
		"ordinary-fork": {AgentID: "bot", Title: "Ordinary Fork", ParentSessionID: "root"},
		"other-root":    {AgentID: "bot", Title: "Other root"},
		"deleted":       {AgentID: "bot", SubagentParentID: "root", Deletion: &domain.SessionDeletion{RolloutID: "deleted-rollout"}},
	} {
		save(t, store, "sessions", id, record)
	}
	s.mu.Lock()
	s.estimate.State = "complete"
	s.cache = map[string]sessionEstimate{
		"root":          {Rollout: "shared", Entries: 100, Extra: 1, State: "complete"},
		"shared-child":  {Rollout: "shared", Entries: 130, Extra: 2, State: "complete"},
		"fresh-child":   {Rollout: "fresh", Entries: 40, Extra: 3, State: "complete"},
		"shared-grand":  {Rollout: "fresh", Entries: 60, Extra: 4, State: "complete"},
		"fresh-grand":   {Rollout: "grand", Entries: 20, Extra: 5, State: "complete"},
		"ordinary-fork": {Rollout: "ordinary", Entries: 70, Extra: 6, State: "complete"},
		"other-root":    {Rollout: "shared", Entries: 130, Extra: 7, State: "complete"},
		"deleted":       {Rollout: "deleted-rollout", Entries: 9000, Extra: 9000, State: "complete"},
	}
	s.mu.Unlock()
	out, err := s.Snapshot(context.Background(), "bot")
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Agents) != 1 || len(out.Agents[0].Sessions) != 3 {
		t.Fatalf("internal Sessions leaked into ordinary rows: %+v", out.Agents)
	}
	rows := map[string]Session{}
	for _, row := range out.Agents[0].Sessions {
		rows[row.SessionID] = row
	}
	// Three Rollouts, five Session-local states/Attempt groups. The largest
	// available shared snapshot is counted once, irrespective of member order.
	if root := rows["root"]; root.LogicalBytes != 225 || root.EstimateState != "complete" {
		t.Fatalf("recursive root estimate: %+v", root)
	}
	if fork := rows["ordinary-fork"]; fork.LogicalBytes != 76 {
		t.Fatalf("ordinary Fork provenance was mistaken for delegation: %+v", fork)
	}
	if other := rows["other-root"]; other.LogicalBytes != 137 {
		t.Fatalf("other ordinary root missing: %+v", other)
	}
	if out.Agents[0].LogicalBytes != 308 {
		t.Fatalf("Bot duplicated shared histories or Session-local extras: %+v", out.Agents[0])
	}
}

func TestChatStorageIncludesAllSubagentRollouts(t *testing.T) {
	s, store := fixture(t, &historyStore{})
	save(t, store, "agents", "bot", domain.Agent{Name: "Bot"})
	save(t, store, "connections", "account", domain.Connection{AgentID: "bot", Name: "Account", Platform: "telegram"})
	save(t, store, "conversations", "chat", domain.Conversation{ConnectionID: "account", Title: "Chat"})
	save(t, store, "sessions", "root", domain.Session{AgentID: "bot", Title: "Root", BindingID: "binding"})
	save(t, store, "sessions", "shared", domain.Session{AgentID: "bot", SubagentParentID: "root"})
	save(t, store, "sessions", "fresh", domain.Session{AgentID: "bot", SubagentParentID: "shared"})
	save(t, store, "sessions", "grand", domain.Session{AgentID: "bot", SubagentParentID: "fresh"})
	save(t, store, "sessions", "other", domain.Session{AgentID: "bot", Title: "Other"})
	save(t, store, "bindings", "binding", domain.ConversationBinding{AgentID: "bot", ConversationID: "chat", SessionID: "root", Enabled: true})
	s.mu.Lock()
	s.estimate.State = "complete"
	s.archives["chat"] = persistence.ArchiveInfo{Bytes: 25}
	s.cache = map[string]sessionEstimate{
		"root":   {Rollout: "r", Entries: 100, Extra: 10, State: "complete"},
		"shared": {Rollout: "r", Entries: 100, Extra: 20, State: "complete"},
		"fresh":  {Rollout: "f", Entries: 40, Extra: 30, State: "complete"},
		"grand":  {Rollout: "g", Entries: 50, Extra: 40, State: "complete"},
		"other":  {Rollout: "o", Entries: 200, Extra: 5, State: "complete"},
	}
	s.mu.Unlock()
	out, err := s.Snapshot(context.Background(), "bot")
	if err != nil {
		t.Fatal(err)
	}
	agent := out.Agents[0]
	if len(agent.Chats) != 1 || len(agent.Sessions) != 1 || agent.Sessions[0].SessionID != "other" {
		t.Fatalf("wrong chat/tree ownership: %+v", agent)
	}
	chat := agent.Chats[0]
	if chat.SessionID != "root" || chat.RolloutBytes != 290 || chat.IMBytes != 25 || chat.LogicalBytes != 315 || chat.EstimateState != "complete" {
		t.Fatalf("chat omitted or duplicated internal work: %+v", chat)
	}
	if agent.LogicalBytes != 520 {
		t.Fatalf("wrong Bot total: %+v", agent)
	}
}

func TestRootStorageReflectsIncompleteDescendantEstimates(t *testing.T) {
	for _, test := range []struct {
		name     string
		child    *sessionEstimate
		state    string
		bytes    int64
		contains string
	}{
		{name: "not yet estimated", state: "pending", bytes: 110},
		{name: "partial child", child: &sessionEstimate{Rollout: "fresh", Entries: 20, Extra: 2, State: "partial", Error: "child history changed"}, state: "partial", bytes: 132, contains: "child history changed"},
		{name: "running child", child: &sessionEstimate{Rollout: "fresh", Entries: 20, Extra: 2, State: "running"}, state: "pending", bytes: 132},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, store := fixture(t, &historyStore{})
			save(t, store, "agents", "bot", domain.Agent{Name: "Bot"})
			save(t, store, "sessions", "root", domain.Session{AgentID: "bot", Title: "Root"})
			save(t, store, "sessions", "child", domain.Session{AgentID: "bot", SubagentParentID: "root"})
			s.mu.Lock()
			s.estimate.State = "complete"
			s.cache["root"] = sessionEstimate{Rollout: "root-rollout", Entries: 100, Extra: 10, State: "complete"}
			if test.child != nil {
				s.cache["child"] = *test.child
			}
			s.mu.Unlock()
			out, err := s.Snapshot(context.Background(), "bot")
			if err != nil {
				t.Fatal(err)
			}
			if len(out.Agents[0].Sessions) != 1 {
				t.Fatalf("incomplete subagent became an ordinary row: %+v", out.Agents)
			}
			root := out.Agents[0].Sessions[0]
			if root.EstimateState != test.state || root.LogicalBytes != test.bytes || test.contains != "" && !strings.Contains(root.Error, test.contains) {
				t.Fatalf("incomplete descendant hidden by root's complete estimate: %+v", root)
			}
		})
	}
}
