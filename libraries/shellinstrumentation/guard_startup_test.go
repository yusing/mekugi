package shellinstrumentation

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/commandanalysis"

	"github.com/yusing/mekugi/libraries/commandanalysis/shellsyntax"
)

// Discover installed native shells without an inherited session's guards.
// exec.Command resolves its executable before the fixture Env is assigned.
func fixtureShell(name string) (string, error) {
	for _, directory := range filepath.SplitList(os.Getenv("PATH")) {
		if filepath.Base(directory) == VCSDirectory {
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
	command := exec.Command(executable, "-c", Functions(guard, []string{tool}, commandanalysis.VCSTools)+shellsyntax.Quote(tool)+" push 'two words'")
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

func TestKnownPaths(t *testing.T) {
	root := t.TempDir()
	bin, cellar, guard, plain := filepath.Join(root, "bin"), filepath.Join(root, "cellar", "bin"), filepath.Join(root, VCSDirectory), filepath.Join(root, "plain")
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
	got := KnownPaths(strings.Join([]string{bin, guard, plain, "relative"}, string(os.PathListSeparator)), commandanalysis.VCSTools)
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
