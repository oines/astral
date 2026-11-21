package harness

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
)

func connectTemporaryDaemon(t *testing.T, f *fixture, sessionID, agentID string, ttl time.Duration) (domain.Computer, *computers.Daemon, context.CancelFunc, <-chan struct{}) {
	t.Helper()
	serverURL := f.daemons[0].Credentials.ServerURL
	enrolled, err := f.hub.Enroll(f.ctx, serverURL, computers.EnrollmentOptions{
		Temporary:      true,
		OwnerSessionID: sessionID,
		OwnerAgentID:   agentID,
		TTL:            ttl,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !enrolled.Enrollment.Temporary || !enrolled.Enrollment.ComputerAPIDisabled {
		t.Fatalf("temporary enrollment must enforce Temporary and ComputerAPIDisabled: %+v", enrolled.Enrollment)
	}
	if !strings.Contains(enrolled.Command, "--temporary") || !strings.Contains(enrolled.PowerShellCommand, "-Temporary") || enrolled.DirectCommand == "" {
		t.Fatalf("missing temporary installer/direct commands: %+v", enrolled)
	}
	tempRoot := t.TempDir()
	bootstrap, err := computers.NewDaemon(tempRoot, computers.Credentials{ServerURL: serverURL, Temporary: true})
	if err != nil {
		t.Fatal(err)
	}
	creds, err := f.hub.Register(f.ctx, enrolled.Enrollment.ID, computers.EnrollmentRequest{Token: enrolled.Token, Hello: bootstrap.Hello})
	if err != nil {
		t.Fatal(err)
	}
	if !creds.Temporary {
		t.Fatal("registered credentials did not carry Temporary=true")
	}
	creds.ServerURL = serverURL
	tempDaemon, err := computers.NewDaemon(tempRoot, creds)
	if err != nil {
		t.Fatal(err)
	}
	daemonCtx, stopDaemon := context.WithCancel(f.ctx)
	daemonDone := make(chan struct{})
	go func() {
		defer close(daemonDone)
		_ = tempDaemon.Run(daemonCtx, nil)
	}()
	t.Cleanup(func() {
		stopDaemon()
		<-daemonDone
	})
	eventually(t, f.ctx, func() bool {
		var c domain.Computer
		return f.store.Get(f.ctx, "computers", creds.ComputerID, &c) == nil && f.hub.Status(c).Online
	})
	var comp domain.Computer
	if err := f.store.Get(f.ctx, "computers", creds.ComputerID, &comp); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(tempRoot, "credentials.json")); !os.IsNotExist(err) {
		t.Fatal("temporary Daemon must not persist credentials.json")
	}
	return comp, tempDaemon, stopDaemon, daemonDone
}

func TestTemporaryComputerScopeSecurityContextAndCleanup(t *testing.T) {
	f := newFixture(t, 1, func(context.Context, int, map[string]any) any { return textReply("ok") })
	f.hub.DisconnectGrace = 120 * time.Millisecond

	originalComputer := f.session.ComputerID
	otherSession, err := f.service.CreateSession(f.ctx, f.agent.ID, "Other Session")
	if err != nil {
		t.Fatal(err)
	}

	comp, tempDaemon, stopDaemon, daemonDone := connectTemporaryDaemon(t, f, f.session.ID, f.agent.ID, 2*time.Hour)

	// 1. Hard untrusted host boundaries: no local API, cannot enable Astral API, cannot update Daemon, cannot assign to Bot.
	if tempDaemon.Hello.LocalAPIURL != "" {
		t.Fatalf("temporary Daemon started local API: %s", tempDaemon.Hello.LocalAPIURL)
	}
	enableAPI := false
	if _, err := f.hub.UpdateComputer(f.ctx, comp.ID, comp.Revision, nil, &enableAPI); err == nil {
		t.Fatal("temporary Computer allowed enabling Astral API")
	}
	if _, err := f.hub.UpdateDaemon(f.ctx, comp.ID); err == nil {
		t.Fatal("temporary Computer allowed managed Daemon update")
	}
	var agent domain.Agent
	if err := f.store.Get(f.ctx, "agents", f.agent.ID, &agent); err != nil {
		t.Fatal(err)
	}
	agent.AllowedComputers = append(agent.AllowedComputers, comp.ID)
	if err := f.service.SaveAgent(f.ctx, &agent); err == nil {
		t.Fatal("SaveAgent allowed adding a temporary Computer to Bot AllowedComputers")
	}

	// 2. Connecting does NOT auto-switch the Session's current Computer, and appends an environment notice.
	var sess domain.Session
	if err := f.store.Get(f.ctx, "sessions", f.session.ID, &sess); err != nil {
		t.Fatal(err)
	}
	if sess.ComputerID != originalComputer {
		t.Fatalf("temporary Computer unexpectedly auto-switched Session Computer from %s to %s", originalComputer, sess.ComputerID)
	}
	eventually(t, f.ctx, func() bool {
		for _, entry := range environmentEntries(t, f, f.session.ID) {
			if len(entry.Message.Content) > 0 {
				text := strings.TrimSpace(entry.Message.Content[0].Text)
				if strings.Contains(text, comp.ID) && strings.Contains(text, "Temporary Computer") {
					return true
				}
			}
		}
		return false
	})

	// 3. Strict scope: allowed for owning root Session and its subagents, denied for other Session of same Bot.
	if _, err := f.hub.Bind(f.ctx, f.agent.ID, otherSession.ID, comp.ID, ""); err == nil {
		t.Fatal("other Session of the same Bot was allowed to bind the temporary Computer")
	}
	if err := f.service.SelectComputer(f.ctx, f.session.ID, comp.ID, ""); err != nil {
		t.Fatalf("owning Session could not select its temporary Computer: %v", err)
	}
	sub := domain.Session{
		Meta:             domain.Meta{ID: domain.ID("session")},
		AgentID:          f.agent.ID,
		Title:            "Subagent",
		SubagentParentID: f.session.ID,
	}
	if !domain.ComputerAllowedForSession(agent, sub, comp) {
		t.Fatal("subagent of owning root Session was denied access to temporary Computer")
	}
	if _, err := f.hub.Bind(f.ctx, f.agent.ID, f.session.ID, comp.ID, ""); err != nil {
		t.Fatalf("owning Session could not bind temporary Computer: %v", err)
	}

	// 4. Fork does not inherit temporary Computer selection.
	view, err := f.service.View(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	forked, err := f.service.Fork(f.ctx, f.session.ID, view.HeadID, "Forked", domain.ID("session"))
	if err != nil {
		t.Fatal(err)
	}
	if forked.ComputerID == comp.ID {
		t.Fatal("forked Session inherited temporary Computer")
	}

	// 5. Graceful exit (Ctrl+C -> daemon.leave) immediately removes the temporary Computer and clears Session selection.
	stopDaemon()
	<-daemonDone
	eventually(t, f.ctx, func() bool {
		var check domain.Computer
		return domain.IsMissing(f.store.Get(f.ctx, "computers", comp.ID, &check))
	})
	var after domain.Session
	if err := f.store.Get(f.ctx, "sessions", f.session.ID, &after); err != nil {
		t.Fatal(err)
	}
	if after.ComputerID != "" || after.CWD != "" {
		t.Fatalf("Session retained stale temporary Computer pointer after cleanup: %+v", after)
	}
	eventually(t, f.ctx, func() bool {
		for _, entry := range environmentEntries(t, f, f.session.ID) {
			if len(entry.Message.Content) > 0 {
				text := entry.Message.Content[0].Text
				if strings.Contains(text, comp.ID) && strings.Contains(text, "disconnected and was removed") {
					return true
				}
			}
		}
		return false
	})
}

func TestTemporaryComputerSessionLifecycleCleanup(t *testing.T) {
	f := newFixture(t, 1, func(context.Context, int, map[string]any) any { return textReply("ok") })
	f.hub.DisconnectGrace = 150 * time.Millisecond

	// A. Archiving owning Session cleans up its temporary Computer.
	archSession, err := f.service.CreateSession(f.ctx, f.agent.ID, "Archive Session")
	if err != nil {
		t.Fatal(err)
	}
	compArch, _, _, _ := connectTemporaryDaemon(t, f, archSession.ID, f.agent.ID, 2*time.Hour)
	archived := true
	if err := f.service.SaveSessionPresentation(f.ctx, archSession.ID, nil, nil, &archived, nil); err != nil {
		t.Fatal(err)
	}
	eventually(t, f.ctx, func() bool {
		var check domain.Computer
		return domain.IsMissing(f.store.Get(f.ctx, "computers", compArch.ID, &check))
	})

	// B. Deleting an owning Session cleans up any temporary Computer belonging to it.
	delSession, err := f.service.CreateSession(f.ctx, f.agent.ID, "Delete Session")
	if err != nil {
		t.Fatal(err)
	}
	compDel, _, _, _ := connectTemporaryDaemon(t, f, delSession.ID, f.agent.ID, 2*time.Hour)
	var latestDel domain.Session
	if err := f.store.Get(f.ctx, "sessions", delSession.ID, &latestDel); err != nil {
		t.Fatal(err)
	}
	// Simulate an archived Session whose temporary Computer was still present so DeleteSession cleans it up.
	latestDel.Archived = true
	if err := f.store.Save(f.ctx, "sessions", &latestDel.Meta, &latestDel); err != nil {
		t.Fatal(err)
	}
	if err := f.service.DeleteSession(f.ctx, delSession.ID, latestDel.Revision); err != nil {
		t.Fatal(err)
	}
	eventually(t, f.ctx, func() bool {
		var check domain.Computer
		return domain.IsMissing(f.store.Get(f.ctx, "computers", compDel.ID, &check))
	})

	// C. Unplanned disconnect (socket dropped without daemon.leave and without reconnecting) cleans up after DisconnectGrace.
	serverURL := f.daemons[0].Credentials.ServerURL
	enrolled, err := f.hub.Enroll(f.ctx, serverURL, computers.EnrollmentOptions{
		Temporary:      true,
		OwnerSessionID: f.session.ID,
		OwnerAgentID:   f.agent.ID,
		TTL:            2 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	dropRoot := t.TempDir()
	bootstrap, err := computers.NewDaemon(dropRoot, computers.Credentials{ServerURL: serverURL, Temporary: true})
	if err != nil {
		t.Fatal(err)
	}
	creds, err := f.hub.Register(f.ctx, enrolled.Enrollment.ID, computers.EnrollmentRequest{Token: enrolled.Token, Hello: bootstrap.Hello})
	if err != nil {
		t.Fatal(err)
	}
	creds.ServerURL = serverURL
	dropDaemon, err := computers.NewDaemon(dropRoot, creds)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = dropDaemon.Connect(f.ctx) }()
	eventually(t, f.ctx, func() bool {
		var c domain.Computer
		return f.store.Get(f.ctx, "computers", creds.ComputerID, &c) == nil && f.hub.Status(c).Online
	})
	_ = dropDaemon.Close(f.ctx)
	eventually(t, f.ctx, func() bool {
		var check domain.Computer
		return domain.IsMissing(f.store.Get(f.ctx, "computers", creds.ComputerID, &check))
	})
}
