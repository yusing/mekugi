package launch

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

const childReplySummaryBody = "## Summary\nFixed exact reply navigation and kept the original evidence available. Focused checks pass, while live model compliance remains unverified.\n\n## Details\n" + "The supporting explanation stays in the retained reply.\n" + "Second detail with **emphasis**.\nThird detail.\nFourth detail.\nFifth detail.\nEnd of evidence."

func TestUISnapshotChildReplySummary(t *testing.T) {
	for _, kind := range []string{"directed", "final", "native-directed", "native-final"} {
		t.Run(kind, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			u.shell.Transcript.ChildReplySummary, u.shell.Agents.ChildReplySummary = true, true
			u.shell.Transcript.Painter.Theme = diffrender.DarkTheme
			appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
			send := func(id, body string) {
				if strings.HasPrefix(kind, "native-") {
					delivery := "MESSAGE"
					if kind == "native-final" {
						delivery = "FINAL_ANSWER"
					}
					publishNativeDelivery(&u.attached, control.NativeInput{Thread: "main", ID: id, Sender: "/root/worker", Recipient: "/root", Kind: delivery, Message: body}, time.Now())
					return
				}
				var item any = map[string]any{"id": id, "type": "agentMessage", "phase": "final_answer", "text": body}
				if kind == "directed" {
					item = coordinationTestCall(t, id, "send_message", "main", u.attached.session.Caller("main"), body)
				}
				appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "child-turn", "item": item})
			}
			send("earlier", "An unrelated reply.")
			send("target", childReplySummaryBody)
			send("target", childReplySummaryBody)
			var seq uint64
			count := 0
			for i, record := range u.attached.source.Entries {
				if record.Source.Text == childReplySummaryBody {
					count++
					seq = record.Source.Seq
					if record.Source.ActivitySeq == 0 || record.Values[0].ReplySummary == "" {
						t.Fatalf("missing summary/identity: %#v", record)
					}
				}
				u.attached.source.Mutate(i, func(record *sessionpresentation.ActivityRecord) {
					record.Source.Observed = time.Date(2026, 10, 6, 11, 50, i, 0, time.Local)
				})
			}
			if count != 1 {
				t.Fatalf("target observations = %d", count)
			}
			for _, width := range []int{100, 32} {
				feed := u.testActivity(u.shell.Transcript).RenderFeed(width, 80)
				plain := ansi.Strip(strings.Join(feed.Lines, "\n"))
				if strings.Contains(plain, "supporting explanation") || strings.Contains(plain, "+") || !strings.Contains(plain, "unverified.") || !strings.Contains(plain, "↩ Open reply") {
					t.Fatalf("summary card: %s", plain)
				}
				assertNativeUISnapshot(t, fmt.Sprintf("child-reply-summary-%s-%d", strings.TrimPrefix(kind, "native-"), width), feed.Lines)
			}
			u.shell.Transcript.Expansion = 1
			expanded := ansi.Strip(strings.Join(u.testActivity(u.shell.Transcript).RenderFeed(100, 80).Lines, "\n"))
			if !strings.Contains(expanded, "End of evidence.") || strings.Count(expanded, "Fixed exact reply navigation") != 1 || strings.Contains(expanded, "Summary") || strings.Contains(expanded, "Details") {
				t.Fatal("expanded disclosure lost Details")
			}
			u.shell.Transcript.Expansion = 0
			u.shell.Transcript.ChildReplySummary, u.shell.Agents.ChildReplySummary = false, false
			u.shell.Transcript.InvalidateRuns(0)
			feed := u.testActivity(u.shell.Transcript).RenderFeed(100, 80)
			if !strings.Contains(ansi.Strip(strings.Join(feed.Lines, "\n")), "+") {
				t.Fatal("disabled summary did not use excerpt")
			}
			assertNativeUISnapshot(t, "child-reply-summary-"+strings.TrimPrefix(kind, "native-")+"-disabled", feed.Lines)
			if !terminalTest(u).openActivityReply(seq) {
				t.Fatal("cannot open disabled-summary reply")
			}
			terminalTest(u).Dialog.Layout(80)
			if !strings.Contains(ansi.Strip(terminalTest(u).Dialog.Document.Lines[0].Text), "Summary") {
				t.Fatal("disabled feature lost original reply headings")
			}
			terminalTest(u).outputKey("\x1b")
			u.shell.Transcript.ChildReplySummary, u.shell.Agents.ChildReplySummary = true, true
			u.shell.Transcript.InvalidateRuns(0)
			if err := u.testPaint(&bytes.Buffer{}, 100, 28); err != nil {
				t.Fatal(err)
			}
			if !terminalTest(u).openActivityReply(seq) {
				t.Fatal("cannot open exact reply")
			}
			if err := u.testPaint(&bytes.Buffer{}, 100, 28); err != nil {
				t.Fatal(err)
			}
			if terminalTest(u).Dialog.Document.Text != childReplySummaryBody {
				t.Fatalf("original changed: %q", terminalTest(u).Dialog.Document.Text)
			}
			var frame bytes.Buffer
			u.attached.cwd = "/workspace"
			terminalTest(u).Frame.Rows = nil
			if err := u.testPaint(&frame, 100, 28); err != nil {
				t.Fatal(err)
			}
			screen := vt.NewEmulator(100, 28)
			defer screen.Close()
			if _, err := screen.Write(frame.Bytes()); err != nil {
				t.Fatal(err)
			}
			if rendered := screen.String(); strings.Count(rendered, "Fixed exact reply navigation") != 1 || strings.Contains(rendered, "Summary") || strings.Contains(rendered, "Details") || !strings.Contains(rendered, "End of evidence.") {
				t.Fatalf("reply details repeated summary or delimiters: %s", rendered)
			}
			assertNativeUISnapshot(t, "child-reply-summary-"+strings.TrimPrefix(kind, "native-")+"-dialog", strings.Split(screen.String(), "\n"))
			terminalTest(u).outputKey("\x1b")
			if terminalTest(u).Dialog != nil {
				t.Fatal("dialog did not dismiss")
			}
		})
	}
}

func TestUISnapshotChildReplySummaryFallback(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.shell.Transcript.ChildReplySummary, u.shell.Agents.ChildReplySummary = true, true
	u.shell.Transcript.Painter.Theme = diffrender.DarkTheme
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "child-turn", "item": map[string]any{
		"id": "plain", "type": "agentMessage", "phase": "final_answer", "text": "Plain result.\n\n" + strings.Repeat("Supporting evidence stays available.\n", 6),
	}})
	for i := range u.attached.source.Entries {
		u.attached.source.Mutate(i, func(record *sessionpresentation.ActivityRecord) {
			record.Source.Observed = time.Date(2026, 10, 6, 11, 50, 0, 0, time.Local)
		})
	}
	assertNativeUISnapshot(t, "child-reply-summary-fallback", u.testActivity(u.shell.Transcript).RenderFeed(60, 40).Lines)
}
