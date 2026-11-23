package terminalview

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

// TestCompactDefaultViewRevamp verifies the compact default view (Expansion == 0):
// 1. No Action x N batch folding representation.
// 2. Earlier reasoning summaries are replaced once a newer reasoning summary arrives.
// 3. Every successful event renders on one line (Search: results count only;
//    Diff: git --shortstat numbers; Status: combined with Diff for the same
//    worktree), while failed commands keep their failure output clearly visible.
// 4. Double newline gaps appear only around messages (user-to-agent,
//    agent-to-user, agent-to-agent), not between adjacent tool/event rows.
func TestUISnapshotCompactDefaultViewRevamp(t *testing.T) {
	now := time.Date(2026, 11, 23, 11, 15, 0, 0, time.Local)
	v := NewView()
	v.Conversation = true
	v.Painter.Theme = diffrender.DarkTheme
	v.Clock = func() time.Time { return now }

	diffChanges := []ChangeRow{
		{Code: "M", Label: "apps/mekugi/internal/launch/vcs_display_test.go", Added: 20, Removed: 4},
		{Code: "M", Label: "modules/activity/presentation/vcs_output.go", Added: 22, Removed: 16},
	}
	statusChanges := []ChangeRow{
		{Code: " M", Label: "apps/mekugi/internal/launch/vcs_display_test.go"},
		{Code: " M", Label: "modules/activity/presentation/vcs_output.go"},
		{Code: "??", Label: "testdata/new.txt"},
	}
	otherWorktreeStatus := []ChangeRow{
		{Code: " M", Label: "apps/mekugi/internal/launch/model_catalog_codex_e2e_test.go"},
	}

	entries := []presentation.Entry{
		{
			Seq: 1, Agent: "You", Kind: "user", Text: "Make default view compact", Observed: now,
		},
		{
			Seq: 2, Agent: "Main", Kind: "reasoning", Restored: true, ItemComplete: true,
			Values: []presentation.Value{{Kind: "summary", Source: 2, Label: "Inspecting snapshot helpers", Body: "**Inspecting snapshot helpers**\n\nOld reasoning details."}},
		},
		{
			Seq: 3, Agent: "Main", Kind: "tool", Restored: true,
			Values: []presentation.Value{
				{
					Kind: "op", Source: 3, Verb: "Search", Label: "`func awaitMain` in `apps/mekugi/internal/launch`",
					Exited: true, Results: new(1),
					Tail: []string{"apps/mekugi/internal/launch/exec_track_test.go:757:func awaitMain(...)"},
				},
			},
		},
		{
			Seq: 4, Agent: "Main", Kind: "tool", Restored: true,
			Values: []presentation.Value{
				{
					Kind: "op", Source: 4, Verb: "Diff", Label: "`working tree` · git --numstat",
					Exited: true, Changes: diffChanges,
				},
			},
		},
		{
			Seq: 5, Agent: "Main", Kind: "tool", Restored: true,
			Values: []presentation.Value{
				{
					Kind: "op", Source: 5, Verb: "Status", Label: "`working tree` · git",
					Exited: true, Changes: statusChanges,
				},
			},
		},
		{
			Seq: 6, Agent: "Main", Kind: "tool", Restored: true,
			Values: []presentation.Value{
				{
					Kind: "op", Source: 6, Verb: "Status", Label: "`working tree` · git -C ../..",
					Exited: true, Changes: otherWorktreeStatus,
				},
			},
		},
		{
			Seq: 7, Agent: "Main", Kind: "tool", Restored: true,
			Values: []presentation.Value{
				{
					Kind: "op", Source: 7, Verb: "Run", Code: "go test ./modules/activity/...",
					Exited: true, ExitCode: 1,
					Tail: []string{"FAIL: TestCompact (0.01s)", "expected 1 line"},
				},
			},
		},
		{
			Seq: 8, Agent: "Main", Kind: "reasoning", Restored: true, ItemComplete: true,
			Values: []presentation.Value{{Kind: "summary", Source: 8, Label: "Checking final diff", Body: "**Checking final diff**\n\nLatest reasoning details."}},
		},
		{
			Seq: 9, Agent: "Main", Kind: "tool", Restored: true,
			Images: []string{"/tmp/img1.png"},
			Values: []presentation.Value{{Kind: "op", Source: 9, Verb: "View", Path: "/tmp/img1.png", Exited: true}},
		},
		{
			Seq: 10, Agent: "Main", Kind: "tool", Restored: true,
			Images: []string{"/tmp/img2.png"},
			Values: []presentation.Value{{Kind: "op", Source: 10, Verb: "View", Path: "/tmp/img2.png", Exited: true}},
		},
		{
			Seq: 11, Agent: "Main", Kind: "text", Text: "Done updating default view.", Observed: now.Add(5 * time.Second), ItemComplete: true,
		},
	}

	v.Update(&presentation.Publication{Entries: entries}, Display{})
	v.Passed = map[uint64]bool{3: true}

	// Default view (Expansion == 0)
	v.Expansion = 0
	feed := v.RenderConversation(100)
	plainLines := make([]string, len(feed.Lines))
	for i, line := range feed.Lines {
		plainLines[i] = ansi.Strip(line)
	}
	plain := strings.Join(plainLines, "\n")

	// 1. No Action x N batch folding.
	if strings.Contains(plain, "Searched 1 pattern") || strings.Contains(plain, "Status ×") {
		t.Fatalf("Action x N batch summary leaked into default view:\n%s", plain)
	}
	// 2. Earlier reasoning replaced once newer reasoning arrived.
	if strings.Contains(plain, "Inspecting snapshot helpers") {
		t.Fatalf("earlier reasoning stayed in default view:\n%s", plain)
	}
	if !strings.Contains(plain, "Checking final diff") {
		t.Fatalf("latest reasoning missing from default view:\n%s", plain)
	}
	// 3a. Search shows results count only, no match output line.
	if !strings.Contains(plain, "(1 results)") || strings.Contains(plain, "exec_track_test.go:757") {
		t.Fatalf("Search did not show compact results count only:\n%s", plain)
	}
	// 3b. Diff + Status for same worktree combined on one line with shortstat + untracked.
	if !strings.Contains(plain, "Diff/Status") || !strings.Contains(plain, "2 files changed, +42 -20") || !strings.Contains(plain, "1 untracked") {
		t.Fatalf("Diff and Status for same worktree did not combine:\n%s", plain)
	}
	// 3c. Status for a different worktree (-C ../..) remains its own single line.
	if !strings.Contains(plain, "Status") || !strings.Contains(plain, "1 modified · git -C ../..") || strings.Contains(plain, "model_catalog_codex_e2e_test.go") {
		t.Fatalf("other worktree Status was not rendered as a compact single line:\n%s", plain)
	}
	// 3d. Failed command keeps its exit code and error output.
	if !strings.Contains(plain, "exit 1") || !strings.Contains(plain, "FAIL: TestCompact") {
		t.Fatalf("failed command lost failure details:\n%s", plain)
	}
	// 4. Blank lines exist around user message and agent message, NOT between View/reasoning/tools.
	if plainLines[1] != "" {
		t.Fatalf("expected blank line after user message, got:\n%s", plain)
	}
	if !slices.Contains(plainLines, "") {
		t.Fatalf("expected blank lines around messages:\n%s", plain)
	}
	img1Idx := slices.IndexFunc(plainLines, func(s string) bool { return strings.Contains(s, "/tmp/img1.png") })
	img2Idx := slices.IndexFunc(plainLines, func(s string) bool { return strings.Contains(s, "/tmp/img2.png") })
	if img1Idx < 0 || img2Idx != img1Idx+1 {
		t.Fatalf("unexpected blank line between consecutive View entries (%d -> %d):\n%s", img1Idx, img2Idx, plain)
	}
	// Combined Diff/Status retains both original pages for the output dialog.
	foundCombined := false
	for _, snippet := range feed.Snippets {
		if block, ok := v.SnippetBlock(snippet); ok && block.Verb == "Diff/Status" {
			foundCombined = true
			pages := presentation.DetailPages(Values([]Block{block}))
			if len(pages) != 2 {
				t.Fatalf("combined Diff/Status dialog pages = %d, want 2", len(pages))
			}
		}
	}
	if !foundCombined {
		t.Fatal("combined Diff/Status row has no clickable dialog snippet")
	}

	// Expanded view (Expansion == 1) still retains earlier reasoning and full VCS rows.
	v.Expansion = 1
	expanded := ansi.Strip(strings.Join(v.RenderConversation(100).Lines, "\n"))
	if !strings.Contains(expanded, "Inspecting snapshot helpers") || !strings.Contains(expanded, "model_catalog_codex_e2e_test.go") {
		t.Fatalf("expanded view lost earlier reasoning or VCS detail:\n%s", expanded)
	}

	v.Expansion = 0
	uisnapshot.AssertTerminal(t, "testdata/snapshots/compact_default_view.txt", append(v.RenderConversation(100).Lines, "plain after compact view"), 100)
}
