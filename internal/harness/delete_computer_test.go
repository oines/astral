package harness

import (
	"context"
	"slices"
	"testing"

	"github.com/oines/astral/internal/domain"
)

func TestDeleteRevokedComputerRemovesAccessAndRetainsHistoricalIdentity(t *testing.T) {
	f := newFixture(t, 2, func(context.Context, int, map[string]any) any { return textReply("unused") })
	id := f.daemons[0].Credentials.ComputerID
	remaining := f.daemons[1].Credentials.ComputerID
	var computer domain.Computer
	if err := f.store.Get(f.ctx, "computers", id, &computer); err != nil {
		t.Fatal(err)
	}
	credential := computer.TokenID
	other := domain.Agent{Meta: domain.Meta{ID: domain.ID("agent")}, Name: "Other Bot", AllowedComputers: []string{id, remaining}, DefaultComputerID: remaining}
	if err := f.service.SaveAgent(f.ctx, &other); err != nil {
		t.Fatal(err)
	}
	file := domain.FileRef{Meta: domain.Meta{ID: domain.ID("file")}, AgentID: f.agent.ID, ComputerID: id, Path: "/retained/work.txt", DisplayName: "work.txt"}
	if err := f.store.Save(f.ctx, "files", &file.Meta, &file); err != nil {
		t.Fatal(err)
	}
	var session domain.Session
	if err := f.store.Get(f.ctx, "sessions", f.session.ID, &session); err != nil {
		t.Fatal(err)
	}
	state, err := f.coreStore.LoadState(f.ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.hub.Revoke(f.ctx, id); err != nil {
		t.Fatal(err)
	}
	if err = f.store.Get(f.ctx, "computers", id, &computer); err != nil {
		t.Fatal(err)
	}
	if err = f.service.DeleteComputer(f.ctx, id, computer.Revision); err != nil {
		t.Fatal(err)
	}
	if err = f.service.DeleteComputer(f.ctx, id, computer.Revision); err != nil {
		t.Fatal("retry:", err)
	}
	if err = f.store.Get(f.ctx, "computers", id, &computer); !domain.IsMissing(err) {
		t.Fatal("Computer registration retained:", err)
	}
	if err = f.store.Version(f.ctx, "computers", id, 1, &computer); !domain.IsMissing(err) {
		t.Fatal("Computer revision retained:", err)
	}
	if _, err = f.store.Secret(f.ctx, credential); err == nil {
		t.Fatal("deleted Computer still has a Server credential")
	}
	var updated domain.Agent
	if err = f.store.Get(f.ctx, "agents", f.agent.ID, &updated); err != nil || updated.DefaultComputerID != "" || !slices.Equal(updated.AllowedComputers, []string{remaining}) {
		t.Fatalf("default/access cleanup: %+v %v", updated, err)
	}
	if err = f.store.Get(f.ctx, "agents", other.ID, &updated); err != nil || updated.DefaultComputerID != remaining || !slices.Equal(updated.AllowedComputers, []string{remaining}) {
		t.Fatalf("other Bot lost its independent default: %+v %v", updated, err)
	}
	var retainedSession domain.Session
	if err = f.store.Get(f.ctx, "sessions", session.ID, &retainedSession); err != nil || retainedSession.ComputerID != id || retainedSession.Revision != session.Revision {
		t.Fatalf("Session historical identity changed: %+v %v", retainedSession, err)
	}
	retainedState, err := f.coreStore.LoadState(f.ctx, session.ID)
	if err != nil || retainedState.RolloutID != state.RolloutID || retainedState.Branch.HeadID != state.Branch.HeadID {
		t.Fatal("Computer deletion changed Core history:", err)
	}
	var retainedFile domain.FileRef
	if err = f.store.Get(f.ctx, "files", file.ID, &retainedFile); err != nil || retainedFile.ComputerID != id || retainedFile.Path != file.Path || retainedFile.Revision != file.Revision {
		t.Fatalf("FileRef identity changed: %+v %v", retainedFile, err)
	}
	if _, err = f.hub.Bind(f.ctx, f.agent.ID, session.ID, id, ""); err == nil {
		t.Fatal("deleted Computer remained usable")
	}
}

func TestDeleteComputerRequiresRevocationAndCurrentRevision(t *testing.T) {
	f := newFixture(t, 1, func(context.Context, int, map[string]any) any { return textReply("unused") })
	id := f.daemons[0].Credentials.ComputerID
	var computer domain.Computer
	if err := f.store.Get(f.ctx, "computers", id, &computer); err != nil {
		t.Fatal(err)
	}
	stale := computer.Revision
	if err := f.service.DeleteComputer(f.ctx, id, computer.Revision); err == nil {
		t.Fatal("connected Computer deleted without revocation")
	}
	if err := f.hub.Revoke(f.ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := f.service.DeleteComputer(f.ctx, id, stale); err == nil {
		t.Fatal("stale confirmation deleted Computer")
	}
	var agent domain.Agent
	if err := f.store.Get(f.ctx, "agents", f.agent.ID, &agent); err != nil || agent.DefaultComputerID != id || !slices.Contains(agent.AllowedComputers, id) {
		t.Fatal("rejected deletion changed Bot permissions:", err)
	}
	if err := f.store.Get(f.ctx, "computers", id, &computer); err != nil {
		t.Fatal("rejected deletion removed Computer:", err)
	}
	if _, err := f.store.Secret(f.ctx, computer.TokenID); err != nil {
		t.Fatal("rejected deletion removed credential:", err)
	}
}
