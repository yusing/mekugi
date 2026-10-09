package vcsguard

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestWrites(t *testing.T) {
	aliases := map[string]string{
		"p":     "push --force-with-lease",
		"pp":    "p",
		"st":    "status -sb",
		"ship":  "!git commit -q && git push",
		"shell": "!echo hi",
		"bad":   `push "unterminated`,
		"env":   "!env -u UNUSED /usr/bin/git push",
		"nice":  "!nice -n 2 /usr/bin/git push",
		"xargs": "!xargs -n 1 /usr/bin/git push",
		"timer": "!timeout -k 1 5 /usr/bin/git push",
		"delay": `!timeout "$delay" /usr/bin/git push`,
	}
	lookup := func(_ []string, name string) (string, bool) {
		expansion, ok := aliases[name]
		return expansion, ok
	}
	for _, tc := range []struct {
		command string
		want    bool
	}{
		{"git -C repo -c push.default=current push", true},
		{"git --git-dir .git --no-pager push", true},
		{"git push --dry-run", false},
		{"git push -n origin main", false},
		{"git push --help", false},
		{"git commit -m push", false},
		{"git stash push", false},
		{"git", false},
		{"git --version", false},
		{"git send-email HEAD~1", true},
		{"git svn dcommit", true},
		{"git svn rebase", false},
		{"git p4 submit", true},
		{"git lfs push origin main", true},
		{"git lfs ls-files", false},
		{"git p", true},
		{"git pp", true},
		{"git st", false},
		{"git ship", true},
		{"git shell", false},
		{"git bad", true},
		{"git env", true},
		{"git nice", true},
		{"git xargs", true},
		{"git timer", true},
		{"git delay", true},
		{"git unknown-command", true},
		{"git subtree push --prefix=lib origin main", true},
		{"git subtree split --prefix=lib", false},
		{"git send-pack origin main", true},
		{"git --attr-source HEAD push", true},
		{"git push --dry-run --no-dry-run", true},
		{"git push --no-dry-run -n", false},
		{"git fast-export --all", false},
		{"git submodule foreach 'git push'", true},
		{"git submodule foreach --recursive git status", false},
		{"git rebase -x 'make test && git push' main", true},
		{"git rebase --exec=true main", false},
		{"git bisect run git push", true},
		{"git bisect run xargs -n 1 /usr/bin/git push", true},
		{"gh pr create --fill", true},
		{"gh pr merge 12", true},
		{"gh pr list --state open", false},
		{"gh pr checkout 12", false},
		{"gh pr comment 12 --body list", true},
		{"gh issue create --title x", true},
		{"gh issue list", false},
		{"gh repo create list --public", true},
		{"gh repo view", false},
		{"gh repo clone o/r", false},
		{"gh gist clone 1", false},
		{"gh label clone o/r", true},
		{"gh repo deploy-key list", false},
		{"gh repo deploy-key add key.pub", true},
		{"gh release create v1", true},
		{"gh release download v1", false},
		{"gh run rerun 1", true},
		{"gh run view 1 --log", false},
		{"gh workflow run ci.yml", true},
		{"gh auth status", false},
		{"gh config set editor vim", false},
		{"gh search repos mekugi", false},
		{"gh --version", false},
		{"gh pr create --help", false},
		{"gh co 12", true},
		{"gh extension install owner/gh-x", true},
		{"gh extension list", false},
		{"gh api repos/o/r", false},
		{"gh api repos/o/r/pulls -f title=x", true},
		{"gh api -X GET search/issues -f q=x", false},
		{"gh api --method=DELETE repos/o/r", true},
		{"gh api -XPATCH repos/o/r", true},
		{"gh api repos/o/r/issues --input body.json", true},
		{"gh api graphql -f query='query { viewer { login } }'", false},
		{"gh api graphql -f query='mutation { addStar }'", true},
		{"gh api graphql -F query=@q.graphql", true},
		{"hg push", true},
		{"hg pus", true},
		{"hg -R repo push", true},
		{"hg pull", false},
		{"hg email -r .", true},
		{"hg outgoing", false},
		{"svn commit -m x", true},
		{"svn ci", true},
		{"svn update", false},
		{"svn copy ^/trunk ^/tags/v1 -m x", true},
		{"svn copy a.txt b.txt", false},
		{"svn rm https://example.com/repo/file -m x", true},
		{"svn propset --revprop -r 1 svn:log x", true},
		{"svn --username me commit", true},
		{"jj git push", true},
		{"jj git push --dry-run", false},
		{"jj git fetch", false},
		{"jj -R repo git push --bookmark main", true},
		{"jj gerrit upload", true},
		{"jj log", false},
		{"jj describe -m push", false},
		{"jj my-alias", true},
	} {
		words, ok := splitAlias(tc.command)
		if !ok {
			t.Fatalf("split %q", tc.command)
		}
		if got := Writes(words, lookup); got != tc.want {
			t.Errorf("Writes(%q) = %v, want %v", tc.command, got, tc.want)
		}
	}
}

