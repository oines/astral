package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/oines/astral/internal/channels"
	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/httpcontract"
	"github.com/oines/astral/internal/testkit"
)

// Account registration's WebSocket identity check is outside this HTTP test;
// operation dispatch restores the actual NapCat factory after setup.
type operationAccountRegistration struct{ channels.Adapter }

func (operationAccountRegistration) Validate(context.Context) (channels.Identity, error) {
	return channels.Identity{ID: "999", Name: "Fixture QQ"}, nil
}

func TestNapCatOperationHTTPPreservesKnownAndUnknownFailuresWithoutRetry(t *testing.T) {
	p := testkit.New(t, 1, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	var mode atomic.Int64
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if r.Method != "POST" || r.URL.Path != "/set_group_ban" || payload["group_id"] != "456" || payload["user_id"] != "123" || r.Header.Get("Authorization") != "Bearer operation-test-token" {
			t.Errorf("unexpected platform request: %s %s %+v", r.Method, r.URL.Path, payload)
		}
		switch mode.Load() {
		case 1401, 200, 1200:
			// Raw platform text can contain credentials. The safe adapter error
			// contains only the HTTP status and protocol return code.
			fmt.Fprintf(w, `{"status":"failed","retcode":%d,"message":"operation-test-token must not escape"}`, mode.Load())
		case -1:
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = connection.Close()
		default:
			t.Errorf("unexpected fixture mode %d", mode.Load())
		}
	}))
	t.Cleanup(server.Close)
	channel := channels.New(p.Store, p.Harness)
	channel.Registry["qq.napcat"] = func(connection domain.Connection, token string, client *http.Client) (channels.Adapter, error) {
		adapter, err := channels.NewNapCat(connection, token, client)
		return operationAccountRegistration{adapter}, err
	}
	account := domain.Connection{AgentID: p.Agent.ID, Name: "QQ operation fixture", Adapter: "qq.napcat", AccessMode: "all", Enabled: true,
		Config: testkit.JSON(channels.NapCatConfig{BaseURL: server.URL, WebSocketURL: "ws" + strings.TrimPrefix(server.URL, "http") + "/events"})}
	token := "operation-test-token"
	if err := channel.SaveConnection(p.Context, &account, &token); err != nil {
		t.Fatal(err)
	}
	channel.Registry["qq.napcat"] = channels.NewNapCat
	chat := domain.Conversation{Meta: domain.Meta{ID: domain.ID("conversation")}, ConnectionID: account.ID, ExternalID: "group:456", Kind: "group"}
	if err := p.Store.Save(p.Context, "conversations", &chat.Meta, &chat); err != nil {
		t.Fatal(err)
	}
	api := New(p.Harness, channel)
	p.Hub.Gateway = api.Handle
	source, err := p.Hub.Bind(p.Context, p.Agent.ID, p.Session.ID, p.Agent.DefaultComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	path := "/v1/accounts/" + account.ID + "/operations/set_group_ban"
	body := testkit.JSON(channels.ChatOperationRequest{ConversationID: chat.ID, Parameters: map[string]any{"user_id": "123", "duration": 60}})
	for _, test := range []struct {
		name, code, message string
		mode                int64
	}{
		{"explicit rejection", "platform_rejected", "NapCat rejected the request (HTTP 200, retcode 1401)", 1401},
		{"HTTP action timeout", "outcome_unknown", "NapCat rejected the request (HTTP 200, retcode 200)", 200},
		{"WS action timeout envelope", "outcome_unknown", "NapCat rejected the request (HTTP 200, retcode 1200)", 1200},
		{"connection closed after dispatch", "outcome_unknown", "NapCat request could not be completed", -1},
	} {
		t.Run(test.name, func(t *testing.T) {
			mode.Store(test.mode)
			before := calls.Load()
			reply, err := api.Handle(p.Context, computers.GatewayRequest{Binding: source, Method: "POST", Path: path, Body: body})
			if err != nil {
				t.Fatal(err)
			}
			var problem httpcontract.Error
			if reply.Status != http.StatusBadGateway || json.Unmarshal(reply.Body, &problem) != nil || problem.Code != test.code || problem.Message != test.message {
				t.Fatalf("Gateway lost the platform outcome: HTTP %d %s", reply.Status, reply.Body)
			}
			if strings.Contains(string(reply.Body), token) || strings.Contains(string(reply.Body), "must not escape") {
				t.Fatalf("Gateway exposed raw platform diagnostics: %s", reply.Body)
			}
			if calls.Load()-before != 1 {
				t.Fatalf("operation automatically retried: %d attempts", calls.Load()-before)
			}
		})
	}
	// Existing validation keeps its original HTTP status/code and never dispatches.
	before := calls.Load()
	bad := testkit.JSON(channels.ChatOperationRequest{ConversationID: chat.ID, Parameters: map[string]any{"user_id": "123", "group_id": "789"}})
	reply, err := api.Handle(p.Context, computers.GatewayRequest{Binding: source, Method: "POST", Path: path, Body: bad})
	var problem httpcontract.Error
	if err != nil || reply.Status != http.StatusBadRequest || json.Unmarshal(reply.Body, &problem) != nil || problem.Code != "invalid" || !strings.Contains(problem.Message, "group_id") || calls.Load() != before {
		t.Fatalf("operation mapping swallowed validation: HTTP %d %s %v", reply.Status, reply.Body, err)
	}
}
