package modelbridge

import (
	"context"
	core "github.com/oines/astral-core"
	"testing"

	"github.com/oines/astral/internal/domain"
)

func TestReadSpecUsesPreparedVersionWithoutConnectionOrCredentials(t *testing.T) {
	ctx := context.Background()
	bridge := testBridge(t)
	connection := domain.Provider{Name: "target", Protocol: "openai-completions", Dialect: "compatible", BaseURL: "https://fixture.example/v1"}
	key, headers := "secret", map[string]string{"Private-Header": "private"}
	if err := bridge.SaveProvider(ctx, &connection, &key, &headers); err != nil {
		t.Fatal(err)
	}
	profile := reasoningIntegrationProfile(connection, "remote-model", nil)
	if err := bridge.SaveProfile(ctx, &profile); err != nil {
		t.Fatal(err)
	}
	preparedRef := Reference(profile, connection)
	profile.Overrides["context_window"] = raw(64000)
	if err := bridge.SaveProfile(ctx, &profile); err != nil {
		t.Fatal(err)
	}
	if err := bridge.Store.RotateSecret(ctx, connection.HeadersCredentialID, "invalid header JSON"); err != nil {
		t.Fatal(err)
	}
	// Live adapter construction cannot read these credentials. Metadata inspection
	// must still use the prepared revision, even after the current resources vanish.
	if _, err := bridge.ResolveSelection(ctx, core.ModelSelection{ID: profile.ID}); err == nil {
		t.Fatal("fixture failed to make credentials unreadable")
	}
	if err := bridge.DeleteProvider(ctx, connection.ID, connection.Revision); err != nil {
		t.Fatal(err)
	}
	spec, err := bridge.ReadSpec(ctx, preparedRef)
	if err != nil || spec.ID != preparedRef || spec.Revision != preparedRef || spec.ContextWindow != 128000 || spec.MaxOutputTokens != 8192 {
		t.Fatalf("immutable metadata lookup depended on live state: %+v %v", spec, err)
	}
}
