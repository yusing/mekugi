package router

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/vcsguard"
)

func TestVCSGuardHostItemIdentity(t *testing.T) {
	t.Parallel()
	shell := newVCSGuardShell(t)
	items := make(chan string, 4)
	go func() {
		for range 4 {
			select {
			case request := <-shell.hub.approvals:
				items <- request.item
				request.reply <- vcsguard.Reply{OK: true}
			case <-t.Context().Done():
				return
			}
		}
	}()
	helper, err := execTrackHelper()
	if err != nil {
		t.Fatal(err)
	}
	guard, _ := vcsguard.Paths(filepath.Join(shell.root, "bin"))
	git := quoteShellWord(filepath.Join(shell.real, "git"))
	file := filepath.Join(t.TempDir(), "push.sh")
	if err := os.WriteFile(file, []byte("git push\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "git push; env -i " + git + " push; env -i sh -c " + quoteShellWord(git+" push") + "; sh " + quoteShellWord(file)
	changed, err := vcsguard.RewriteForItem(script, helper, guard, "host-item")
	if err != nil {
		t.Fatal(err)
	}
	if run := runShell(t, shell.env, "bash", "-c", changed); run.code != 0 {
		t.Fatalf("run = %+v", run)
	}
	for range 4 {
		if item := <-items; item != "host-item" {
			t.Fatalf("guard item = %q", item)
		}
	}
}

// Run a guarded-write fixture inside another guarded session. Any escaped
// request reaches this test's channel, never a live user's approval dialog.
func TestVCSGuardFixturesIsolateInheritedSession(t *testing.T) {
	t.Parallel()
	outer := newVCSGuardShell(t)
	asked := outer.answer(t, false)
	guard, _ := vcsguard.Paths(filepath.Join(outer.root, "bin"))
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestVCSGuardApprovedWriteRunsUnchanged$", "-test.count=1")
	cmd.Env = append(os.Environ(), "PATH="+guard+string(os.PathListSeparator)+os.Getenv("PATH"), "BASH_ENV="+filepath.Join(outer.root, "bash-env"))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixtures in guarded session: %v\n%s", err, output)
	}
	if len(asked) != 0 {
		t.Fatalf("fixtures sent %d requests to the inherited session", len(asked))
	}
}

func TestVCSGuardInstrumentedNativeShells(t *testing.T) {
	for _, name := range []string{"bash", "sh", "zsh"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			execTrackShellExecutable(t, name)
			shell := newVCSGuardShell(t)
			asked := shell.answer(t, false)
			path := filepath.Join(t.TempDir(), "unlisted tools")
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			for _, tool := range []string{"git", "gh"} {
				data, err := os.ReadFile(filepath.Join(shell.real, tool))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(path, tool), data, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			git := quoteShellWord(filepath.Join(path, "git"))
			gh := quoteShellWord(filepath.Join(path, "gh"))
			script := git + " add -A; " + git + " commit -m x; env -i " + git + " push upstream main; echo DENIED $?; command " + git + " log; env -u UNUSED " + git + " push --tags && echo NEVER; " + gh + " pr view 1"
			helper, err := execTrackHelper()
			if err != nil {
				t.Fatal(err)
			}
			guard, _ := vcsguard.Paths(filepath.Join(shell.root, "bin"))
			changed, err := vcsguard.Rewrite(script, helper, guard)
			if err != nil {
				t.Fatal(err)
			}
			run := runShell(t, shell.env, name, "-c", changed)
			if run.code != 0 || run.stdout != "DENIED 1\n" || strings.Count(run.stderr, "remote write denied") != 2 {
				t.Fatalf("run = %+v; rewritten = %s", run, changed)
			}
			if got := shell.invoked(t); !slices.Equal(got, []string{"git_add_-A", "git_commit_-m_x", "git_log", "gh_pr_view_1"}) {
				t.Fatalf("real commands = %q", got)
			}
			for range 2 {
				if argv := <-asked; argv[1] != "push" {
					t.Fatalf("approval = %q", argv)
				}
			}
			// The parent expands a dynamic -c payload. The helper instruments it
			// before execing the same nested shell, with its $0/$1 unchanged.
			body := git + " push origin main; echo NESTED $? $0 $1"
			script = "body=" + quoteShellWord(body) + "; env -i sh -c \"$body\" label argument"
			changed, err = vcsguard.Rewrite(script, helper, guard)
			if err != nil {
				t.Fatal(err)
			}
			run = runShell(t, shell.env, name, "-c", changed)
			if run.code != 0 || run.stdout != "NESTED 1 label argument\n" || !strings.Contains(run.stderr, "remote write denied") {
				t.Fatalf("nested run = %+v", run)
			}
			if argv := <-asked; argv[1] != "push" {
				t.Fatalf("nested approval = %q", argv)
			}
			// Native function lookup and a command-local PATH stay native.
			script = "git() { printf 'FUNCTION %s\\n' \"$1\"; }; git status; unset -f git; PATH=" + quoteShellWord(path) + " git push; echo LOCAL_PATH $?"
			changed, err = vcsguard.Rewrite(script, helper, guard)
			if err != nil {
				t.Fatal(err)
			}
			run = runShell(t, shell.env, name, "-c", changed)
			if run.code != 0 || run.stdout != "FUNCTION status\nLOCAL_PATH 1\n" || !strings.Contains(run.stderr, "remote write denied") {
				t.Fatalf("function and local PATH run = %+v; script=%s", run, changed)
			}
			if argv := <-asked; argv[1] != "push" {
				t.Fatalf("local PATH approval = %q", argv)
			}
			for _, wrapper := range []string{
				`value=fixture; env TOKEN="$value" ` + git + " push",
				"env -- TOKEN=fixture " + git + " push",
				"sh -c -e " + quoteShellWord(git+" push; echo NEVER"),
				"bash -c -e " + quoteShellWord(git+" push; echo NEVER"),
				"bash +e -c " + quoteShellWord(git+" push"),
				"bash -c -- " + quoteShellWord(git+" push"),
				"bash -o errexit -c " + quoteShellWord(git+" push; echo NEVER"),
				"bash -c -o errexit -- " + quoteShellWord(git+" push; echo NEVER"),
			} {
				changed, err := vcsguard.Rewrite(wrapper+"; echo STATUS $?", helper, guard)
				if err != nil {
					t.Fatal(err)
				}
				run := runShell(t, shell.env, name, "-c", changed)
				if run.code != 0 || run.stdout != "STATUS 1\n" || !strings.Contains(run.stderr, "remote write denied") {
					t.Fatalf("wrapper %s: %+v; script=%s", wrapper, run, changed)
				}
				if argv := <-asked; argv[1] != "push" {
					t.Fatalf("wrapper approval = %q", argv)
				}
			}
			script = `sh() { printf 'FUNCTION:%s\n' "$*"; }; sh -c 'echo EXTERNAL'`
			changed, err = vcsguard.Rewrite(script, helper, guard)
			if err != nil {
				t.Fatal(err)
			}
			run = runShell(t, shell.env, name, "-c", changed)
			if run.code != 0 || run.stdout != "FUNCTION:-c echo EXTERNAL\n" || run.stderr != "" {
				t.Fatalf("shell function = %+v", run)
			}
			script = "PATH=" + quoteShellWord(path) + ":\"$PATH\"; command -p git --version"
			original := runShell(t, shell.env, name, "-c", script)
			changed, err = vcsguard.Rewrite(script, helper, guard)
			if err != nil {
				t.Fatal(err)
			}
			run = runShell(t, shell.env, name, "-c", changed)
			if run != original || !strings.HasPrefix(run.stdout, "git version ") {
				t.Fatalf("command -p = %+v; original=%+v", run, original)
			}
		})
	}
}

