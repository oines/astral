package channels

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/filestream"
	"github.com/oines/astral/internal/testkit"
)

func TestNapCatOperationUploadsReadOriginalComputerAndRespectRevocation(t *testing.T) {
	p := testkit.New(t, 2, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	var calls atomic.Int64
	const original = "original Computer file bytes"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		field := "file"
		if r.URL.Path == "/_send_group_notice" {
			field = "image"
		} else if r.URL.Path != "/upload_group_file" && r.URL.Path != "/set_group_portrait" {
			t.Errorf("unexpected platform operation: %s", r.URL.Path)
		}
		data, _ := body[field].(string)
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(data, "base64://"))
		if err != nil || string(decoded) != original || body["group_id"] != "456" {
			t.Errorf("upload lost original Computer bytes or selected chat: %+v, %v", body, err)
		}
		calls.Add(1)
		fmt.Fprint(w, `{"status":"ok","retcode":0,"data":null}`)
	}))
	defer server.Close()
	s := New(p.Store, p.Harness)
	account := domain.Connection{Meta: domain.Meta{ID: "operation-account"}, AgentID: p.Agent.ID, Adapter: "qq.napcat", Platform: "qq", AccountID: "999", Enabled: true, AccessMode: "all",
		Config: encoded(NapCatConfig{BaseURL: server.URL, WebSocketURL: strings.Replace(server.URL, "http:", "ws:", 1) + "/ws"})}
	chat := domain.Conversation{Meta: domain.Meta{ID: "operation-group"}, ConnectionID: account.ID, ExternalID: "group:456", Kind: "group"}
	if err := p.Store.Save(p.Context, "connections", &account.Meta, &account); err != nil {
		t.Fatal(err)
	}
	if err := p.Store.Save(p.Context, "conversations", &chat.Meta, &chat); err != nil {
		t.Fatal(err)
	}
	binding, err := p.Hub.Bind(p.Context, p.Agent.ID, p.Session.ID, p.Daemons[0].Credentials.ComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	fs, err := (&computers.Backend{Hub: p.Hub}).FileSystem(p.Context, core.Invocation{SessionID: binding.SessionID, Binding: computers.BindingJSON(binding)})
	if err != nil {
		t.Fatal(err)
	}
	path, err := fs.ResolvePath(p.Context, "operation-file.bin", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.WriteFile(p.Context, path.Path, []byte(original)); err != nil {
		t.Fatal(err)
	}
	ref, err := p.Harness.Files.Reference(p.Context, binding, path.Path, true)
	if err != nil {
		t.Fatal(err)
	}
	// A command retains its source Computer after the session selects another.
	var session domain.Session
	if err := p.Store.Get(p.Context, "sessions", p.Session.ID, &session); err != nil {
		t.Fatal(err)
	}
	session.ComputerID = p.Daemons[1].Credentials.ComputerID
	if err := p.Store.Save(p.Context, "sessions", &session.Meta, &session); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, field string
		params      map[string]any
	}{
		{"upload_group_file", "file", map[string]any{"name": "shared.bin"}},
		{"set_group_portrait", "file", map[string]any{}},
		{"_send_group_notice", "image", map[string]any{"content": "Picture announcement"}},
	} {
		for _, source := range []string{path.Path, filestream.URL(ref)} {
			_, err := s.AccountOperation(p.Context, binding, account.ID, test.name, ChatOperationRequest{ConversationID: chat.ID, Parameters: test.params, Files: map[string]string{test.field: source}})
			if err != nil {
				t.Fatalf("%s failed for a Computer source: %v", test.name, err)
			}
		}
	}
	if calls.Load() != 6 {
		t.Fatalf("each upload should make one platform request: %d", calls.Load())
	}
	for _, files := range []map[string]string{
		{"other": path.Path},
		{"file": "https://example.com/external.bin"},
		{"file": ""},
	} {
		if _, err := s.AccountOperation(p.Context, binding, account.ID, "upload_group_file", ChatOperationRequest{ConversationID: chat.ID, Parameters: map[string]any{"name": "bad.bin"}, Files: files}); err == nil {
			t.Fatalf("unsupported upload source was accepted: %+v", files)
		}
	}
	account.AccessMode = "specified"
	if err := p.Store.Save(p.Context, "connections", &account.Meta, &account); err != nil {
		t.Fatal(err)
	}
	request := ChatOperationRequest{ConversationID: chat.ID, Parameters: map[string]any{"name": "shared.bin"}, Files: map[string]string{"file": filestream.URL(ref)}}
	if _, err := s.AccountOperation(p.Context, binding, account.ID, "upload_group_file", request); err == nil {
		t.Fatal("file upload bypassed the chat access list")
	}
	account.AccessMode = "all"
	if err := p.Store.Save(p.Context, "connections", &account.Meta, &account); err != nil {
		t.Fatal(err)
	}
	p.Agent.AllowedComputers = []string{p.Daemons[1].Credentials.ComputerID}
	p.Agent.DefaultComputerID = p.Daemons[1].Credentials.ComputerID
	if err := p.Harness.SaveAgent(p.Context, &p.Agent); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AccountOperation(p.Context, binding, account.ID, "upload_group_file", request); err == nil {
		t.Fatal("an existing FileRef bypassed revoked Computer access")
	}
	if calls.Load() != 6 || p.Requests.Load() != 0 {
		t.Fatal("a rejected upload reached the platform or an operation woke the model")
	}
	if observations, err := p.Store.Observations(p.Context, chat.ID, 0, 10); err != nil || len(observations) != 0 {
		t.Fatal("resource uploads were fabricated as inbound messages:", err)
	}
}
