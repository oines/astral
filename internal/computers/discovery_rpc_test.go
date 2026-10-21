package computers_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/oines/astral/internal/computers"
)

func TestDiscoveryRPCPinsAuthorizationAndDaemonBoot(t *testing.T) {
	s := fixture(t, 1)
	binding := s.bind(t, 0)
	if err := os.WriteFile(filepath.Join(binding.CWD, "AGENTS.md"), []byte("from the selected Computer"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := s.hub.Discover(s.ctx, binding)
	if err != nil || result.Status != "ok" || result.AgentsMD.Content != "from the selected Computer" || result.ComputerID != binding.ComputerID {
		t.Fatalf("discovery RPC: %+v, %v", result, err)
	}
	forged := binding
	forged.AgentID = "forged-agent"
	if _, err = s.hub.Discover(s.ctx, forged); err == nil {
		t.Fatal("discovery accepted a forged authorization")
	}
	replacement, err := computers.NewDaemon(t.TempDir(), s.daemons[0].Credentials)
	if err != nil {
		t.Fatal(err)
	}
	replacement.Hello.Home = t.TempDir()
	replacement.Hello.Capabilities = nil // An older daemon has no discovery RPC.
	s.daemons = append(s.daemons, replacement)
	go func() { _ = replacement.Connect(s.ctx) }()
	waitOnline(t, s, binding.ComputerID, replacement.Hello.BootID)
	if _, err = s.hub.Discover(s.ctx, binding); err == nil {
		t.Fatal("discovery silently rebound a stale boot")
	}
	binding = s.bind(t, 0)
	result, err = s.hub.Discover(s.ctx, binding)
	if err != nil || result.Status != "unsupported" || len(result.Skills) != 0 || result.AgentsMD.Content != "" {
		t.Fatalf("old daemon did not report unsupported: %+v, %v", result, err)
	}
	s.agent.AllowedComputers = nil
	if err = s.store.Save(s.ctx, "agents", &s.agent.Meta, &s.agent); err != nil {
		t.Fatal(err)
	}
	if _, err = s.hub.Discover(s.ctx, binding); err == nil {
		t.Fatal("discovery ignored revoked Agent access")
	}
}
