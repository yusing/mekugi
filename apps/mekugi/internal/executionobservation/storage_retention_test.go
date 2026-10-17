package executionobservation

import (
	"encoding/json"

	"path/filepath"

	"strings"

	"testing"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"

	"github.com/gofrs/flock"

	retainedhistory "github.com/yusing/mekugi/modules/sessionhistory/history"
)

func TestStorageStaleTerminalPreservesNewerHandoff(t *testing.T) {
	t.Parallel()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := newManagedMekugiProxy(t)
	setTestProxyStore(p, store)
	workspace := t.TempDir()
	start := func() *mekugiResponseTransform {
		t.Helper()
		request := activityAdmissionRequest(t, nil)
		transform, err := p.prepareRequest(t.Context(), &request, "routing", "thread", codexresponses.TurnMetadata{
			RequestKind: "turn", ThreadID: "thread", Directories: map[string]json.RawMessage{workspace: nil},
		}, true)
		if err != nil {
			t.Fatal(err)
		}
		return transform
	}
	older := start()
	older.storageIdle = true
	newer := start()
	newer.Close() // The host has a tool to dispatch; no terminal was delivered.
	older.Close() // Its terminal must not retire the newer request's lease.
	lease := flock.New(filepath.Join(store.directory, strings.TrimSuffix(retainedhistory.SessionName("thread"), ".json")+".lock"))
	acquired, err := lease.TryLock()
	if acquired {
		_ = lease.Unlock()
	}
	if err != nil || acquired {
		t.Fatalf("newer handoff lost lease: acquired=%v err=%v", acquired, err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
}
