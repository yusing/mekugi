package presentation

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestVCSCommitWithoutItsObjectKeepsOutputEvidence(t *testing.T) {
	output := "[main abcd1234] feat: add b\n 3 files changed, 3 insertions(+), 2 deletions(-)\n create mode 100644 b.go\n delete mode 100644 gone.txt\n"
	// Without the host's cwd there is no repository to read the commit from.
	rows, _ := VCSOutputRows("git commit -m 'feat: add b'", "", output)
	hash := gitCommitHead.FindStringSubmatch(strings.Split(output, "\n")[0])[2]
	want := []ChangeRow{
		{Code: "A", Label: "b.go"},
		{Code: "D", Label: "gone.txt"},
		{Footer: true, Label: hash, Note: "on main · 3 files", Added: 3, Removed: 2},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows = %+v\nwant %+v", rows, want)
	}
	// A message the command did not spell out comes from the output.
	rows, _ = VCSOutputRows("git commit --amend --no-edit", "", output)
	if footer := rows[len(rows)-1]; footer.Note != "feat: add b · on main · 3 files" {
		t.Fatalf("unnamed commit footer = %+v", footer)
	}
	// Output that names no commit claims none.
	if rows, _ := VCSOutputRows("git commit -m x", "", "On branch main\nnothing to commit, working tree clean\n"); rows != nil {
		t.Fatalf("nothing committed, rows = %+v", rows)
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
	want := []ChangeRow{
		{Code: "M", Label: "internal/a.go", Added: 3, Removed: 2},
		{Code: "A", Label: "new.go", Added: 1},
		{Code: "D", Label: "gone.go", Removed: 2},
		{Code: "R", From: "old name.go", Label: "new name.go"},
		{Code: "M", Label: "logo.png", Note: "binary"},
		{Code: "M", Label: "tab    here.go", Added: 1, Removed: 1},
	}
	if rows, _ := VCSOutputRows("git diff", "", "\x1b[1m"+gitPatch); !reflect.DeepEqual(rows, want) {
		t.Fatalf("git rows = %+v\nwant %+v", rows, want)
	}
	svnPatch := "Index: trunk/a.c\n===================================================================\n--- trunk/a.c\t(revision 4811)\n+++ trunk/a.c\t(working copy)\n@@ -1 +1,2 @@\n-a\n+b\n+c\n" +
		"Index: trunk/new.c\n===================================================================\n--- trunk/new.c\t(nonexistent)\n+++ trunk/new.c\t(working copy)\n@@ -0,0 +1 @@\n+x\n"
	want = []ChangeRow{{Code: "M", Label: "trunk/a.c", Added: 2, Removed: 1}, {Code: "A", Label: "trunk/new.c", Added: 1}}
	if rows, _ := VCSOutputRows("svn diff", "", svnPatch); !reflect.DeepEqual(rows, want) {
		t.Fatalf("svn rows = %+v", rows)
	}
	// mchanges prints each record's patch with quoted paths; one path's
	// records sum.
	mchangesPatch := "amber1\n--- \"a.go\"\n+++ \"a.go\"\n@@ -1 +1 @@\n-x\n+y\namber2\n--- \"a.go\"\n+++ \"a.go\"\n@@ -1 +1,2 @@\n-y\n+z\n+w\n--- /dev/null\n+++ \"b.go\"\n@@ -0,0 +1 @@\n+b\n"
	want = []ChangeRow{{Code: "M", Label: "a.go", Added: 3, Removed: 2}, {Code: "A", Label: "b.go", Added: 1}}
	if rows, _ := VCSOutputRows("mchanges amber1..amber2", "", mchangesPatch); !reflect.DeepEqual(rows, want) {
		t.Fatalf("mchanges rows = %+v", rows)
	}
	// A merge's combined diff names no counts, and its headers no move.
	merge := "commit 0123456789abcdef\nMerge: 1234567 89abcde\n\n    merge\n\ndiff --cc a.go\nindex 1,2..3\n--- a/a.go\n+++ b/a.go\n@@@ -1,1 -1,1 +1,2 @@@\n  x\n++y\n" + gitPatch
	if rows, _ := VCSOutputRows("git show", "", merge); len(rows) != 6 || rows[0].Label != "internal/a.go" {
		t.Fatalf("merge rows = %+v", rows)
	}
	// A list's combined output cannot be attributed to its diff.
	if rows, _ := VCSOutputRows("echo x && git diff", "", gitPatch); rows != nil {
		t.Fatalf("combined output read as a diff: %+v", rows)
	}
	var many strings.Builder
	for i := range ChangeRowsShown + 1 {
		fmt.Fprintf(&many, "--- f%d\n+++ f%d\n@@ -1 +1 @@\n-a\n+b\n", i, i)
	}
	rows, _ := VCSOutputRows("svn diff", "", many.String())
	if footer := rows[len(rows)-1]; !footer.Footer || footer.Note != "13 files" || footer.Added != 13 || footer.Removed != 13 {
		t.Fatalf("many-file footer = %+v", footer)
	}
}

func TestVCSStatRows(t *testing.T) {
	for _, tc := range []struct {
		command, output string
		want            []ChangeRow
	}{
		{"git diff --stat", " a.go         | 3 ++-\n {old => new}/b.go | 0\n logo.png     | Bin 0 -> 12 bytes\n 3 files changed, 2 insertions(+), 1 deletion(-)\n", []ChangeRow{
			{Code: "M", Label: "a.go", Added: 2, Removed: 1},
			{Code: "R", From: "old/b.go", Label: "new/b.go"},
			{Code: "M", Label: "logo.png", Note: "binary"},
			{Footer: true, Note: "3 files", Added: 2, Removed: 1},
		}},
		// A scaled graph does not split its counts; only the totals are exact.
		{"git diff --stat", " a.go | 300 ++++++++++-----\n b.go |   4 +\n 2 files changed, 204 insertions(+), 100 deletions(-)\n", []ChangeRow{
			{Code: "M", Label: "a.go", Note: "300 lines"},
			{Code: "M", Label: "b.go", Note: "4 lines"},
			{Footer: true, Note: "2 files", Added: 204, Removed: 100},
		}},
		{"git show --numstat", "commit 0123456789abcdef\nAuthor: T <t@example.com>\n\n    subject\n\n12\t3\ta.go\n-\t-\tlogo.png\n1\t1\tsrc/{a => b}/c.go\n", []ChangeRow{
			{Code: "M", Label: "a.go", Added: 12, Removed: 3},
			{Code: "M", Label: "logo.png", Note: "binary"},
			{Code: "R", From: "src/a/c.go", Label: "src/b/c.go", Added: 1, Removed: 1},
		}},
		{"git diff --shortstat", " 1 file changed, 4 insertions(+)\n", []ChangeRow{{Footer: true, Note: "1 file", Added: 4}}},
		{"git diff --name-status", "M\ta.go\nA\tb.go\nR087\told.go\tnew.go\nD\tgone.go\n", []ChangeRow{
			{Code: "M", Label: "a.go"}, {Code: "A", Label: "b.go"}, {Code: "R", From: "old.go", Label: "new.go"}, {Code: "D", Label: "gone.go"},
		}},
		{"git show --name-only", "commit 0123456789abcdef\nAuthor: T <t@example.com>\nDate:   now\n\n    subject\n\na.go\n", []ChangeRow{{Label: "a.go"}}},
		{"svn diff --summarize", "M       trunk/a.c\nA       trunk/b.c\n M      trunk\n", []ChangeRow{
			{Verb: "Edited", Label: "trunk/a.c"}, {Verb: "Created", Label: "trunk/b.c"}, {Verb: "Edited", Label: "trunk", Note: "properties"},
		}},
	} {
		if rows, _ := VCSOutputRows(tc.command, "", tc.output); !reflect.DeepEqual(rows, tc.want) {
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
	want := []ChangeRow{
		{Code: "MM", Label: "internal/router/vcs_display.go"},
		{Code: "A ", Label: "internal/router/vcs_display_test.go"},
		{Code: "R ", From: "old.go", Label: "new.go"},
		{Code: " D", Label: "gone.go"},
		{Code: " M", Label: "sub"},
		{Code: "UU", Label: "conflict.go"},
		{Code: "??", Label: "scratch.txt"},
		{Footer: true, Label: "feat/vcs-events", Note: "↑2"},
	}
	if rows, _ := VCSOutputRows("git status", "", long); !reflect.DeepEqual(rows, want) {
		t.Fatalf("long status = %+v\nwant %+v", rows, want)
	}
	short := "## main...origin/main [ahead 1, behind 3]\nM  a.go\n M b.go\nR  x -> y\n?? z\n"
	want = []ChangeRow{{Code: "M ", Label: "a.go"}, {Code: " M", Label: "b.go"}, {Code: "R ", From: "x", Label: "y"}, {Code: "??", Label: "z"}, {Footer: true, Label: "main", Note: "↑1 ↓3"}}
	if rows, _ := VCSOutputRows("git status -sb", "", short); !reflect.DeepEqual(rows, want) {
		t.Fatalf("short status = %+v", rows)
	}
	clean := "On branch main\nYour branch and 'origin/main' have diverged,\nand have 1 and 2 different commits each, respectively.\n\nnothing to commit, working tree clean\n"
	want = []ChangeRow{{Footer: true, Label: "main", Note: "↑1 ↓2 · clean"}}
	if rows, _ := VCSOutputRows("git status", "", clean); !reflect.DeepEqual(rows, want) {
		t.Fatalf("clean status = %+v", rows)
	}
	svn := "M       trunk/a.c\n?       trunk/new.c\n M      trunk\nC       trunk/c.c\n"
	want = []ChangeRow{{Code: "M ", Label: "trunk/a.c"}, {Code: "? ", Label: "trunk/new.c"}, {Code: " M", Label: "trunk"}, {Code: "C ", Label: "trunk/c.c"}}
	if rows, _ := VCSOutputRows("svn st", "", svn); !reflect.DeepEqual(rows, want) {
		t.Fatalf("svn status = %+v", rows)
	}
}

func TestVCSSVNCommitRows(t *testing.T) {
	output := "Sending        trunk/a.c\nAdding  (bin)  trunk/logo.png\nDeleting       trunk/old.c\nTransmitting file data ..done\nCommitting transaction...\nCommitted revision 4812.\n"
	want := []ChangeRow{
		{Verb: "Edited", Label: "trunk/a.c"}, {Verb: "Created", Label: "trunk/logo.png"}, {Verb: "Deleted", Label: "trunk/old.c"},
		{Footer: true, Label: "r4812"},
	}
	if rows, _ := VCSOutputRows("svn commit -m 'r: fix'", "", output); !reflect.DeepEqual(rows, want) {
		t.Fatalf("svn commit rows = %+v", rows)
	}
}
