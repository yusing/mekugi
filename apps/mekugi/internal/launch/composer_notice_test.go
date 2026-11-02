package launch

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"

	"github.com/yusing/mekugi/modules/conversation/composer"

	"github.com/yusing/mekugi/libraries/terminalui"

	"github.com/yusing/mekugi/libraries/textsanitize"

	"github.com/yusing/mekugi/libraries/diffrender"

	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

func TestComposerErrorClickAndDismiss(t *testing.T) {
	for _, tc := range []struct {
		kind  string
		width int
	}{
		{"command", 12}, {"command", 13}, {"command", 60}, {"attachment", 60}, {"host", 60},
	} {
		t.Run(fmt.Sprintf("%s/%d", tc.kind, tc.width), func(t *testing.T) {
			u, wire := newAppServerTestUI()

			defer terminalTest(u).Diff.Close()
			u.Turn, u.attached.presentation.Status.Text, u.Text = "active", "Working", "keep draft"
			switch tc.kind {
			case "command":
				u.Text = "/unknown"
				appServerTestKeys(t, u, "\r")
			case "attachment":
				d := conversationmodel.Input{Files: []composer.Token{{Path: "/missing/setup.sh"}}}
				d.SnapshotFiles("/missing")
				setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), d.AttachmentNotice, true)
				frames, ok := attachments.Decode(d.Attachments[0])
				if !ok || strings.Contains(strings.Join(frames, "\n"), d.AttachmentNotice) || !strings.Contains(strings.Join(frames, "\n"), "CONTENT NOT ATTACHED") {
					t.Fatal("UI warning leaked into the model attachment result")
				}
			case "host":
				appServerTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"main","turn":{"id":"active","status":"failed","error":{"message":"provider failed\nFull diagnostics"}}}}`)
			}
			full, draft, turn, sent := u.composerErrorText(), u.Text, u.Turn, wire.String()
			screen := vt.NewEmulator(tc.width+2, 16)
			defer screen.Close()
			if err := u.testPaint(screen, tc.width+2, 16); err != nil {
				t.Fatal(err)
			}
			click := func(rect terminalsession.Rect) {
				t.Helper()
				if rect.W == 0 {
					t.Fatal("missing error control")
				}
				x, y := rect.X+terminalTest(u).Layout.Main.X+1, rect.Y+terminalTest(u).Layout.Main.Y+1
				if err := terminalTest(u).mouse(fmt.Sprintf("\x1b[<0;%d;%dM", x, y)); err != nil {
					t.Fatal(err)
				}
			}
			click(u.shell.Main.NoticeDetails)
			if terminalTest(u).Dialog == nil {
				t.Fatal("truncated error did not open")
			}
			drawOutputDialog(terminalTest(u))
			if terminalTest(u).Dialog.Document.Text != textsanitize.Safe(full, false) {
				t.Fatal("details lost full error")
			}
			terminalTest(u).outputKey("q")
			click(u.shell.Main.NoticeDismiss)
			if u.composerErrorText() != "" || u.Text != draft || u.Turn != turn || wire.String() != sent || len(u.attached.source.Entries) != 0 {
				t.Fatal("dismissal changed draft, host state, wire, or transcript")
			}
		})
	}
}

func TestComposerErrorClearsOnSuccessfulModelRequest(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.issues = testCriticalErrors()
	u.Turn = "still-running"
	u.clock = time.Now
	defer terminalTest(u).Diff.Close()
	setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "previous request failed", true)
	u.testMainFrame(80, 8, 0)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"ok\",\"status\":\"completed\",\"output\":[]}}\n\n")
	}))
	defer provider.Close()
	executor := requestexecution.Executor{Issues: u.issues, Provider: inferencegateway.NewClient(provider.URL, provider.Client(), requestexecution.ProviderDiagnostics())}
	request, err := codexresponses.ParseRequest([]byte(`{"model":"gpt-test","stream":true,"input":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, thread := range []string{"unrelated", "main"} {
		headers := http.Header{}
		headers.Set("Authorization", "Bearer fixture")
		headers.Set("ChatGPT-Account-ID", "fixture-account")
		headers.Set(codexresponses.ThreadIDHeader, thread)
		headers.Set(codexresponses.TurnMetadataHeader, `{"thread_id":"`+thread+`","turn_id":"still-running","request_kind":"turn"}`)
		var output bytes.Buffer
		if err := executor.ExecuteHTTP(t.Context(), t.Context(), request, headers, &output); err != nil {
			t.Fatal(err)
		}
		applyCriticalNotices(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, u.clock, preferences.Applied{}, u.serviceTiers, u.title, u.backendVersion, &u.restoring, &u.side, u.audience, u.issues)
		rows, _ := u.testMainFrame(80, 8, 0)
		if thread == "unrelated" {
			if !u.attached.feedback.Alert || !strings.Contains(ansi.Strip(strings.Join(rows, "\n")), "[x]") {
				t.Fatal("unrelated success cleared feedback")
			}
		} else if u.attached.feedback.Alert || u.shell.Main.NoticeDismiss.W != 0 || u.Turn != "still-running" {
			t.Fatal("successful model request did not clear the error during its host turn")
		}
	}
}

func TestUISnapshotComposerError(t *testing.T) {
	for _, width := range []int{12, 20, 36, 80} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.shell.Transcript.Painter.Theme = diffrender.DarkTheme
			u.clock = func() time.Time { return time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC) }
			u.attached.presentation.Status.Text = "Ready"
			setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "File contents omitted: setup.sh (attachment budget exceeded).\nThe attachment result names the missing content.", true)
			rows, _ := u.testMainFrame(width, 8, 0)
			assertNativeUISnapshot(t, fmt.Sprintf("composer-error-%d", width), rows)
			if !strings.Contains(rows[u.shell.Main.NoticeDismiss.Y], terminalui.Green) || !strings.Contains(ansi.Strip(rows[u.shell.Main.NoticeDismiss.Y]), "[x]") {
				t.Fatal("dismiss check is missing its shared success color")
			}
		})
	}
	u, _ := newAppServerTestUI()
	setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "failed", true)
	u.testMainFrame(80, 8, 0)
	setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "done", false)
	u.testMainFrame(80, 8, 0)
	if u.shell.Main.NoticeDetails.W != 0 || u.shell.Main.NoticeDismiss.W != 0 {
		t.Fatal("stale error controls survived feedback")
	}
}
