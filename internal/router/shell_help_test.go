package router

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	codexinstructions "github.com/yusing/mekugi/contrib/codex"
)

func TestShellHelpAuthenticatedProcess(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	for _, interpreter := range []string{"bash", "sh"} {
		command := exec.CommandContext(t.Context(), registry.shellRuntime, "-test.run=^TestShellHelpWorkerProcess$")
		command.Dir = t.TempDir()
		command.Env = append(os.Environ(), "MEKUGI_HELP_TEST_INTERPRETER="+interpreter)
		output, err := command.CombinedOutput()
		want, _ := codexinstructions.Help("recovery")
		if err != nil || string(output) != want {
			t.Fatalf("authenticated %s help: %v, matching help %v", interpreter, err, string(output) == want)
		}
	}
}

func TestShellHelpWorkerProcess(t *testing.T) {
	interpreter := os.Getenv("MEKUGI_HELP_TEST_INTERPRETER")
	if interpreter == "" {
		return
	}
	handled, status := RunToolPluginWorker(t.Context(), os.Args[0], []string{interpreter, "hhelp recovery"}, os.Stdin, os.Stdout, os.Stderr)
	if !handled {
		os.Exit(99)
	}
	os.Exit(status)
}

func TestShellHelp(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	for _, interpreter := range []string{"bash", "sh"} {
		t.Run(interpreter, func(t *testing.T) {
			// No checkout, parent process, or previous help request is needed.
			directory := t.TempDir()
			invocation := newShellWorkerTestInvocation(directory)
			for _, topic := range []string{"", "shell", "read", "journal", "recovery", "changes"} {
				stdout, stderr, status := runShellWorkerTest(t, registry, interpreter, nil, "hhelp "+topic, nil, invocation)
				want, _ := codexinstructions.Help(topic)
				if stdout != want || stderr != "" || status != 0 {
					t.Fatalf("topic %q: status %d, stderr %q, matching help %v", topic, status, stderr, stdout == want)
				}
			}
			for _, input := range []string{"hhelp unknown", "hhelp read shell", "hhelp ../instructions"} {
				stdout, stderr, status := runShellWorkerTest(t, registry, interpreter, nil, input, nil, invocation)
				if stdout != "" || !strings.HasPrefix(stderr, "hhelp:") || status != 2 {
					t.Fatalf("invalid input: stdout %q stderr %q status %d", stdout, stderr, status)
				}
			}
			entries, err := os.ReadDir(directory)
			if err != nil || len(entries) != 0 {
				t.Fatalf("help changed workspace: %v, %v", entries, err)
			}
			stdout, stderr, status := runShellWorkerTest(t, registry, interpreter, nil, "hhelp recovery > help.md", nil, invocation)
			got, err := os.ReadFile(filepath.Join(directory, "help.md"))
			want, _ := codexinstructions.Help("recovery")
			if stdout != "" || stderr != "" || status != 0 || err != nil || string(got) != want {
				t.Fatalf("redirected help: status %d, stderr %q, read %v", status, stderr, err)
			}
		})
	}
}
