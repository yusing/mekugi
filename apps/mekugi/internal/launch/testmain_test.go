package launch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/dlclark/regexp2/v2"
	"github.com/yusing/mekugi/modules/agentmetrics/evidence"
)

// Provider selection must not depend on credentials in the developer's shell.
// Empty overrides also disable credentials from the local configuration file.
func TestMain(m *testing.M) {
	if os.Getenv("MEKUGI_NATIVE_SERVER_FIXTURE") != "" && len(os.Args) > 1 && os.Args[1] == "server" {
		runNativeServerFixture()
	}
	if os.Getenv("MEKUGI_INVOCATION_FIXTURE") == "1" {
		runInvocationFixtureChild()
	}
	if os.Getenv(routerTestWorkerEnvironment) == "1" {
		if filepath.Base(os.Args[0]) == "mread" && os.Getenv(routerTestWorkerUnscopedEnvironment) == "1" {
			_ = os.Unsetenv("CODEX_THREAD_ID")
		}
		if handled, code := runToolPluginWorkerInHostProcess(testHistory(), testRecovery(), testCommands(),
			context.Background(), os.Args[0], os.Args[1:], os.Stdin, os.Stdout, os.Stderr,
		); handled {
			os.Exit(code)
		}
	}
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
	// Keep test publications and configuration outside the user's directories,
	// including parallel cases. Tests that need settings supply their own config.
	var stateDirectory string
	// Child test executables must retain their parent's authenticated store path.
	if os.Getenv("MEKUGI_LAUNCHER_TEST_STATE_ISOLATED") == "" {
		stateDirectory, err = os.MkdirTemp("", "mekugi-test-state-")
		if err == nil {
			err = os.Setenv("XDG_STATE_HOME", stateDirectory)
		}
		if err == nil {
			err = os.Setenv("XDG_CONFIG_HOME", filepath.Join(stateDirectory, "config"))
		}
		if err == nil {
			err = os.Setenv("MEKUGI_LAUNCHER_TEST_STATE_ISOLATED", "1")
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	// Chroma lexers set regexp2 match timeouts, which lazily start one shared
	// clock goroutine. Start it here so a synctest bubble never owns it.
	clock := regexp2.MustCompile("x", regexp2.None)
	clock.MatchTimeout = 1000 * time.Hour
	if _, err := clock.MatchString("x"); err != nil {
		fmt.Fprintln(os.Stderr, "start regexp2 timeout clock:", err)
		os.Exit(1)
	}
	if err := os.Unsetenv(evidence.AXReadOutputEnvironment); err != nil {
		fmt.Fprintln(os.Stderr, "isolate AX test instrumentation:", err)
		os.Exit(1)
	}
	for _, name := range []string{
		"OPENCODE_API_KEY",
		"OPENCODE_GO_API_KEY",
		"OPENCODE_ZEN_API_KEY",
	} {
		if err := os.Setenv(name, ""); err != nil {
			fmt.Fprintf(os.Stderr, "isolate test environment variable %s: %v\n", name, err)
			os.Exit(1)
		}
	}
	code := m.Run()
	if stateDirectory != "" {
		err = os.RemoveAll(stateDirectory)
	}
	for _, fixture := range []*proxyRegistryFixture{&proxyTestFixture} {
		if fixture.registry != nil {
			err = errors.Join(err, fixture.registry.Close())
		}
		if fixture.directory != "" {
			err = errors.Join(err, os.RemoveAll(fixture.directory))
		}
	}
	if execTrackHelperDirectory != "" {
		err = errors.Join(err, os.RemoveAll(execTrackHelperDirectory))
	}
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "clean up launch test fixtures: %v\n", err)
		code = 1
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
