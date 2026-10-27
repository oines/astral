package modelbridge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
)

// Exercise public Save/Resolve and the real SDK encoder. A route helper alone
// cannot prove the selected protocol's URL and compatibility reach the wire.
func TestPresetResponsesWireContract(t *testing.T) {
	for _, test := range []struct {
		preset, model, base, protocol, wantURL string
		include, store                         bool
	}{
		{"deepseek", "deepseek-v4-pro", "https://api.deepseek.com/v1", "openai-responses", "https://api.deepseek.com/responses", false, false},
		{"qwen", "qwen-test", "https://dashscope.aliyuncs.com/compatible-mode/v1", "openai-responses", "https://dashscope.aliyuncs.com/compatible-mode/v1/responses", false, true},
		{"zhipu", "glm-test", "https://open.bigmodel.cn/api/paas/v4", "openai-responses", "https://open.bigmodel.cn/api/v1/responses", false, true},
		{"groq", "openai/gpt-oss-120b", "https://api.groq.com/openai/v1", "openai-responses", "https://api.groq.com/openai/v1/responses", false, true},
		{"fireworks", "test", "https://api.fireworks.ai/inference/v1", "openai-responses", "https://api.fireworks.ai/inference/v1/responses", false, true},
		{"moonshot", "kimi-k3", "https://api.moonshot.cn/v1", "openai-responses", "https://api.moonshot.cn/v1/responses", false, true},
		{"cloudflare", "@cf/openai/gpt-oss-120b", "https://api.cloudflare.com/client/v4/accounts/test/ai/v1", "openai-responses", "https://api.cloudflare.com/client/v4/accounts/test/ai/v1/responses", false, true},
	} {
		t.Run(test.preset, func(t *testing.T) {
			b, _, _ := reasoningIntegrationBridge(t)
			called := false
			b.HTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				called = true
				if req.URL.String() != test.wantURL {
					t.Errorf("URL = %s, want %s", req.URL, test.wantURL)
				}
				var body map[string]json.RawMessage
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if _, ok := body["include"]; ok != test.include {
					t.Errorf("include present = %v, want %v", ok, test.include)
				}
				if _, ok := body["store"]; ok != test.store {
					t.Errorf("store present = %v, want %v", ok, test.store)
				}
				if value, ok := body["store"]; ok && string(value) != "false" {
					t.Errorf("store = %s", value)
				}
				if string(body["stream"]) != "false" {
					t.Errorf("stream = %s", body["stream"])
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"response-test","status":"completed","output":[{"type":"message","id":"message-test","role":"assistant","status":"completed","content":[{"type":"output_text","text":"done"}]}]}`))}, nil
			})}
			preset := FindPreset(test.preset)
			p := domain.Provider{Name: preset.Name, PresetID: preset.ID, BaseURL: test.base, Protocol: preset.Protocol, Dialect: preset.Dialect}
			key := "test-key"
			if err := b.SaveProvider(context.Background(), &p, &key, nil); err != nil {
				t.Fatal(err)
			}
			profile := reasoningIntegrationProfile(p, test.model, map[string]any{})
			profile.Protocol = test.protocol
			if err := b.SaveProfile(context.Background(), &profile); err != nil {
				t.Fatal(err)
			}
			ref, _, err := b.Selection(context.Background(), profile.ID)
			if err != nil {
				t.Fatal(err)
			}
			model, err := b.Resolve(context.Background(), ref, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := model.Stream(context.Background(), core.Request{ModelID: ref, ModelRevision: ref, MaxOutputTokens: 1024, Messages: []core.Message{core.User("hello")}}, func(core.ModelEvent) {}); err != nil {
				t.Fatal(err)
			}
			if !called {
				t.Fatal("request was not sent")
			}
		})
	}
}
