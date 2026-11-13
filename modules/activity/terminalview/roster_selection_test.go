package terminalview

import (
	"fmt"
	"slices"
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
				for _, focused := range []bool{true, false} {
					frame := v.NativeRoster(64, 4, time.Unix(100, 0), focused)
					found := false
					for _, hit := range v.Hits {
						found = found || hit.Agent == caller
						selected := strings.Contains(frame[hit.Row-1], theme.SelectionBackground())
						if selected != (hit.Agent == caller && (focused || caller != "/root")) {
							t.Fatalf("wrong selection for %s displaying %s: %q", hit.Agent, caller, frame[hit.Row-1])
						}
					}
					if !found || v.Selected != "/root/f" {
						t.Fatalf("displayed row missing or remembered child lost: caller=%s offset=%d selected=%s", caller, v.RosterOffset, v.Selected)
					}
					rows = append(rows, frame...)
					rows = append(rows, "plain after roster")
				}
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
		for _, focused := range []bool{true, false} {
			frame := v.NativeRoster(64, 1, time.Unix(100, 0), focused)
			if len(frame) != 2 || len(v.Hits) != 1 || v.Hits[0].Agent != caller {
				t.Fatalf("one-row roster lost displayed %s: hits=%+v frame=%q", caller, v.Hits, frame)
			}
			rows = append(rows, frame...)
			rows = append(rows, "plain after roster")
		}
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
	v.NativeRoster(64, 3, time.Unix(100, 0), true)
	var frames []string
	for _, step := range []int{1, 1, 100, -1, -100} {
		v.ScrollRoster(step)
		rows := v.NativeRoster(64, 3, time.Unix(100, 0), true)
		if step == 1 && v.Hits[0].Agent == "/root" || step == 100 && v.Hits[len(v.Hits)-1].Agent != "/root/e" || step == -100 && v.Hits[0].Agent != "/root" {
			t.Fatalf("scroll %d ignored: %+v", step, v.Hits)
		}
		frames = append(frames, rows...)
	}
	v.Update(publication, Display{Caller: "/root/e"})
	rows := v.NativeRoster(64, 3, time.Unix(100, 0), true)
	if v.RosterManual || v.Hits[len(v.Hits)-1].Agent != "/root/e" {
		t.Fatalf("selection did not reveal its row: %+v", v.Hits)
	}
	frames = append(frames, rows...)
	uisnapshot.AssertTerminal(t, "testdata/snapshots/native-roster-scroll-true.txt", frames, 64)
}

func TestNativeRosterKeepsSelectedFinishedChild(t *testing.T) {
	v := NewView()
	publication := &presentation.Publication{}
	for i, name := range []string{"/root", "/root/done-a", "/root/done-b", "/root/done-c", "/root/work-a", "/root/work-b", "/root/work-c", "/root/work-d"} {
		agent := presentation.Agent{Name: name, Responding: i > 3, Status: "responding"}
		if i >= 1 && i <= 3 {
			agent.Final, agent.Status = true, "answered"
		}
		publication.Roster = append(publication.Roster, presentation.AgentRow{Agent: agent})
	}
	for _, caller := range []string{"/root/done-b", "/root"} {
		v.Update(publication, Display{Caller: caller})
		v.RosterManual = true
		rows := v.NativeRoster(100, 20, time.Unix(100, 0), false)
		found := false
		for _, hit := range v.Hits {
			found = found || hit.Agent == caller
			if hit.Agent == "/root/done-a" {
				t.Fatal("completed sibling displaced active work")
			}
		}
		if !found || len(v.Hits) < 5 || !strings.Contains(strings.Join(rows, "\n"), "more") {
			t.Fatalf("selected transcript or active work lost: hits=%+v rows=%q", v.Hits, rows)
		}
	}
	for i, row := range v.Roster() {
		if row.Agent.Name != publication.Roster[i].Agent.Name {
			t.Fatal("display priority changed canonical navigation order")
		}
	}
}