func TestVCSGuardNestedShellMissingPayload(t *testing.T) {
	t.Parallel()
	shell := newVCSGuardShell(t)
	helper, err := execTrackHelper()
	if err != nil {
		t.Fatal(err)
	}
	guard, _ := vcsguard.Paths(filepath.Join(shell.root, "bin"))
	for _, script := range []string{"bash -c", "bash -c --", "bash -o"} {
		original := runShell(t, shell.env, "bash", "-c", script)
		changed, err := vcsguard.Rewrite(script, helper, guard)
		if err != nil {
			t.Fatal(err)
		}
		run := runShell(t, shell.env, "bash", "-c", changed)
		if run.code != original.code || run.stdout != original.stdout || (run.stderr == "") != (original.stderr == "") {
			t.Fatalf("%s: guarded=%+v; original=%+v", script, run, original)
		}
	}
}

func TestVCSGuardInstrumentedExecArgv0(t *testing.T) {
	t.Parallel()
	shell := newVCSGuardShell(t)
	bash := execTrackShellExecutable(t, "bash")
	data, err := os.ReadFile(bash)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(target, data, 0o700); err != nil {
		t.Fatal(err)
	}
	helper, err := execTrackHelper()
	if err != nil {
		t.Fatal(err)
	}
	guard, _ := vcsguard.Paths(filepath.Join(shell.root, "bin"))
	// A native executable is necessary: a shebang interpreter replaces argv0.
	// Git's classifier sees only a global -c option, so no approval is involved.
	script := "exec -a custom-name " + quoteShellWord(target) + " -c 'printf \"%s\\n\" \"$0\"'"
	changed, err := vcsguard.Rewrite(script, helper, guard)
	if err != nil {
		t.Fatal(err)
	}
	run := runShell(t, shell.env, "bash", "-c", changed)
	if run.code != 0 || run.stdout != "custom-name\n" || run.stderr != "" {
		t.Fatalf("argv0 run = %+v", run)
	}
}
