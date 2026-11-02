package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/oines/astral/internal/harness"
	"github.com/oines/astral/internal/testkit"
)

func TestOwnerSubagentSessionIsReadableButCannotBeControlled(t *testing.T) {
	p := testkit.New(t, 1, func(context.Context, int, map[string]any) any { return testkit.Text("child result") })
	child, err := p.Harness.CreateSession(p.Context, p.Agent.ID, "Internal work")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Harness.Submit(p.Context, child.ID, harness.InputRequest{ID: "child-task", Text: "Read the source"}); err != nil {
		t.Fatal(err)
	}
	p.Idle(t, child.ID)
	before, err := p.CoreStore.LoadState(p.Context, child.ID)
	if err != nil || before.LastRun == nil {
		t.Fatalf("child did not finish its initial work: state=%+v error=%v", before, err)
	}
	if err := p.Store.Get(p.Context, "sessions", child.ID, &child); err != nil {
		t.Fatal(err)
	}
	child.SubagentParentID, child.Private = p.Session.ID, true
	if err := p.Store.Save(p.Context, "sessions", &child.Meta, &child); err != nil {
		t.Fatal(err)
	}
	_, server := apiFixture(t, p)
	path := "/api/v1/sessions/" + child.ID
	for _, suffix := range []string{"", "/history", "/timeline", "/snapshot", "/attempts"} {
		response := request(t, p.Context, server, http.MethodGet, path+suffix, nil, true)
		raw, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("Owner cannot inspect child %s: status=%d body=%s error=%v", suffix, response.StatusCode, raw, err)
		}
	}
	response := request(t, p.Context, server, http.MethodGet, "/api/v1/sessions", nil, true)
	var views []harness.View
	err = json.NewDecoder(response.Body).Decode(&views)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("Owner list failed: %d %v", response.StatusCode, err)
	}
	found := false
	for _, view := range views {
		found = found || view.ID == child.ID && view.SubagentParentID == p.Session.ID
	}
	if !found {
		t.Fatal("Owner list omitted child state needed by its parent's UI")
	}
	for _, mutation := range []struct{ method, suffix, body string }{
		{"POST", "/inputs", `{"id":"owner-bypass","text":"do different work"}`},
		{"POST", "/continue", `{}`},
		{"POST", "/stop", `{}`},
		{"POST", "/compact", `{}`},
		{"POST", "/computer", `{}`},
		{"POST", "/model", `{}`},
		{"POST", "/pending/clear", `{}`},
		{"PATCH", "/pending/child-task", `{}`},
		{"DELETE", "/pending/child-task", ""},
		{"POST", "/pending/child-task/move", `{}`},
		{"POST", "/fork", `{}`},
		{"POST", "/approvals/approval", `{}`},
		{"PATCH", "", `{"archived":true}`},
		{"DELETE", "?revision=1", ""},
		{"POST", "/attachments?name=bypass.txt", "must not upload"},
	} {
		t.Run(mutation.method+mutation.suffix, func(t *testing.T) {
			response := request(t, p.Context, server, mutation.method, path+mutation.suffix, bytes.NewBufferString(mutation.body), true)
			raw, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil || response.StatusCode != http.StatusForbidden {
				t.Fatalf("Owner child mutation escaped: status=%d body=%s error=%v", response.StatusCode, raw, err)
			}
		})
	}
	// Converting the completed fixture into a subagent legitimately wakes its
	// parent. Verify the target child's durable state rather than counting all
	// model requests, which also includes that asynchronous parent response.
	after, err := p.CoreStore.LoadState(p.Context, child.ID)
	if err != nil || after.LastRun == nil || after.LastRun.ID != before.LastRun.ID || after.Version != before.Version {
		t.Fatalf("Owner actions changed child work: before=%+v after=%+v error=%v", before, after, err)
	}
	if _, err := p.Harness.Receipt(p.Context, child.ID, "owner-bypass"); err == nil {
		t.Fatal("rejected Owner input was durably accepted")
	}
}
