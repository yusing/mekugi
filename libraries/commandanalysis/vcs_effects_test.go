package commandanalysis

import "testing"

// Source: internal/vcsguard/vcsguard_test.go:188:219@[8dfc5600].
func TestWorktreeWrites(t *testing.T) {
	for _, tc := range []struct {
		argv []string
		want bool
	}{
		{[]string{"/usr/bin/git", "-c", "core.fsmonitor=false", "status", "--short"}, false},
		{[]string{"git", "-C", "/workspace", "cherry-pick", "HEAD"}, true},
		{[]string{"git", "switch", "branch"}, true},
		{[]string{"git", "rm", "file"}, true},
		{[]string{"git", "diff", "--output=patch"}, true},
		{[]string{"git", "log", "-o", "output"}, true},
		{[]string{"git", "show", "--output", "output"}, true},
		{[]string{"git", "diff", "--stat"}, false},
		{[]string{"git", "diff", "--output-indicator-new=+"}, false},
		{[]string{"git", "diff", "-opatch"}, true},
		{[]string{"hg", "cat", "--output=copy", "file"}, true},
		{[]string{"hg", "-R", "repo", "status"}, false},
		{[]string{"svn", "--username", "user", "status"}, false},
		{[]string{"jj", "--repository", "repo", "status"}, false},
		{[]string{"git", "mv", "old", "new"}, true},
		{[]string{"svn", "move", "old", "new"}, true},
		{[]string{"hg", "revert", "file"}, true},
		{[]string{"jj", "restore", "file"}, true},
		{[]string{"gh", "pr", "checkout", "1"}, true},
		{[]string{"gh", "run", "view", "1"}, false},
		{[]string{"python3", "edit.py"}, false},
	} {
		if got := WorktreeWrites(tc.argv); got != tc.want {
			t.Errorf("WorktreeWrites(%q) = %v, want %v", tc.argv, got, tc.want)
		}
	}
}
