package main

import (
	"bytes"
	"os"
	"testing"

	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestUISnapshotClaudeHelp(t *testing.T) {
	var output bytes.Buffer
	if code := runClaude(t.Context(), []string{"--help"}, os.Stdin, os.Stdout, &output); code != 0 {
		t.Fatalf("help exited with %d", code)
	}
	uisnapshot.Assert(t, "testdata/snapshots/claude-help.txt", output.String())
}
