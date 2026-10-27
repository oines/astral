package modelbridge

import (
	"context"
	"strconv"
	"strings"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
)

// ReadSpec reads the immutable model facts used by an already prepared request.
// It does not resolve the current selection, construct an adapter or read secrets.
func (b *Bridge) ReadSpec(ctx context.Context, ref string) (core.ModelSpec, error) {
	parts := strings.Split(ref, "~")
	if len(parts) != 3 || parts[0] == "" {
		return core.ModelSpec{}, domain.Invalid("invalid versioned model reference")
	}
	revision, err := strconv.ParseInt(parts[1], 10, 64)
	connectionRevision, connectionErr := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || connectionErr != nil || revision < 1 || connectionRevision < 1 {
		return core.ModelSpec{}, domain.Invalid("invalid model revision")
	}
	var profile domain.ModelProfile
	if err = b.Store.Version(ctx, "models", parts[0], revision, &profile); err != nil {
		return core.ModelSpec{}, err
	}
	values, _, err := profile.Effective()
	if err != nil {
		return core.ModelSpec{}, err
	}
	return core.ModelSpec{ID: ref, Revision: ref, ContextWindow: values.ContextWindow, MaxInputTokens: values.MaxInputTokens, MaxOutputTokens: values.MaxOutputTokens}, nil
}
