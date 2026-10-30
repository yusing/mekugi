package terminalview

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestNativeRosterNonAttachableNavigation(t *testing.T) {
	v := NewView()
	v.ChildrenOnly = true
	publication := &presentation.Publication{Roster: []presentation.AgentRow{
		{Agent: presentation.Agent{AgentFacts: presentation.AgentFacts{Name: "/root"}}},
		{Agent: presentation.Agent{AgentFacts: presentation.AgentFacts{Name: "/root/claude", NonAttachable: true}}},
		{Agent: presentation.Agent{AgentFacts: presentation.AgentFacts{Name: "/root/codex"}}},
	}}
	v.Update(publication, Display{Caller: "/root"})
	painted := ansi.Strip(strings.Join(v.NativeRoster(100, 10, time.Unix(100, 0), true), "\n"))
	if !strings.Contains(painted, "claude") {
		t.Fatalf("nonattachable row disappeared: %s", painted)
	}
	for _, hit := range v.Hits {
		if hit.Agent == "/root/claude" {
			v.PointAgent('m', hit.Row, hit.First)
			v.PointAgent('\r', hit.Row, hit.First)
			if v.Hovered != "" || v.DisplayRequest != nil {
				t.Fatal("nonattachable row emitted a display request or hover affordance")
			}
		}
	}
	v.HandleRosterKey("n")
	if v.DisplayRequest == nil || v.DisplayRequest.Caller != "/root/codex" {
		t.Fatalf("keyboard did not skip nonattachable child: %+v", v.DisplayRequest)
	}
	v.DisplayRequest = nil
	v.Selected = "/root/claude"
	v.HandleRosterKey("a")
	if v.DisplayRequest != nil {
		t.Fatal("remembered selection reopened nonattachable child")
	}
}

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
	for _, focused := range []bool{false, true} {
		t.Run(fmt.Sprint(focused), func(t *testing.T) {
			v := NewView()
			v.Painter.Theme = diffrender.DarkTheme
			publication := &presentation.Publication{}
			for _, name := range []string{"/root", "/root/a", "/root/b", "/root/c", "/root/d", "/root/e"} {
				publication.Roster = append(publication.Roster, presentation.AgentRow{Agent: presentation.Agent{AgentFacts: presentation.AgentFacts{Name: name}}})
			}
			v.Update(publication, Display{Caller: "/root"})
			v.NativeRoster(64, 3, time.Unix(100, 0), focused)
			var frames []string
			for _, step := range []int{1, 1, 100, -1, -100} {
				v.ScrollRoster(step)
				rows := v.NativeRoster(64, 3, time.Unix(100, 0), focused)
				if step == 1 && v.Hits[0].Agent == "/root" || step == 100 && v.Hits[len(v.Hits)-1].Agent != "/root/e" || step == -100 && v.Hits[0].Agent != "/root" {
					t.Fatalf("scroll %d ignored: %+v", step, v.Hits)
				}
				frames = append(frames, rows...)
			}
			v.Update(publication, Display{Caller: "/root/e"})
			rows := v.NativeRoster(64, 3, time.Unix(100, 0), focused)
			if v.RosterManual || v.Hits[len(v.Hits)-1].Agent != "/root/e" {
				t.Fatalf("selection did not reveal its row: %+v", v.Hits)
			}
			frames = append(frames, rows...)
			uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/native-roster-scroll-%t.txt", focused), frames, 64)
		})
	}
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

func TestNativeRosterWheelKeepsCompactHeight(t *testing.T) {
	v := NewView()
	publication := &presentation.Publication{}
	for i := range 8 {
		name := fmt.Sprintf("/root/done-%d", i)
		if i == 0 {
			name = "/root"
		}
		publication.Roster = append(publication.Roster, presentation.AgentRow{Agent: presentation.Agent{AgentFacts: presentation.AgentFacts{Name: name, Final: i > 0}, Status: "answered"}})
	}
	v.Update(publication, Display{Caller: "/root"})
	before := v.NativeRoster(80, 14, time.Unix(100, 0), false)
	if !strings.Contains(strings.Join(before, "\n"), "+5 more") {
		t.Fatal("fixture did not collapse finished agents")
	}
	for range 7 {
		v.ScrollRoster(1)
		rows := v.NativeRoster(80, 14, time.Unix(100, 0), false)
		if len(rows) != len(before) || len(v.Hits) != 3 {
			t.Fatalf("wheel expanded the compact roster: %+v", v.Hits)
		}
	}
	if v.Hits[2].Agent != "/root/done-7" {
		t.Fatalf("scrolling did not reach the final finished agent: %+v", v.Hits)
	}
	v.ScrollRoster(-100)
	v.NativeRoster(80, 14, time.Unix(100, 0), false)
	if v.Hits[0].Agent != "/root" {
		t.Fatal("upward scrolling did not return to Main")
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
