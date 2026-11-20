package terminalview

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/modules/activity/feed"
	"github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestUISnapshotNativeRosterDisplaySelection(t *testing.T) {
	for _, theme := range []diffrender.Theme{diffrender.TerminalTheme, diffrender.DarkTheme, diffrender.LightTheme} {
		t.Run(fmt.Sprint(theme), func(t *testing.T) {
			v := NewView()
			v.Painter.Theme, v.ChildrenOnly = theme, true
			publication := &presentation.Publication{}
			for _, name := range []string{"/root", "/root/a", "/root/b", "/root/c", "/root/d", "/root/e", "/root/f"} {
				publication.Roster = append(publication.Roster, presentation.AgentRow{Agent: presentation.Agent{AgentFacts: presentation.AgentFacts{Name: name}}})
			}
			v.Selected = "/root/f"
			v.Update(publication, Display{Caller: v.Selected})
			var rows []string
			for _, caller := range []string{v.Selected, "/root"} {
				// Display admission must reach the roster even with the same source publication.
				v.Update(publication, Display{Caller: caller})
				frame := v.RosterSheet(64, 4, time.Unix(100, 0))
				found := false
				for _, hit := range v.Hits {
					found = found || hit.Agent == caller
					selected := strings.Contains(frame[hit.Row-1], theme.SelectionBackground())
					if selected != (hit.Agent == caller) || hit.Agent == "/root" {
						t.Fatalf("wrong selection for %s displaying %s: %q", hit.Agent, caller, frame[hit.Row-1])
					}
				}
				// Main has no sheet row; the remembered child stays a toggle target only.
				if found != (caller != "/root") || v.Selected != "/root/f" {
					t.Fatalf("displayed row or remembered child: caller=%s offset=%d selected=%s", caller, v.RosterOffset, v.Selected)
				}
				rows = append(rows, frame...)
				rows = append(rows, "plain after roster")
			}
			uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/native-roster-display-selection-%d.txt", theme), rows, 64)
		})
	}
}

func TestUISnapshotNativeRosterOneRow(t *testing.T) {
	v := NewView()
	v.Painter.Theme = diffrender.DarkTheme
	publication := &presentation.Publication{}
	for _, name := range []string{"/root", "/root/first", "/root/last"} {
		publication.Roster = append(publication.Roster, presentation.AgentRow{Agent: presentation.Agent{AgentFacts: presentation.AgentFacts{Name: name, Role: "worker"}}})
	}
	var rows []string
	for _, caller := range []string{"/root/last", "/root"} {
		v.Update(publication, Display{Caller: caller})
		want := caller
		if caller == "/root" {
			want = "/root/first"
		}
		frame := v.RosterSheet(64, 1, time.Unix(100, 0))
		if len(frame) != 2 || len(v.Hits) != 1 || v.Hits[0].Agent != want {
			t.Fatalf("one-row roster lost displayed %s: hits=%+v frame=%q", caller, v.Hits, frame)
		}
		rows = append(rows, frame...)
		rows = append(rows, "plain after roster")
	}
	uisnapshot.AssertTerminal(t, "testdata/snapshots/native-roster-one-row.txt", rows, 64)
}

func TestUISnapshotNativeRosterScroll(t *testing.T) {
	v := NewView()
	v.Painter.Theme = diffrender.DarkTheme
	publication := &presentation.Publication{}
	for _, name := range []string{"/root", "/root/a", "/root/b", "/root/c", "/root/d", "/root/e"} {
		publication.Roster = append(publication.Roster, presentation.AgentRow{Agent: presentation.Agent{AgentFacts: presentation.AgentFacts{Name: name}}})
	}
	v.Update(publication, Display{Caller: "/root"})
	v.RosterSheet(64, 3, time.Unix(100, 0))
	var frames []string
	for _, step := range []int{1, 1, 100, -1, -100} {
		v.ScrollRoster(step)
		rows := v.RosterSheet(64, 3, time.Unix(100, 0))
		if step == 1 && v.Hits[0].Agent == "/root/a" || step == 100 && v.Hits[len(v.Hits)-1].Agent != "/root/e" || step == -100 && v.Hits[0].Agent != "/root/a" {
			t.Fatalf("scroll %d ignored: %+v", step, v.Hits)
		}
		frames = append(frames, rows...)
	}
	v.Update(publication, Display{Caller: "/root/e"})
	rows := v.RosterSheet(64, 3, time.Unix(100, 0))
	if v.RosterManual || v.Hits[len(v.Hits)-1].Agent != "/root/e" {
		t.Fatalf("selection did not reveal its row: %+v", v.Hits)
	}
	frames = append(frames, rows...)
	uisnapshot.AssertTerminal(t, "testdata/snapshots/native-roster-scroll-true.txt", frames, 64)
}

func TestUISnapshotNativeRosterFailedRead(t *testing.T) {
	for _, theme := range []diffrender.Theme{diffrender.TerminalTheme, diffrender.DarkTheme, diffrender.LightTheme} {
		t.Run(fmt.Sprint(theme), func(t *testing.T) {
			source := presentation.CommandSource{Script: "cat missing.txt", Display: "cat missing.txt"}
			invocation := presentation.NewInvocation(source, time.Unix(100, 0), new(feed.Retention))
			invocation.Write("cat: missing.txt: No such file or directory\n")
			invocation.Complete(presentation.CommandCompletion{Source: source, Exit: new(3)})
			command := invocation.Snapshot()
			command.Source = 7
			output, _ := invocation.Output().Context(feed.OutputBytes)
			command.Output = &output
			values := (presentation.ItemContent{Seq: 7, Kind: "tool", Text: presentation.ShellSource(command.Script, ""), Command: command, Details: &presentation.ItemDetails{CommandText: command.Script, Output: 1}}).Values()
			if len(values) != 1 || values[0].Kind != "reads" || !values[0].FailedOutcome() || !values[0].Exited || values[0].ExitCode != 3 {
				t.Fatalf("read projection = %+v", values)
			}
			pages := presentation.DetailPages(values)
			if len(pages) != 1 || pages[0].Source != command.Source || pages[0].ExitCode != 3 || command.Output == nil || len(command.Output.Lines) != 1 {
				t.Fatalf("retained read detail = %+v", pages)
			}
			agent := presentation.Agent{Name: "/root/reader", Responding: true, Status: "responding", Summary: []presentation.Entry{{Seq: 7, Kind: "tool", Values: values}}}
			v := NewView()
			v.Painter.Theme, v.ChildrenOnly = theme, true
			v.Update(&presentation.Publication{Roster: []presentation.AgentRow{{Agent: agent}}}, Display{Caller: "/root"})
			rows := v.RosterSheet(90, 4, time.Unix(100, 0))
			plain := ansi.Strip(strings.Join(rows, "\n"))
			if !strings.Contains(plain, "Read missing.txt (exit 3)") {
				t.Fatalf("failed read roster: %s", plain)
			}
			summary := v.Painter.Summary([]Block{{Value: values[0]}}, 90)
			if !strings.HasPrefix(summary, terminalui.Red+"Read") {
				t.Fatalf("failed read tone: %q", summary)
			}
			uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/native-roster-failed-read-%d.txt", theme), append(rows, "plain after roster"), 90)
		})
	}
}
