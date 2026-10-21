package launch

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/libraries/diffrender"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

const childReplySummaryBody = "## Summary\nFixed exact reply navigation and kept the original evidence available. Focused checks pass, while live model compliance remains unverified.\n\n## Details\n" + "The supporting explanation stays in the retained reply.\n" + "Second detail with **emphasis**.\nThird detail.\nFourth detail.\nFifth detail.\nEnd of evidence."

func TestUISnapshotChildReplySummary(t *testing.T) {
	for _, kind := range []string{"directed", "final"} {
		t.Run(kind, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			u.shell.Transcript.ChildReplySummary = true
			u.shell.Transcript.Painter.Theme = diffrender.DarkTheme
			appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
			send := func(id, body string) {
				item := map[string]any{"id": id, "type": "agentMessage", "phase": "final_answer", "text": body}
				if kind == "directed" {
					item = map[string]any{"id": id, "type": "collabAgentToolCall", "tool": "sendMessage", "status": "completed", "senderThreadId": "child", "receiverThreadIds": []string{"main"}, "prompt": body}
				}
				appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "child-turn", "item": item})
			}
			send("earlier", "An unrelated reply.")
			send("target", childReplySummaryBody)
			send("target", childReplySummaryBody)
			var seq uint64
			count := 0
			for i, record := range u.attached.source.Entries {
				if record.Source.Native != nil && strings.HasPrefix(record.Source.Native.Item, "target") {
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
				assertNativeUISnapshot(t, fmt.Sprintf("child-reply-summary-%s-%d", kind, width), feed.Lines)
			}
			u.shell.Transcript.Expansion = 1
			expanded := ansi.Strip(strings.Join(u.testActivity(u.shell.Transcript).RenderFeed(100, 80).Lines, "\n"))
			if !strings.Contains(expanded, "End of evidence.") {
				t.Fatal("expanded disclosure lost Details")
			}
			u.shell.Transcript.Expansion = 0
			u.shell.Transcript.ChildReplySummary = false
			u.shell.Transcript.InvalidateRuns(0)
			feed := u.testActivity(u.shell.Transcript).RenderFeed(100, 80)
			if !strings.Contains(ansi.Strip(strings.Join(feed.Lines, "\n")), "+") {
				t.Fatal("disabled summary did not use excerpt")
			}
			assertNativeUISnapshot(t, "child-reply-summary-"+kind+"-disabled", feed.Lines)
			u.shell.Transcript.ChildReplySummary = true
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
			terminalTest(u).Frame.Rows = nil
			if err := u.testPaint(&frame, 100, 28); err != nil {
				t.Fatal(err)
			}
			screen := vt.NewEmulator(100, 28)
			defer screen.Close()
			if _, err := screen.Write(frame.Bytes()); err != nil {
				t.Fatal(err)
			}
			assertNativeUISnapshot(t, "child-reply-summary-"+kind+"-dialog", strings.Split(screen.String(), "\n"))
			terminalTest(u).outputKey("\x1b")
			if terminalTest(u).Dialog != nil {
				t.Fatal("dialog did not dismiss")
			}
		})
	}
}

func TestUISnapshotChildReplySummaryFallback(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.shell.Transcript.ChildReplySummary = true
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

func TestChildReplySummaryDeliveredSupplement(t *testing.T) {
	audience := sessioncontrol.NewAudience(nil)
	audience.Attach("main")
	audience.Observe("child", "main", "/root/worker", true)
	audience.Collect(activitypresentation.Supplement{Thread: "child", Source: "delivered", Kind: "reply", Text: childReplySummaryBody,
		Message: &activitypresentation.SupplementMessage{From: "/root/worker", To: "/root", Text: childReplySummaryBody}})
	entries := takeNativeActivity(audience, "main")
	if len(entries) != 1 {
		t.Fatalf("delivered entries = %d", len(entries))
	}
	entry := entries[0]
	values := sessionpresentation.ActivityContent(entry).Values()
	if len(values) != 1 || values[0].ReplySummary == "" || values[0].Body != childReplySummaryBody {
		t.Fatalf("completed recipient input lost summary/original: %+v", values)
	}
	entry.Native.Phase = "item/started"
	if sessionpresentation.ActivityContent(entry).Values()[0].ReplySummary != "" {
		t.Fatal("in-flight body interpreted as complete")
	}
}
