package launch

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/testsupport/uisnapshot"

	"github.com/charmbracelet/x/ansi"
)

func TestUISnapshotNativeMainQuotedReplyBoundary(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.shell.Transcript.Conversation = true
	u.testActivity(u.shell.Transcript).testAudience()
	u.shell.Transcript.Painter.Theme = diffrender.DarkTheme
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn",
		"item": map[string]any{"id": "prompt", "type": "userMessage", "content": []map[string]string{{"type": "text", "text": "Show me how the new transcript search instruction looks like"}}}})
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn",
		"item": map[string]any{"id": "answer", "type": "agentMessage", "text": "> Read retained output before searching the transcript for exact historical details."}})
	for i := range u.attached.source.Entries {
		u.attached.source.Mutate(i, func(record *sessionpresentation.ActivityRecord) {
			record.Source.Observed = time.Date(2026, 10, 6, 11, 50, 6+i*6, 0, time.Local)
		})
	}
	for _, width := range []int{100, 32} {
		feed := u.testActivity(u.shell.Transcript).RenderFeed(width, 60)
		assertNativeUISnapshot(t, fmt.Sprintf("main-quoted-reply-boundary-%d", width), feed.Lines)
	}
}

func TestNativeActivityKeepsAttachedRootFamilyIsolated(t *testing.T) {
	a := sessioncontrol.NewAudience(nil)
	a.Attach("main")
	a.Observe("child", "main", "/root/worker", true)
	a.Observe("grandchild", "child", "/root/worker/helper", true)
	a.Observe("other", "", "/root", false)
	for _, thread := range []string{"main", "child", "grandchild", "other"} {
		a.Collect(activitypresentation.Supplement{Thread: thread, Source: "reply-" + thread, Kind: "error", Text: "Observed failure from " + thread})
	}
	entries := takeNativeActivity(a, "main")
	if len(entries) != 3 {
		t.Fatalf("attached root family = %+v", entries)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Text, "other") {
			t.Fatalf("borrowed unrelated root: %+v", entries)
		}
	}
	a.Detach()
	a.Attach("other")
	entries = takeNativeActivity(a, "other")
	if len(entries) != 1 || entries[0].Text != "Observed failure from other" {
		t.Fatalf("other root lost its activity: %+v", entries)
	}
}

func TestNativeChildAnswerPromotionRequiresMatchingSuccessfulTurn(t *testing.T) {
	for _, tc := range []struct {
		name, status, completionTurn string
		wantFinal                    bool
	}{
		{"successful", "completed", "child-turn", true},
		{"failed", "failed", "child-turn", false},
		{"interrupted", "interrupted", "child-turn", false},
		{"different turn", "completed", "other-turn", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
			appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "child", "turn": map[string]any{"id": "child-turn"}})
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "child-turn", "item": map[string]any{"id": "note", "type": "agentMessage", "text": childReplySummaryBody}})
			appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "child", "turn": map[string]any{"id": tc.completionTurn, "status": tc.status}})
			finals := 0
			for _, entry := range u.attached.source.Entries {
				if entry.Source.Agent == "/root/worker" && entry.Source.Kind == "final" {
					finals++
					if entry.Values[0].ReplySummary == "" {
						t.Fatal("promoted answer lost its authored summary")
					}
				}
			}
			if (finals == 1) != tc.wantFinal {
				t.Fatalf("final promotion for status %q and turn %q: %d", tc.status, tc.completionTurn, finals)
			}
		})
	}
}

