package terminalview

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/libraries/diffrender"
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
