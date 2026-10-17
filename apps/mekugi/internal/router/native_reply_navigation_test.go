package router

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/terminalui"

	activitymodel "github.com/yusing/mekugi/modules/activity/feed"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func TestNativeReplyExcerptOpensExactActivityMessage(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.Turn = "main-turn"
	applyTranscriptFixture(u.testActivity(u.view), true, "", "main", "main", u.Turn, "progress", "item/completed", "", binding.ThreadItem{Type: "agentMessage", Text: "Checking the requested reply."})
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
	message := "The requested reply.\n\n" + strings.Repeat("More detail about this reply.\n", 16) + "End of requested reply."
	send := func(id, body string) {
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "child-turn", "item": map[string]any{
			"id": id, "type": "collabAgentToolCall", "tool": "sendMessage", "status": "completed", "senderThreadId": "child", "receiverThreadIds": []string{"main"}, "prompt": body,
		}})
	}
	send("earlier", "An earlier unrelated reply.")
	send("target", message)
	for i := range 12 {
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "child-turn", "item": map[string]any{"id": fmt.Sprint("cmd-", i), "type": "commandExecution", "command": "echo later", "status": "completed"}})
	}
	if err := u.testPaint(&bytes.Buffer{}, 120, 28); err != nil {
		t.Fatal(err)
	}
	main := ansi.Strip(strings.Join(u.testActivity(u.view).RenderFeed(110, 50).Lines, "\n"))
	if strings.Contains(main, "End of requested reply") || !strings.Contains(main, "↩ Open reply") {
		t.Fatalf("Main did not collapse the actual message: %s", main)
	}
	// Restore the painted geometry after inspecting the complete feed.
	if err := u.testPaint(&bytes.Buffer{}, 120, 28); err != nil {
		t.Fatal(err)
	}
	var seq, activity uint64
	for _, entry := range u.source.Entries {
		if entry.Source.Native != nil && entry.Source.Native.Item == "target\x00main" {
			seq, activity = entry.Source.Seq, entry.Source.ActivitySeq
		}
	}
	if seq == 0 || activity == 0 {
		t.Fatal("reply has no cross-pane identity")
	}
	clicked := false
	for row, target := range u.view.FeedQuestions {
		if target != seq {
			continue
		}
		x := terminalTest(u).Layout.Main.X + u.view.FeedLeft + 1
		y := terminalTest(u).Layout.Main.Y + u.view.FeedTop + row
		if err := terminalTest(u).mouse(fmt.Sprintf("\x1b[<0;%d;%dM", x, y)); err != nil {
			t.Fatal(err)
		}
		if err := terminalTest(u).mouse(fmt.Sprintf("\x1b[<0;%d;%dm", x, y)); err != nil {
			t.Fatal(err)
		}
		clicked = true
		break
	}
	if !clicked || terminalTest(u).Dialog == nil || terminalTest(u).DiffOpen {
		t.Fatalf("reply link did not open modal: clicked=%v seq=%d links=%v diff=%v", clicked, seq, u.view.FeedQuestions, terminalTest(u).DiffOpen)
	}
	screen := vt.NewEmulator(120, 28)
	defer screen.Close()
	terminalTest(u).Frame.Rows = nil
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	if err := pty.Setsize(master, &pty.Winsize{Cols: 120, Rows: 28}); err != nil {
		t.Fatal(err)
	}
	frames := make(chan []byte, 1)
	go func() {
		var frame []byte
		buffer := make([]byte, 8192)
		for {
			n, err := master.Read(buffer)
			frame = append(frame, buffer[:n]...)
			if bytes.Contains(frame, []byte("\x1b[?2026l")) {
				frames <- frame
				return
			}
			if err != nil {
				return
			}
		}
	}()
	if err := u.testPaint(slave, 120, 28); err != nil {
		t.Fatal(err)
	}
	select {
	case frame := <-frames:
		if _, err := screen.Write(frame); err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("PTY reply frame did not complete")
	}
	if !strings.Contains(screen.String(), "The requested reply.") || !strings.Contains(terminalTest(u).Dialog.Document.Text, "End of requested reply.") || strings.Contains(terminalTest(u).Dialog.Document.Text, "An earlier unrelated reply") {
		t.Fatalf("dialog did not reach exact full reply:\n%s", screen.String())
	}
	terminalTest(u).outputKey("\x1b")
	if terminalTest(u).Dialog != nil {
		t.Fatal("closing reply changed panes")
	}
}

