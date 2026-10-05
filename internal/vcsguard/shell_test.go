package vcsguard

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/shellsyntax"
)

func TestFunctionsPreserveRuntimeDirectory(t *testing.T) {
	for _, shell := range []string{"bash", "zsh"} {
		t.Run(shell, func(t *testing.T) {
			executable, err := exec.LookPath(shell)
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
	zsh, err := exec.LookPath("zsh")
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
