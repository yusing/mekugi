package terminalsession

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/contracts/presentation"
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
			if !strings.Contains(line, "project • ") || width < 80 && strings.Contains(line, "/home/dev") {
				t.Fatalf("workspace suffix displaced by prefix: %q", line)
			}
			if strings.Contains(ansi.Strip(captionLine(view.Caption, captionUsage{}, width)), "-project") {
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
		{120, "  Main • ◐ 1 • Fix login • /home/dev/projects/mekugi • skills: go • ↓ roster • ^E view"},
		{80, "  Main • ◐ 1 • Fix login • …v/projects/mekugi • skills: go • ↓ roster • ^E view"},
		{56, "  Main • ◐ 1 • Fix login • …s/mekugi • ↓ roster"},
		{44, "  Main • ◐ 1 • Fix login • ↓ roster"},
		{30, "  Main • ◐ 1 • Fix login"},
	} {
		if got := ansi.Strip(s.StatLine(in, tc.width, 0)); got != tc.want {
			t.Errorf("width %d: stat line = %q, want %q", tc.width, got, tc.want)
		}
	}
}

func TestMainComposerTierWithoutModel(t *testing.T) {
	caption := sessionpresentation.Caption{Tier: "flex"}
	if line := ansi.Strip(captionLine(caption, captionUsage{}, 80)); line != "  flex | 0%" {
		t.Fatalf("tier without model: %q", line)
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
		{160, "  model | ━━╸─────── 25% 100K/400K | $0.12 · total $0.48 | ↑ 12K ↓ 3K | 96.0 tok/s | 5h: 12% Weekly: 21%"},
		{90, "  model | ━━╸─────── 25% | $0.12 · total $0.48 | ↑ 12K ↓ 3K | 5h: 12% Weekly: 21%"},
		{60, "  model | ━━╸─────── 25% | $0.12 | 5h: 12% Weekly: 21%"},
		{40, "  model | ━━╸─────── 25% | $0.12"},
		{24, "  model | 25% | $0.12"},
		{16, "  model | 25%"},
	} {
		usage := captionUsage{Tokens: "↑ 12K ↓ 3K", Cost: "$0.12", Total: "$0.48"}
		if got := ansi.Strip(captionLine(caption, usage, tc.width)); got != tc.want {
			t.Errorf("width %d: caption = %q, want %q", tc.width, got, tc.want)
		}
	}
}
