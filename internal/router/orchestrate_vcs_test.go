package router

import (
	"path/filepath"
	"testing"
)

func orchestrateVCSWorkspace(t *testing.T, vcs string) string {
	t.Helper()
	if vcs == "svn" {
		return orchestrateSVNWorkspace(t)
	}
	if vcs == "shadow" {
		workspace := t.TempDir()
		writeTestFile(t, filepath.Join(workspace, "file"), "base")
		return workspace
	}
	if vcs == "git" {
		workspace := gitTestWorkspace(t)
		writeTestFile(t, filepath.Join(workspace, "file"), "base")
		gitTestCommit(t, workspace)
		return workspace
	}
	t.Fatalf("unsupported orchestration test VCS %q", vcs)
	return ""
}
