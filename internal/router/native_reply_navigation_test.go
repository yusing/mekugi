package router

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
)

func TestNativeReplyExcerptOpensExactActivityMessage(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
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
	label := ansi.Strip(v.questionLinkLabel(liveActivityAnswerGroup{question: "Which exact assignment?"}, 0, 100))
	if label != "↩ re: Which exact assignment?" {
		t.Fatalf("question replaced by a placeholder: %q", label)
	}
}

func TestNativeAssignmentExcerptWrapsAndRetainsTarget(t *testing.T) {
	v := newLiveActivityView()
	var out conversationLines
	v.assignmentExcerpt(&out, activityPaneEntry{Seq: 42, Kind: "assignment", assignment: &activityAssignment{text: "Review the response and verify the assignment excerpt wraps without losing its navigation target."}}, "│ ", 36)
	v.replyExcerpt(&out, activityPaneEntry{Seq: 43}, "The response excerpt remains visible.", "│ ", 36)
	if len(out.lines) < 6 || !strings.HasPrefix(ansi.Strip(out.lines[1]), "│ │ Review") || !strings.HasPrefix(ansi.Strip(out.lines[2]), "│ │ ") {
		t.Fatalf("assignment was not separately quoted and wrapped: %q", out.lines)
	}
	for i, line := range out.lines {
		if ansi.StringWidth(line) > 38 {
			t.Fatalf("row %d overflows: %q", i, line)
		}
		if i < 4 && out.questions[i] != 42 {
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
		block := parseLiveActivityOperation(classified)
		if block.verb != "Skill" {
			t.Fatalf("skill command misclassified: %q", classified)
		}
		rendered := strings.Join(v.painter.block(block, 120), "\n")
		if !strings.Contains(rendered, "\x1b[1mgolang-best-practices"+liveActivityUndim) || !strings.Contains(ansi.Strip(rendered), operand) {
			t.Fatalf("skill name not bold or operand changed: %q", rendered)
		}
	}
}
