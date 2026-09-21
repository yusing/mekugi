package router

import (
	"context"
	"io"
	"net/http"
)

// executeRequest preserves the direct attempt setup used throughout the package
// tests while production code owns stable services through requestExecutor.
func executeRequest(
	ctx context.Context,
	executionCtx context.Context,
	request parsedResponsesRequest,
	headers http.Header,
	sessionID string,
	provider responseProvider,
	output io.Writer,
	issues *CriticalErrors,
	mekugiCalls *mekugiProxy,
	mentor *mentorHandoff,
) error {
	executor := requestExecutor{
		provider: provider, output: output, issues: issues,
		mekugiCalls: mekugiCalls, mentor: mentor,
	}
	return executor.execute(ctx, executionCtx, request, headers, sessionID)
}
