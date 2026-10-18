package launch

import (
	"os"

	"os/exec"

	"testing"
)

func gitTestWorkspace(t *testing.T) string {
	t.Helper()
	workspace := t.TempDir()
	for _, args := range [][]string{{"init", "--quiet"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "test"}} {
		command := exec.Command("git", args...)
		command.Dir = workspace
		command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	return workspace
}

func gitTestCommit(t *testing.T, workspace string) {
	t.Helper()
	for _, args := range [][]string{{"add", "-A"}, {"commit", "--quiet", "-m", "base"}} {
		command := exec.Command("git", args...)
		command.Dir = workspace
		command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
}
