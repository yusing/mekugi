package router

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

func TestNativeReplyExcerptOpensExactActivityMessage(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.turn = "main-turn"
	u.view.applyAppServerItem("main", "main", u.turn, "progress", "item/completed", "", appServerItem{Type: "agentMessage", Text: "Checking the requested reply."})
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
	u.shell.side = false
	if err := u.paint(&bytes.Buffer{}, 120, 28); err != nil {
		t.Fatal(err)
	}
	main := ansi.Strip(strings.Join(u.view.renderFeed(110, 50).lines, "\n"))
	if strings.Contains(main, "End of requested reply") || !strings.Contains(main, "↩ Open reply in Activity") {
		t.Fatalf("Main did not collapse the actual message: %s", main)
	}
	// Restore the painted geometry after inspecting the complete feed.
	if err := u.paint(&bytes.Buffer{}, 120, 28); err != nil {
		t.Fatal(err)
	}
	var seq, activity uint64
	for _, entry := range u.view.entries {
		if entry.native != nil && entry.native.item == "target\x00main" {
			seq, activity = entry.Seq, entry.activitySeq
		}
	}
	if seq == 0 || activity == 0 {
		t.Fatal("reply has no cross-pane identity")
	}
	clicked := false
	for row, target := range u.view.feedQuestions {
		if target != seq {
			continue
		}
		x := u.shell.layout.codex.x + u.view.feedLeft + 1
		y := u.shell.layout.codex.y + u.view.feedTop + row
		if err := u.shell.mouse(fmt.Sprintf("\x1b[<0;%d;%dM", x, y)); err != nil {
			t.Fatal(err)
		}
		if err := u.shell.mouse(fmt.Sprintf("\x1b[<0;%d;%dm", x, y)); err != nil {
			t.Fatal(err)
		}
		clicked = true
		break
	}
	if !clicked || !u.shell.activityOpen || !u.shell.side || u.shell.diffOpen || u.shell.focus != 2 {
		t.Fatalf("reply link did not activate Activity: clicked=%v seq=%d links=%v side=%v activity=%v diff=%v focus=%d pending=%d", clicked, seq, u.view.feedQuestions, u.shell.side, u.shell.activityOpen, u.shell.diffOpen, u.shell.focus, u.agents.pendingTarget)
	}
	screen := vt.NewEmulator(120, 28)
	defer screen.Close()
	u.shell.paintedRows = nil
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
	if err := u.paint(slave, 120, 28); err != nil {
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
	if u.agents.following || u.agents.selected != "/root/worker" || u.agents.flashQuestion != activity || !strings.Contains(screen.String(), "The requested reply.") {
		t.Fatalf("jump did not reach exact reply: offset=%d target=%d\n%s", u.agents.offset, u.agents.questionRows[activity], screen.String())
	}
	full := ansi.Strip(strings.Join(u.agents.renderFeed(80, 100).lines, "\n"))
	if !strings.Contains(full, "End of requested reply.") {
		t.Fatal("Activity lost the full message")
	}
}

func TestNativeUnretainedQuestionUsesActualText(t *testing.T) {
	v := newLiveActivityView()
	var out conversationLines
	v.journalReplyContext(&out, activityui.AnswerGroup{Question: "Which exact assignment?", Target: 99}, 0, "", 100)
	if len(out.lines) != 2 || !strings.Contains(ansi.Strip(out.lines[0]), "not loaded") || ansi.Strip(out.lines[1]) != "▎ Which exact assignment?" {
		t.Fatalf("missing original lost its quoted excerpt: %q", out.lines)
	}
	for _, target := range out.questions {
		if target != 0 {
			t.Fatal("unretained question acquired a navigation target")
		}
	}
}

func TestNativeAssignmentExcerptWrapsAndRetainsTarget(t *testing.T) {
	v := newLiveActivityView()
	var out conversationLines
	v.replyContext(&out, activityPaneEntry{Seq: 42, Kind: "assignment", assignment: &activityAssignment{text: "Review the response and verify the assignment excerpt wraps without losing its navigation target."}}, "│ ", 36)
	v.replyExcerpt(&out, activityPaneEntry{Seq: 43}, "The response excerpt remains visible.", "│ ", "│ ", 36, conversationLatestRows)
	if len(out.lines) < 5 || !strings.HasPrefix(ansi.Strip(out.lines[1]), "│ ▎ Review") || !strings.HasPrefix(ansi.Strip(out.lines[2]), "│ ▎ ") {
		t.Fatalf("assignment was not separately quoted and wrapped: %q", out.lines)
	}
	if !strings.HasSuffix(ansi.Strip(out.lines[2]), "…") || strings.TrimSpace(ansi.Strip(out.lines[2])) == "│ ▎ …" {
		t.Fatalf("assignment ellipsis must end the last excerpt line: %q", out.lines)
	}
	for i, line := range out.lines {
		if ansi.StringWidth(line) > 38 {
			t.Fatalf("row %d overflows: %q", i, line)
		}
		if i < 3 && out.questions[i] != 42 {
			t.Fatalf("assignment row %d lost its target", i)
		}
	}
	if !strings.Contains(ansi.Strip(strings.Join(out.lines, "\n")), "The response excerpt") || out.questions[len(out.questions)-1] != 43 {
		t.Fatal("response excerpt or its Activity target was lost")
	}
}

func TestLiveActivitySkillNamesBold(t *testing.T) {
	v := newLiveActivityView()
	for _, operand := range []string{"golang-best-practices", "golang-best-practices/references/api.md 1:20", "run golang-best-practices/scripts/check.sh --all", "run  golang-best-practices/scripts/check.sh", "run\tgolang-best-practices/scripts/check.sh"} {
		command := "skills-mgr get " + operand
		if strings.HasPrefix(operand, "run ") || strings.HasPrefix(operand, "run\t") {
			command = "skills-mgr " + operand
		}
		classified := toolActivityShell(command)
		block := activityui.ParseOperation(classified)
		if block.Verb != "Skill" {
			t.Fatalf("skill command misclassified: %q", classified)
		}
		rendered := strings.Join(v.painter.Block(block, 120), "\n")
		if !strings.Contains(rendered, "\x1b[1mgolang-best-practices"+activityui.Undim) || !strings.Contains(ansi.Strip(rendered), operand) {
			t.Fatalf("skill name not bold or operand changed: %q", rendered)
		}
	}
}

func TestNativeReplyExcerptEllipsisStaysInline(t *testing.T) {
	v := newLiveActivityView()
	var out conversationLines
	v.replyExcerpt(&out, activityPaneEntry{Seq: 43}, "First response line.\nSecond response line.\nThird response line.", "│ ", "│ ", 24, conversationEarlierRows)
	if len(out.lines) != 3 || ansi.Strip(out.lines[1]) != "│ Second response line.…" {
		t.Fatalf("response ellipsis did not stay inline: %q", out.lines)
	}
	for _, line := range out.lines {
		if ansi.StringWidth(line) > 26 {
			t.Fatalf("excerpt overflow: %q", line)
		}
	}
}

func TestNativeExcerptEllipsisSkipsParagraphGaps(t *testing.T) {
	v := newLiveActivityView()
	var out conversationLines
	v.replyContext(&out, activityPaneEntry{Seq: 42, Kind: "assignment", assignment: &activityAssignment{text: "Fix the finding.\n\nThen rerun the tests.\n\nReport back."}}, "", 40)
	v.replyExcerpt(&out, activityPaneEntry{Seq: 43}, "APPROVE. No remaining actionable findings.\n\nVerified with make test.\n\nDone.", "", "", 60, conversationEarlierRows)
	got := make([]string, len(out.lines))
	for i, line := range out.lines {
		got[i] = ansi.Strip(line)
	}
	want := []string{
		got[0],
		"▎ Fix the finding.",
		"▎ Then rerun the tests.…",
		"APPROVE. No remaining actionable findings.",
		"Verified with make test.…",
		"↩ Open reply in Activity · +1 line",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("excerpt rows:\n got %q\nwant %q", got, want)
	}
}

func TestNativeRepliesShareHeaderQuoteAnswerLayout(t *testing.T) {
	for _, width := range []int{32, 90} {
		for _, kind := range []string{"main raw", "main journal", "agent raw", "agent journal", "agent excerpt"} {
			t.Run(fmt.Sprintf("%s/%d", kind, width), func(t *testing.T) {
				v := newLiveActivityView()
				question := activityPaneEntry{Seq: 1, Agent: "You", Kind: "text", Text: "Original request.\n\nSecond detail.\n\nThird detail.", Observed: time.Now()}
				entry := activityPaneEntry{Seq: 2, Agent: "Main", Kind: "text", Text: "Answer body.", native: &liveActivityNativeItem{question: 1}}
				block := activityui.Block{Kind: "text", Body: entry.Text}
				if strings.HasPrefix(kind, "agent") {
					question.Kind = "assignment"
					question.assignment = &activityAssignment{to: "/root/worker", text: question.Text}
					entry.Agent, entry.Kind, block.Kind = "/root/worker", "final", "final"
				}
				if strings.Contains(kind, "journal") || kind == "agent excerpt" {
					entry.journal = &journalItem{}
					block.Journal = &activityui.Journal{Groups: []activityui.AnswerGroup{{Question: question.Text, Target: 1, Answers: []activityui.Answer{{Text: entry.Text}}}}}
				}
				if kind == "agent excerpt" {
					entry.activitySeq = 5
				}
				v.entries = []activityPaneEntry{question, entry}
				v.blocks = [][]activityui.Block{nil, {block}}
				run := v.conversationItem(1, 1, width, conversationThread{})
				if len(run.lines) < 5 {
					t.Fatalf("missing reply rows: %q", run.lines)
				}
				for i, line := range run.lines {
					plain := ansi.Strip(line)
					if ansi.StringWidth(line) > width {
						t.Fatalf("row %d exceeds width: %q", i, plain)
					}
					if i >= 1 && i <= 3 && run.questions[i] != question.Seq {
						t.Fatalf("context row %d lost navigation", i)
					}
				}
				if !strings.Contains(ansi.Strip(run.lines[1]), "↩ re:") || strings.Contains(ansi.Strip(run.lines[1]), "Original") || !strings.HasSuffix(ansi.Strip(run.lines[2]), "▎ Original request.") || !strings.HasSuffix(ansi.Strip(run.lines[3]), "▎ Second detail.…") || !strings.HasSuffix(ansi.Strip(run.lines[4]), "Answer body.") {
					t.Fatalf("expected header, quoted excerpt, answer: %q", run.lines)
				}
			})
		}
	}
}
