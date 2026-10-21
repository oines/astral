package modelbridge

import (
	"context"
	"strings"

	"github.com/oines/astral-models/provider/anthropic"
	"github.com/oines/astral-models/provider/google"
	"github.com/oines/astral-models/provider/openai/completions"
	"github.com/oines/astral-models/provider/openai/responses"
	"github.com/oines/astral/internal/domain"
)

type sessionContextKey struct{}

func WithSessionID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, sessionContextKey{}, id)
}
func sessionID(ctx context.Context) string {
	id, _ := ctx.Value(sessionContextKey{}).(string)
	return id
}

func sessionHeaders(presetID, id string) map[string]string {
	if strings.HasPrefix(presetID, "opencode") {
		return map[string]string{"X-Opencode-Session": id}
	}
	if presetID == "github-copilot" {
		return map[string]string{"X-Interaction-Id": id}
	}
	return nil
}

func modelRoute(profile domain.ModelProfile, provider domain.Provider) (protocol, dialect, baseURL string, err error) {
	protocol, dialect, baseURL = profile.Protocol, profile.Dialect, provider.BaseURL
	if protocol == "" {
		protocol = provider.Protocol
	}
	if dialect == "" {
		dialect = provider.Dialect
	}
	switch protocol {
	case anthropic.Protocol:
		dialect = "anthropic"
	case google.Protocol:
		dialect = "google"
	case responses.Protocol:
		if dialect != "deepseek" {
			dialect = "openai"
		}
	case completions.Protocol:
		if dialect == "anthropic" || dialect == "google" {
			dialect = "compatible"
		}
	default:
		return "", "", "", domain.Invalid("unsupported model protocol")
	}
	if strings.HasPrefix(provider.PresetID, "opencode") {
		switch protocol {
		case anthropic.Protocol:
			baseURL += "/anthropic/v1"
		case google.Protocol:
			baseURL += "/google/v1beta"
		default:
			baseURL += "/openai/v1"
		}
	}
	return protocol, dialect, baseURL, nil
}
