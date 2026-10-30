package launch

import (
	"context"
	"encoding/json/jsontext"
	"io"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/executionobservation"
	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

// Exercise the admitted request's milestone and invocation notice queue together.
func queueContextWarning(t *testing.T, issues *requestexecution.Failures, thread string) {
	t.Helper()
	tokens, window := uint64(600), uint64(1000)
	preparer := executionobservation.New(executionobservation.Config{
		Registry: sharedProxyTestRegistry(t), Control: new(sessioncontrol.Observations), Notice: issues.AddThreadNotice,
		Inspect: func(context.Context, string) (control.SessionDetails, error) {
			return control.SessionDetails{ContextTokens: &tokens, ContextWindow: &window}, nil
		},
	})
	request := &responses.Request{Fields: map[string]jsontext.Value{"input": jsontext.Value(`[{"role":"user","content":"continue"}]`)}}
	prepared, err := preparer.Prepare(t.Context(), request, requestexecution.Preparation{Session: t.Name(), Thread: thread, MetadataValid: true, Metadata: responses.TurnMetadata{RequestKind: "turn"}})
	if err != nil {
		t.Fatal(err)
	}
	if prepared != nil {
		prepared.Close()
	}
}

func TestContextWarningReachesTerminal(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.Client.Thread = t.Name()
	queueContextWarning(t, u.issues, u.Client.Thread)
	delivery := applyCriticalNotices(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, u.clock, preferences.Applied{}, u.serviceTiers, u.title, u.backendVersion, &u.restoring, &u.side, u.audience, u.issues)
	if delivery == nil || !u.attached.feedback.Alert || !strings.Contains(u.attached.feedback.Text, "Mekugi context usage: 60% (600/1000 tokens).") || len(u.attached.source.Entries) != 0 {
		t.Fatalf("context warning missing or entered transcript: %+v", u.attached.feedback)
	}
	defer terminalTest(u).Diff.Close()
	if err := u.testPaint(io.Discard, 100, 16); err != nil {
		t.Fatal(err)
	}
	if !terminalTest(u).Frame.Notices.Composer {
		t.Fatal("warning was not visibly painted")
	}
	delivery.finish(terminalTest(u).Frame.Notices)
	if pending := u.issues.Notices.Pending(); len(pending) != 0 {
		t.Fatalf("painted warning remained pending: %v", pending)
	}
}
