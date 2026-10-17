package executionobservation

import (
	json "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	retainedhistory "github.com/yusing/mekugi/modules/sessionhistory/history"
)

func TestRetainInputPreservesAllCombinedRecoveryHandles(t *testing.T) {
	t.Parallel()
	registry, err := buildToolRegistryForTest(t, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := registry.Close(); err != nil {
			t.Error(err)
		}
	})
	replayDirectory := registry.ReplayDirectory
	store, err := openMekugiReplayStore(replayDirectory)
	if err != nil {
		t.Fatal(err)
	}
	parentThread := uniqueTestThread("combined-recovery-parent")
	parent, releaseParent := retentionTestSession(t, store, parentThread, 0)
	first, err := store.putShellOutput(parent, "first retained output\n", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.putShellOutput(parent, "second retained output\n", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	childThread := uniqueTestThread("combined-recovery-child")
	child, _ := retentionTestSession(t, store, childThread, 0)
	child = bindTestHandleScope(t, store, child, parentThread, "")
	// Visible tool text arrives JSON-encoded, including newline and tab separators.
	raw, err := json.Marshal([]map[string]string{{
		"type":    "message",
		"content": fmt.Sprintf("\nread: incomplete; next_call: mread\t%s\t%s --max-tokens 128\n", first, second),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.retainInput(child, "/w", raw, nil, nil); err != nil {
		t.Fatal(err)
	}
	releaseParent()
	retentionTestAge(t, store, parentThread, 30*24*time.Hour)
	if err := store.cleanupSessions(child); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store.directory, retainedhistory.SessionName(parentThread))); !os.IsNotExist(err) {
		t.Fatalf("expired parent catalog remains: %v", err)
	}
	stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil,
		"mread "+first+" "+second, nil,
		newShellWorkerTestInvocation(t.TempDir(), "CODEX_THREAD_ID="+childThread))
	if status != 0 || stderr != "" || !strings.Contains(stdout, "first retained output\n") || !strings.Contains(stdout, "second retained output\n") {
		t.Fatalf("combined recovery after parent expiry: status=%d stdout=%q stderr=%q", status, stdout, stderr)
	}
}