func TestUISnapshotNativeRosterActiveAfterBrowsing(t *testing.T) {
	v := NewView()
	v.Painter.Theme = diffrender.DarkTheme
	publication := &presentation.Publication{}
	for i := range 8 {
		name := fmt.Sprintf("/root/done-%d", i)
		if i == 0 {
			name = "/root"
		}
		publication.Roster = append(publication.Roster, presentation.AgentRow{Agent: presentation.Agent{AgentFacts: presentation.AgentFacts{Name: name, Final: i > 0}, Status: "answered"}})
	}
	publication.Roster[0].Agent.Status = ""
	publication.Roster = append(publication.Roster, presentation.AgentRow{Agent: presentation.Agent{AgentFacts: presentation.AgentFacts{Name: "/root/working", Responding: true}, Status: "responding"}})
	v.Update(publication, Display{Caller: "/root"})
	var frames []string
	// Browse completed history, then leave the roster while work is active.
	v.NativeRoster(80, 4, time.Unix(100, 0), true)
	v.ScrollRoster(4)
	frames = append(frames, v.NativeRoster(80, 4, time.Unix(100, 0), true)...)
	if v.Hits[0].Agent != "/root/done-4" {
		t.Fatalf("focused browsing lost its viewport: %+v", v.Hits)
	}
	frames = append(frames, v.NativeRoster(80, 4, time.Unix(100, 0), false)...)
	if v.Hits[0].Agent != "/root" || v.Hits[2].Agent != "/root/working" {
		t.Fatalf("manual viewport displaced active priority: %+v", v.Hits)
	}
	hit := v.Hits[2]
	v.PointAgent('\r', hit.Row, hit.First)
	if v.DisplayRequest == nil || v.DisplayRequest.Caller != "/root/working" {
		t.Fatal("compacted row lost its canonical navigation target")
	}
	uisnapshot.AssertTerminal(t, "testdata/snapshots/native-roster-active-after-browsing.txt", append(frames, "plain after roster"), 80)
}

func TestUISnapshotNativeRosterUnpinnedErrorsAndHiddenParents(t *testing.T) {
	v := NewView()
	publication := &presentation.Publication{}
	for _, name := range []string{"/root", "/root/reviewer/error-a", "/root/reviewer/error-b", "/root/target_95", "/root/target_95/browser", "/root/target_95_codex"} {
		agent := presentation.Agent{Name: name}
		switch {
		case strings.Contains(name, "error-"):
			agent.Status, agent.LatestKind = "error", "error"
		case name == "/root/target_95":
			agent.Status, agent.Final = "answered", true
		case name == "/root/target_95_codex":
			agent.Status, agent.Responding = "responding", true
		}
		publication.Roster = append(publication.Roster, presentation.AgentRow{Agent: agent, Depth: strings.Count(name, "/") - 1})
	}
	v.Update(publication, Display{Caller: "/root"})
	rows := v.NativeRoster(100, 8, time.Unix(100, 0), false)
	for _, hit := range v.Hits {
		if strings.Contains(hit.Agent, "error-") {
			t.Fatalf("error remained pinned: %+v", v.Hits)
		}
	}
	text := ansi.Strip(strings.Join(rows, "\n"))
	if !strings.Contains(text, "2 error") || !strings.Contains(text, "+3 more") || !strings.Contains(text, "├ target_95/browser") || !strings.Contains(text, "└ target_95_codex") {
		t.Fatalf("lost errors or canonical hierarchy:\n%s", text)
	}
	uisnapshot.AssertTerminal(t, "testdata/snapshots/native-roster-unpinned-errors.txt", append(rows, "plain after roster"), 100)
	v.ScrollRoster(1)
	v.NativeRoster(100, 8, time.Unix(100, 0), true)
	if !slices.ContainsFunc(v.Hits, func(hit Hit) bool { return strings.Contains(hit.Agent, "error-") }) {
		t.Fatalf("error unavailable through ordinary browsing: %+v", v.Hits)
	}
	v.Update(publication, Display{Caller: "/root/reviewer/error-b"})
	v.NativeRoster(100, 8, time.Unix(100, 0), false)
	if !slices.ContainsFunc(v.Hits, func(hit Hit) bool { return hit.Agent == "/root/reviewer/error-b" }) {
		t.Fatal("displayed error lost its priority")
	}
}

func TestUISnapshotNativeRosterNarrowOverflow(t *testing.T) {
	v := NewView()
	publication := &presentation.Publication{}
	for i := range 20 {
		publication.Roster = append(publication.Roster, presentation.AgentRow{Agent: presentation.Agent{
			AgentFacts: presentation.AgentFacts{Name: fmt.Sprintf("/root/agent-%d", i), Responding: true}, Status: "responding",
		}})
	}
	v.Update(publication, Display{Caller: "/root/agent-0"})
	for _, width := range []int{20, 32} {
		var frame []string
		for _, focused := range []bool{false, true} {
			rows := v.NativeRoster(width, 1, time.Unix(100, 0), focused)
			if !strings.Contains(rows[0], "+19 more") {
				t.Fatalf("overflow lost at width %d: %q", width, rows[0])
			}
			frame = append(frame, rows...)
			frame = append(frame, "plain after roster")
		}
		uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/native-roster-narrow-overflow-%d.txt", width), frame, width)
	}
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
			rows := v.NativeRoster(90, 4, time.Unix(100, 0), false)
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
