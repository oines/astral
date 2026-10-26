package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral-core/standardtools"
	"github.com/oines/astral/internal/channels"
	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/testkit"
	testshell "github.com/oines/astral/internal/testkit/shell"
)

func TestConcurrentLocalGatewaysKeepAgentSessionAndSourceIdentity(t *testing.T) {
	p := testkit.New(t, 2, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	channel := channels.New(p.Store, p.Harness)
	gateway := New(p.Harness, channel)
	p.Hub.Gateway = gateway.Handle
	other := p.Agent
	other.Meta = domain.Meta{}
	other.Name = "other Agent"
	if err := p.Harness.SaveAgent(p.Context, &other); err != nil {
		t.Fatal(err)
	}
	otherSession, err := p.Harness.CreateSession(p.Context, other.ID, "other")
	if err != nil {
		t.Fatal(err)
	}
	first, err := p.Hub.Bind(p.Context, p.Agent.ID, p.Session.ID, p.Agent.DefaultComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.Hub.Bind(p.Context, other.ID, otherSession.ID, p.Agent.DefaultComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	backend := &computers.Backend{Hub: p.Hub}
	invoke := func(b domain.ExecutionBinding, cmd string) (string, error) {
		process, err := backend.StartProcess(p.Context, core.Invocation{SessionID: b.SessionID, ExecutionID: domain.ID("execution"), Binding: computers.BindingJSON(b)}, standardtools.ProcessRequest{Command: cmd})
		if err != nil {
			return "", err
		}
		body, err := io.ReadAll(process.Output())
		_ = process.Output().Close()
		if _, waitErr := process.Wait(); err == nil {
			err = waitErr
		}
		return string(body), err
	}
	if first.APIURL == "" || second.APIURL == "" || first.APIURL == second.APIURL {
		t.Fatalf("Agents must have separate loopback addresses: %q %q", first.APIURL, second.APIURL)
	}
	var workers sync.WaitGroup
	for _, b := range []domain.ExecutionBinding{first, second} {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 4 {
				body, err := invoke(b, testshell.HTTP("GET", b.APIURL+"/v1/context", ""))
				if err != nil {
					t.Error(err)
					return
				}
				var view Context
				if json.Unmarshal([]byte(body), &view) != nil || view.Agent.ID != b.AgentID || view.Session.ID != b.SessionID || view.SourceComputerID != b.ComputerID {
					t.Errorf("crossed execution context: %s", body)
				}
			}
		}()
	}
	workers.Wait()
	if err = p.Harness.SelectComputer(p.Context, p.Session.ID, p.Daemons[1].Credentials.ComputerID, ""); err != nil {
		t.Fatal(err)
	}
	body, err := invoke(first, testshell.HTTP("GET", first.APIURL+"/v1/context", ""))
	if err != nil {
		t.Fatal(err)
	}
	var view Context
	_ = json.Unmarshal([]byte(body), &view)
	if view.SourceComputerID != first.ComputerID || view.Session.ComputerID == first.ComputerID {
		t.Fatal("old process Gateway was rebound to current Computer")
	}
	reply, err := gateway.Handle(p.Context, computers.GatewayRequest{Binding: first, Method: "GET", Path: "/v1/sessions/" + otherSession.ID + "/history"})
	if err != nil || reply.Status != http.StatusForbidden {
		t.Fatalf("cross-Agent history visible: %+v %v", reply, err)
	}
	reply, err = gateway.Handle(p.Context, computers.GatewayRequest{Binding: first, Method: "POST", Path: "/v1/im/messages", Body: json.RawMessage(`{"text":"forged","agent_id":"other"}`), Intent: "forged"})
	if err != nil || reply.Status != 400 {
		t.Fatalf("forged identity accepted: %+v %v", reply, err)
	}
	reply, err = gateway.Handle(p.Context, computers.GatewayRequest{Binding: first, Method: "POST", Path: "/v1/im/messages", Body: json.RawMessage(`{"text":"no implicit target"}`), Intent: "no-target"})
	if err != nil || reply.Status != 400 || !strings.Contains(string(reply.Body), "no default chat") {
		t.Fatalf("Web Session acquired an implicit IM destination: %+v %v", reply, err)
	}
	forged := first
	forged.AgentID = other.ID
	if _, err = gateway.Handle(p.Context, computers.GatewayRequest{Binding: forged, Method: "GET", Path: "/v1/context"}); err == nil {
		t.Fatal("forged internal grant accepted")
	}
	p.Agent.AllowedComputers = []string{p.Daemons[1].Credentials.ComputerID}
	p.Agent.DefaultComputerID = p.Daemons[1].Credentials.ComputerID
	if err = p.Harness.SaveAgent(p.Context, &p.Agent); err != nil {
		t.Fatal(err)
	}
	if _, err = gateway.Handle(p.Context, computers.GatewayRequest{Binding: first, Method: "GET", Path: "/v1/context"}); err == nil {
		t.Fatal("revoked source retained API access")
	}
}

type attachmentAdapter struct {
	channels.Adapter
	payload []byte
}

func (a attachmentAdapter) Capabilities() channels.Capabilities {
	return channels.Capabilities{Platform: "fixture", Adapter: "fixture", Name: "fixture", DownloadLimit: int64(len(a.payload)), UploadLimit: 10 << 20, TextLimit: 4096}
}
func (a attachmentAdapter) Validate(context.Context) (channels.Identity, error) {
	return channels.Identity{ID: "fixture-account"}, nil
}
func (a attachmentAdapter) Download(context.Context, domain.Attachment) (channels.Download, error) {
	return channels.Download{Body: io.NopCloser(bytes.NewReader(a.payload)), Name: "original.bin", Size: int64(len(a.payload))}, nil
}

func TestAttachmentBytesAndFilteredPaginationThroughRealGateway(t *testing.T) {
	p := testkit.New(t, 1, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	payload := bytes.Repeat([]byte{0, 255, 1, 3, 7, 31, 128, 10}, 300000)
	channel := channels.New(p.Store, p.Harness)
	channel.Registry["fixture"] = func(domain.Connection, string, *http.Client) (channels.Adapter, error) {
		return attachmentAdapter{payload: payload}, nil
	}
	account := domain.Connection{Name: "source", AgentID: p.Agent.ID, Adapter: "fixture", AccessMode: "all", Enabled: true}
	token := "fixture-token"
	if err := channel.SaveConnection(p.Context, &account, &token); err != nil {
		t.Fatal(err)
	}
	conversation := domain.Conversation{Meta: domain.Meta{ID: domain.ID("conversation")}, ConnectionID: account.ID, ExternalID: "42", Title: "originals", Kind: "group"}
	if err := p.Store.Save(p.Context, "conversations", &conversation.Meta, &conversation); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 142; i++ {
		text := "background"
		if i >= 140 {
			text = "needle match"
		}
		event := domain.Observation{SchemaVersion: 1, ID: fmt.Sprint("event-", i), ExternalEventID: fmt.Sprint(i), ConnectionID: account.ID, ConversationID: conversation.ID, MessageID: fmt.Sprint(i), Kind: "message.created", Text: text, ReceivedAt: time.Now(), SentAt: time.Now()}
		if i == 141 {
			event.Attachments = []domain.Attachment{{Ref: "original:0", ExternalID: "fixture", Name: "original.bin", MediaType: "application/octet-stream", Size: int64(len(payload))}}
		}
		if _, err := p.Store.Archive(p.Context, event); err != nil {
			t.Fatal(err)
		}
	}
	gateway := New(p.Harness, channel)
	p.Hub.Gateway = gateway.Handle
	b, err := p.Hub.Bind(p.Context, p.Agent.ID, p.Session.ID, p.Agent.DefaultComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	client := testkit.ComputerAPI(t, p, b)
	query := func(after int64) MessagePage {
		t.Helper()
		response, err := client.Get(fmt.Sprintf("%s/v1/conversations/%s/messages?q=needle&limit=1&after=%d", b.APIURL, conversation.ID, after))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var page MessagePage
		if err = json.NewDecoder(response.Body).Decode(&page); err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != 200 {
			t.Fatalf("query failed: %d", response.StatusCode)
		}
		return page
	}
	first := query(0)
	if len(first.Items) != 1 || first.Items[0].Observation.MessageID != "140" {
		t.Fatalf("search applied after pagination: %+v", first)
	}
	second := query(first.NextSequence)
	if len(second.Items) != 1 || second.Items[0].Observation.MessageID != "141" {
		t.Fatalf("filtered cursor skipped match: %+v", second)
	}
	path := "/v1/conversations/" + conversation.ID + "/messages/141/attachments/original:0/content"
	response, err := client.Get(b.APIURL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	hash := sha256.New()
	length, err := io.Copy(hash, response.Body)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(payload)
	if response.StatusCode != 200 || length != int64(len(payload)) || !bytes.Equal(hash.Sum(nil), want[:]) {
		t.Fatalf("binary response changed: status=%d bytes=%d", response.StatusCode, length)
	}
	if response.Header.Get("Content-Type") != "application/octet-stream" || !strings.Contains(response.Header.Get("Content-Disposition"), "original.bin") {
		t.Fatalf("headers lost: %v", response.Header)
	}
	files, err := p.Store.List(p.Context, "files")
	if err != nil || len(files) != 0 {
		t.Fatal("download created a cached Computer file")
	}
	bindings, err := p.Store.List(p.Context, "bindings")
	if err != nil || len(bindings) != 0 {
		t.Fatal("reading originals activated a Session")
	}
	message, err := gateway.Handle(p.Context, computers.GatewayRequest{Binding: b, Method: "GET", Path: "/v1/conversations/" + conversation.ID + "/messages/141"})
	if err != nil || message.Status != 200 {
		t.Fatalf("message ID lookup failed: %+v %v", message, err)
	}
}
