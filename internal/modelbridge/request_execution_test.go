package modelbridge

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral-core/models"
	"github.com/oines/astral-core/models/provider/openai/completions"
)

type bridgeExecutionTransport func(*http.Request) (*http.Response, error)

type bridgeMutableValidator struct{ core.ModelFunc }

func (bridgeMutableValidator) EstimateTokens(context.Context, core.Request) (int, error) {
	return 1, nil
}

func (bridgeMutableValidator) ValidateRequest(_ context.Context, request core.Request) error {
	request.Messages[0].Content[0].Text = "private validator mutation"
	return nil
}

func TestBoundReadonlyValidationPreservesLegacyIsolation(t *testing.T) {
	request := core.Request{Messages: []core.Message{core.User("caller-owned text")}}
	if err := core.ValidateRequest(context.Background(), &boundModel{adapter: bridgeMutableValidator{}}, request); err != nil {
		t.Fatal(err)
	}
	if request.Messages[0].Content[0].Text != "caller-owned text" {
		t.Fatal("bridge readonly forwarding exposed caller data to a legacy validator")
	}
}

func (f bridgeExecutionTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestBoundExecutionKeepsAttemptHeadersAndSafeErrorBoundary(t *testing.T) {
	streaming := false
	var bodies [][]byte
	sdk, err := completions.New(models.Config{Spec: core.ModelSpec{ID: "execution", ContextWindow: 64000, MaxOutputTokens: 1024}, Model: "remote", Streaming: &streaming, HTTPClient: &http.Client{Transport: bridgeExecutionTransport(func(request *http.Request) (*http.Response, error) {
		raw, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		bodies = append(bodies, raw)
		if request.Header.Get("X-Interaction-Id") != "session-execution" || request.Header.Get("X-Attempt") != strconv.Itoa(len(bodies)) {
			t.Error("prepared bridge execution omitted current Host/attempt headers")
		}
		status := 200
		response := `{"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"done"}}]}`
		if len(bodies) == 1 {
			status = 402
			response = `{"error":{"type":"insufficient_quota","code":"insufficient_quota","message":"PRIVATE upstream diagnostic"}}`
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(response)), Request: request}, nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	bound := &boundModel{adapter: sdk, presetID: "github-copilot", sessionID: "session-execution"}
	request := core.Request{ModelID: "execution", MaxOutputTokens: 100, Messages: []core.Message{core.User("frozen bridge request")}}
	execution, err := bound.PrepareExecution(context.Background(), request)
	if err != nil || execution == nil {
		t.Fatalf("prepared forwarding: %v", err)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		ctx := models.WithRequestHeaders(context.Background(), map[string]string{"X-Attempt": strconv.Itoa(attempt)})
		reply, err := execution.Stream(ctx, func(core.ModelEvent) {})
		if attempt == 1 {
			var native *models.APIError
			if !errors.As(err, &native) || native.StatusCode != 402 || !strings.Contains(native.Message, "PRIVATE") {
				t.Fatalf("native cause lost through prepared bridge: %v", err)
			}
			if strings.Contains(err.Error(), "PRIVATE") || strings.Contains(reply.Message.ErrorMessage, "PRIVATE") || reply.Message.ErrorMessage != err.Error() {
				t.Fatal("prepared execution bypassed safe public diagnostics")
			}
		} else if err != nil || reply.Message.Role != core.RoleAssistant {
			t.Fatalf("second attempt: %+v %v", reply, err)
		}
	}
	if len(bodies) != 2 || !bytes.Equal(bodies[0], bodies[1]) {
		t.Fatal("bridge changed frozen body between attempts")
	}
}

func TestBoundProjectionRetainsLegacyAvailabilityGuard(t *testing.T) {
	sdk, err := completions.New(models.Config{Spec: core.ModelSpec{ID: "execution", ContextWindow: 64000, MaxOutputTokens: 1024}, Model: "remote"})
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("connection access was revoked")
	checks := 0
	bound := &boundModel{adapter: sdk, validate: func(context.Context) error { checks++; return failure }}
	request := core.Request{ModelID: "execution", MaxOutputTokens: 100, Messages: []core.Message{core.User("must check access before SDK projection")}}
	if _, err := core.ProjectRequest(context.Background(), bound, request); !errors.Is(err, failure) || checks != 1 {
		t.Fatalf("readonly projection bypassed Host validation: checks=%d err=%v", checks, err)
	}
}
