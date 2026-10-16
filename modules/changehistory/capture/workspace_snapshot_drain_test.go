package capture

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
	"time"
)

type delayedSnapshotOutput struct{ buffer bytes.Buffer }

func (w *delayedSnapshotOutput) Write(p []byte) (int, error) {
	// Model a pipe copier that is scheduled after the successful Git exit.
	time.Sleep(250 * time.Millisecond)
	return w.buffer.Write(p)
}

func TestWorkspaceSnapshotDrainsSuccessfulGitOutput(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	repository := workspaceSnapshotRepo{git: git, worktree: t.TempDir()}
	command := repository.command(t.Context(), nil, "--version")
	var output delayedSnapshotOutput
	command.Stdout = &output
	if err := command.Run(); err != nil {
		t.Fatalf("successful Git output was discarded: %v", err)
	}
	if !command.ProcessState.Success() || !strings.HasPrefix(output.buffer.String(), "git version ") || t.Context().Err() != nil {
		t.Fatalf("Git completion: state=%v output=%q context=%v", command.ProcessState, output.buffer.String(), t.Context().Err())
	}
}
