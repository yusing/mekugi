package launch

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/diffrender"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	changes "github.com/yusing/mekugi/modules/changehistory/presentation"
)

func TestUISnapshotNativeSelectedAgentDiff(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", "/workspace/project")
	u.testMainFrame(120, 24, 0)
	terminal, _ := u.terminalViews.NewAttachment(diffrender.DarkTheme)
	terminal.Width, terminal.Height = 120, 24
	c := &nativeClient{renderer: nativeClientRenderer{terminal: terminal, views: u.terminalViews}}
	publication := changes.Publication{}
	var captures []changes.Chunk
	for i, caller := range []string{"/root", "/root/worker", "/root/other"} {
		name := fmt.Sprintf("file%d.go", i)
		captures = append(captures, changes.Chunk{Key: name, Workspace: u.attached.cwd, CaptureOrder: uint64(i + 1), Origin: changes.Origin{Caller: caller, Change: name}, Review: diffmodel.RenderReviewFile("", filepath.Join(u.attached.cwd, name), "", "package selected\n// "+caller+" edit\n")})
	}
	publication = publication.With(changes.GroupCaptures(captures))
	c.event.Frame = sessionapi.NativeFrame{View: u.attached.presentation.View(), Review: &changes.ReviewView{Revision: 1, Workspace: u.attached.cwd, Publication: publication}}
	now := time.Unix(100, 0)
	for _, caller := range []string{"/root/worker", "/root/other", "/root/empty", "/root"} {
		c.event.Frame.View.Displayed = sessionpresentation.Thread{ID: caller, Caller: caller}
		c.refreshView(now)
		rows, err := terminal.Diff.Paint(t.Context(), terminal.Auxiliary, 100, 12)
		if err != nil {
			t.Fatal(err)
		}
		plain := ansi.Strip(strings.Join(rows, "\n"))
		if caller != "/root" && caller != "/root/empty" && !strings.Contains(plain, caller+" edit") {
			t.Fatalf("selected evidence absent for %s:\n%s", caller, plain)
		}
		if caller != "/root" && strings.Contains(plain, "file0.go") {
			t.Fatalf("selected agent retained Main's file:\n%s", plain)
		}
		assertNativeUISnapshot(t, "native-selected-agent-diff-"+filepath.Base(caller), rows)
		// Local caller navigation survives ordinary publications. The next
		// displayed-agent change restores its own filter.
		terminal.Auxiliary.Key("0")
		c.event.Frame.View.Version.Revision++
		c.refreshView(now)
		rows, err = terminal.Diff.Paint(t.Context(), terminal.Auxiliary, 100, 12)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(ansi.Strip(strings.Join(rows, "\n")), "file0.go") {
			t.Fatal("ordinary publication discarded the explicit all-callers filter")
		}
	}
}

func TestNativeRosterFocusAndWheel(t *testing.T) {
	for _, selector := range []string{"keyboard", "pointer"} {
		t.Run(selector, func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.startSession("main", t.TempDir())
			u.testMainFrame(120, 24, 0)
			terminal, _ := u.terminalViews.NewAttachment(diffrender.DarkTheme)
			terminal.Width, terminal.Height = 120, 24
			c := &nativeClient{renderer: nativeClientRenderer{terminal: terminal, views: u.terminalViews}}
			roster := &activity.Publication{}
			for i := range 20 {
				caller := fmt.Sprintf("/root/agent-%02d", i)
				if i == 0 {
					caller = "/root"
				}
				roster.Roster = append(roster.Roster, activity.AgentRow{Agent: activity.Agent{AgentFacts: activity.AgentFacts{Name: caller, Thread: caller}}})
			}
			c.event.Frame = sessionapi.NativeFrame{View: u.attached.presentation.View(), Agents: roster}
			c.editor("main").InsertText("retained draft")
			c.refreshView(time.Unix(100, 0))
			paint := func() {
				t.Helper()
				if _, err := c.renderer.render(t.Context(), io.Discard, time.Unix(100, 0), ""); err != nil {
					t.Fatal(err)
				}
			}
			paint()
			if selector == "keyboard" {
				c.dispatch(terminalsession.Input{Kind: terminalsession.KeyInput, Text: "\x02"})
				c.dispatch(terminalsession.Input{Kind: terminalsession.KeyInput, Text: "3"})
			} else {
				x := strings.Index(ansi.Strip(terminal.Frame.Rows[23]), "3 Agents")
				c.dispatch(terminalsession.Input{Kind: terminalsession.MouseInput, Text: fmt.Sprintf("\x1b[<0;%d;24M", x+1)})
			}
			paint()
			c.dispatch(terminalsession.Input{Kind: terminalsession.KeyInput, Text: "\x1b[B"})
			if terminal.Focus != 3 || len(c.calls) != 1 || c.calls[0].operation.Display == nil || c.calls[0].operation.Display.Thread.Caller != "/root/agent-01" || c.editor("main").Text != "retained draft" {
				t.Fatalf("roster key did not retain focus and navigate: focus=%d calls=%+v", terminal.Focus, c.calls)
			}
			before := terminal.Agents.Hits[0].Agent
			c.dispatch(terminalsession.Input{Kind: terminalsession.MouseInput, Text: fmt.Sprintf("\x1b[<65;5;%dM", terminal.Layout.Roster.Y+2)})
			paint()
			if terminal.Agents.Hits[0].Agent == before || terminal.Focus != 3 {
				t.Fatal("roster wheel failed to move visible agents independently")
			}
		})
	}
}
