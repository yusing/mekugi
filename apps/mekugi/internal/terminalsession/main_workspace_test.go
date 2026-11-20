package terminalsession

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/contracts/presentation"
	"github.com/yusing/mekugi/libraries/diffrender"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestUISnapshotMainWorkspacePaths(t *testing.T) {
	const workspace = "/home/dev/projects/workspace/.worktrees/distinctive-project"
	for _, width := range []int{40, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			view := sessionpresentation.View{DisplayedWorkspace: workspace, Caption: sessionpresentation.Caption{Model: "model"}}
			s := Shell{RosterLabel: "Agents"}
			rendered := s.StatLine(ShellStatus{Agent: "Main", Title: "Session title", Workspace: view.SourceWorkspace()}, width, 0)
			line := ansi.Strip(rendered)
			if !strings.Contains(line, "-project") && !strings.Contains(line, "…project") || width < 80 && strings.Contains(line, "/home/dev") {
				t.Fatalf("workspace suffix displaced by prefix: %q", line)
			}
			if strings.Contains(ansi.Strip(captionLine(diffrender.TerminalTheme, view.Caption, captionUsage{}, width)), "-project") {
				t.Fatal("workspace stayed on the caption line")
			}
			if view.DisplayedWorkspace != workspace {
				t.Fatal("painting changed the operational workspace")
			}
			uisnapshot.Assert(t, fmt.Sprintf("testdata/snapshots/workspace_path_%d.txt", width), rendered)
		})
	}
}

// The workspace follows the title, gives it room, and abbreviates to a sixth
// of the line before hints drop; it drops after skills, before the roster
// hint.
func TestStatLineWorkspaceOrderAndDrops(t *testing.T) {
	s := Shell{RosterLabel: "Agents"}
	in := ShellStatus{Agent: "Main", Title: "Fix login", Workspace: "/home/dev/projects/mekugi", Empty: true, Skills: "skills: go",
		Summary: activityui.RosterSummary{Counts: []string{"◐ 1"}, Children: true}}
	for _, tc := range []struct {
		width int
		want  string
	}{
		{120, "  Main • ◐ 1 • Fix login • /home/dev/projects/mekugi • skills: go • ↓ roster • ctrl+e view"},
		{80, "  Main • ◐ 1 • Fix login • …ojects/mekugi • skills: go • ↓ roster • ctrl+e view"},
		{56, "  Main • ◐ 1 • Fix login • …s/mekugi • ↓ roster"},
		{44, "  Main • ◐ 1 • Fix login • ↓ roster"},
		{30, "  Main • ◐ 1 • Fix login"},
	} {
		if got := ansi.Strip(s.StatLine(in, tc.width, 0)); got != tc.want {
			t.Errorf("width %d: stat line = %q, want %q", tc.width, got, tc.want)
		}
	}
}

// A limit near exhaustion takes the warning or danger tone and outlasts the
// cost and the other limit; a neutral limit drops before the context bar.
func TestMainComposerCaptionHighLimit(t *testing.T) {
	caption := sessionpresentation.Caption{Model: "model", ContextKnown: true, ContextTokens: 360000, ContextWindow: 400000,
		Limits: []sessionpresentation.CaptionLimit{{Label: "5h", Used: 97}, {Label: "weekly", Used: 40}}}
	usage := captionUsage{Cost: "$0.12"}
	for _, tc := range []struct {
		width int
		want  string
	}{
		{80, "  model | ━━━━━━━━━─ 90% 360K/400K | $0.12 | 5h 3% left · Weekly 60% left"},
		{60, "  model | ━━━━━━━━━─ 90% | $0.12 | 5h 3% left"},
		{40, "  model | 90% | $0.12 | 5h 3% left"},
		{30, "  model | 90% | 5h 3% left"},
		{20, "  model | 90%"},
	} {
		if got := ansi.Strip(captionLine(diffrender.TerminalTheme, caption, usage, tc.width)); got != tc.want {
			t.Errorf("width %d: caption = %q, want %q", tc.width, got, tc.want)
		}
	}
	line := captionLine(diffrender.DarkTheme, caption, usage, 80)
	if !strings.Contains(line, diffrender.DarkTheme.Danger()+"5h 3% left") || !strings.Contains(line, diffrender.DarkTheme.Info()+"Weekly 60% left") {
		t.Fatalf("limit tones: %q", line)
	}
	caption.Limits[1].Used = 80
	if line := captionLine(diffrender.DarkTheme, caption, usage, 80); !strings.Contains(line, diffrender.DarkTheme.Warning()+"Weekly 20% left") {
		t.Fatalf("warning limit tone: %q", line)
	}
}

// Failure and working counts outlast the title.
func TestStatLineCountsOutlastTitle(t *testing.T) {
	s := Shell{RosterLabel: "Agents"}
	in := ShellStatus{Agent: "Main", Title: "Fix the flaky login test", Workspace: "/workspace",
		Summary: activityui.RosterSummary{Counts: []string{"◐ 2", "✓ 3", "! 1"}, Children: true}}
	for _, tc := range []struct {
		width int
		want  string
	}{
		{40, "  Main • ◐ 2 • ✓ 3 • ! 1 • Fix the fla…"},
		{34, "  Main • ◐ 2 • ! 1 • Fix the fla…"},
		{28, "  Main • ◐ 2 • ! 1"},
	} {
		if got := ansi.Strip(s.StatLine(in, tc.width, 0)); got != tc.want {
			t.Errorf("width %d: stat line = %q, want %q", tc.width, got, tc.want)
		}
	}
}

