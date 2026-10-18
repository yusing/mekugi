package launch

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/binding"

	json "encoding/json/v2"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	"github.com/yusing/mekugi/modules/conversation/composer"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

func TestUISnapshotLoadedSkillsRestoreClickAndReset(t *testing.T) {
	for _, thread := range []string{"main", "child"} {
		t.Run(thread, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			content, _ := json.Marshal([]map[string]string{{"type": "text", "text": attachments.Encode(composer.SkillFrames("council", "/work/council/SKILL.md", "# Council\n"))}})
			info := binding.ThreadInfo{ID: thread, Cwd: u.attached.cwd, AgentNickname: "worker", AgentRole: "worker", Turns: []binding.HistoryTurn{{ID: "t", Status: "completed", Items: []binding.ThreadItem{
				{ID: "old", Type: "commandExecution", Command: "skills-mgr get old", ExitCode: new(0)},
				{ID: "boundary", Type: "contextCompaction"},
				{ID: "attached", Type: "userMessage", Content: content},
				{ID: "skill", Type: "commandExecution", Command: "skills-mgr get commit", Status: "completed", ExitCode: new(0)},
				{ID: "ref", Type: "commandExecution", Command: "skills-mgr get commit/references/example.md", Status: "completed", ExitCode: new(0)},
				{ID: "failed", Type: "commandExecution", Command: "skills-mgr get failed", Status: "failed"},
				{ID: "declined", Type: "commandExecution", Command: "skills-mgr get declined", Status: "declined"},
			}}}}
			view, owner := u.testActivity(u.shell.Transcript), "Main"
			label, body := "2 loaded skills", "- commit\n- council"
			if thread == "child" {
				info.Turns[0].Items = slices.DeleteFunc(info.Turns[0].Items, func(item binding.ThreadItem) bool { return item.ID == "attached" })
				label, body = "1 loaded skill", "- commit"
			}
			if thread == "main" {
				u.restoreHistory(info.Turns)
			} else {
				registerThread(&u.attached.session, info)
				restoreActivityThread(&u.attached, u.shell, u.clock, &u.restoring, u.ctx, u.review, &u.pendingEditReceipts, u.retained, u.retainedSession, u.activity, u.usage, u.segments, &u.restoredSegments, info, false)
				view, owner = u.testActivity(u.shell.Agents), "/root/worker"
			}
			for range liveActivityFeedLimit {
				view.Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{{Seq: u.attached.session.Activity().NextSequence(), Agent: owner, Kind: "text", Text: "note"}}})
			}
			terminalTest(u).Focus = 3
			screen := vt.NewEmulator(120, 28)
			defer screen.Close()
			if err := u.testPaint(screen, 120, 28); err != nil {
				t.Fatal(err)
			}
			if thread == "main" {
				assertNativeUISnapshot(t, "native-main-title-active-skills", strings.Split(screen.String(), "\n")[:1])
			}
			x, y := -1, -1
			for row, text := range strings.Split(screen.String(), "\n") {
				if at := strings.Index(text, label); at >= 0 {
					x, y = ansi.StringWidth(text[:at]), row
				}
			}
			if x < 0 {
				t.Fatalf("confirmed skill count missing after restore and trim: %v", view.ActiveSkills()[owner])
			}
			if thread == "child" {
				assertNativeUISnapshot(t, "native-roster-loaded-skill", []string{strings.Split(screen.String(), "\n")[y]})
			}
			// Hover decorates only the clickable count, and clears on leave or focus loss.
			for _, hover := range []struct {
				event string
				want  bool
			}{
				{fmt.Sprintf("\x1b[<35;%d;%dM", x+1, y+1), true},
				{fmt.Sprintf("\x1b[<35;%d;%dM", x, y+1), false},
				{fmt.Sprintf("\x1b[<35;%d;%dM", x+1, y+1), true},
				{"\x1b[O", false},
			} {
				for _, key := range []byte(hover.event) {
					if err := terminalTest(u).key(key); err != nil {
						t.Fatal(err)
					}
				}
				if err := u.testPaint(screen, 120, 28); err != nil {
					t.Fatal(err)
				}
				for col := x; col < x+len(label); col++ {
					if got := screen.CellAt(col, y).Style.Underline == uv.UnderlineSingle; got != hover.want {
						t.Fatalf("count underline at %d after %q = %t, want %t", col, hover.event, got, hover.want)
					}
				}
				if screen.CellAt(x-1, y).Style.Underline != uv.UnderlineNone {
					t.Fatal("hover underlined the count separator")
				}
			}
			for _, key := range []byte(fmt.Sprintf("\x1b[<0;%d;%dM\x1b[<0;%d;%dm", x+1, y+1, x+1, y+1)) {
				if err := terminalTest(u).key(key); err != nil {
					t.Fatal(err)
				}
			}
			if terminalTest(u).Dialog == nil || len(terminalTest(u).Dialog.Pages) != 1 || terminalTest(u).Dialog.Pages[0].Body != body {
				t.Fatal("rendered count did not open the unique skill names")
			}
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": "live", "item": map[string]any{"id": "reset", "type": "contextCompaction"}})
			if len(view.ActiveSkills()[owner]) != 0 {
				t.Fatalf("skills survived a live context reset: %+v", view.Entries[len(view.Entries)-1].Source)
			}
		})
	}
}
