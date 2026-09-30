package router

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/execsegment"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
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
	u.view.conversation = true
	return u
}

func TestVCSCommitShowsTheRecordedCommit(t *testing.T) {
	g, output := vcsCommitFixture(t, "amend! feat(router): pin latest reply")
	hash := gitCommitHead.FindStringSubmatch(strings.Split(output, "\n")[0])[2]
	script := "git diff --check && git add a.go b.go gone.txt && git commit -F - <<'EOF'\namend! feat(router): pin latest reply\n\nbody\nEOF"
	u := vcsCommandUI(t, g.dir)
	item := map[string]any{"id": "cmd", "type": "commandExecution", "command": "/usr/bin/bash -lc " + quoteShellWord(script), "cwd": g.dir, "status": "inProgress"}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	u.view.pace(time.Now().Add(time.Second))
	// Staging leads into the commit, so only the check and commit show; the
	// request is not yet a recorded commit.
	if got, want := mainFeed(u, 90), "├ Check  working tree · git diff --check\n└ Commit amend! feat(router): pin latest reply · git"; got != want {
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
	settleActivity(time.Now().Add(activityui.OutputDebounce), u.view)
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
			u := vcsCommandUI(t)
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

func TestVCSCommitWithoutItsObjectKeepsOutputEvidence(t *testing.T) {
	_, output := vcsCommitFixture(t, "feat: add b")
	// Without the host's cwd there is no repository to read the commit from.
	rows, _ := vcsOutputRows("git commit -m 'feat: add b'", "", output)
	hash := gitCommitHead.FindStringSubmatch(strings.Split(output, "\n")[0])[2]
	want := []activityui.ChangeRow{
		{Code: "A", Label: "b.go"},
		{Code: "D", Label: "gone.txt"},
		{Footer: true, Label: hash, Note: "on main · 3 files", Added: 3, Removed: 2},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows = %+v\nwant %+v", rows, want)
	}
	// A message the command did not spell out comes from the output.
	rows, _ = vcsOutputRows("git commit --amend --no-edit", "", output)
	if footer := rows[len(rows)-1]; footer.Note != "feat: add b · on main · 3 files" {
		t.Fatalf("unnamed commit footer = %+v", footer)
	}
	// Output that names no commit claims none.
	if rows, _ := vcsOutputRows("git commit -m x", "", "On branch main\nnothing to commit, working tree clean\n"); rows != nil {
		t.Fatalf("nothing committed, rows = %+v", rows)
	}
}

func TestVCSCommitReadIsBoundedToItsObject(t *testing.T) {
	g, output := vcsCommitFixture(t, "feat: cached")
	hash := gitCommitHead.FindStringSubmatch(strings.Split(output, "\n")[0])[2]
	calls := 0
	run := gitShowRun
	gitShowRun = func(ctx context.Context, dir string, args ...string) ([]byte, error) {
		calls++
		if dir != g.dir || !slices.Contains(args, hash+"^{commit}") {
			t.Errorf("git show in %q with %q", dir, args)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Error("git show has no deadline")
		}
		return run(ctx, dir, args...)
	}
	t.Cleanup(func() { gitShowRun = run })
	// A relative -C directory resolves against the host's cwd.
	rows, key := vcsOutputRows("git -C "+filepath.Base(g.dir)+" commit -m x", filepath.Dir(g.dir), output)
	if key != (gitCommitKey{g.dir, hash}) {
		t.Fatalf("key = %+v", key)
	}
	// Until its object is read, the output's rows stand and the object is
	// requested rather than read in place.
	if got := commitChanges(rows, key); !reflect.DeepEqual(got, rows) || calls != 0 {
		t.Fatalf("unread rows = %+v after %d reads", got, calls)
	}
	if !slices.Contains(gitShowRequests(), key) {
		t.Fatal("unread commit was not requested")
	}
	if !gitShowRead(t.Context(), key) {
		t.Fatal("commit object unread")
	}
	// A commit hash names immutable content, so it is read once.
	for range 2 {
		if got := commitChanges(rows, key); len(got) != 4 || got[0].Added != 2 || got[3] != rows[len(rows)-1] {
			t.Fatalf("rows = %+v", got)
		}
	}
	if calls != 1 || slices.Contains(gitShowRequests(), key) {
		t.Fatalf("git show ran %d times", calls)
	}
}

func TestVCSCommitReadDoesNotBlockTheUI(t *testing.T) {
	g, output := vcsCommitFixture(t, "feat: slow")
	release := make(chan struct{})
	run := gitShowRun
	gitShowRun = func(ctx context.Context, dir string, args ...string) ([]byte, error) {
		if dir == g.dir {
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return run(ctx, dir, args...)
	}
	t.Cleanup(func() { gitShowRun = run })
	u := vcsCommandUI(t, g.dir)
	item := map[string]any{"id": "cmd", "type": "commandExecution", "command": "git commit -m 'feat: slow'", "cwd": g.dir,
		"status": "completed", "exitCode": 0, "aggregatedOutput": output}
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	u.startCommitReads()
	// While the object is read, the output's own rows show.
	if got := mainFeed(u, 90); !strings.Contains(got, "A  b.go\n") || strings.Contains(got, "a.go") {
		t.Fatalf("pending commit =\n%s", got)
	}
	close(release)
	if got := awaitMain(t, u, "M  a.go      +2 -1"); !strings.Contains(got, "A  b.go      +1") {
		t.Fatalf("read commit =\n%s", got)
	}
}

func TestVCSCommitOfManyFilesClosesOnce(t *testing.T) {
	g := newGitFixture(t)
	for i := range activityui.ChangeRowsShown + 1 {
		g.write(fmt.Sprintf("f%02d", i), "x\n")
	}
	g.run("add", ".")
	output := g.run("commit", "-m", "many")
	rows, key := vcsOutputRows("git commit -m many", g.dir, output)
	commitChanges(rows, key)
	if !gitShowRead(t.Context(), key) {
		t.Fatal("commit object unread")
	}
	rows = commitChanges(rows, key)
	if footers := slices.DeleteFunc(slices.Clone(rows), func(row activityui.ChangeRow) bool { return !row.Footer }); len(footers) != 1 || len(rows) != 14 {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestVCSTrackedCommitShowsItsRows(t *testing.T) {
	g, output := vcsCommitFixture(t, "feat: tracked")
	u, hub := newTrackedAppServerUI(t)
	u.session.cwd = g.dir
	script := "git add a.go b.go && git commit -m 'feat: tracked'"
	item := map[string]any{"id": "cmd", "type": "commandExecution", "command": "/usr/bin/bash -lc " + quoteShellWord(script), "cwd": g.dir, "status": "inProgress"}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	report := dialExecTrackReport(t, hub, script)
	// Tabs survive in the segment's own output, which the rows read.
	report.send(
		execsegment.Message{Type: execsegment.Begin, Index: 0},
		execsegment.Message{Type: execsegment.End, Index: 0, Code: new(0)},
		execsegment.Message{Type: execsegment.Begin, Index: 1},
		execsegment.Message{Type: execsegment.Output, Index: 1, Data: output},
		execsegment.Message{Type: execsegment.End, Index: 1, Code: new(0)},
		execsegment.Message{Type: execsegment.Done, Code: new(0)},
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

func TestVCSDiffRowsCountEachHunk(t *testing.T) {
	gitPatch := strings.Join([]string{
		"diff --git a/internal/a.go b/internal/a.go",
		"index 1..2 100644",
		"--- a/internal/a.go",
		"+++ b/internal/a.go",
		"@@ -1,3 +1,3 @@",
		" keep",
		"--- a removed line that looks like a header",
		"+++ an added line that looks like a header",
		" keep",
		"@@ -10 +10,2 @@",
		"-old",
		"+new",
		"+more",
		"\\ No newline at end of file",
		"diff --git a/new.go b/new.go",
		"new file mode 100644",
		"--- /dev/null",
		"+++ b/new.go",
		"@@ -0,0 +1 @@",
		"+package x",
		"diff --git a/gone.go b/gone.go",
		"deleted file mode 100644",
		"--- a/gone.go",
		"+++ /dev/null",
		"@@ -1,2 +0,0 @@",
		"-a",
		"-b",
		"diff --git a/old name.go b/new name.go",
		"similarity index 90%",
		"rename from old name.go",
		"rename to new name.go",
		"diff --git a/logo.png b/logo.png",
		"Binary files a/logo.png and b/logo.png differ",
		"diff --git \"a/tab\\there.go\" \"b/tab\\there.go\"",
		"--- \"a/tab\\there.go\"",
		"+++ \"b/tab\\there.go\"",
		"@@ -1 +1 @@",
		"-x",
		"+y",
		"",
	}, "\n")
	want := []activityui.ChangeRow{
		{Code: "M", Label: "internal/a.go", Added: 3, Removed: 2},
		{Code: "A", Label: "new.go", Added: 1},
		{Code: "D", Label: "gone.go", Removed: 2},
		{Code: "R", From: "old name.go", Label: "new name.go"},
		{Code: "M", Label: "logo.png", Note: "binary"},
		{Code: "M", Label: "tab    here.go", Added: 1, Removed: 1},
	}
	if rows, _ := vcsOutputRows("git diff", "", "\x1b[1m"+gitPatch); !reflect.DeepEqual(rows, want) {
		t.Fatalf("git rows = %+v\nwant %+v", rows, want)
	}
	svnPatch := "Index: trunk/a.c\n===================================================================\n--- trunk/a.c\t(revision 4811)\n+++ trunk/a.c\t(working copy)\n@@ -1 +1,2 @@\n-a\n+b\n+c\n" +
		"Index: trunk/new.c\n===================================================================\n--- trunk/new.c\t(nonexistent)\n+++ trunk/new.c\t(working copy)\n@@ -0,0 +1 @@\n+x\n"
	want = []activityui.ChangeRow{{Code: "M", Label: "trunk/a.c", Added: 2, Removed: 1}, {Code: "A", Label: "trunk/new.c", Added: 1}}
	if rows, _ := vcsOutputRows("svn diff", "", svnPatch); !reflect.DeepEqual(rows, want) {
		t.Fatalf("svn rows = %+v", rows)
	}
	// mchanges prints each record's patch with quoted paths; one path's
	// records sum.
	mchangesPatch := "amber1\n--- \"a.go\"\n+++ \"a.go\"\n@@ -1 +1 @@\n-x\n+y\namber2\n--- \"a.go\"\n+++ \"a.go\"\n@@ -1 +1,2 @@\n-y\n+z\n+w\n--- /dev/null\n+++ \"b.go\"\n@@ -0,0 +1 @@\n+b\n"
	want = []activityui.ChangeRow{{Code: "M", Label: "a.go", Added: 3, Removed: 2}, {Code: "A", Label: "b.go", Added: 1}}
	if rows, _ := vcsOutputRows("mchanges amber1..amber2", "", mchangesPatch); !reflect.DeepEqual(rows, want) {
		t.Fatalf("mchanges rows = %+v", rows)
	}
	// A merge's combined diff names no counts, and its headers no move.
	merge := "commit 0123456789abcdef\nMerge: 1234567 89abcde\n\n    merge\n\ndiff --cc a.go\nindex 1,2..3\n--- a/a.go\n+++ b/a.go\n@@@ -1,1 -1,1 +1,2 @@@\n  x\n++y\n" + gitPatch
	if rows, _ := vcsOutputRows("git show", "", merge); len(rows) != 6 || rows[0].Label != "internal/a.go" {
		t.Fatalf("merge rows = %+v", rows)
	}
	// A list's combined output cannot be attributed to its diff.
	if rows, _ := vcsOutputRows("echo x && git diff", "", gitPatch); rows != nil {
		t.Fatalf("combined output read as a diff: %+v", rows)
	}
	var many strings.Builder
	for i := range activityui.ChangeRowsShown + 1 {
		fmt.Fprintf(&many, "--- f%d\n+++ f%d\n@@ -1 +1 @@\n-a\n+b\n", i, i)
	}
	rows, _ := vcsOutputRows("svn diff", "", many.String())
	if footer := rows[len(rows)-1]; !footer.Footer || footer.Note != "13 files" || footer.Added != 13 || footer.Removed != 13 {
		t.Fatalf("many-file footer = %+v", footer)
	}
}

func TestVCSStatRows(t *testing.T) {
	for _, tc := range []struct {
		command, output string
		want            []activityui.ChangeRow
	}{
		{"git diff --stat", " a.go         | 3 ++-\n {old => new}/b.go | 0\n logo.png     | Bin 0 -> 12 bytes\n 3 files changed, 2 insertions(+), 1 deletion(-)\n", []activityui.ChangeRow{
			{Code: "M", Label: "a.go", Added: 2, Removed: 1},
			{Code: "R", From: "old/b.go", Label: "new/b.go"},
			{Code: "M", Label: "logo.png", Note: "binary"},
			{Footer: true, Note: "3 files", Added: 2, Removed: 1},
		}},
		// A scaled graph does not split its counts; only the totals are exact.
		{"git diff --stat", " a.go | 300 ++++++++++-----\n b.go |   4 +\n 2 files changed, 204 insertions(+), 100 deletions(-)\n", []activityui.ChangeRow{
			{Code: "M", Label: "a.go", Note: "300 lines"},
			{Code: "M", Label: "b.go", Note: "4 lines"},
			{Footer: true, Note: "2 files", Added: 204, Removed: 100},
		}},
		{"git show --numstat", "commit 0123456789abcdef\nAuthor: T <t@example.com>\n\n    subject\n\n12\t3\ta.go\n-\t-\tlogo.png\n1\t1\tsrc/{a => b}/c.go\n", []activityui.ChangeRow{
			{Code: "M", Label: "a.go", Added: 12, Removed: 3},
			{Code: "M", Label: "logo.png", Note: "binary"},
			{Code: "R", From: "src/a/c.go", Label: "src/b/c.go", Added: 1, Removed: 1},
		}},
		{"git diff --shortstat", " 1 file changed, 4 insertions(+)\n", []activityui.ChangeRow{{Footer: true, Note: "1 file", Added: 4}}},
		{"git diff --name-status", "M\ta.go\nA\tb.go\nR087\told.go\tnew.go\nD\tgone.go\n", []activityui.ChangeRow{
			{Code: "M", Label: "a.go"}, {Code: "A", Label: "b.go"}, {Code: "R", From: "old.go", Label: "new.go"}, {Code: "D", Label: "gone.go"},
		}},
		{"git show --name-only", "commit 0123456789abcdef\nAuthor: T <t@example.com>\nDate:   now\n\n    subject\n\na.go\n", []activityui.ChangeRow{{Label: "a.go"}}},
		{"svn diff --summarize", "M       trunk/a.c\nA       trunk/b.c\n M      trunk\n", []activityui.ChangeRow{
			{Verb: "Edited", Label: "trunk/a.c"}, {Verb: "Created", Label: "trunk/b.c"}, {Verb: "Edited", Label: "trunk", Note: "properties"},
		}},
	} {
		if rows, _ := vcsOutputRows(tc.command, "", tc.output); !reflect.DeepEqual(rows, tc.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", tc.command, rows, tc.want)
		}
	}
}

func TestVCSStatusRows(t *testing.T) {
	long := strings.Join([]string{
		"On branch feat/vcs-events",
		"Your branch is ahead of 'origin/feat/vcs-events' by 2 commits.",
		"  (use \"git push\" to publish your local commits)",
		"",
		"Changes to be committed:",
		"  (use \"git restore --staged <file>...\" to unstage)",
		"\tmodified:   internal/router/vcs_display.go",
		"\tnew file:   internal/router/vcs_display_test.go",
		"\trenamed:    old.go -> new.go",
		"",
		"Changes not staged for commit:",
		"\tmodified:   internal/router/vcs_display.go",
		"\tdeleted:    gone.go",
		"\tmodified:   sub (new commits)",
		"",
		"Unmerged paths:",
		"\tboth modified:   conflict.go",
		"",
		"Untracked files:",
		"  (use \"git add <file>...\" to include in what will be committed)",
		"\tscratch.txt",
		"",
	}, "\n")
	want := []activityui.ChangeRow{
		{Code: "MM", Label: "internal/router/vcs_display.go"},
		{Code: "A ", Label: "internal/router/vcs_display_test.go"},
		{Code: "R ", From: "old.go", Label: "new.go"},
		{Code: " D", Label: "gone.go"},
		{Code: " M", Label: "sub"},
		{Code: "UU", Label: "conflict.go"},
		{Code: "??", Label: "scratch.txt"},
		{Footer: true, Label: "feat/vcs-events", Note: "↑2"},
	}
	if rows, _ := vcsOutputRows("git status", "", long); !reflect.DeepEqual(rows, want) {
		t.Fatalf("long status = %+v\nwant %+v", rows, want)
	}
	short := "## main...origin/main [ahead 1, behind 3]\nM  a.go\n M b.go\nR  x -> y\n?? z\n"
	want = []activityui.ChangeRow{{Code: "M ", Label: "a.go"}, {Code: " M", Label: "b.go"}, {Code: "R ", From: "x", Label: "y"}, {Code: "??", Label: "z"}, {Footer: true, Label: "main", Note: "↑1 ↓3"}}
	if rows, _ := vcsOutputRows("git status -sb", "", short); !reflect.DeepEqual(rows, want) {
		t.Fatalf("short status = %+v", rows)
	}
	clean := "On branch main\nYour branch and 'origin/main' have diverged,\nand have 1 and 2 different commits each, respectively.\n\nnothing to commit, working tree clean\n"
	want = []activityui.ChangeRow{{Footer: true, Label: "main", Note: "↑1 ↓2 · clean"}}
	if rows, _ := vcsOutputRows("git status", "", clean); !reflect.DeepEqual(rows, want) {
		t.Fatalf("clean status = %+v", rows)
	}
	svn := "M       trunk/a.c\n?       trunk/new.c\n M      trunk\nC       trunk/c.c\n"
	want = []activityui.ChangeRow{{Code: "M ", Label: "trunk/a.c"}, {Code: "? ", Label: "trunk/new.c"}, {Code: " M", Label: "trunk"}, {Code: "C ", Label: "trunk/c.c"}}
	if rows, _ := vcsOutputRows("svn st", "", svn); !reflect.DeepEqual(rows, want) {
		t.Fatalf("svn status = %+v", rows)
	}
}

func TestVCSSVNCommitRows(t *testing.T) {
	output := "Sending        trunk/a.c\nAdding  (bin)  trunk/logo.png\nDeleting       trunk/old.c\nTransmitting file data ..done\nCommitting transaction...\nCommitted revision 4812.\n"
	want := []activityui.ChangeRow{
		{Verb: "Edited", Label: "trunk/a.c"}, {Verb: "Created", Label: "trunk/logo.png"}, {Verb: "Deleted", Label: "trunk/old.c"},
		{Footer: true, Label: "r4812"},
	}
	if rows, _ := vcsOutputRows("svn commit -m 'r: fix'", "", output); !reflect.DeepEqual(rows, want) {
		t.Fatalf("svn commit rows = %+v", rows)
	}
}