func TestNativeMainAnswerBindsOwnTurnPromptAcrossUpdates(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	user := func(id, turn, message string) {
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": turn,
			"item": map[string]any{"id": id, "type": "userMessage", "content": []map[string]string{{"type": "text", "text": message}}}})
	}
	user("old", "old-turn", "Earlier question")
	user("current", "current-turn", "Current question")
	appServerTestNotify(t, u, "item/agentMessage/delta", map[string]any{"threadId": "main", "turnId": "current-turn", "itemId": "answer", "delta": "First "})
	user("later", "later-turn", "Later question")
	appServerTestNotify(t, u, "item/agentMessage/delta", map[string]any{"threadId": "main", "turnId": "current-turn", "itemId": "answer", "delta": "part"})
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "current-turn", "item": map[string]any{"id": "answer", "type": "agentMessage", "text": "First part"}})
	var current uint64
	for _, entry := range u.attached.source.Entries {
		if entry.Source.Native != nil && entry.Source.Native.Item == "current" {
			current = entry.Source.Seq
		}
	}
	if current == 0 {
		t.Fatal("current user prompt missing")
	}
	answerCount := 0
	for _, entry := range u.attached.source.Entries {
		if entry.Source.Native != nil && entry.Source.Native.Item == "answer" {
			answerCount++
			if entry.Source.Native.Question != current || entry.Source.Text != "First part" {
				t.Fatalf("answer lost its original same-turn prompt: %+v", entry)
			}
		}
	}
	if answerCount != 1 {
		t.Fatalf("delta/completion did not share one answer identity: %d", answerCount)
	}
	u.shell.Transcript.Conversation = true
	u.testActivity(u.shell.Transcript).testAudience()
	feed := u.testActivity(u.shell.Transcript).RenderFeed(90, 40)
	if !strings.Contains(ansi.Strip(strings.Join(feed.Lines, "\n")), "↩ re: your message") {
		t.Fatal("answer has no visible reply link")
	}
	found := false
	for _, target := range feed.Questions {
		found = found || target == current
	}
	if !found {
		t.Fatal("answer link did not target its own turn prompt")
	}
}

func TestNativeMainRepliesQuoteTheirMessageOnce(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.shell.Transcript.Conversation = true
	u.testActivity(u.shell.Transcript).testAudience()
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn",
		"item": map[string]any{"id": "prompt", "type": "userMessage", "content": []map[string]string{{"type": "text", "text": "Fix the build"}}}})
	reply := func(id, text string) {
		t.Helper()
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": map[string]any{"id": id, "type": "agentMessage", "text": text}})
	}
	quotes := func() int {
		t.Helper()
		return strings.Count(ansi.Strip(strings.Join(u.testActivity(u.shell.Transcript).RenderFeed(90, 60).Lines, "\n")), "↩ re: your message")
	}
	reply("first", "Looking at the Makefile.")
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "turn", "item": map[string]any{"id": "cmd", "type": "commandExecution", "command": "make build"}})
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": map[string]any{"id": "cmd", "type": "commandExecution", "command": "make build", "status": "completed", "exitCode": 0}})
	reply("second", "The build passes now.")
	if got := quotes(); got != 1 {
		t.Fatalf("replies separated only by Main's own tools quoted the message %d times", got)
	}
	// Another message between replies makes the next one quote it again.
	u.testActivity(u.shell.Transcript).Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{Seq: u.attached.source.LastSeq + 1, Agent: "Session", Kind: "text", Text: "Blocked on a request"}}})
	reply("third", "Continuing.")
	if got := quotes(); got != 2 {
		t.Fatalf("reply after another message quoted %d times; want 2\n%s", got, ansi.Strip(strings.Join(u.testActivity(u.shell.Transcript).RenderFeed(90, 60).Lines, "\n")))
	}
}

func TestUISnapshotNativeStartedAgentIdentity(t *testing.T) {
	for _, prompt := range []string{"Review the fix.", ""} {
		t.Run(fmt.Sprintf("assigned=%t", prompt != ""), func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			u.clock = func() time.Time { return time.Date(2026, 10, 21, 12, 0, 0, 0, time.Local) }
			appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
			u.attached.session.Agent("/root/worker").Settings.Model = "gpt-effective"
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": coordinationTestCall(t, "spawn", "spawn_agent", "child", u.attached.session.Caller("child"), prompt)})
			var rows []string
			for _, child := range []bool{false, true} {
				view := u.testActivity(u.shell.Transcript)
				if child {
					view = u.testActivity(u.shell.Agents)
					view.setDisplayFilter(true, "/root/worker")
				}
				view.Conversation = true
				view.Painter.Theme = diffrender.DarkTheme
				feed := view.RenderFeed(80, 20)
				plain := ansi.Strip(strings.Join(feed.Lines, "\n"))
				want := "▶ worker started · gpt-effective"
				if prompt != "" {
					want = "▶ main → worker started · gpt-effective"
				}
				if !strings.Contains(plain, want) {
					t.Fatalf("child=%t: start lost recipient identity:\n%s", child, plain)
				}
				rows = append(rows, feed.Lines...)
			}
			uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/started_agent_assigned_%t.txt", prompt != ""), append(rows, "plain after starts"), 80)
		})
	}
}
