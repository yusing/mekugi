package router

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	"github.com/yusing/mekugi/libraries/shellinstrumentation"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	segmenttransport "github.com/yusing/mekugi/modules/commandsegments/transport"
)

// gitFixture is a repository whose commands ignore the user's git config.
type gitFixture struct {
	t   *testing.T
	dir string
}

func newGitFixture(t *testing.T) gitFixture {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	g := gitFixture{t: t, dir: t.TempDir()}
	g.run("init", "-q", "-b", "main")
	return g
}

func (g gitFixture) run(args ...string) string {
	g.t.Helper()
	command := exec.Command("git", append([]string{"-C", g.dir, "-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		g.t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}

func (g gitFixture) write(name, content string) {
	g.t.Helper()
	if err := os.WriteFile(filepath.Join(g.dir, name), []byte(content), 0o644); err != nil {
		g.t.Fatal(err)
	}
}

// vcsCommitFixture commits a.go's edit, b.go's creation, and gone.txt's
// deletion, returning git's own commit output.
func vcsCommitFixture(t *testing.T, subject string) (gitFixture, string) {
	t.Helper()
	g := newGitFixture(t)
	g.write("a.go", "one\ntwo\nthree\n")
	g.write("gone.txt", "x\n")
	g.run("add", ".")
	g.run("commit", "-q", "-m", "base")
	g.write("a.go", "one\n2\nthree\nfour\n")
	g.write("b.go", "b\n")
	if err := os.Remove(filepath.Join(g.dir, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	g.run("add", "-A")
	return g, g.run("commit", "-m", subject, "-m", "body")
}

func vcsCommandUI(t *testing.T, workspace string) *appServerUI {
	t.Helper()
	u := newAppServerSessionTestUI(t, workspace)
	u.shell.Transcript.Conversation = true
	u.testActivity(u.shell.Transcript).testAudience()
	return u
}

func TestVCSCommitShowsTheRecordedCommit(t *testing.T) {
	g, output := vcsCommitFixture(t, "amend! feat(router): pin latest reply")
	_, commit := activitypresentation.VCSOutputRows("git commit -m x", g.dir, output)
	hash := commit.Hash
	script := "git diff --check && git add a.go b.go gone.txt && timeout 90s git commit -F - <<'EOF'\namend! feat(router): pin latest reply\n\nbody\nEOF"
	var err error
	script, err = shellinstrumentation.RewriteCommands(script, displayGuardHelper, displayGuardDirectory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	u := vcsCommandUI(t, g.dir)
	now := time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC)
	u.clock = func() time.Time { return now }
	u.shell.Transcript.Clock = u.clock
	item := map[string]any{"id": "cmd", "type": "commandExecution", "command": "/usr/bin/bash -lc " + quoteShellWord(script), "cwd": g.dir, "status": "inProgress"}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	u.testActivity(u.shell.Transcript).testPace(now.Add(time.Second))
	// Staging folds into the commit; the extra batch row owns invocation timing.
	// The request is not yet a recorded commit.
	if got, want := mainFeed(u, 90), "├ Check   working tree · git diff --check\n├ Commit  amend! feat(router): pin latest reply · git\n└ Running shell batch"; got != want {
		t.Fatalf("started commit =\n%s\nwant\n%s", got, want)
	}
	item["status"], item["exitCode"], item["aggregatedOutput"] = "completed", 0, output
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	awaitMain(t, u, "M  a.go")
	want := strings.Join([]string{
		"├ Check working tree · git diff --check",
		"└ Committed amend! feat(router): pin latest reply · git",
		"            M  a.go      +2 -1 ━━━━━━━━",
		"            A  b.go      +1    ━━━━━━━━",
		"            D  gone.txt  -1    ━━━━━━━━",
		"            " + hash + " on main · 3 files +3 -2",
	}, "\n")
	if got := mainFeed(u, 90); got != want {
		t.Fatalf("committed rows =\n%s\nwant\n%s", got, want)
	}
	// A narrow row shortens the subject, never the source.
	if got := mainFeed(u, 50); !strings.Contains(got, "└ Committed amend! feat(router): pin latest… · git\n") {
		t.Fatalf("narrow commit =\n%s", got)
	}
	// The rows are the commit's result; they stay after the agent moves on.
	nextEvent(t, u, "main")
	settleActivity(time.Now().Add(activityui.OutputDebounce), u.testActivity(u.shell.Transcript))
	if got := mainFeed(u, 90); !strings.Contains(got, "A  b.go") {
		t.Fatalf("commit rows collapsed:\n%s", got)
	}
}

func TestVCSStatAndCommitShowSharedFileCodes(t *testing.T) {
	g := newGitFixture(t)
	g.write("edit.txt", "before\n")
	g.write("gone.txt", "gone\n")
	g.write("old.txt", "rename\n")
	g.run("add", ".")
	g.run("commit", "-qm", "base")
	g.write("edit.txt", "after\n")
	g.write("new.txt", "new\n")
	if err := os.Remove(filepath.Join(g.dir, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	g.run("mv", "old.txt", "renamed.txt")
	g.run("add", "-A")
	stat := g.run("diff", "--cached", "--stat", "--summary")
	commit := g.run("commit", "-m", "file codes")
	for _, tc := range []struct{ command, output string }{
		{"git diff --cached --stat --summary", stat},
		{"git commit -m 'file codes'", commit},
	} {
		t.Run(tc.command, func(t *testing.T) {
			u := vcsCommandUI(t, g.dir)
			item := map[string]any{"id": "codes", "type": "commandExecution", "command": tc.command, "cwd": g.dir,
				"status": "completed", "exitCode": 0, "aggregatedOutput": tc.output}
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": item})
			got := awaitMain(t, u, "M  edit.txt")
			for _, row := range []string{"M  edit.txt", "A  new.txt", "D  gone.txt", "R  old.txt → renamed.txt"} {
				if !strings.Contains(got, row) {
					t.Errorf("missing %q in rendered result:\n%s", row, got)
				}
			}
		})
	}
}

func TestVCSCommitOfManyFilesClosesOnce(t *testing.T) {
	g := newGitFixture(t)
	for i := range activitypresentation.ChangeRowsShown + 1 {
		g.write(fmt.Sprintf("f%02d", i), "x\n")
	}
	g.run("add", ".")
	output := g.run("commit", "-m", "many")
	rows, key := activitypresentation.VCSOutputRows("git commit -m many", g.dir, output)
	reader := activitypresentation.NewCommitReader(t.Context())
	defer reader.Close()
	reader.Request(key)
	select {
	case <-reader.Updates():
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	files, ready := reader.Request(key)
	if !ready {
		t.Fatal("commit object unread")
	}
	rows = activitypresentation.CommitChanges(rows, key, files)
	if footers := slices.DeleteFunc(slices.Clone(rows), func(row activitypresentation.ChangeRow) bool { return !row.Footer }); len(footers) != 1 || len(rows) != 14 {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestVCSCommitReadDoesNotBlockTheUI(t *testing.T) {
	g, output := vcsCommitFixture(t, "feat: slow")
	bin := t.TempDir()
	program := "#!/bin/sh\nwhile [ ! -f \"$2/release\" ]; do sleep 0.01; done\nprintf '2\\t1\\ta.go\\n1\\t0\\tb.go\\n0\\t1\\tgone.txt\\n create mode 100644 b.go\\n delete mode 100644 gone.txt\\n'\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(program), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	u := vcsCommandUI(t, g.dir)
	item := map[string]any{"id": "cmd", "type": "commandExecution", "command": "git commit -m 'feat: slow'", "cwd": g.dir,
		"status": "completed", "exitCode": 0, "aggregatedOutput": output}
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	u.dirty = startCommitReads(&u.attached, u.commits) || u.dirty
	if got := mainFeed(u, 90); !strings.Contains(got, "A  b.go\n") || strings.Contains(got, "a.go") {
		t.Fatalf("pending commit =\n%s", got)
	}
	if err := os.WriteFile(filepath.Join(g.dir, "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := awaitMain(t, u, "M  a.go      +2 -1"); !strings.Contains(got, "A  b.go      +1") {
		t.Fatalf("read commit =\n%s", got)
	}
	previous := u.commits
	replaceAppServerTestSession(t, u, "replacement")
	if u.commits == previous {
		t.Fatal("replacement retained commit cache")
	}
	_, key := activitypresentation.VCSOutputRows("git commit -m x", g.dir, output)
	if _, ready := previous.Request(key); ready {
		t.Fatal("retired attachment remained active")
	}
}

func TestVCSTrackedCommitShowsItsRows(t *testing.T) {
	g, output := vcsCommitFixture(t, "feat: tracked")
	u, hub := newTrackedAppServerUI(t)
	u.attached.cwd = g.dir
	script, err := shellinstrumentation.RewriteCommands("git add a.go b.go && timeout 90s git commit -F - <<'EOF'\nfeat: tracked\n\nbody\nEOF", displayGuardHelper, displayGuardDirectory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	item := map[string]any{"id": "cmd", "type": "commandExecution", "command": "/usr/bin/bash -lc " + quoteShellWord(script), "cwd": g.dir, "status": "inProgress"}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	report := dialExecTrackReport(t, hub, script)
	// Tabs survive in the segment's own output, which the rows read.
	report.send(
		segmenttransport.Message{Type: segmenttransport.Begin, Index: 0},
		segmenttransport.Message{Type: segmenttransport.End, Index: 0, Code: new(0)},
		segmenttransport.Message{Type: segmenttransport.Begin, Index: 1},
		segmenttransport.Message{Type: segmenttransport.Output, Index: 1, Data: output},
		segmenttransport.Message{Type: segmenttransport.End, Index: 1, Code: new(0)},
		segmenttransport.Message{Type: segmenttransport.Done, Code: new(0)},
		segmenttransport.Message{Type: segmenttransport.Closed},
	)
	report.conn.Close()
	item["status"], item["exitCode"], item["aggregatedOutput"] = "completed", 0, output
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	got := awaitMain(t, u, "A  b.go      +1")
	if !strings.Contains(got, "└ Committed feat: tracked · git\n") || strings.Contains(got, "Stage") {
		t.Fatalf("tracked commit =\n%s", got)
	}
}

func TestVCSCommandClassification(t *testing.T) {
	for _, tc := range []struct{ source, want string }{
		{"git commit -m 'fix: bound it' -m body", "Commit `fix: bound it` · git"},
		{"timeout 90s git commit -F - <<'EOF'\ndocs(ui): describe content-detected inline highlighting\nEOF", "Commit `docs(ui): describe content-detected inline highlighting` · git (timeout 90s)"},
		{"timeout 1.5m git commit -m 'fix: bounded'", "Commit `fix: bounded` · git (timeout 1.5m)"},
		{"git commit -am 'fix: all'", "Commit `fix: all` · git"},
		{"git commit -qm'fix: attached'", "Commit `fix: attached` · git"},
		{"git commit --message='feat: `code` in subject'", "Commit `` feat: `code` in subject `` · git"},
		{"git commit -F - <<'EOF'\n\nfeat: heredoc\n\nbody\nEOF", "Commit `feat: heredoc` · git"},
		{"git commit -F- <<< 'feat: here-string'", "Commit `feat: here-string` · git"},
		{"git commit -m \"$(cat <<'EOF'\nfeat: substituted\n\nbody\nEOF\n)\"", "Commit `feat: substituted` · git"},
		{"git commit --amend --no-edit", "Commit · git --amend"},
		{"git commit --fixup=HEAD~2", "Commit · git --fixup=HEAD~2"},
		{"git commit -m x -- a.go b.go", "Commit `x` in `a.go` `b.go` · git"},
		{"git -C sub --no-pager commit -m x", "Commit `x` · git -C sub"},
		{"svn ci -m 'r: fix' trunk/a.c", "Commit `r: fix` in `trunk/a.c` · svn"},
		{"git add a.go b.go", "Stage `a.go` `b.go` · git"},
		{"git add -A", "Stage · git add -A"},
		{"git diff", "Diff `working tree` · git"},
		{"git diff --cached -- internal/ui", "Diff `staged` in `internal/ui` · git"},
		{"git diff --staged HEAD~1", "Diff `staged vs HEAD~1` · git"},
		{"git diff main...HEAD --stat=120", "Diff `main...HEAD` · git --stat"},
		{"git diff --numstat -- a b c d", "Diff `working tree` in `a` `b` `c` +1 more · git --numstat"},
		{"git diff --check HEAD", "Check `HEAD` · git diff --check"},
		{"git show", "Diff `HEAD` · git show"},
		{"git show --stat abc123 2>&1", "Diff `abc123` · git show --stat"},
		{"git status -sb", "Status `working tree` · git"},
		{"git status --porcelain -- doc", "Status `working tree` in `doc` · git"},
		{"svn diff -c 4812", "Diff `r4812` · svn"},
		{"svn di -r100:HEAD --summarize trunk", "Diff `r100:HEAD` in `trunk` · svn --summarize"},
		{"svn st -q", "Status `working copy` · svn"},
		{"timeout 90s mchanges", "Diff `mine` · mchanges (timeout 90s)"},
		{"mchanges amber1..amber4", "Diff `amber1..amber4` · mchanges"},
		{"mchanges --net -- a.go", "Diff `mine` in `a.go` · mchanges --net"},
		{"/tmp/bin/mchanges amber2 --summary", "Diff `amber2` · mchanges --summary"},
		{"git status && git diff --stat", "Status `working tree` · git\n\nDiff `working tree` · git --stat"},
	} {
		if got := toolActivityShell(tc.source); got != tc.want {
			t.Errorf("%q:\n got %q\nwant %q", tc.source, got, tc.want)
		}
	}
	// Forms whose effect or message the display cannot read stay Run.
	for _, source := range []string{
		"timeout invalid git commit -m x",
		"timeout 90s git commit -m \"$msg\"",
		"git commit -m \"$msg\"",
		"git add *.go",
		"git diff ~/other",
		"git diff {a,b}.go",
		"git commit --dry-run -m x",
		"git commit -p -m x",
		"git commit -c HEAD",
		"git commit -F msg.txt <<'EOF'\nx\nEOF",
		"git commit -F - <<EOF\nfeat: $name\nEOF",
		"git commit -m x > out.txt",
		"GIT_EDITOR=true git commit",
		"git -c core.pager=less diff",
		"git add -p",
		"git add",
		"git diff --output=patch.diff",
		"git diff --ext-diff",
		"git show HEAD:README.md",
		"git status --porcelain=v2",
		"git status -z",
		"git log --stat",
		"svn st -u",
		"mchanges --list",
		"mchanges --history amber1",
		"mchanges revert amber1",
	} {
		if got := toolActivityShell(source); !strings.HasPrefix(got, "Run") {
			t.Errorf("%q classified as %q", source, got)
		}
	}
}
