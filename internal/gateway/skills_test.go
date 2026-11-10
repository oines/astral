package gateway_test

import (
	"bytes"
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

	call := func(client *http.Client, method, url string, input any, headers http.Header, want int) []byte {
		t.Helper()
		var data []byte
		if input != nil {
			data = testkit.JSON(input)
		}
		request, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(data))
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
	for _, d := range p.Daemons {
		b, err := p.Hub.Bind(ctx, p.Agent.ID, p.Session.ID, d.Credentials.ComputerID, "")
		if err != nil {
			t.Fatal(err)
		}
		bindings = append(bindings, b)
		clients = append(clients, testkit.ComputerAPI(t, p, b))
	}
	var readOnly domain.Computer
	if err := p.Store.Get(ctx, "computers", bindings[2].ComputerID, &readOnly); err != nil {
		t.Fatal(err)
	}
	disabled := true
	if _, err := p.Hub.UpdateComputer(ctx, readOnly.ID, readOnly.Revision, nil, &disabled); err != nil {
		t.Fatal(err)
	}

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
	call(clients[2], "GET", bindings[2].APIURL+"/v1/skills", nil, nil, http.StatusForbidden)

	status := func(computer int) gateway.ManagedSkillsView {
		t.Helper()
		var view gateway.ManagedSkillsView
		raw := call(clients[computer], "GET", bindings[computer].APIURL+"/v1/skills", nil, nil, http.StatusOK)
		if err := json.Unmarshal(raw, &view); err != nil {
			t.Fatal(err)
		}
		return view
	}
	localSkill := func(computer int) computers.LocalSkill {
		t.Helper()
		for _, item := range status(computer).Local.Shared {
			if item.Name == "code-review" {
				return item
			}
		}
		t.Fatal("local code-review skill missing")
		return computers.LocalSkill{}
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("---\nname: code-review\ndescription: Review a patch.\n---\n"+body+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	publish := func(computer int, intent string, input gateway.SkillPublishInput, want int) skills.Version {
		t.Helper()
		raw := call(clients[computer], "POST", bindings[computer].APIURL+"/v1/skills/shared/code-review/publish",
			input, http.Header{"Idempotency-Key": {intent}}, want)
		var version skills.Version
		if want == http.StatusOK {
			if err := json.Unmarshal(raw, &version); err != nil {
				t.Fatal(err)
			}
		}
		return version
	}
	request := func(computer int, base int64) gateway.SkillPublishInput {
		t.Helper()
		local := localSkill(computer)
		return gateway.SkillPublishInput{BaseRevision: &base, Digest: local.ContentDigest}
	}

	pathA := filepath.Join(status(0).Local.SharedRoot, "code-review", "SKILL.md")
	write(pathA, "initial revision")
	create := request(0, 0)
	publish(2, "disabled", create, http.StatusForbidden)
	first := publish(0, "initial", create, http.StatusOK)
	if first.Revision != 1 || publish(0, "initial", create, http.StatusOK) != first {
		t.Fatal("publish retry did not return the initial revision")
	}
	pathB := waitForSkill(1, "code-review", "astral-shared", "initial revision")
	pathC := waitForSkill(2, "code-review", "astral-shared", "initial revision")
	write(pathB, "unpublished B edit")
	write(pathA, "new published files")
	second := publish(0, "second", request(0, 1), http.StatusOK)
	if second.Revision != 2 {
		t.Fatalf("unexpected second revision: %+v", second)
	}
	if waitForSkill(2, "code-review", "astral-shared", "new published files") != pathC {
		t.Fatal("remote sync changed the path of C's unmodified copy")
	}
	if waitForSkill(1, "code-review", "astral-shared", "unpublished B edit") != pathB {
		t.Fatal("remote sync moved B's local draft")
	}
	publish(1, "stale-b", request(1, 1), http.StatusConflict)
	old := call(clients[1], "GET", bindings[1].APIURL+"/v1/skills/shared/code-review/revisions/1/content", nil, nil, http.StatusOK)
	oldDir := t.TempDir()
	if err := skills.Unpack(ctx, old, oldDir); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(oldDir, "SKILL.md")); err != nil || !strings.Contains(string(data), "initial revision") {
		t.Fatalf("older revision unavailable: %s (%v)", data, err)
	}
	write(pathB, "manually combined revisions")
	if v := publish(1, "merged-b", request(1, 2), http.StatusOK); v.Revision != 3 {
		t.Fatalf("unexpected reconciled revision: %+v", v)
	}
	waitForSkill(2, "code-review", "astral-shared", "manually combined revisions")
	if p.Requests.Load() != 0 {
		t.Fatal("managed skill operations invoked a model")
	}
}
