//go:build unix

package router

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

func TestShellCommandRoutingNativeRegressions(t *testing.T) {
	if _, err := exec.LookPath("rtk"); err != nil {
		t.Skip("RTK is not installed")
	}
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	invocation := newShellWorkerTestInvocation(directory)
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = directory
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %q: %v: %s", args, err, output)
		}
		return string(output)
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(directory, "file.txt"), []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr, status := runShellWorkerTest(t, registry, "bash", nil, "git add", nil, invocation)
	if status != 0 || git("diff", "--cached", "--name-only") != "" {
		t.Fatalf("operand-free git add changed the index: status=%d, stderr=%q", status, stderr)
	}

	for _, name := range []string{"one.bin", "two.bin"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte{0xff}, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil, "diff one.bin two.bin", nil, invocation)
	if status != 0 || stdout != "" || stderr != "" {
		t.Fatalf("native binary diff changed: %q, %q, %d", stdout, stderr, status)
	}
	_, stderr, status = runShellWorkerTest(t, registry, "bash", nil, "find definitely_missing", nil, invocation)
	if status == 0 || !strings.Contains(stderr, "definitely_missing") {
		t.Fatalf("missing find root lost its failure: %q, %d", stderr, status)
	}

	// RTK's lint dispatcher needs the tool name even for bare directory operands.
	bin := t.TempDir()
	for name, source := range map[string]string{
		"eslint": "#!/bin/sh\nprintf '%s\\n' \"$@\" > lint-args\nprintf '[]\\n'\n",
		"pnpm":   "#!/bin/sh\nprintf '%s\\n' \"$@\"\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(source), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	invocation = newShellWorkerTestInvocation(directory, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	_, stderr, status = runShellWorkerTest(t, registry, "bash", nil, "eslint src", nil, invocation)
	args, err := os.ReadFile(filepath.Join(directory, "lint-args"))
	if err != nil || status != 0 || !bytes.Contains(args, []byte("src\n")) {
		t.Fatalf("eslint directory dispatch: args=%q, err=%v, stderr=%q, status=%d", args, err, stderr, status)
	}
	stdout, stderr, status = runShellWorkerTest(t, registry, "bash", nil, "pnpm --filter web typecheck", nil, invocation)
	if stdout != "--filter\nweb\ntypecheck\n" || stderr != "" || status != 0 {
		t.Fatalf("package script replaced: %q, %q, %d", stdout, stderr, status)
	}

	// Combined short flags must also preserve stdin through explicit hrun.
	git("add", "file.txt")
	if err := os.WriteFile(filepath.Join(directory, "file.txt"), []byte("piped\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr, status = runShellWorkerTest(t, registry, "bash", nil,
		"printf 'y\\n' | hrun -n 100 -- git add -vp file.txt", nil, invocation)
	if status != 0 || git("show", ":file.txt") != "piped\n" {
		t.Fatalf("combined interactive flags lost stdin: %q, %d", stderr, status)
	}

	// Feed an affirmative response through a real PTY. Interactive staging must
	// reach native Git rather than RTK's capture helper, which discards stdin.
	git("add", "file.txt")
	if err := os.WriteFile(filepath.Join(directory, "file.txt"), []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	terminal, input, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	defer input.Close()
	if _, err := io.WriteString(terminal, "y\n"); err != nil {
		t.Fatal(err)
	}
	manifest, err := readToolWorkerManifest(filepath.Join(registry.SnapshotDir, toolPluginManifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	shell, _ := registry.contribution("shell")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	result, err := executeShellTool(ctx, manifest, registry.RuntimeRoot, &shell,
		[]string{"bash", "git add -p file.txt"}, input, directory, os.Environ(), nil, nil, nil)
	if err != nil || result.ExitCode != 0 || git("show", ":file.txt") != "after\n" {
		t.Fatalf("interactive staging did not receive stdin: %+v, %v", result, err)
	}
}
