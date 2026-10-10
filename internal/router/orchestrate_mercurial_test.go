package router

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func orchestrateVCSWorkspace(t *testing.T, vcs string) string {
	t.Helper()
	if vcs == "git" {
		workspace := gitTestWorkspace(t)
		writeTestFile(t, filepath.Join(workspace, "file"), "base")
		gitTestCommit(t, workspace)
		return workspace
	}
	if _, err := exec.Command("hg", "version").Output(); err != nil {
		t.Skip("Mercurial is unavailable:", err)
	}
	t.Setenv("HGRCPATH", os.DevNull)
	workspace := t.TempDir()
	hgTestRun(t, workspace, "init")
	writeTestFile(t, filepath.Join(workspace, "file"), "base")
	hgTestRun(t, workspace, "add")
	hgTestRun(t, workspace, "commit", "-u", "test", "-m", "base")
	return workspace
}

func hgTestRun(t *testing.T, cwd string, args ...string) {
	t.Helper()
	command := exec.CommandContext(t.Context(), "hg", append([]string{"--cwd", cwd, "--config", "extensions.share="}, args...)...)
	command.Env = append(os.Environ(), "HGPLAIN=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("hg %v: %v: %s", args, err, output)
	}
}
