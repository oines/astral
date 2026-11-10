package skills

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func writeFixture(t *testing.T, name, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestPackStableAcrossCopiesAndLocalCaches(t *testing.T) {
	ctx := context.Background()
	source := t.TempDir()
	writeFixture(t, filepath.Join(source, "SKILL.md"), "---\nname: demo\ndescription: A test skill\n---\n")
	writeFixture(t, filepath.Join(source, "scripts", "程序.py"), "print('hello')\n")
	writeFixture(t, filepath.Join(source, "scripts", "__pycache__", "program.pyc"), "generated")
	if err := os.Mkdir(filepath.Join(source, "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	data, err := Pack(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	if err := Unpack(ctx, data, dest); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(dest, "empty")); err != nil || !info.IsDir() {
		t.Fatalf("empty directory was not installed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "scripts", "__pycache__")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cache should have been excluded: %v", err)
	}
	date := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(dest, "scripts", "程序.py"), date, date); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(dest, ".DS_Store"), "local only")
	if other, err := Pack(ctx, dest); err != nil || !bytes.Equal(other, data) {
		t.Fatalf("round-trip archive changed, error %v", err)
	}
	writeFixture(t, filepath.Join(dest, "scripts", "程序.py"), "print('changed')\n")
	if other, err := Pack(ctx, dest); err != nil || bytes.Equal(other, data) {
		t.Fatalf("edited archive did not change, error %v", err)
	}
}

func TestUnpackRejectsUnsafeArchivesBeforeWriting(t *testing.T) {
	cases := map[string][]*tar.Header{
		"escape":    {archiveHeader("../escaped", false, 0)},
		"case":      {archiveHeader("README", false, 0), archiveHeader("Readme", false, 0)},
		"device":    {archiveHeader("COM¹.txt", false, 0)},
		"separator": {archiveHeader(`x\y`, false, 0)},
		"alias":     {{Name: "alias", Typeflag: tar.TypeSymlink, Linkname: "../escaped", Mode: 0600}},
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			var data bytes.Buffer
			writer := tar.NewWriter(&data)
			for _, entry := range entries {
				if err := writer.WriteHeader(entry); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			dest := t.TempDir()
			if err := Unpack(context.Background(), data.Bytes(), dest); err == nil {
				t.Fatal("unsafe archive was accepted")
			}
			if files, err := os.ReadDir(dest); err != nil || len(files) != 0 {
				t.Fatalf("rejected archive changed staging directory: %v, %v", files, err)
			}
		})
	}
}

func fixtureStore(t *testing.T) (*persistence.Store, domain.ExecutionBinding) {
	t.Helper()
	store, err := persistence.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	bot := domain.Agent{Meta: domain.Meta{ID: "bot"}, AllowedComputers: []string{"computer"}}
	computer := domain.Computer{Meta: domain.Meta{ID: "computer"}}
	session := domain.Session{Meta: domain.Meta{ID: "session"}, AgentID: bot.ID, ComputerID: computer.ID}
	err = store.Update(context.Background(), func(tx *persistence.Tx) error {
		if err := tx.Save(context.Background(), "agents", &bot.Meta, &bot); err != nil {
			return err
		}
		if err := tx.Save(context.Background(), "computers", &computer.Meta, &computer); err != nil {
			return err
		}
		return tx.Save(context.Background(), "sessions", &session.Meta, &session)
	})
	if err != nil {
		t.Fatal(err)
	}
	return store, domain.ExecutionBinding{AgentID: bot.ID, ComputerID: computer.ID, SessionID: session.ID}
}

