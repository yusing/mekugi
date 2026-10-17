package router

import (
	"os"

	"os/exec"

	"path/filepath"

	"strings"

	"testing"
)

func runExecVCSTestGit(t *testing.T, repo string, args ...string) []byte {
	t.Helper()
	return runExecVCSTestGitAt(t, repo, repo, args...)
}

func runExecVCSTestGitAt(t *testing.T, repo, directory string, args ...string) []byte {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	command.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+filepath.Join(repo, ".test-global-config"),
		"GIT_TERMINAL_PROMPT=0",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return output
}
