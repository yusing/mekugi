package launch

import (
	"bytes"
	"encoding/base64"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/textsanitize"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"

	jsonv1 "encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func TestErrorDetailsLiveCodeMode(t *testing.T) {
	for _, child := range []bool{false, true} {
		t.Run(map[bool]string{false: "main", true: "child"}[child], func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			u.shell.Transcript.Conversation = true
			u.testActivity(u.shell.Transcript).testAudience()
			activity := sessioncontrol.NewAudience(nil)
			activity.Attach("main")
			proxy := &observationFixture{activity: activity}
			setTestAppServerIntegration(u, proxy)
			thread, view, focus := "main", u.testActivity(u.shell.Transcript), 0
			if child {
				thread, view, focus = "child", u.testActivity(u.shell.Agents), 0
				u.testActivity(u.shell.Agents).setDisplayFilter(true, "/root/worker")
				activity.Observe("child", "main", "/root/worker", true)
				registerThread(&u.attached.session, binding.ThreadInfo{ID: "child", AgentNickname: "worker"})
			}
			// The decisive reason is beyond both the old first-line clip and
			// Activity's generic 64 KiB message limit. No shell is executed.
			detail := "exec_command failed: CreateProcess { message: Rejected(" + strings.Repeat("escaped-command ", 5000) + ") }\n\x1b[31mrejected: forced cleanup is not permitted\x1b[0m\n    at cell:17\n"
			output := []any{
				map[string]any{"type": "input_text", "text": "Script failed\nWall time 0.0 seconds\nOutput:\n"},
				map[string]any{"type": "input_text", "text": "Script error:\n" + detail},
			}
			input := responses.MustMarshalJSON([]any{
				map[string]any{"type": "custom_tool_call", "name": "exec", "call_id": "rejected", "input": "opaque rejected script"},
				map[string]any{"type": "custom_tool_call_output", "call_id": "rejected", "output": output},
			})
			original := bytes.Clone(input)
			request := &codexresponses.Request{Fields: map[string]jsonv1.RawMessage{"input": input}}
			for range 2 {
				observeActivityRequest(t, proxy, thread, map[bool]string{false: "/root", true: "/root/worker"}[thread != "main"], request)
				applyObservedActivity(&u.attached, u.shell, &u.Client, &u.streams, u.usage, u.review, &u.approvals, &u.pendingEditReceipts, u.clock, u.restoring, u.audience)
			}
			if !bytes.Equal(original, request.Fields["input"]) {
				t.Fatal("stock host evidence changed")
			}
			full := "exec script failed: " + detail
			if len(view.Entries) != 1 || view.Entries[0].Source.ErrorDetail != full || view.Entries[0].Source.Text != activityui.ErrorPreview(full) {
				t.Fatal("full evidence or bounded preview lost, or repeated failure duplicated")
			}
			if child {
				view.setDisplayFilter(true, "/root/worker")
			}
			feed := view.RenderFeed(80, 40)
			row := slices.IndexFunc(feed.Snippets, func(s activityui.Snippet) bool { return s != activityui.Snippet{} })
			if row < 0 || !terminalTest(u).openOutput(view.View, feed.Snippets[row]) {
				t.Fatal("error click target cannot open shared dialog")
			}
			assertErrorDialog(t, terminalTest(u), textsanitize.Safe(full, false))
			terminalTest(u).outputKey("q")
			terminalTest(u).Focus = focus
			u.Text = "keep the user's draft"
			for _, key := range []byte{2, '!'} {
				if err := terminalTest(u).key(key); err != nil {
					t.Fatal(err)
				}
			}
			if u.Text != "keep the user's draft" || terminalTest(u).Focus != focus || terminalTest(u).Dialog == nil {
				t.Fatal("keyboard detail navigation modified composer or failed to open")
			}
			assertErrorDialog(t, terminalTest(u), textsanitize.Safe(full, false))
		})
	}
}

func assertErrorDialog(t *testing.T, u *terminalUI, full string) {
	t.Helper()
	drawOutputDialog(u)
	if u.Dialog.Document.Text != full {
		t.Fatal("dialog copy target is not the complete sanitized error")
	}
	u.outputKey("/")
	u.outputKey("forced cleanup")
	u.outputKey("\r")
	if frame := drawOutputDialog(u); strings.Contains(frame, "no match") || !strings.Contains(frame, "forced cleanup") {
		t.Fatal("could not search the decisive suffix beyond the inline preview")
	}
	u.outputKey("y")
	want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(full)) + "\x07"
	if u.Frame.Clipboard != want {
		t.Fatal("whole-error copy dropped retained evidence")
	}
}

func TestErrorDetailsRestoredRollout(t *testing.T) {
	for _, child := range []bool{false, true} {
		t.Run(map[bool]string{false: "main", true: "child"}[child], func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			u.Startup.Thread = "main"
			u.shell.Agents = terminalTest(u).Agents
			detail := "CreateProcess: " + strings.Repeat("command ", 9000) + "\nrejected: forced cleanup is not permitted\n    at cell:17"
			records := append([]map[string]any{rolloutRecord(1, "event_msg", map[string]any{"type": "task_started", "turn_id": "t"})}, rolloutFailedCell(2, "rejected", detail)...)
			thread := "main"
			if child {
				thread = "child"
				u.Thread = thread
				u.startSession(thread, u.attached.cwd)
			}
			info := binding.ThreadInfo{ID: thread, Cwd: u.attached.cwd, Path: writeTestRollout(t, thread, records...), Turns: []binding.HistoryTurn{{ID: "t", Status: "failed"}}}
			if err := u.restoreContent(info); err != nil {
				t.Fatal(err)
			}
			view := u.testActivity(u.shell.Transcript)
			full := "exec script failed: " + detail
			index := slices.IndexFunc(view.Entries, func(e sessionpresentation.ActivityRecord) bool {
				return e.Source.Kind == "error" && e.Source.CallID == "rejected"
			})
			if index < 0 || view.Entries[index].Source.ErrorDetail != full || view.Entries[index].Source.Text != activityui.ErrorPreview(full) {
				t.Fatal("restoration clipped the full host error")
			}
			if !terminalTest(u).openEntry(view.bind(terminalTest(u)), view.Entries[index].Source.Seq) {
				t.Fatal("restored error has no dialog")
			}
			assertErrorDialog(t, terminalTest(u), full)
		})
	}
}

