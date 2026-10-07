package vcsguard

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/shellsyntax"
)

// Discover installed native shells without an inherited session's guards.
// exec.Command resolves its executable before the fixture Env is assigned.
func fixtureShell(name string) (string, error) {
	for _, directory := range filepath.SplitList(os.Getenv("PATH")) {
		if filepath.Base(directory) == Directory {
			continue
		}
		candidate, err := filepath.Abs(filepath.Join(directory, name))
		if err != nil {
			return "", err
		}
		if path, err := exec.LookPath(candidate); err == nil {
			if resolved, err := filepath.EvalSymlinks(path); err == nil && filepath.Base(resolved) == "mise" {
				continue
			}
			return path, nil
		}
	}
	return "", exec.ErrNotFound
}

func TestFunctionsPreserveRuntimeDirectory(t *testing.T) {
	for _, shell := range []string{"bash", "zsh"} {
		t.Run(shell, func(t *testing.T) {
			executable, err := fixtureShell(shell)
			if err != nil {
				t.Skipf("%s unavailable", shell)
			}
			root := t.TempDir()
			guard := filepath.Join(root, "guard '$unexpected $(touch INJECTED)")
			if err := os.Mkdir(guard, 0o700); err != nil {
				t.Fatal(err)
			}
			tool := filepath.Join(root, "git")
			for path, script := range map[string]string{
				tool:                        "#!/bin/sh\necho BYPASSED\n",
				filepath.Join(guard, "git"): "#!/bin/sh\nprintf '%s\\n' \"$MEKUGI_VCS_GUARD_REAL\" \"$@\"\n",
			} {
				if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			command := exec.Command(executable, "-c", Functions(shell, guard, []string{tool})+shellsyntax.Quote(tool)+" push 'two words'")
			command.Dir = root
			command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + root}
			output, err := command.CombinedOutput()
			if want := tool + "\npush\ntwo words\n"; err != nil || string(output) != want {
				t.Fatalf("guard output = %q, %v; want %q", output, err, want)
			}
			if _, err := os.Stat(filepath.Join(root, "INJECTED")); !os.IsNotExist(err) {
				t.Fatalf("runtime directory was evaluated: %v", err)
			}
		})
	}
}

func TestZshStartupPreservesLogout(t *testing.T) {
	zsh, err := fixtureShell("zsh")
	if err != nil {
		t.Skip("zsh unavailable")
	}
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "home", true: "ZDOTDIR"}[explicit], func(t *testing.T) {
			root, user := t.TempDir(), t.TempDir()
			if err := os.WriteFile(filepath.Join(user, ".zlogout"), []byte("printf 'logout %s' \"${ZDOTDIR-unset}\"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			wrapper := filepath.Join(root, "startup")
			if err := WriteZshStartup(wrapper, "true\n"); err != nil {
				t.Fatal(err)
			}
			command := exec.Command(zsh, "-lic", "printf 'body\\n'")
			command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + user, "ZDOTDIR=" + wrapper}
			want := "body\nlogout unset"
			if explicit {
				command.Env = append(command.Env, UserZdotdirEnvironment+"="+user)
				want = "body\nlogout " + user
			}
			output, err := command.CombinedOutput()
			if err != nil || strings.TrimSpace(string(output)) != want {
				t.Fatalf("login shell = %q, %v; want %q", output, err, want)
			}
		})
	}
}
