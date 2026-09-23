package router

import (
	"context"
	"io"
	"net/http"
	"testing"
)

func TestDeprecatedTerraRoutesToSol(t *testing.T) {
	for _, mentorEnabled := range []bool{false, true} {
		mentor := newMentorHandoff(mentorEnabled, mentorEnabled)
		if mentorEnabled {
			mentor.sessions["terra-thread"] = mentorSession{complete: true}
		}
		request := mentorTestRequest(t, "gpt-5.6-terra")
		headers := serverMetadataHeaders(t, "turn", nil)
		headers.Set(threadIDHeader, "terra-thread")
		attempt := newRequestAttempt(requestExecutor{
			provider: &serverFakeProvider{}, issues: NewCriticalErrors(), mentor: mentor,
		}, t.Context(), t.Context(), request, headers, "")
		if err := attempt.prepare(); err != nil {
			t.Fatal(err)
		}
		if got := attempt.request.modelDescription(); got != "gpt-6-sol medium" {
			t.Fatalf("mentor enabled=%t: route=%q", mentorEnabled, got)
		}
	}
}

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
