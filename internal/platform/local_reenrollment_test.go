package platform

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
)

func TestDeletedLocalComputerReenrollmentRequiresExplicitOwnerAction(t *testing.T) {
	for _, restart := range []bool{false, true} {
		name := "same-Server"
		if restart {
			name = "after-restart"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			root := t.TempDir()
			config := Config{Listen: "127.0.0.1:0", DataDirectory: filepath.Join(root, "server"), ComputerDirectory: filepath.Join(root, "computers")}
			s, err := New(config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close(context.Background()) })
			if err = s.Start(); err != nil {
				t.Fatal(err)
			}
			original, err := s.UseLocal(ctx)
			if err != nil {
				t.Fatal(err)
			}
			bot := domain.Agent{Meta: domain.Meta{ID: domain.ID("agent")}, Name: "Retained Bot", AllowedComputers: []string{original.ID}, DefaultComputerID: original.ID}
			if err = s.Harness.SaveAgent(ctx, &bot); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(original.Root, "retained-work.txt")
			if err = os.WriteFile(marker, []byte("keep workspace"), 0600); err != nil {
				t.Fatal(err)
			}
			if err = s.Hub.Revoke(ctx, original.ID); err != nil {
				t.Fatal(err)
			}
			if _, err = s.UseLocal(ctx); err == nil {
				t.Fatal("UseLocal restored a revoked registration")
			}
			var revoked domain.Computer
			if err = s.Store.Get(ctx, "computers", original.ID, &revoked); err != nil {
				t.Fatal(err)
			}
			if err = s.Harness.DeleteComputer(ctx, original.ID, revoked.Revision); err != nil {
				t.Fatal(err)
			}
			if restart {
				if err = s.Close(ctx); err != nil {
					t.Fatal(err)
				}
				s, err = New(config)
				if err != nil {
					t.Fatal(err)
				}
				if err = s.Start(); err != nil {
					t.Fatal(err)
				}
			} else if _, err = s.useLocal(ctx, false); err == nil {
				t.Fatal("background reconnect recreated a deleted registration")
			}
			items, err := s.Store.List(ctx, "computers")
			if err != nil || len(items) != 0 {
				t.Fatal("Server start recreated deleted Computer:", err)
			}
			replacement, err := s.UseLocal(ctx)
			if err != nil || !replacement.Online || replacement.ID == original.ID || replacement.Root != original.Root {
				t.Fatalf("explicit reconnect did not create a new identity in the same directory: %+v %v", replacement, err)
			}
			if raw, err := os.ReadFile(marker); err != nil || string(raw) != "keep workspace" {
				t.Fatal("re-enrollment changed local workspace:", err)
			}
			if err = s.Store.Get(ctx, "agents", bot.ID, &bot); err != nil || bot.DefaultComputerID != "" || len(bot.AllowedComputers) != 0 {
				t.Fatal("new identity inherited old Bot permissions:", err)
			}
			again, err := s.UseLocal(ctx)
			if err != nil || again.ID != replacement.ID {
				t.Fatal("retry duplicated replacement Computer:", err)
			}
		})
	}
}