// While One-off is displayed, Main's tab precedes it and selects Main.
func TestStatLineMainTabBesideOneOff(t *testing.T) {
	s := Shell{}
	line := ansi.Strip(s.StatLine(ShellStatus{Agent: "One-off", SideLabel: "One-off", SideSelected: true, Workspace: "/workspace"}, 48, 5))
	if line != "  Main • One-off • /workspace • ctrl+e view" {
		t.Fatalf("stat line = %q", line)
	}
	if len(s.Targets) != 1 || s.Targets[0].Action != StatMainTab || s.Targets[0].Rect != (Rect{2, 5, 4, 1}) {
		t.Fatalf("main tab target = %+v", s.Targets)
	}
	s.StatLine(ShellStatus{Agent: "Main", SideLabel: "One-off", Workspace: "/workspace"}, 40, 5)
	if len(s.Targets) != 1 || s.Targets[0].Action != StatSide {
		t.Fatalf("Main view lost the One-off tab: %+v", s.Targets)
	}
}

// While a child thread is displayed, a clickable Main tab precedes it.
func TestStatLineMainTabOnChildThread(t *testing.T) {
	s := Shell{}
	line := ansi.Strip(s.StatLine(ShellStatus{Agent: "reader", MainTab: true, Workspace: "/workspace"}, 48, 5))
	if line != "  Main • reader • /workspace • ctrl+e view" {
		t.Fatalf("stat line = %q", line)
	}
	if len(s.Targets) != 1 || s.Targets[0].Action != StatMainTab || s.Targets[0].Rect != (Rect{2, 5, 4, 1}) {
		t.Fatalf("main tab target = %+v", s.Targets)
	}
}

// Split Diff uses a single vertical separator without a box frame.
func TestSplitDiffUsesSeparatorWithoutFrame(t *testing.T) {
	s := Shell{Panes: Panes{DiffOpen: true, Focus: 1}, AuxiliaryLabel: "Diff"}
	layout, left, right := s.Panes.Geometry(100, 20, 2)
	plan := ShellPlan{Layout: layout, Left: left, Right: right, Width: 100, Height: 20, Stats: 2}
	if plan.Layout.Vertical != 50 || plan.Left.W != 50 || plan.Right.X != 51 || plan.Right.W != 49 || plan.Layout.Diff != plan.Right {
		t.Fatalf("unexpected split geometry: %+v", plan)
	}
	rows := s.Compose(plan, ShellContent{
		Main:  []string{"main-row"},
		Diff:  []string{"diff-row"},
		Stats: []string{"stat1", "stat2"},
	})
	rendered, _ := Screen(100, 20, rows, -1, -1)
	first := ansi.Strip(rendered[0])
	if strings.ContainsAny(first, "┌┐└┘") {
		t.Fatalf("diff pane still rendered a box frame: %q", first)
	}
	if !strings.Contains(first, "main-row") || !strings.Contains(first, "│diff-row") {
		t.Fatalf("expected vertical separator between Main and Diff: %q", first)
	}
	if got := compactDiffState("files focus" + "\x1b[2m · \x1b[22m" + "e editor" + "\x1b[2m · \x1b[22m" + "LIVE" + "\x1b[2m · \x1b[22m" + "@main" + "\x1b[2m · \x1b[22m" + "1/2"); ansi.Strip(got) != "LIVE · @main · 1/2" {
		t.Fatalf("compactDiffState = %q", ansi.Strip(got))
	}
}

// The scroll position names lines below the viewport only once the reader
// scrolls back.
func TestScrollLabel(t *testing.T) {
	v := &activityui.View{FeedLines: 100, FeedRows: 20, Offset: 80, Following: true}
	if got := scrollLabel(v); got != "" {
		t.Fatalf("following label = %q", got)
	}
	v.Offset, v.Following, v.Unseen = 50, false, 3
	if got := ansi.Strip(scrollLabel(v)); got != "▼ 30 below · 3 new" {
		t.Fatalf("scrolled label = %q", got)
	}
}

func TestMainComposerTierWithoutModel(t *testing.T) {
	caption := sessionpresentation.Caption{Tier: "flex"}
	if line := ansi.Strip(captionLine(diffrender.TerminalTheme, caption, captionUsage{}, 80)); line != "  flex" {
		t.Fatalf("tier without model or known context: %q", line)
	}
}

func TestMainComposerCaptionUsage(t *testing.T) {
	caption := sessionpresentation.Caption{Model: "model", ContextKnown: true, ContextTokens: 100000, ContextWindow: 400000,
		Limits: []sessionpresentation.CaptionLimit{{Label: "5h", Used: 12}, {Label: "weekly", Used: 21}},
		Items:  []presentation.Item{{Text: "96.0 tok/s"}}}
	for _, tc := range []struct {
		width int
		want  string
	}{
		{160, "  model | ━━╸─────── 25% 100K/400K | $0.12 · total $0.48 | ↑ 12K ↓ 3K | 96.0 tok/s | 5h 88% left · Weekly 79% left"},
		{100, "  model | ━━╸─────── 25% | $0.12 · total $0.48 | ↑ 12K ↓ 3K | 5h 88% left · Weekly 79% left"},
		{68, "  model | ━━╸─────── 25% | $0.12 | 5h 88% left · Weekly 79% left"},
		{40, "  model | ━━╸─────── 25% | $0.12"},
		{24, "  model | 25% | $0.12"},
		{16, "  model | 25%"},
	} {
		usage := captionUsage{Tokens: "↑ 12K ↓ 3K", Cost: "$0.12", Total: "$0.48"}
		if got := ansi.Strip(captionLine(diffrender.TerminalTheme, caption, usage, tc.width)); got != tc.want {
			t.Errorf("width %d: caption = %q, want %q", tc.width, got, tc.want)
		}
	}
}
