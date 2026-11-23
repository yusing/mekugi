package terminalview

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffrender"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
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

func TestCompactDefaultViewVCSEdgeCases(t *testing.T) {
	p := Painter{Theme: diffrender.DarkTheme}

	// 1. Combined unstaged diff + status preserves staged changes.
	diffBlock := Block{
		Value: presentation.Value{
			Kind: "op", Verb: "Diff", Label: "`working tree` · git --stat",
			VCSTool: "git", VCSWorktree: "/repo", Exited: true,
			Changes: []ChangeRow{
				{Code: "M", Label: "b.go", Added: 5, Removed: 1},
			},
		},
		Annotation: Annotation{Compact: true},
	}
	statusBlock := Block{
		Value: presentation.Value{
			Kind: "op", Verb: "Status", Label: "`working tree` · git",
			VCSTool: "git", VCSWorktree: "/repo", Exited: true,
			Changes: []ChangeRow{
				{Code: "A ", Label: "a.go"},
				{Code: " M", Label: "b.go"},
			},
		},
		Annotation: Annotation{Compact: true},
	}
	merged := MergeVCSDiffStatus([]Block{diffBlock, statusBlock})
	if len(merged) != 1 || merged[0].Verb != "Diff/Status" {
		t.Fatalf("expected 1 combined Diff/Status block, got %d", len(merged))
	}
	merged[0].Compact = true
	line := ansi.Strip(strings.Join(p.Block(merged[0], 120), "\n"))
	if !strings.Contains(line, "1 file changed, +5 -1") || !strings.Contains(line, "1 staged") {
		t.Fatalf("combined diff/status hid staged file: %s", line)
	}

	// 2. Separate worktrees with spaces in -C paths are NOT merged.
	repoOneCmd := presentation.Command{
		Source: 1, Script: "git -C 'repo one' diff --stat", Display: "git -C 'repo one' diff --stat",
		Text: "Diff `working tree` · git -C repo one --stat", Directory: "/work", Exited: true,
	}
	repoTwoCmd := presentation.Command{
		Source: 2, Script: "git -C 'repo two' status", Display: "git -C 'repo two' status",
		Text: "Status `working tree` · git -C repo two", Directory: "/work", Exited: true,
	}
	v1 := presentation.Operations(repoOneCmd.Text)
	repoOneCmd.ApplyFacts(v1)
	v2 := presentation.Operations(repoTwoCmd.Text)
	repoTwoCmd.ApplyFacts(v2)
	if v1[0].VCSWorktree == v2[0].VCSWorktree {
		t.Fatalf("distinct worktrees collided: %q == %q", v1[0].VCSWorktree, v2[0].VCSWorktree)
	}
	if separate := MergeVCSDiffStatus([]Block{{Value: v1[0]}, {Value: v2[0]}}); len(separate) != 2 {
		t.Fatalf("separate worktrees with spaces were merged: %+v", separate)
	}

	// 3. Repeated diffs before status do not double-count files.
	repeated := MergeVCSDiffStatus([]Block{diffBlock, diffBlock, statusBlock})
	if len(repeated) != 2 || repeated[0].Verb != "Diff" || repeated[1].Verb != "Diff/Status" {
		t.Fatalf("repeated diffs should only pair the adjacent diff+status, got %d blocks", len(repeated))
	}
	repeated[1].Compact = true
	repeatedLine := ansi.Strip(strings.Join(p.Block(repeated[1], 120), "\n"))
	if !strings.Contains(repeatedLine, "1 file changed, +5 -1") || strings.Contains(repeatedLine, "2 files changed") {
		t.Fatalf("repeated diff inflated file count: %s", repeatedLine)
	}

	// 4. Limited git diff --stat respects authoritative footer file count.
	limited := Block{
		Value: presentation.Value{
			Kind: "op", Verb: "Diff", Label: "`working tree` · git --stat",
			Exited: true,
			Changes: []ChangeRow{
				{Code: "M", Label: "a.go", Added: 2, Removed: 1},
				{Footer: true, Note: "10 files", Added: 40, Removed: 12},
			},
		},
		Annotation: Annotation{Compact: true},
	}
	limitedLine := ansi.Strip(strings.Join(p.Block(limited, 120), "\n"))
	if !strings.Contains(limitedLine, "10 files changed, +40 -12") {
		t.Fatalf("limited stat undercounted files: %s", limitedLine)
	}

	// 5. Incomplete mchanges capture stays expanded and does not count warnings/? as changed files.
	incomplete := Block{
		Value: presentation.Value{
			Kind: "op", Verb: "Diff", Label: "`amber1` · mchanges amber1 --summary",
			Exited: true,
			Changes: []ChangeRow{
				{Note: "amber1 incomplete captured scope; use --history for diagnostics"},
				{Verb: "Created", Label: "new.txt", Added: 2},
				{Code: "?", Label: "outside.txt", Note: "resolved path is outside the pre-cell baseline"},
			},
		},
		Annotation: Annotation{Compact: true},
	}
	if incomplete.compactSingleLine() {
		t.Fatal("incomplete mchanges capture should not collapse into a single line")
	}
	if stat := ansi.Strip(vcsDiffShortstat(diffrender.DarkTheme, incomplete.Changes)); stat != "1 file changed, +2" {
		t.Fatalf("vcsDiffShortstat counted capture gaps as changed files: %q", stat)
	}
}

