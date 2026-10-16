package router

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yusing/mekugi/modules/agentmetrics/evidence"
)

func TestCompiledOutlineAuthenticatedReadEvidence(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	path := filepath.Join(directory, "sample.go")
	if err := os.WriteFile(path, []byte("package p\nfunc Item() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(directory, "reads.jsonl")
	const thread = "compiled-outline-acceptance"
	invocation := newShellWorkerTestInvocation(directory,
		"CODEX_THREAD_ID="+thread, evidence.AXReadOutputEnvironment+"="+log)
	stdout, stderr, status := runShellWorkerTest(t, registry, "sh", nil,
		"inspect_file sample.go", nil, invocation)
	if stdout != "2-2 function Item\n" || stderr != "" || status != 0 {
		t.Fatalf("authenticated outline: stdout=%q stderr=%q status=%d", stdout, stderr, status)
	}
	stdout, stderr, status = runShellWorkerTest(t, registry, "sh", nil,
		"inspect_file missing.go", nil, invocation)
	if stdout != "" || stderr != "inspect_file: missing.go: path does not exist\n" || status != 1 {
		t.Fatalf("authenticated failure: stdout=%q stderr=%q status=%d", stdout, stderr, status)
	}
	reads, err := new(evidence.Service).ReadReads(t.Context(), log, thread)
	if err != nil || reads.Started != 2 || reads.Completed != 2 || reads.Succeeded != 1 || reads.Failed != 1 || reads.Incomplete != 0 || reads.ByTool["inspect_file"] != 2 || reads.FailuresByClass["not_found"] != 1 {
		t.Fatalf("compiled reader AX=%+v err=%v", reads, err)
	}
	if len(reads.Failures) != 1 || reads.Failures[0].ExitCode == nil || *reads.Failures[0].ExitCode != 1 {
		t.Fatalf("compiled reader failure status=%+v", reads.Failures)
	}
}
