package activity

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func vcsRows(p *Painter, block Block, width int) string {
	return ansi.Strip(strings.Join(p.Block(block, width), "\n"))
}

func TestVCSRowsShowTheirResult(t *testing.T) {
	var p Painter
	commit := Block{Kind: "op", Verb: "Commit", Label: "`amend! feat(router): pin latest reply` · git"}
	if got := vcsRows(&p, commit, 80); got != "Commit amend! feat(router): pin latest reply · git" {
		t.Fatalf("requested commit = %q", got)
	}
	if raw := strings.Join(p.Block(commit, 80), ""); !strings.Contains(raw, Amber+"amend!") {
		t.Fatalf("autosquash marker is not a marker: %q", raw)
	}
	// Without a named commit, output does not make the request a recorded commit.
	commit.Changes = []ChangeRow{{Verb: "Edited", Label: "a.go", Added: 2, Removed: 1}}
	if got := RowVerb(commit); got != "Commit" {
		t.Fatalf("unnamed commit verb = %q", got)
	}
	commit.Changes = append(commit.Changes, ChangeRow{Verb: "Created", Label: "doc/b.md", Added: 9}, ChangeRow{Footer: true, Label: "3a1f9c2", Note: "on main · 2 files", Added: 11, Removed: 1})
	commit.Tail, commit.Collapsed = []string{"[main 3a1f9c2] x", " 2 files changed"}, true
	want := strings.Join([]string{
		"Committed amend! feat(router): pin latest reply · git",
		"          Edited  a.go      +2 -1 ━━━━━━━━",
		"          Created doc/b.md  +9    ━━━━━━━━",
		"          3a1f9c2 on main · 2 files +11 -1",
	}, "\n")
	if got := vcsRows(&p, commit, 80); got != want {
		t.Fatalf("committed rows =\n%s\nwant\n%s", got, want)
	}
	// The subject gives way to keep the source in view.
	if got := strings.Split(vcsRows(&p, commit, 40), "\n")[0]; got != "Committed amend! feat(router): pi… · git" {
		t.Fatalf("narrow heading = %q", got)
	}
	if got := ansi.Strip(p.Summary([]Block{commit})); got != "Committed amend! feat(router): pin latest reply +11 -1 · git" {
		t.Fatalf("roster summary = %q", got)
	}
	failed := Block{Kind: "op", Verb: "Commit", Label: "`fix: x` · git", ExitCode: 1, Tail: []string{"hook failed"}}
	if got := vcsRows(&p, failed, 80); !strings.HasPrefix(got, "Commit fix: x · git · exit 1\n") || RowVerb(failed) != "Commit" {
		t.Fatalf("failed commit =\n%s", got)
	}
	if raw := p.Block(failed, 80)[0]; !strings.HasPrefix(raw, Red) {
		t.Fatalf("failed commit verb is not red: %q", raw)
	}
	diff := Block{Kind: "op", Verb: "Diff", Label: "`HEAD~3..HEAD` in `internal/ui` `doc` · git --stat"}
	if got := vcsRows(&p, diff, 80); got != "Diff   HEAD~3..HEAD in internal/ui doc · git --stat" {
		t.Fatalf("diff heading = %q", got)
	}
	stage := Block{Kind: "op", Verb: "Stage", Label: "`a.go` `b.go` · git"}
	if got := vcsRows(&p, stage, 80); got != "Stage  a.go b.go · git" {
		t.Fatalf("stage = %q", got)
	}
	// A heading that is not a VCS source keeps its ordinary layout.
	if (Block{Kind: "op", Verb: "Check", Label: "remaining context"}).VCS() {
		t.Fatal("a non-VCS Check read as a VCS row")
	}
}

