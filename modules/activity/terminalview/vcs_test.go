package terminalview

import (
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/modules/activity/presentation"

	"github.com/charmbracelet/x/ansi"

	"github.com/yusing/mekugi/libraries/terminalui"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestUISnapshotMRunCommit(t *testing.T) {
	source := "mrun --tail -n 35 git commit -F - <<'EOF'\nfix: keep commit activity\n\nRetain the message body.\nEOF\n"
	values := presentation.Operations(presentation.ShellSource(source, "/workspace"))
	if len(values) != 1 || values[0].Verb != "Commit" || !strings.Contains(values[0].Label, "fix: keep commit activity") {
		t.Fatalf("wrapped commit source = %+v", values)
	}
	block := Block{Value: values[0]}
	var p Painter
	if got := RowVerb(block); got != "Commit" {
		t.Fatalf("requested verb = %q", got)
	}
	output := "[main 3a1f9c2] fix: keep commit activity\n 1 file changed, 2 insertions(+)\n"
	block.Changes, _ = presentation.VCSOutputRows(source, "/workspace", output)
	if got := RowVerb(block); got != "Committed" {
		t.Fatalf("observed verb = %q", got)
	}
	uisnapshot.AssertTerminal(t, "testdata/snapshots/mrun_commit.txt", append(p.Block(block, 80), "plain"), 80)
	block.ExitCode = 1
	if got := RowVerb(block); got != "Commit" {
		t.Fatalf("failed verb = %q", got)
	}
}

func vcsRows(p *Painter, block Block, width int) string {
	return ansi.Strip(strings.Join(p.Block(block, width), "\n"))
}

func TestVCSRowsShowTheirResult(t *testing.T) {
	var p Painter
	commit := Block{Kind: "op", Verb: "Commit", Label: "`amend! feat(router): pin latest reply` · git"}
	if got := vcsRows(&p, commit, 80); got != "Commit amend! feat(router): pin latest reply · git" {
		t.Fatalf("requested commit = %q", got)
	}
	if raw := strings.Join(p.Block(commit, 80), ""); !strings.Contains(raw, terminalui.Amber+"amend!") {
		t.Fatalf("autosquash marker is not a marker: %q", raw)
	}
	// Without a named commit, output does not make the request a recorded commit.
	commit.Changes = []ChangeRow{{Code: "M", Label: "a.go", Added: 2, Removed: 1}}
	if got := RowVerb(commit); got != "Commit" {
		t.Fatalf("unnamed commit verb = %q", got)
	}
	commit.Changes = append(commit.Changes, ChangeRow{Code: "A", Label: "doc/b.md", Added: 9}, ChangeRow{Footer: true, Label: "3a1f9c2", Note: "on main · 2 files", Added: 11, Removed: 1})
	commit.Tail, commit.Collapsed = []string{"[main 3a1f9c2] x", " 2 files changed"}, true
	want := strings.Join([]string{
		"Committed amend! feat(router): pin latest reply · git",
		"          M  a.go      +2 -1 ━━━━━━━━",
		"          A  doc/b.md  +9    ━━━━━━━━",
		"          3a1f9c2 on main · 2 files +11 -1",
	}, "\n")
	if got := vcsRows(&p, commit, 80); got != want {
		t.Fatalf("committed rows =\n%s\nwant\n%s", got, want)
	}
	// The subject gives way to keep the source in view.
	if got, _, _ := strings.Cut(vcsRows(&p, commit, 40), "\n"); got != "Committed amend! feat(router): pi… · git" {
		t.Fatalf("narrow heading = %q", got)
	}
	if got := ansi.Strip(p.Summary([]Block{commit}, 80)); got != "Committed amend! feat(router): pin latest reply +11 -1 · git" {
		t.Fatalf("roster summary = %q", got)
	}
	failed := Block{Kind: "op", Verb: "Commit", Label: "`fix: x` · git", ExitCode: 1, Tail: []string{"hook failed"}}
	if got := vcsRows(&p, failed, 80); !strings.HasPrefix(got, "Commit fix: x · git · exit 1\n") || RowVerb(failed) != "Commit" {
		t.Fatalf("failed commit =\n%s", got)
	}
	if raw := p.Block(failed, 80)[0]; !strings.HasPrefix(raw, terminalui.Red) {
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
	if got := statusCode("MM"); got != terminalui.Green+"M\x1b[39m"+terminalui.Red+"M\x1b[39m" {
		t.Fatalf("staged code colors = %q", got)
	}
	if got := statusCode("UU"); strings.Contains(got, terminalui.Green) {
		t.Fatalf("conflict colored as staged: %q", got)
	}
	clean := Block{Kind: "op", Verb: "Status", Label: "`working tree` · git", Tail: []string{"x", "y"}, Changes: []ChangeRow{{Footer: true, Label: "main", Note: "clean"}}}
	if got := vcsRows(&p, clean, 80); got != "Status working tree · git\n       main clean" {
		t.Fatalf("clean status =\n%s", got)
	}
}

func TestVCSFooterFollowsElidedRows(t *testing.T) {
	var rows []ChangeRow
	for range presentation.ChangeRowsShown + 2 {
		rows = append(rows, ChangeRow{Code: "M", Label: "a.go", Added: 1})
	}
	rows = append(rows, ChangeRow{Footer: true, Note: "14 files", Added: 14})
	lines := changeRows(rows, "", 60)
	if got := ansi.Strip(strings.Join(lines[len(lines)-2:], "\n")); got != "… +2 more\n14 files +14" {
		t.Fatalf("closing rows =\n%s", got)
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

func TestTimeoutSuffixIsMuted(t *testing.T) {
	now := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	p := Painter{Clock: func() time.Time { return now }}
	for _, tc := range []struct {
		name  string
		block Block
		want  bool
	}{
		{"live before threshold", Block{Running: true, Started: now.Add(-499 * time.Millisecond)}, false},
		{"live at threshold", Block{Running: true, Started: now.Add(-500 * time.Millisecond)}, true},
		{"fast completion", Block{Duration: 499 * time.Millisecond}, false},
		{"slow completion", Block{Duration: 500 * time.Millisecond}, true},
		{"unavailable timing", Block{}, false},
		{"skipped", Block{Skipped: true, Duration: time.Second}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			block := Block{Content: activitymodel.ParseOperation("Skill `skill` (timeout 123)")}
			block.Running, block.Started, block.Duration, block.Skipped = tc.block.Running, tc.block.Started, tc.block.Duration, tc.block.Skipped
			raw := strings.Join(p.Block(block, 80), "\n")
			if got := strings.Contains(raw, terminalui.Dim+"(timeout 2m3s)"+terminalui.Undim); got != tc.want {
				t.Fatalf("muted timeout shown = %v, want %v: %q", got, tc.want, raw)
			}
		})
	}
}