// TestUISnapshotCompactDefaultViewAlignsAdjacentEventLabels verifies that
// adjacent tool items whose intervening reasoning summaries are omitted on
// default view (Expansion == 0) share one verb column so their labels align
// vertically, while separated items in Expansion == 1 keep their own local
// verb column.
func TestUISnapshotCompactDefaultViewAlignsAdjacentEventLabels(t *testing.T) {
	now := time.Date(2026, 11, 23, 11, 15, 0, 0, time.Local)
	v := NewView()
	v.Conversation = true
	v.Painter.Theme = diffrender.DarkTheme
	v.Clock = func() time.Time { return now }

	entries := []presentation.Entry{
		{Seq: 1, Agent: "You", Kind: "user", Text: "Update setup", Observed: now},
		{
			Seq: 2, Agent: "Main", Kind: "reasoning", Restored: true, ItemComplete: true,
			Values: []presentation.Value{{Kind: "summary", Source: 2, Body: "**Editing setup files**"}},
		},
		{
			Seq: 3, Agent: "Main", Kind: "tool", Restored: true,
			Values: []presentation.Value{
				{Kind: "op", Source: 3, Verb: "Edit", Label: "`setup.sh` +41 -10", EditSource: "apply_patch", EditHeader: true, Exited: true},
				{Kind: "op", Source: 3, Verb: "Edit", Label: "`setup.json` +5 -3", EditSource: "apply_patch", Exited: true},
			},
		},
		{
			Seq: 4, Agent: "Main", Kind: "reasoning", Restored: true, ItemComplete: true,
			Values: []presentation.Value{{Kind: "summary", Source: 4, Body: "**Searching references**"}},
		},
		{
			Seq: 5, Agent: "Main", Kind: "tool", Restored: true,
			Values: []presentation.Value{{Kind: "op", Source: 5, Verb: "Search", Label: "`PM` in `setup.sh`", Exited: true}},
		},
		{
			Seq: 6, Agent: "Main", Kind: "tool", Restored: true,
			Values: []presentation.Value{{Kind: "reads", Source: 6, Verb: "Read", Reads: []activitymodel.Read{{Path: "setup_config_test.sh", Ranges: []string{"200:246"}}}, Exited: true}},
		},
		{
			Seq: 7, Agent: "Main", Kind: "tool", Restored: true,
			Values: []presentation.Value{{Kind: "op", Source: 7, Verb: "Run", Code: "rg -n PM setup.sh", Exited: true}},
		},
		{
			Seq: 8, Agent: "Main", Kind: "reasoning", Restored: true, ItemComplete: true,
			Values: []presentation.Value{{Kind: "summary", Source: 8, Body: "**Applying follow-up edit**"}},
		},
		{
			Seq: 9, Agent: "Main", Kind: "tool", Restored: true,
			Values: []presentation.Value{{Kind: "op", Source: 9, Verb: "Edit", Label: "`setup.sh` +5 -5", EditSource: "apply_patch", EditHeader: true, Exited: true}},
		},
		{
			Seq: 10, Agent: "Main", Kind: "reasoning", Restored: true, ItemComplete: true,
			Values: []presentation.Value{{Kind: "summary", Source: 10, Body: "**Viewing screenshot**"}},
		},
		{
			Seq: 11, Agent: "Main", Kind: "tool", Restored: true,
			Images: []string{"/tmp/shot.png"},
			Values: []presentation.Value{{Kind: "op", Source: 11, Verb: "View", Path: "/tmp/shot.png", Exited: true}},
		},
		{
			Seq: 12, Agent: "Main", Kind: "reasoning", Restored: true, ItemComplete: true,
			Values: []presentation.Value{{Kind: "summary", Source: 12, Body: "**Verifying final result**"}},
		},
	}

	v.Update(&presentation.Publication{Entries: entries}, Display{})

	// Default compact view: earlier reasoning summaries are omitted, so all tool
	// items above the latest reasoning sit directly adjacent and share column 8.
	v.Expansion = 0
	compactFeed := v.RenderConversation(100)
	var compactLines []string
	for _, line := range compactFeed.Lines {
		compactLines = append(compactLines, ansi.Strip(line))
	}
	wantPrefixes := []string{
		"└ Edited  setup.sh",
		"          setup.json",
		"├ Search  PM in setup.sh",
		"├ Read    setup_config_test.sh",
		"└ Ran     rg -n PM setup.sh",
		"└ Edited  setup.sh +5 -5",
		"└ View    /tmp/shot.png",
	}
	for _, want := range wantPrefixes {
		if !slices.ContainsFunc(compactLines, func(line string) bool { return strings.HasPrefix(line, want) }) {
			t.Fatalf("missing aligned row %q in compact view:\n%s", want, strings.Join(compactLines, "\n"))
		}
	}

	// Expanded view: reasoning summaries remain between tool items, so isolated
	// Edited and View items keep their local verb columns.
	v.Expansion = 1
	expandedFeed := v.RenderConversation(100)
	var expandedLines []string
	for _, line := range expandedFeed.Lines {
		expandedLines = append(expandedLines, ansi.Strip(line))
	}
	for _, want := range []string{"└ Edited setup.sh +5 -5", "└ View /tmp/shot.png"} {
		if !slices.ContainsFunc(expandedLines, func(line string) bool { return strings.HasPrefix(line, want) }) {
			t.Fatalf("missing locally-aligned row %q in expanded view:\n%s", want, strings.Join(expandedLines, "\n"))
		}
	}

	v.Expansion = 0
	uisnapshot.AssertTerminal(t, "testdata/snapshots/compact_default_view_aligned_labels.txt", append(v.RenderConversation(100).Lines, "plain after aligned labels"), 100)
}