func TestScriptWrites(t *testing.T) {
	for _, tc := range []struct {
		script string
		want   bool
	}{
		{"git status && git log", false},
		{"git add -A; git push origin main", true},
		{"env GIT_TRACE=1 git push", true},
		{`env GIT_TRACE="$trace" /usr/bin/git push`, true},
		{"env -u UNUSED /usr/bin/git status", false},
		{"nice -n 2 /usr/bin/git status", false},
		{"xargs -n 1 /usr/bin/git status", false},
		{"timeout -k 1 5 /usr/bin/git status", false},
		{"command git push", true},
		{"command -v /usr/bin/git push", false},
		{"timeout 10 git push", true},
		{`timeout "$delay" /usr/bin/git status`, false},
		{`git "push"`, true},
		{`git "$cmd" origin`, true},
		{`git -C "$dir" status`, false},
		{"if true; then gh pr create; fi", true},
		{"echo git push", false},
		{"git push (", true},
	} {
		if got := scriptWrites(tc.script, nil, 0); got != tc.want {
			t.Errorf("ScriptWrites(%q) = %v, want %v", tc.script, got, tc.want)
		}
	}
}

func TestAliasLookupKeepsGlobals(t *testing.T) {
	var seen []string
	lookup := func(globals []string, name string) (string, bool) {
		seen = append(append(seen, globals...), name)
		return "", false
	}
	Writes([]string{"git", "-C", "repo", "-c", "alias.x=push", "x"}, lookup)
	if got := strings.Join(seen, " "); got != "-C repo -c alias.x=push x" {
		t.Fatalf("lookup saw %q", got)
	}
}

func TestPaths(t *testing.T) {
	directory, channel := Paths("/run/mekugi-tools-1/bin")
	if directory != "/run/mekugi-tools-1/vcs-guard" || channel != "/run/mekugi-tools-1/vcs-approval.sock" || ChannelOf(directory) != channel {
		t.Fatalf("paths = %q %q", directory, channel)
	}
}

func TestKnownPaths(t *testing.T) {
	root := t.TempDir()
	bin, cellar, guard, plain := filepath.Join(root, "bin"), filepath.Join(root, "cellar", "bin"), filepath.Join(root, Directory), filepath.Join(root, "plain")
	for _, directory := range []string{bin, cellar, guard, plain} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for path, mode := range map[string]os.FileMode{filepath.Join(cellar, "git"): 0o700, filepath.Join(guard, "gh"): 0o700, filepath.Join(plain, "hg"): 0o600} {
		if err := os.WriteFile(path, nil, mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(cellar, "git"), filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	got := KnownPaths(strings.Join([]string{bin, guard, plain, "relative"}, string(os.PathListSeparator)))
	var mine []string
	for _, path := range got {
		if strings.HasPrefix(path, root) {
			mine = append(mine, path)
		}
	}
	if want := []string{filepath.Join(bin, "git"), filepath.Join(cellar, "git")}; !slices.Equal(mine, want) {
		t.Fatalf("KnownPaths = %q, want %q", mine, want)
	}
}

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