func TestVCSStatusRowsUseTwoCellCodes(t *testing.T) {
	var p Painter
	status := Block{Kind: "op", Verb: "Status", Label: "`working tree` · git", Tail: []string{"x", "y"}, Changes: []ChangeRow{
		{Code: "MM", Label: "internal/router/vcs_display.go"},
		{Code: "R ", From: "old.go", Label: "new.go"},
		{Code: "??", Label: "scratch.txt"},
		{Footer: true, Label: "feat/vcs-events", Note: "↑2"},
	}}
	want := strings.Join([]string{
		"Status working tree · git",
		"       MM internal/router/vcs_display.go",
		"       R  old.go → new.go",
		"       ?? scratch.txt",
		"       feat/vcs-events ↑2",
	}, "\n")
	if got := vcsRows(&p, status, 80); got != want {
		t.Fatalf("status =\n%s\nwant\n%s", got, want)
	}
	if got := statusCode("MM"); got != Green+"M\x1b[39m"+Red+"M\x1b[39m" {
		t.Fatalf("staged code colors = %q", got)
	}
	if got := statusCode("UU"); strings.Contains(got, Green) {
		t.Fatalf("conflict colored as staged: %q", got)
	}
	clean := Block{Kind: "op", Verb: "Status", Label: "`working tree` · git", Tail: []string{"x", "y"}, Changes: []ChangeRow{{Footer: true, Label: "main", Note: "clean"}}}
	if got := vcsRows(&p, clean, 80); got != "Status working tree · git\n       main clean" {
		t.Fatalf("clean status =\n%s", got)
	}
}

func TestVCSFooterFollowsElidedRows(t *testing.T) {
	var rows []ChangeRow
	for range ChangeRowsShown + 2 {
		rows = append(rows, ChangeRow{Verb: "Edited", Label: "a.go", Added: 1})
	}
	rows = append(rows, ChangeRow{Footer: true, Note: "14 files", Added: 14})
	lines := changeRows(rows, "", 60)
	if got := ansi.Strip(strings.Join(lines[len(lines)-2:], "\n")); got != "… +2 more\n14 files +14" {
		t.Fatalf("closing rows =\n%s", got)
	}
}

func TestFoldStagesIntoTheirCommit(t *testing.T) {
	stage := Block{Kind: "op", Verb: "Stage", Label: "`a.go` · git"}
	commit := Block{Kind: "op", Verb: "Commit", Label: "`x` · git"}
	check := Block{Kind: "op", Verb: "Check", Label: "`working tree` · git diff --check"}
	verbs := func(blocks []Block) string {
		var out []string
		for _, block := range blocks {
			out = append(out, block.Verb)
		}
		return strings.Join(out, " ")
	}
	failed, skipped := stage, commit
	failed.ExitCode, skipped.Skipped = 1, true
	for _, tc := range []struct {
		blocks []Block
		want   string
	}{
		{[]Block{check, stage, stage, commit}, "Check Commit"},
		{[]Block{stage}, "Stage"},
		{[]Block{stage, check, commit}, "Stage Check Commit"},
		{[]Block{failed, skipped}, "Stage Commit"},
		{[]Block{stage, skipped}, "Stage Commit"},
	} {
		if got := verbs(FoldStages(tc.blocks)); got != tc.want {
			t.Errorf("%s: folded to %q, want %q", verbs(tc.blocks), got, tc.want)
		}
	}
}

func TestDialogColorsVCSPatches(t *testing.T) {
	lines := []string{"diff --git a/a.go b/a.go", "@@ -1 +1 @@", "-old", "+new"}
	var p Painter
	page := p.DialogPage(Block{Kind: "op", Verb: "Diff", Label: "`working tree` · git", Tail: lines}, 80)
	if page.Text != strings.Join(lines, "\n") {
		t.Fatalf("changed retained content: %q", page.Text)
	}
	for i, line := range page.Lines {
		if ansi.Strip(line.Text) != lines[i] {
			t.Fatalf("changed line %d: %q", i, line.Text)
		}
	}
	if page.Lines[3].Text == lines[3] {
		t.Error("patch colors missing")
	}
}