func TestErrorDetailsKeyboardPaginationAndFiltering(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.testActivity(u.shell.Agents).setDisplayFilter(true, "/root/worker")
	u.testActivity(u.shell.Agents).Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{
		{Seq: 1, Agent: "/root/a", Kind: "error", Text: "old error"},
		{Seq: 2, Agent: "/root/b", Kind: "error", Text: "other agent"},
		{Seq: 3, Agent: "/root/a", Kind: "error", Text: "new error"},
	}})
	u.testActivity(u.shell.Agents).setDisplayFilter(true, "/root/a")
	terminalTest(u).openErrors()
	if terminalTest(u).Dialog == nil || len(terminalTest(u).Dialog.Pages) != 2 || terminalTest(u).Dialog.Page != 1 {
		t.Fatal("keyboard error details lost selection or filter")
	}
	drawOutputDialog(terminalTest(u))
	if terminalTest(u).Dialog.Document.Text != "new error" {
		t.Fatal("keyboard did not open newest error")
	}
	terminalTest(u).outputKey("\x1b[D")
	drawOutputDialog(terminalTest(u))
	if terminalTest(u).Dialog.Document.Text != "old error" {
		t.Fatal("could not navigate to earlier error")
	}
}

func TestErrorDetailsSearchAcrossVisualWraps(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	query := strings.Repeat("q", 80) + " forced cleanup"
	full := strings.Repeat("prefix ", 160) + query + strings.Repeat(" tail", 80) + "\n" + query
	u.testActivity(u.shell.Transcript).Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{Seq: 1, Agent: "Main", Kind: "error", Text: full}}})
	if !terminalTest(u).openEntry(u.shell.Transcript, 1) {
		t.Fatal("could not open error")
	}
	drawOutputDialog(terminalTest(u))
	terminalTest(u).outputKey("/")
	terminalTest(u).outputKey(query)
	terminalTest(u).outputKey("\r")
	first := drawOutputDialog(terminalTest(u))
	if !strings.Contains(first, "qqqq") {
		t.Fatal("matched text was not scrolled into view")
	}
	terminalTest(u).outputKey("n")
	if next := drawOutputDialog(terminalTest(u)); next == first || !strings.Contains(next, "qqqq") {
		t.Fatal("next did not reach the next logical match")
	}
	terminalTest(u).outputKey("N")
	if drawOutputDialog(terminalTest(u)) != first {
		t.Fatal("previous did not return to the wrapped match")
	}
	assertErrorDialog(t, terminalTest(u), full)
}

func TestErrorDetailsTerminalClick(t *testing.T) {
	for _, child := range []bool{false, true} {
		t.Run(map[bool]string{false: "main", true: "child"}[child], func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			view, agent := u.testActivity(u.shell.Transcript), "Main"
			if child {
				view, agent = u.testActivity(u.shell.Agents), "/root/worker"
				u.testActivity(u.shell.Agents).setDisplayFilter(true, "/root/worker")
			}
			full := "Failed: " + strings.Repeat("diagnostic ", 40) + "\nrejected: forced cleanup is not permitted"
			view.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{Seq: 1, Agent: agent, Kind: "error", Text: full}}})
			u.Text = "preserve draft"
			if child {
				u.testActivity(u.shell.Agents).setDisplayFilter(true, agent)
			}
			screen := vt.NewEmulator(120, 40)
			defer screen.Close()
			if err := u.testPaint(screen, 120, 40); err != nil {
				t.Fatal(err)
			}
			row := slices.IndexFunc(view.FeedSnippets, func(s activityui.Snippet) bool { return s != activityui.Snippet{} })
			if row < 0 {
				t.Fatal("painted error lacks pointer target")
			}
			pane := terminalTest(u).Layout.Main
			if child {
				pane = terminalTest(u).Layout.Main
			}
			x, y := pane.X+view.FeedLeft, pane.Y+view.FeedTop+row
			offset, following := view.Offset, view.Following
			for _, suffix := range []string{"M", "m"} {
				if err := terminalTest(u).mouse(fmt.Sprintf("\x1b[<0;%d;%d%s", x, y, suffix)); err != nil {
					t.Fatal(err)
				}
			}
			if terminalTest(u).Dialog == nil || u.Text != "preserve draft" || view.Offset != offset || view.Following != following {
				t.Fatal("terminal click did not open details without disturbing the pane")
			}
			assertErrorDialog(t, terminalTest(u), full)
		})
	}
}

func TestUISnapshotErrorDetailsConversation(t *testing.T) {
	view := newActivityAdapter()
	view.Conversation = true
	view.testAudience()
	view.Clock = func() time.Time { return time.Date(2026, 10, 2, 12, 40, 21, 0, time.Local) }
	view.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{Seq: 1, Agent: "Main", Kind: "error", Observed: view.Now(),
		Text: "exec script failed", ErrorDetail: "exec script failed: exec_command failed: CreateProcess { message: Rejected(\"" + strings.Repeat("escaped command ", 30) + "\") }\nrejected: forced cleanup is not permitted"}}})
	assertNativeUISnapshot(t, "error-details-conversation", view.RenderFeed(80, 20).Lines)
}
