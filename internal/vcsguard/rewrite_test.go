package vcsguard

import (
	"strings"
	"testing"
)

func TestRewriteExecutablePositions(t *testing.T) {
	for _, script := range []string{
		`'/unlisted directory/git' push origin main; echo after`,
		`env -i -u NAME '/unlisted directory/git' push --tags && echo after`,
		`command /usr/bin/git push upstream main`,
		`exec /usr/bin/gh pr create`,
		`timeout -k 1 5 nice -n 2 /usr/bin/hg push`,
		`timeout "$delay" /usr/bin/git push`,
		`printf arg | xargs -n 1 /usr/bin/svn commit`,
		`sh -c 'git add -A; /usr/bin/git push; git log'`,
		`for ref in main tag; do /usr/bin/git push origin "$ref"; done`,
		`printf '%s' "$(/usr/bin/git status)"`,
		`g\it push`,
	} {
		t.Run(script, func(t *testing.T) {
			got, err := Rewrite(script, "/private/helper", "/private/guard")
			if err != nil || (!strings.Contains(got, "--vcs-") && !strings.Contains(got, "PATH=")) {
				t.Fatalf("Rewrite = %q, %v", got, err)
			}
			again, err := Rewrite(got, "/private/helper", "/private/guard")
			if err != nil || again != got {
				t.Fatalf("second rewrite = %q, %v; want %q", again, err, got)
			}
		})
	}
	for _, script := range []string{`printf '/usr/bin/git push'`, `command -v /usr/bin/git`, "cat <<'EOF'\ngit push\nEOF\n"} {
		got, err := Rewrite(script, "/private/helper", "/private/guard")
		if err != nil || got != script {
			t.Fatalf("non-command changed: %q, %v", got, err)
		}
	}
}

func TestRewriteLeavesZshUnsupported(t *testing.T) {
	const script = `zsh -c 'git push origin main'`
	if got, err := Rewrite(script, "/private/helper", "/private/guard"); err != nil || got != script {
		t.Fatalf("unsupported shell rewritten: %q, %v", got, err)
	}
	if _, err := Rewrite(`git push origin ${(q)branch}`, "/private/helper", "/private/guard"); err == nil {
		t.Fatal("Zsh-only syntax was accepted")
	}
}
