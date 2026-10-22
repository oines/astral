package channels

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/oines/astral/internal/domain"
)

func TestNapCatMessageOperationsVerifyCurrentPlatformScope(t *testing.T) {
	for _, test := range []struct {
		name, chat, platformMessage string
		allowed                     bool
	}{
		{"matching group", "group:456", `{"message_id":-42,"message_type":"group","group_id":456,"user_id":123}`, true},
		{"other group", "group:456", `{"message_id":-42,"message_type":"group","group_id":789,"user_id":123}`, false},
		{"different message", "group:456", `{"message_id":-43,"message_type":"group","group_id":456,"user_id":123}`, false},
		{"wrong kind", "private:123", `{"message_id":-42,"message_type":"group","group_id":123,"user_id":123}`, false},
		{"incoming private", "private:123", `{"message_id":-42,"message_type":"private","user_id":123}`, true},
		{"other private", "private:123", `{"message_id":-42,"message_type":"private","user_id":789}`, false},
		{"outgoing private with target", "private:123", `{"message_id":-42,"message_type":"private","user_id":999,"target_id":123}`, true},
		{"outgoing private wrong target", "private:123", `{"message_id":-42,"message_type":"private","user_id":999,"target_id":789}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, operation := range []string{"get_message", "delete_message"} {
				var deletes atomic.Int64
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/get_msg" {
						fmt.Fprint(w, `{"status":"ok","retcode":0,"data":`+test.platformMessage+`}`)
						return
					}
					if r.URL.Path != "/delete_msg" {
						t.Errorf("unexpected operation: %s", r.URL.Path)
					}
					deletes.Add(1)
					fmt.Fprint(w, `{"status":"ok","retcode":0,"data":null}`)
				}))
				n := napcatTestAdapter(t, server)
				_, err := napcatOperation(context.Background(), n, operation, domain.Conversation{ExternalID: test.chat}, map[string]any{"message_id": "-42"})
				if (err == nil) != test.allowed {
					t.Errorf("%s scope result: %v", operation, err)
				}
				if operation == "delete_message" && !test.allowed && deletes.Load() != 0 {
					t.Fatal("unauthorized destructive request reached NapCat")
				}
				server.Close()
			}
		})
	}
}

func TestNapCatSelfPrivateOperationsNeedChatScopedHistoryProof(t *testing.T) {
	for _, present := range []bool{false, true} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]json.RawMessage
			_ = json.NewDecoder(r.Body).Decode(&body)
			if r.URL.Path != "/get_friend_msg_history" || string(body["user_id"]) != `"123"` || string(body["count"]) != "100" {
				t.Errorf("history escaped selected chat: %s %s", r.URL.Path, encoded(body))
			}
			if present {
				fmt.Fprint(w, `{"status":"ok","retcode":0,"data":{"messages":[{"message_id":-42}]}}`)
			} else {
				fmt.Fprint(w, `{"status":"ok","retcode":0,"data":{"messages":[{"message_id":-43}]}}`)
			}
		}))
		n := napcatTestAdapter(t, server)
		err := napcatMessageScope(context.Background(), n, domain.Conversation{ExternalID: "private:123"}, json.RawMessage(`{"message_id":-42,"message_type":"private","user_id":999}`), "-42")
		if (err == nil) != present {
			t.Fatalf("private history proof: %v", err)
		}
		server.Close()
	}
}

func TestNapCatMutatingOperationsClassifyConfirmedFailure(t *testing.T) {
	for _, test := range []struct {
		response string
		status   int
		unknown  bool
	}{
		{`{"status":"failed","retcode":1401}`, 200, false},
		{`{"status":"failed","retcode":1200}`, 200, true},
		{`{"status":"failed","retcode":200}`, 200, true},
		{`{"status":"failed","retcode":500}`, 500, true},
		{`broken`, 200, true},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/set_group_ban" {
				t.Errorf("unexpected method: %s", r.URL.Path)
			}
			w.WriteHeader(test.status)
			fmt.Fprint(w, test.response)
		}))
		n := napcatTestAdapter(t, server)
		_, err := napcatOperation(context.Background(), n, "set_group_ban", domain.Conversation{ExternalID: "group:456"}, map[string]any{"user_id": "123", "duration": float64(60)})
		var sendErr *SendError
		if !errors.As(err, &sendErr) || sendErr.Unknown != test.unknown {
			t.Fatalf("mutation failure semantics: %v", err)
		}
		server.Close()
	}
}

func TestNapCatOperationsRejectInexactIDsAndPrivateGroupActions(t *testing.T) {
	n := &napcat{}
	for _, test := range []struct {
		name, chat string
		params     map[string]any
	}{
		{"get_member", "group:456", map[string]any{"user_id": float64(1 << 54)}},
		{"get_member", "group:456", map[string]any{"user_id": float64(1.5)}},
		{"get_members", "private:123", nil},
		{"set_group_ban", "group:456", map[string]any{"user_id": "123", "duration": "-1"}},
		{"unknown", "group:456", nil},
	} {
		if _, err := napcatOperation(context.Background(), n, test.name, domain.Conversation{ExternalID: test.chat}, test.params); err == nil {
			t.Fatalf("accepted invalid operation: %+v", test)
		}
	}
}
