package gateway_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oines/astral/internal/channels"
	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/gateway"
	"github.com/oines/astral/internal/skills"
	"github.com/oines/astral/internal/testkit"
)

func TestManagedSkillsPublishAndSyncAcrossComputers(t *testing.T) {
	p := testkit.NewWithHub(t, 3, func(context.Context, int, map[string]any) any {
		return testkit.Text("unused")
	}, func(p *testkit.Platform) {
		var err error
		p.Hub.Skills, err = skills.New(p.Store)
		if err != nil {
			t.Fatal(err)
		}
	})
	ctx, cancel := context.WithTimeout(p.Context, 20*time.Second)
	defer cancel()
	c := channels.New(p.Store, p.Harness)
	p.Hub.Gateway = gateway.New(p.Harness, c).Handle

	call := func(t *testing.T, client *http.Client, method, url string, headers http.Header, want int) []byte {
		t.Helper()
		request, err := http.NewRequestWithContext(ctx, method, url, nil)
		if err != nil {
			t.Fatal(err)
		}
		if headers != nil {
			request.Header = headers.Clone()
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d; %s (%v)", method, url, response.StatusCode, want, raw, err)
		}
		return raw
	}

	var bindings []domain.ExecutionBinding
	var clients []*http.Client
	for i, d := range p.Daemons {
		b, err := p.Hub.Bind(ctx, p.Agent.ID, p.Session.ID, d.Credentials.ComputerID, "")
		if err != nil {
			t.Fatal(err)
		}
		bindings = append(bindings, b)
		if i < 2 {
			clients = append(clients, testkit.ComputerAPI(t, p, b))
		}
	}
	var readOnly domain.Computer
	if err := p.Store.Get(ctx, "computers", bindings[2].ComputerID, &readOnly); err != nil {
		t.Fatal(err)
	}
	t.Run("disabled API", func(t *testing.T) {
		client := testkit.ComputerAPI(t, p, bindings[2])
		disabled := true
		if _, err := p.Hub.UpdateComputer(ctx, readOnly.ID, readOnly.Revision, nil, &disabled); err != nil {
			t.Fatal(err)
		}
		url := bindings[2].APIURL + "/v1/skills"
		call(t, client, "GET", url, nil, http.StatusForbidden)
		call(t, client, "POST", url+"/shared/code-review/publish", http.Header{"Idempotency-Key": {"disabled-publish"}}, http.StatusForbidden)
		call(t, client, "DELETE", url+"/shared/code-review", http.Header{"Idempotency-Key": {"disabled-delete"}}, http.StatusForbidden)
	})

	waitForSkill := func(computer int, name, scope, body string) string {
		t.Helper()
		var path string
		testkit.Await(t, ctx, func() bool {
			result, err := p.Hub.Discover(ctx, bindings[computer])
			if err != nil {
				return false
			}
			for _, item := range result.Skills {
				if item.Name == name && item.Scope == scope {
					data, err := os.ReadFile(item.Path)
					if err == nil && strings.Contains(string(data), body) {
						path = item.Path
						return true
					}
				}
			}
			return false
		})
		return path
	}
	for _, name := range []string{"skill-creator", "session-history", "astral-api"} {
		path := waitForSkill(2, name, "astral-builtin", "")
		if name == "astral-api" {
			cli := filepath.Join(filepath.Dir(path), "scripts", "astral_api.py")
			if info, err := os.Stat(cli); err != nil || !info.Mode().IsRegular() {
				t.Fatalf("bundled CLI missing on API-disabled Computer: %v", err)
			}
		}
	}
	status := func(computer int) gateway.ManagedSkillsView {
		t.Helper()
		var view gateway.ManagedSkillsView
		raw := call(t, clients[computer], "GET", bindings[computer].APIURL+"/v1/skills", nil, http.StatusOK)
		if err := json.Unmarshal(raw, &view); err != nil {
			t.Fatal(err)
		}
		return view
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		name := filepath.Base(filepath.Dir(path))
		if err := os.WriteFile(path, []byte("---\nname: "+name+"\ndescription: Review a patch.\n---\n"+body+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	mutate := func(computer int, method, suffix, intent string, want int) skills.Version {
		t.Helper()
		raw := call(t, clients[computer], method, bindings[computer].APIURL+"/v1/skills/shared/code-review"+suffix,
			http.Header{"Idempotency-Key": {intent}}, want)
		var version skills.Version
		if want == http.StatusOK {
			if err := json.Unmarshal(raw, &version); err != nil {
				t.Fatal(err)
			}
		}
		return version
	}
	waitForRevision := func(revision int64) {
		t.Helper()
		testkit.Await(t, ctx, func() bool {
			for _, item := range status(0).Local.Shared {
				if item.Name == "code-review" && item.Revision == revision {
					return true
				}
			}
			return false
		})
	}

	pathA := filepath.Join(status(0).Local.SharedRoot, "code-review", "SKILL.md")
	write(pathA, "initial revision")
	first := mutate(0, "POST", "/publish", "initial", http.StatusOK)
	if first.Revision != 1 || mutate(0, "POST", "/publish", "initial", http.StatusOK) != first {
		t.Fatal("publish retry did not return the initial revision")
	}
	waitForRevision(first.Revision)
	pathB := waitForSkill(1, "code-review", "astral-shared", "initial revision")
	pathC := waitForSkill(2, "code-review", "astral-shared", "initial revision")
	localOnly := filepath.Join(filepath.Dir(filepath.Dir(pathB)), "local-only", "SKILL.md")
	write(localOnly, "never published")
	write(pathB, "unpublished B edit")
	write(pathA, "new published files")
	second := mutate(0, "POST", "/publish", "second", http.StatusOK)
	if second.Revision != 2 {
		t.Fatalf("unexpected second revision: %+v", second)
	}
	if waitForSkill(2, "code-review", "astral-shared", "new published files") != pathC {
		t.Fatal("remote sync changed the path of C's unmodified copy")
	}
	if waitForSkill(1, "code-review", "astral-shared", "new published files") != pathB {
		t.Fatal("remote sync changed the path of B's edited copy")
	}
	waitForRevision(second.Revision)
	if err := p.Daemons[2].Close(ctx); err != nil {
		t.Fatal(err)
	}
	write(pathB, "unpublished edit before deletion")
	deleted := mutate(0, "DELETE", "", "delete", http.StatusOK)
	if deleted.Revision != 3 || len(status(0).Manifest.Shared) != 0 {
		t.Fatalf("unexpected deletion: %+v", deleted)
	}
	waitForRemoval := func(path string) {
		t.Helper()
		testkit.Await(t, ctx, func() bool {
			_, err := os.Stat(filepath.Dir(path))
			return os.IsNotExist(err)
		})
	}
	waitForRemoval(pathA)
	waitForRemoval(pathB)
	if _, err := os.Stat(localOnly); err != nil {
		t.Fatalf("sync removed an unpublished Skill: %v", err)
	}
	if _, err := os.Stat(pathC); err != nil {
		t.Fatalf("offline Computer changed before reconnecting: %v", err)
	}
	reconnected, err := computers.NewDaemon(p.Daemons[2].Root, p.Daemons[2].Credentials)
	if err != nil {
		t.Fatal(err)
	}
	reconnected.Hello.Home = p.Daemons[2].Hello.Home
	p.Daemons[2] = reconnected
	go func() { _ = reconnected.Connect(ctx) }()
	waitForRemoval(pathC)
	bindings[2], err = p.Hub.Bind(ctx, p.Agent.ID, p.Session.ID, readOnly.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	write(pathA, "published again")
	recreated := mutate(0, "POST", "/publish", "recreate", http.StatusOK)
	if recreated.Revision != 4 || mutate(0, "DELETE", "", "delete", http.StatusOK) != deleted {
		t.Fatalf("unexpected recreation or deletion retry: %+v", recreated)
	}
	waitForSkill(2, "code-review", "astral-shared", "published again")
	if p.Requests.Load() != 0 {
		t.Fatal("managed skill operations invoked a model")
	}
}
