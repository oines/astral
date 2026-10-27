package modelbridge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	core "github.com/oines/astral-core"
)

// Exercise the actual SDK adapter behind the authenticated account transport,
// including an opaque native reasoning item across a tool-result boundary.
func TestAccountOAuthCatalogInferenceAndEncryptedReasoningReplay(t *testing.T) {
	for _, preset := range []string{"codex", "github-copilot"} {
		t.Run(preset, func(t *testing.T) {
			b := testBridge(t)
			ctx := context.Background()
			p := saveAuthProvider(t, b, preset)
			calls := 0
			b.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				switch r.URL.Path {
				case "/api/accounts/deviceauth/usercode":
					return authResponse(200, `{"device_auth_id":"device","usercode":"CODE"}`), nil
				case "/api/accounts/deviceauth/token":
					return authResponse(200, `{"authorization_code":"code","code_verifier":"verifier"}`), nil
				case "/oauth/token":
					return authResponse(200, `{"access_token":"fixture","refresh_token":"refresh","expires_in":3600}`), nil
				case "/login/device/code":
					return authResponse(200, `{"device_code":"device","user_code":"CODE","verification_uri":"https://github.com/login/device"}`), nil
				case "/login/oauth/access_token":
					return authResponse(200, `{"access_token":"fixture"}`), nil
				}
				if r.Header.Get("Authorization") != "Bearer fixture" {
					t.Fatal("missing account OAuth credential")
				}
				if r.Method == http.MethodGet {
					if preset == "codex" {
						return authResponse(200, `{"models":[{"slug":"account-model","visibility":"list","context_window":128000,"max_output_tokens":8192}]}`), nil
					}
					return authResponse(200, `{"data":[{"id":"account-model","model_picker_enabled":true,"supported_endpoints":["/responses"],"capabilities":{"type":"chat","limits":{"max_context_window_tokens":128000,"max_output_tokens":8192}}}]}`), nil
				}
				if !strings.HasSuffix(r.URL.Path, "/responses") {
					t.Fatalf("account model used wrong route: %s", r.URL)
				}
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				calls++
				if preset == "codex" {
					if string(body["stream"]) != "true" || string(body["store"]) != "false" {
						t.Fatalf("Codex wire mode: %s", body)
					}
					if _, ok := body["max_output_tokens"]; ok {
						t.Fatal("Codex received unsupported max_output_tokens")
					}
				}
				output := `[{"type":"reasoning","id":"rs1","summary":[],"encrypted_content":"opaque-cipher","vendor_counter":9007199254740993},{"type":"function_call","id":"fc1","call_id":"call1","name":"lookup","arguments":"{}","status":"completed"}]`
				if calls == 2 {
					input := string(body["input"])
					if !strings.Contains(input, `"encrypted_content":"opaque-cipher"`) || !strings.Contains(input, `9007199254740993`) || !strings.Contains(input, `"type":"function_call_output"`) {
						t.Fatalf("lost native reasoning or tool-result replay: %s", input)
					}
					output = `[{"type":"message","id":"m1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"done"}]}]`
				}
				result := `{"id":"response","status":"completed","output":` + output + `}`
				if string(body["stream"]) == "true" {
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":" + result + "}\n\n"))}, nil
				}
				return authResponse(200, result), nil
			})}
			flow, err := b.StartAuthorization(ctx, p.ID)
			if err != nil {
				t.Fatal(err)
			}
			done, err := b.PollAuthorization(ctx, flow.ID)
			if err != nil {
				t.Fatal(err)
			}
			p = *done.Provider
			catalog, err := b.Catalog(ctx, p.ID)
			if err != nil || len(catalog) != 1 {
				t.Fatalf("catalog %+v %v", catalog, err)
			}
			profile := reasoningIntegrationProfile(p, catalog[0].ID, map[string]any{"supported": true, "efforts": []string{"high"}})
			profile.Protocol = catalog[0].Protocol
			profile.Dialect = catalog[0].Dialect
			if preset == "codex" {
				profile.Recommendations["streaming"] = raw(true)
			}
			if err := b.SaveProfile(ctx, &profile); err != nil {
				t.Fatal(err)
			}
			model, err := b.construct(ctx, profile, p)
			if err != nil {
				t.Fatal(err)
			}
			req := core.Request{ModelID: model.Spec().ID, ModelRevision: model.Spec().Revision, Messages: []core.Message{core.User("lookup")}, MaxOutputTokens: 1024, Tools: []core.ToolDefinition{{Name: "lookup", Parameters: json.RawMessage(`{"type":"object","properties":{}}`)}}}
			reply, err := model.Stream(ctx, req, func(core.ModelEvent) {})
			if err != nil {
				t.Fatal(err)
			}
			if len(reply.Message.ToolCalls()) != 1 {
				t.Fatalf("missing tool call: %+v", reply.Message)
			}
			req.Messages = append(req.Messages, reply.Message, core.Message{Role: core.RoleTool, ToolCallID: "call1", Content: core.Text("result")})
			reply, err = model.Stream(ctx, req, func(core.ModelEvent) {})
			if err != nil {
				t.Fatal(err)
			}
			if reply.Message.FinishReason != core.FinishStop || calls != 2 {
				t.Fatalf("incomplete account roundtrip %+v calls=%d", reply.Message, calls)
			}
		})
	}
}
