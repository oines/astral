package channels

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestWeixinTypingUsesPrivateContextAndKeepsTicketOutOfResult(t *testing.T) {
	for _, active := range []bool{true, false} {
		t.Run(map[bool]string{true: "start", false: "stop"}[active], func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				if body["ilink_user_id"] != "peer" || r.Header.Get("Authorization") != "Bearer secret-login-token" {
					t.Error("typing request used the wrong user/account")
				}
				uin, err := base64.StdEncoding.DecodeString(r.Header.Get("X-WECHAT-UIN"))
				info, _ := body["base_info"].(map[string]any)
				if err != nil || len(uin) == 0 || r.Header.Get("AuthorizationType") != "ilink_bot_token" || r.Header.Get("iLink-App-Id") != "bot" || r.Header.Get("iLink-App-ClientVersion") != "132106" || info["bot_agent"] != "Astral/1.0" || info["channel_version"] != weixinProtocolVersion {
					t.Error("missing iLink common metadata")
				}
				switch r.URL.Path {
				case "/ilink/bot/getconfig":
					if body["context_token"] != "private-peer-context" {
						t.Error("typing configuration omitted the peer's context")
					}
					_, _ = io.WriteString(rw, `{"ret":0,"typing_ticket":"secret-typing-ticket"}`)
				case "/ilink/bot/sendtyping":
					expected := float64(2)
					if active {
						expected = 1
					}
					if body["typing_ticket"] != "secret-typing-ticket" || body["status"] != expected {
						t.Error("typing ticket or status was incorrect")
					}
					_, _ = io.WriteString(rw, `{}`)
				default:
					t.Error("unexpected typing API")
				}
			}))
			defer server.Close()
			w := weixinTestAdapter(t, server.URL, nil)
			_ = w.putWire(t.Context(), "context", "peer", "private-peer-context")
			result, err := w.operation(t.Context(), "set_typing", w.conversation("peer"), map[string]any{"active": active})
			if err != nil || calls.Load() != 2 || strings.Contains(string(result), "secret") || !strings.Contains(string(result), `"requested":true`) {
				t.Fatalf("typing result = %s %v, calls=%d", result, err, calls.Load())
			}
		})
	}
}

func TestWeixinTypingDoesNotRetryUncertainState(t *testing.T) {
	var attempts atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ilink/bot/getconfig" {
			_, _ = io.WriteString(rw, `{"ret":0,"typing_ticket":"secret-ticket"}`)
			return
		}
		attempts.Add(1)
		rw.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(rw, `secret-ticket and secret-login-token`)
	}))
	defer server.Close()
	w := weixinTestAdapter(t, server.URL, nil)
	_ = w.putWire(t.Context(), "context", "peer", "context")
	_, err := w.operation(t.Context(), "set_typing", w.conversation("peer"), map[string]any{"active": true})
	var sendErr *SendError
	if !errors.As(err, &sendErr) || !sendErr.Unknown || attempts.Load() != 1 || strings.Contains(err.Error(), "secret-") {
		t.Fatalf("uncertain typing request was retried or leaked its ticket: %#v, attempts=%d", err, attempts.Load())
	}
	for _, params := range []map[string]any{{}, {"active": "true"}, {"active": true, "to_user_id": "another-peer"}} {
		if _, err := w.operation(t.Context(), "set_typing", w.conversation("peer"), params); err == nil {
			t.Fatal("accepted invalid parameters")
		}
	}
	if _, err := w.operation(t.Context(), "set_typing", w.conversation("unknown-peer"), map[string]any{"active": true}); err == nil {
		t.Fatal("typing request invented an unknown peer context")
	}
	if attempts.Load() != 1 {
		t.Fatal("invalid requests made external mutations")
	}
}