func archiveFixture(t *testing.T, name, body string) []byte {
	t.Helper()
	source := t.TempDir()
	writeFixture(t, filepath.Join(source, "SKILL.md"), "---\nname: "+name+"\ndescription: A test skill\n---\n"+body+"\n")
	data, err := Pack(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func exportFixture(data []byte, base int64) func() ([]byte, int64, error) {
	return func() ([]byte, int64, error) { return data, base, nil }
}

func TestSharedSkillCurrentPublicationSurvivesRestartAndDeletion(t *testing.T) {
	ctx := context.Background()
	store, binding := fixtureStore(t)
	service, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	firstData := archiveFixture(t, "demo", "first")
	v1, err := service.Publish(ctx, binding, "demo", "create", exportFixture(firstData, 0))
	if err != nil || v1.Revision != 1 || v1.Digest != Digest(firstData) {
		t.Fatalf("first publication: %+v, %v", v1, err)
	}
	changedData := archiveFixture(t, "demo", "second")
	v2, err := service.Publish(ctx, binding, "demo", "edit", exportFixture(changedData, v1.Revision))
	if err != nil || v2.Revision != 2 || v2.Digest != Digest(changedData) {
		t.Fatalf("second publication: %+v, %v", v2, err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := persistence.Open(store.Directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	service, err = New(reopened)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := service.Manifest(ctx)
	if err != nil || len(manifest.Shared) != 1 || manifest.Shared[0] != v2 || manifest.Builtin.Digest == "" {
		t.Fatalf("restored manifest: %+v, %v", manifest, err)
	}
	retry, err := service.Publish(ctx, binding, "demo", "create", nil)
	if err != nil || retry != v1 {
		t.Fatalf("retry after newer revision: %+v, %v", retry, err)
	}
	var problem *domain.Error
	if _, err = service.Publish(ctx, binding, "other", "create", nil); !errors.As(err, &problem) || problem.Status != 409 {
		t.Fatalf("receipt accepted an ID previously used for another skill: %v", err)
	}
	assertUnavailable := func(v Version) {
		t.Helper()
		_, content, err := service.Download(ctx, Selector{Kind: Shared, Name: v.Name, Revision: v.Revision, Digest: v.Digest})
		if content != nil {
			_ = content.Close()
		}
		if !domain.IsMissing(err) {
			t.Fatalf("revision %d should be unavailable: %v", v.Revision, err)
		}
	}
	assertUnavailable(v1)
	current, content, err := service.Download(ctx, Selector{Kind: Shared, Name: v2.Name, Revision: v2.Revision})
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(content)
	_ = content.Close()
	if err != nil || current != v2 || !bytes.Equal(data, changedData) {
		t.Fatalf("current download: %+v, %v", current, err)
	}
	staleData := archiveFixture(t, "demo", "stale")
	if _, err = service.Publish(ctx, binding, "demo", "stale", exportFixture(staleData, v1.Revision)); !errors.As(err, &problem) || problem.Status != 409 || !strings.Contains(problem.Error(), "Sync this Computer") {
		t.Fatalf("stale edit must ask the source Computer to sync: %v", err)
	}
	if _, err = service.Delete(ctx, binding, "demo", "create"); !errors.As(err, &problem) || problem.Status != 409 {
		t.Fatalf("delete accepted an ID previously used for publishing: %v", err)
	}
	removed, err := service.Delete(ctx, binding, "demo", "delete")
	if err != nil || removed.Name != "demo" || removed.Revision != 3 || removed.Digest != "" {
		t.Fatalf("deleting shared skill: %+v, %v", removed, err)
	}
	manifest, err = service.Manifest(ctx)
	if err != nil || len(manifest.Shared) != 0 || len(manifest.Removed) != 1 || manifest.Removed[0] != removed {
		t.Fatalf("manifest after deletion: %+v, %v", manifest, err)
	}
	assertUnavailable(v2)
	assertUnavailable(removed)

	if _, err = service.Publish(ctx, binding, "demo", "stale", exportFixture(staleData, v2.Revision)); !errors.As(err, &problem) || problem.Status != 409 {
		t.Fatalf("stale edit must not recreate a deleted skill: %v", err)
	}
	v4, err := service.Publish(ctx, binding, "demo", "recreate", exportFixture(firstData, removed.Revision))
	if err != nil || v4.Revision != 4 || v4.Digest != v1.Digest {
		t.Fatalf("recreating the same name: %+v, %v", v4, err)
	}
	retry, err = service.Delete(ctx, binding, "demo", "delete")
	if err != nil || retry != removed {
		t.Fatalf("replaying the old deletion: %+v, %v", retry, err)
	}
	manifest, err = service.Manifest(ctx)
	if err != nil || len(manifest.Shared) != 1 || manifest.Shared[0] != v4 || len(manifest.Removed) != 0 {
		t.Fatalf("recreated publication missing after old deletion retry: %+v, %v", manifest, err)
	}
}

func TestPublishHonorsExistingComputerAPIAccess(t *testing.T) {
	for _, change := range []string{"disable API", "revoke Computer", "remove Bot access"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			store, binding := fixtureStore(t)
			service, err := New(store)
			if err != nil {
				t.Fatal(err)
			}
			firstData := archiveFixture(t, "demo", "first")
			v1, err := service.Publish(ctx, binding, "demo", "create", exportFixture(firstData, 0))
			if err != nil || v1.Revision != 1 {
				t.Fatalf("publishing through the existing API binding: %+v, %v", v1, err)
			}
			if change == "remove Bot access" {
				var agent domain.Agent
				if err := store.Get(ctx, "agents", binding.AgentID, &agent); err != nil {
					t.Fatal(err)
				}
				agent.AllowedComputers = nil
				if err := store.Save(ctx, "agents", &agent.Meta, &agent); err != nil {
					t.Fatal(err)
				}
			} else {
				var computer domain.Computer
				if err := store.Get(ctx, "computers", binding.ComputerID, &computer); err != nil {
					t.Fatal(err)
				}
				computer.ComputerAPIDisabled = change == "disable API"
				computer.Revoked = change == "revoke Computer"
				if err := store.Save(ctx, "computers", &computer.Meta, &computer); err != nil {
					t.Fatal(err)
				}
			}
			var problem *domain.Error
			if _, err := service.Publish(ctx, binding, "demo", "create", nil); !errors.As(err, &problem) || problem.Status != 403 {
				t.Fatalf("receipt remained readable after %s: %v", change, err)
			}
			if _, err := service.Publish(ctx, binding, "demo", "edit", nil); !errors.As(err, &problem) || problem.Status != 403 {
				t.Fatalf("publication was not denied after %s: %v", change, err)
			}
			if _, err := service.Delete(ctx, binding, "demo", "delete"); !errors.As(err, &problem) || problem.Status != 403 {
				t.Fatalf("deletion was not denied after %s: %v", change, err)
			}
			manifest, err := service.Manifest(ctx)
			if err != nil || len(manifest.Shared) != 1 || manifest.Shared[0] != v1 {
				t.Fatalf("denied publication changed the manifest: %+v, %v", manifest, err)
			}
		})
	}
}

func TestEmbeddedBuiltinDownloadsAtManifestDigest(t *testing.T) {
	ctx := context.Background()
	store, _ := fixtureStore(t)
	service, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := service.Manifest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	version, body, err := service.Download(ctx, Selector{Kind: Builtin, Digest: manifest.Builtin.Digest})
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	archive, err := io.ReadAll(body)
	if err != nil || version != manifest.Builtin || Digest(archive) != version.Digest {
		t.Fatalf("Built-in archive: %+v, %v", version, err)
	}
	dest := t.TempDir()
	if err = Unpack(ctx, archive, dest); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(dest, "skill-creator", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentPublishHonorsReceiptAndBase(t *testing.T) {
	ctx := context.Background()
	store, binding := fixtureStore(t)
	service, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		version Version
		err     error
	}
	publish := func(a, b string, base int64, dataA, dataB []byte) (result, result) {
		start := make(chan struct{})
		results := make(chan result, 2)
		for _, candidate := range []struct {
			id   string
			data []byte
		}{{a, dataA}, {b, dataB}} {
			go func() {
				<-start
				version, err := service.Publish(ctx, binding, "demo", candidate.id, exportFixture(candidate.data, base))
				results <- result{version, err}
			}()
		}
		close(start)
		return <-results, <-results
	}
	firstData := archiveFixture(t, "demo", "first")
	a, b := publish("create", "create", 0, firstData, firstData)
	if a.err != nil || b.err != nil || a.version != b.version || a.version.Revision != 1 {
		t.Fatalf("concurrent retry must reuse revision 1: %+v, %+v", a, b)
	}
	leftData := archiveFixture(t, "demo", "left")
	rightData := archiveFixture(t, "demo", "right")
	a, b = publish("left", "right", 1, leftData, rightData)
	if a.err != nil {
		a, b = b, a
	}
	var problem *domain.Error
	if a.err != nil || a.version.Revision != 2 || !errors.As(b.err, &problem) || problem.Status != 409 {
		t.Fatalf("competing base revision must publish once and conflict once: %+v, %+v", a, b)
	}
}