func TestLiveActivitySkillNamesBold(t *testing.T) {
	v := newActivityAdapter()
	for _, operand := range []string{"golang-best-practices", "golang-best-practices/references/api.md 1:20", "run golang-best-practices/scripts/check.sh --all", "run  golang-best-practices/scripts/check.sh", "run\tgolang-best-practices/scripts/check.sh"} {
		command := "skills-mgr get " + operand
		if strings.HasPrefix(operand, "run ") || strings.HasPrefix(operand, "run\t") {
			command = "skills-mgr " + operand
		}
		classified := toolActivityShell(command)
		block := activityui.Block{Content: activitymodel.ParseOperation(classified)}
		if block.Verb != "Skill" {
			t.Fatalf("skill command misclassified: %q", classified)
		}
		rendered := strings.Join(v.Painter.Block(block, 120), "\n")
		if !strings.Contains(rendered, "\x1b[1mgolang-best-practices"+terminalui.Undim) || !strings.Contains(ansi.Strip(rendered), operand) {
			t.Fatalf("skill name not bold or operand changed: %q", rendered)
		}
	}
}

func TestNativeRepliesShareHeaderQuoteAnswerLayout(t *testing.T) {
	for _, width := range []int{32, 90} {
		for _, kind := range []string{"main raw", "agent raw", "agent excerpt"} {
			t.Run(fmt.Sprintf("%s/%d", kind, width), func(t *testing.T) {
				v := newActivityAdapter()
				v.Conversation = true
				v.testAudience()
				question := sessionpresentation.ActivityEntry{Seq: 1, Agent: "You", Kind: "text", Text: "Original request.\n\nSecond detail.\n\nThird detail.", Observed: time.Now()}
				entry := sessionpresentation.ActivityEntry{Seq: 2, Agent: "Main", Kind: "text", Text: "Answer body.", Native: &sessionpresentation.NativeItem{Question: 1}}
				block := activityui.Block{Kind: "text", Body: entry.Text}
				if strings.HasPrefix(kind, "agent") {
					question.Kind = "assignment"
					question.Assignment = &sessionpresentation.ActivityAssignment{To: "/root/worker", Text: question.Text}
					entry.Agent, entry.Kind, block.Kind = "/root/worker", "final", "final"
				}
				if kind == "agent excerpt" {
					entry.ActivitySeq = 5
				}
				v.Entries = []sessionpresentation.ActivityRecord{
					{Source: question, Values: activityui.Values(nil)},
					{Source: entry, Values: activityui.Values([]activityui.Block{block})},
				}
				run := v.testConversationItem(1, 1, width, activityui.ConversationThread{})
				if len(run.Lines) < 5 {
					t.Fatalf("missing reply rows: %q", run.Lines)
				}
				for i, line := range run.Lines {
					plain := ansi.Strip(line)
					if ansi.StringWidth(line) > width {
						t.Fatalf("row %d exceeds width: %q", i, plain)
					}
					if i >= 1 && i <= 3 && run.Questions[i] != question.Seq {
						t.Fatalf("context row %d lost navigation", i)
					}
				}
				answerRow := 4
				if kind == "main raw" {
					answerRow++
					if run.Questions[4] != 0 {
						t.Fatal("Main's reply boundary gained a navigation target")
					}
				}
				if !strings.Contains(ansi.Strip(run.Lines[1]), "↩ re:") || strings.Contains(ansi.Strip(run.Lines[1]), "Original") || !strings.HasSuffix(ansi.Strip(run.Lines[2]), "▎ Original request.") || !strings.HasSuffix(ansi.Strip(run.Lines[3]), "▎ Second detail.…") || !strings.HasSuffix(ansi.Strip(run.Lines[answerRow]), "Answer body.") {
					t.Fatalf("expected header, quoted excerpt, answer: %q", run.Lines)
				}
			})
		}
	}
}
