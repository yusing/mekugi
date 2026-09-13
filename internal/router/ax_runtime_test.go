package router

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/capturer"
)

func TestAXObservesExecutedPrivateReaders(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	root := t.TempDir()
	path := filepath.Join(root, "input.txt")
	journal := filepath.Join(root, "reads.jsonl")
	if err := os.WriteFile(path, []byte("private-source-marker\n"), 0600); err != nil {
		t.Fatal(err)
	}
	invocation := newShellWorkerTestInvocation(root, capturer.AXReadOutputEnvironment+"="+journal, "CODEX_THREAD_ID=test_thread")
	script := "printf 'hcat is only an example\n'; " +
		"if false; then hcat /not-executed; fi; " +
		"hcat " + shellQuoteArgument(path) + "; " +
		"inspect_file " + shellQuoteArgument(path) + "; " +
		"hcat " + shellQuoteArgument(filepath.Join(root, "missing")) + "; true"
	stdout, stderr, code := runShellWorkerTest(t, registry, "bash", nil, script, nil, invocation)
	if code != 0 || strings.Count(stdout, "private-source-marker") != 1 ||
		!strings.Contains(stdout, "hcat is only an example") || !strings.Contains(stderr, "ENOENT") {
		t.Fatalf("worker code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	reads, err := capturer.ReadAXReads(t.Context(), journal, "test_thread")
	if err != nil || reads.Started != 3 || reads.Succeeded != 2 || reads.Failed != 1 ||
		reads.ByTool["hcat"] != 2 || reads.ByTool["inspect_file"] != 1 {
		t.Fatalf("runtime evidence = %+v, %v", reads, err)
	}
	data, _ := os.ReadFile(journal)
	if strings.Contains(string(data), root) || strings.Contains(string(data), "private-source-marker") ||
		strings.Contains(string(data), "/not-executed") {
		t.Fatal("runtime journal retained private source or submitted examples")
	}
}
func TestAXWriteFailureDoesNotChangeReaderOutcome(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	root := t.TempDir()
	path := filepath.Join(root, "input.txt")
	if err := os.WriteFile(path, []byte("ok\n"), 0600); err != nil {
		t.Fatal(err)
	}
	invocation := newShellWorkerTestInvocation(root, capturer.AXReadOutputEnvironment+"="+filepath.Join(root, "missing", "reads.jsonl"))
	stdout, stderr, code := runShellWorkerTest(t, registry, "bash", nil, "hcat "+shellQuoteArgument(path), nil, invocation)
	if code != 0 || !strings.Contains(stdout, "ok") || !strings.Contains(stderr, "AX read evidence unavailable") {
		t.Fatalf("worker code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}
