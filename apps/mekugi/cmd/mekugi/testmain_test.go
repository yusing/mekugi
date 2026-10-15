package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Provider selection must not depend on credentials in the developer's shell.
// Empty overrides also disable credentials from the local configuration file.
func TestMain(m *testing.M) {
	// macOS temporary paths can start with /var -> /private/var. Use the
	// canonical root for fixtures, not weaker production state-directory checks.
	temporary, err := filepath.Abs(os.TempDir())
	if err == nil {
		temporary, err = filepath.EvalSymlinks(temporary)
	}
	if err == nil {
		err = os.Setenv("TMPDIR", temporary)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve test temporary directory: %v\n", err)
		os.Exit(1)
	}
	// Generated debug bundles and retention must never use the developer's state.
	// Child fixtures keep the explicit state root selected by their parent.
	var stateDirectory string
	if os.Getenv("MEKUGI_LAUNCHER_TEST_STATE_ISOLATED") == "" {
		var err error
		stateDirectory, err = os.MkdirTemp("", "mekugi-launcher-test-state-")
		if err == nil {
			err = os.Setenv("XDG_STATE_HOME", stateDirectory)
		}
		if err == nil {
			err = os.Setenv("MEKUGI_LAUNCHER_TEST_STATE_ISOLATED", "1")
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	for _, name := range []string{"OPENCODE_API_KEY", "OPENCODE_GO_API_KEY", "OPENCODE_ZEN_API_KEY"} {
		if err := os.Setenv(name, ""); err != nil {
			fmt.Fprintf(os.Stderr, "isolate test environment %s: %v\n", name, err)
			os.Exit(1)
		}
	}
	code := m.Run()
	if stateDirectory != "" {
		if err := os.RemoveAll(stateDirectory); err != nil {
			fmt.Fprintln(os.Stderr, err)
			code = 1
		}
	}
	os.Exit(code)
}

func TestLauncherFixturesWithSymlinkTempDirectory(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.Mkdir(real, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", alias)
	// The debug fixture uses TestMain's state; the Codex fixture replaces it
	// with t.TempDir. Both must work beneath an OS temporary-directory alias.
	command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^(TestWrapCodexSkipsThirdPartyCatalog|TestUISnapshotDebugHandoffAfterCodexExit)$", "-test.count=1")
	command.Env = append(os.Environ(), "MEKUGI_LAUNCHER_TEST_STATE_ISOLATED=")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("launch fixtures under symlink TMPDIR: %v\n%s", err, output)
	}
}
