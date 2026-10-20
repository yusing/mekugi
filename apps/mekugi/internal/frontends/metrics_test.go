package frontends

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/modules/agentmetrics/evidence"
)

func TestAgentMetricsAbsentAuthenticatedReader(t *testing.T) {
	registry := sharedTestRegistry(t)
	axPath := filepath.Join(t.TempDir(), "reads.jsonl")
	t.Setenv(evidence.AXReadOutputEnvironment, axPath)
	invocation, err := Recognize(registry.Frontends["mcat"])
	if err != nil || invocation == nil {
		t.Fatalf("recognize reader: %v", err)
	}
	source := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(source, []byte("reader still works\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := invocation.Run(testHistory(), testRecovery(), nil, testCommands(), command.HostProcessGroup,
		t.Context(), []string{source}, nil, &stdout, &stderr)
	if code != 0 || stdout.String() != "reader still works\n" || stderr.Len() != 0 {
		t.Fatalf("reader code=%d stdout=%q stderr=%q", code, &stdout, &stderr)
	}
	if _, err := os.Stat(axPath); !os.IsNotExist(err) {
		t.Fatalf("reader created ambient AX log: %v", err)
	}
}
