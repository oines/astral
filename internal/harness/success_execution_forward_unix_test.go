//go:build (darwin || linux) && astral_prepared_execution

package harness

import (
	"context"
	core "github.com/oines/astral-core"
)

func init() {
	successLifecycleStartProcess = startSuccessLifecycleProcess
	successLifecycleConfigure = func(config *core.Config) {
		config.EstimateTokensReadOnly, config.EstimateTokens = config.EstimateTokens, nil
	}
}

func (m successLifecycleHistoricalModel) ValidateRequestReadOnly(ctx context.Context, request core.Request) error {
	return core.ValidateRequest(ctx, m.Model, request)
}

// Forward the optional execution capabilities through the historical Host adapter.
// The baseline Core predates these capabilities and uses the same fixture without this tag.
func (m successLifecycleHistoricalModel) PrepareExecution(ctx context.Context, request core.Request) (core.RequestExecution, error) {
	if preparer, ok := m.Model.(core.RequestExecutionPreparer); ok {
		return preparer.PrepareExecution(ctx, request)
	}
	return nil, nil
}

func (m successLifecycleHistoricalModel) ProjectRequest(ctx context.Context, request core.Request) (core.Request, error) {
	var out core.Request
	var err error
	if projector, ok := m.Model.(core.RequestProjector); ok {
		out, err = projector.ProjectRequest(ctx, request)
	} else {
		out, err = core.PrepareRequest(ctx, m.Model, request)
	}
	if err == nil && out.SystemPrompt == m.basePrompt {
		out.SystemPrompt = m.preparedPrompt
	}
	return out, err
}
