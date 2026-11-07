package computers_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
)

func TestDirectoryRPCPagesSortedLiveEntriesFromFileOrDirectory(t *testing.T) {
	s := fixture(t, 2)
	b := s.bind(t, 0)
	root := filepath.Join(b.CWD, "reports with spaces")
	for _, name := range []string{"z-directory", "A-directory"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"z.txt", ".hidden", "B.txt", "a.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("file content"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var names []string
	path, cursor := filepath.Join(root, "z.txt"), ""
	for {
		page, err := s.hub.ListDirectory(s.ctx, b, path, cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		if page.ComputerID != b.ComputerID || page.Path != root || page.Parent != filepath.Dir(root) || len(page.Entries) > 2 {
			t.Fatalf("wrong directory source/page: %+v", page)
		}
		for _, entry := range page.Entries {
			if entry.Path != filepath.Join(root, entry.Name) || entry.ModifiedAt.IsZero() || !entry.IsDir && entry.Size != int64(len("file content")) {
				t.Fatalf("wrong entry metadata: %+v", entry)
			}
			names = append(names, entry.Name)
		}
		if page.NextCursor == "" {
			break
		}
		if page.NextCursor == cursor {
			t.Fatal("cursor did not advance")
		}
		path, cursor = root, page.NextCursor
	}
	if want := []string{"A-directory", "z-directory", ".hidden", "B.txt", "a.txt", "z.txt"}; !slices.Equal(names, want) {
		t.Fatalf("directory order/pages: %v, want %v", names, want)
	}
	page, err := s.hub.ListDirectory(s.ctx, b, "reports with spaces", "", 2)
	if err != nil || page.Path != root || page.NextCursor == "" {
		t.Fatalf("target-OS relative path failed: %+v, %v", page, err)
	}
	if _, err = s.hub.ListDirectory(s.ctx, b, b.CWD, page.NextCursor, 2); err == nil {
		t.Fatal("accepted cursor from another directory")
	}
	for _, limit := range []int{-1, computers.MaxDirectoryLimit + 1} {
		if _, err = s.hub.ListDirectory(s.ctx, b, root, "", limit); err == nil {
			t.Fatalf("accepted invalid limit %d", limit)
		}
	}
	if _, err = s.hub.ListDirectory(s.ctx, b, root, "invalid-cursor", 2); err == nil {
		t.Fatal("accepted malformed cursor")
	}
	page, err = s.hub.ListDirectory(s.ctx, b, "", "", 0)
	if err != nil || page.Path != b.CWD {
		t.Fatalf("empty path did not use bound CWD: %+v, %v", page, err)
	}
	page, err = s.hub.ListDirectory(s.ctx, b, filepath.Join(root, "A-directory"), "", 0)
	if err != nil || page.Entries == nil || len(page.Entries) != 0 || page.NextCursor != "" {
		t.Fatalf("empty directory was not an empty page: %+v, %v", page, err)
	}
	var stored domain.Session
	if err = s.store.Get(s.ctx, "sessions", s.session.ID, &stored); err != nil || stored.ComputerID != b.ComputerID || stored.CWD != s.session.CWD {
		t.Fatalf("directory browsing changed Session selection: %+v, %v", stored, err)
	}
	other := s.bind(t, 1)
	page, err = s.hub.ListDirectory(s.ctx, other, "", "", 1)
	if err != nil || page.ComputerID != other.ComputerID || page.Path != other.AgentHome {
		t.Fatalf("explicit other Computer was not independently bound: %+v, %v", page, err)
	}
}

func TestDirectoryRPCFollowsDirectorySymlinksWithoutRewritingTheirPaths(t *testing.T) {
	s := fixture(t, 1)
	b := s.bind(t, 0)
	root := filepath.Join(b.CWD, "links")
	target := filepath.Join(b.CWD, "target")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(target, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "linked-directory")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(target, "inside.txt"), []byte("linked"), 0600); err != nil {
		t.Fatal(err)
	}
	page, err := s.hub.ListDirectory(s.ctx, b, root, "", 1)
	if err != nil || len(page.Entries) != 1 || !page.Entries[0].IsDir || page.Entries[0].Path != link {
		t.Fatalf("directory symlink metadata: %+v, %v", page, err)
	}
	page, err = s.hub.ListDirectory(s.ctx, b, link, "", 1)
	if err != nil || page.Path != link || len(page.Entries) != 1 || page.Entries[0].Path != filepath.Join(link, "inside.txt") {
		t.Fatalf("directory symlink navigation: %+v, %v", page, err)
	}
}

func TestDirectoryRPCRejectsStaleUnauthorizedAndUnsupportedComputers(t *testing.T) {
	s := fixture(t, 1)
	b := s.bind(t, 0)
	forged := b
	forged.AgentID = "another-bot"
	if _, err := s.hub.ListDirectory(s.ctx, forged, "", "", 1); err == nil {
		t.Fatal("accepted forged execution binding")
	}
	replacement, err := computers.NewDaemon(t.TempDir(), s.daemons[0].Credentials)
	if err != nil {
		t.Fatal(err)
	}
	replacement.Hello.Capabilities = nil
	s.daemons = append(s.daemons, replacement)
	go func() { _ = replacement.Connect(s.ctx) }()
	waitOnline(t, s, b.ComputerID, replacement.Hello.BootID)
	if _, err = s.hub.ListDirectory(s.ctx, b, "", "", 1); err == nil {
		t.Fatal("old boot silently rebound")
	}
	b = s.bind(t, 0)
	_, err = s.hub.ListDirectory(s.ctx, b, "", "", 1)
	var unsupported *domain.Error
	if !errors.As(err, &unsupported) || unsupported.Status != 501 || unsupported.Code != "unsupported" {
		t.Fatalf("old daemon did not report unsupported: %v", err)
	}
	s.agent.AllowedComputers = nil
	if err = s.store.Save(s.ctx, "agents", &s.agent.Meta, &s.agent); err != nil {
		t.Fatal(err)
	}
	_, err = s.hub.ListDirectory(s.ctx, b, "", "", 1)
	var forbidden *domain.Error
	if !errors.As(err, &forbidden) || forbidden.Status != 403 {
		t.Fatalf("directory browsing ignored revoked Bot permission: %v", err)
	}
}
