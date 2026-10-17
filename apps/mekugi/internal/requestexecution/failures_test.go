package requestexecution

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestCriticalErrorsIdentifyEveryGenericFailurePhaseWithCompleteCauses(t *testing.T) {
	for _, test := range []struct {
		phase requestFailurePhase
		label string
	}{
		{requestFailureForward, "upstream request forwarding"},
		{requestFailureInspectResponse, "upstream response inspection"},
		{requestFailureWriteResponse, "downstream response writing"},
	} {
		t.Run(string(test.phase), func(t *testing.T) {
			c := testCriticalErrors()
			record := func(err error) {
				c.record(&requestFinalization{sessionID: "one", failurePhase: test.phase,
					observation: requestObservation{outcome: requestOutcomeFailed}}, err)
			}
			first := errors.New("Authorization Bearer firstsecret")
			record(first)
			record(first)
			record(errors.New("plain secondsecret prompt"))
			if len(c.Notices.Pending()) != 2 || !strings.Contains(c.Notices.Pending()[0], "occurred 2 times") || strings.Contains(c.Notices.Pending()[1], "occurred") {
				t.Fatalf("non-transform causes collapsed or did not deduplicate: %+v", c.Notices.Pending())
			}
			pending := strings.Join(c.Notices.Pending(), "\n")
			if !strings.Contains(pending, first.Error()) || !strings.Contains(pending, "plain secondsecret prompt") ||
				strings.Count(pending, "Failure phase: "+test.label+".") != 2 ||
				strings.Count(pending, "Diagnostic reference:") != 2 {
				t.Fatalf("generic notices omitted causes or phases: %s", pending)
			}
		})
	}

	terminal := testCriticalErrors()
	terminal.record(&requestFinalization{sessionID: "one", failurePhase: requestFailureTerminalValidation,
		observation: requestObservation{outcome: requestOutcomeFailed}}, fmt.Errorf("validate response: %w", ErrMissingTerminal))
	terminalNotice := strings.Join(terminal.Notices.Pending(), "\n")
	if !strings.Contains(terminalNotice, "Failure phase: terminal response validation.") ||
		!strings.Contains(terminalNotice, "ended without a completed or failed terminal state") {
		t.Fatalf("known terminal cause was hidden: %s", terminalNotice)
	}

	prepare := testCriticalErrors()
	prepare.record(&requestFinalization{sessionID: "one", failurePhase: requestFailurePrepare,
		observation: requestObservation{outcome: requestOutcomeFailed}}, errors.New("plain preparesecret"))
	prepareNotice := strings.Join(prepare.Notices.Pending(), "\n")
	if strings.Contains(prepareNotice, "check the request error") || !strings.Contains(prepareNotice, "preparesecret") ||
		!strings.Contains(prepareNotice, "Failure phase: request preparation.") {
		t.Fatalf("prepare notice was not self-contained: %s", prepareNotice)
	}
}

func TestCriticalErrorsSuccessAndCancellationSilent(t *testing.T) {
	c := testCriticalErrors()
	for _, outcome := range []requestOutcome{requestOutcomeCompleted, requestOutcomeCanceledBeforeResponse, requestOutcomeCanceledAfterResponse} {
		c.record(&requestFinalization{observation: requestObservation{outcome: outcome}}, context.Canceled)
	}
	if len(c.Notices.Pending()) != 0 {
		t.Fatal("success or cancellation emitted a notice")
	}
}
