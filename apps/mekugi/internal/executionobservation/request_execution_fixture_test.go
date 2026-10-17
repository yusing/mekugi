package executionobservation

import (
	"context"

	"io"

	"net/http"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

func executeRequest(
	ctx context.Context,
	executionCtx context.Context,
	request codexresponses.Request,
	headers http.Header,
	sessionID string,
	provider inferencegateway.Forwarder,
	output io.Writer,
	issues *requestexecution.Failures,
	mekugiCalls *Preparer,
) error {
	executor := testRequestExecutor(provider, issues, mekugiCalls)
	executor.Output = output
	return executor.Execute(ctx, executionCtx, request, headers, sessionID)
}
