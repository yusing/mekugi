package vcsguard

import (
	"os"
	"os/exec"
	"path/filepath"
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
	executable, err := fixtureShell("bash")
	if err != nil {
		t.Skip("bash unavailable")
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
	command := exec.Command(executable, "-c", Functions(guard, []string{tool})+shellsyntax.Quote(tool)+" push 'two words'")
	command.Dir = root
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + root}
	output, err := command.CombinedOutput()
	if want := tool + "\npush\ntwo words\n"; err != nil || string(output) != want {
		t.Fatalf("guard output = %q, %v; want %q", output, err, want)
	}
	if _, err := os.Stat(filepath.Join(root, "INJECTED")); !os.IsNotExist(err) {
		t.Fatalf("runtime directory was evaluated: %v", err)
	}
}
