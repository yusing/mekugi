package router

import (
	"context"
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/executionobservation"
	"github.com/yusing/mekugi/libraries/legacycall"
)

func startShellObservationTest(t *testing.T, p *observationFixture, workspace, thread, turn, item, script string) (*executionobservation.ShellOperation, context.Context) {
	t.Helper()
	store := testProxyStore(p)
	ctx, _ := retentionTestSession(t, store, thread, 0)
	ctx = bindTestHandleScope(t, store, ctx, "", "")
	selected := store.scoped(ctx)
	operation := executionobservation.PrepareShell(ctx, selected.changeScope(), p.changeSource, p.snapshots, p.recovered, p.publishChanges, p.execWindows, p.activity, p.noticeSink, executionobservation.ShellInput{Thread: thread, Turn: turn, Namespace: selected.session.Namespace, Workspace: workspace, Caller: "/root", Command: script, Shell: "bash"})
	if operation == nil {
		t.Fatal("missing shell observation")
	}
	operation.BindItem(item)
	operation.Begin(p.review)
	t.Cleanup(operation.Discard)
	return operation, ctx
}

// Resolve the recorded host identity, independent of the opaque operation ID.
func lookupShellTestRecord(t *testing.T, store *mekugiReplayStore, workspace, thread, item string) (legacycall.Record, bool) {
	t.Helper()
	names, err := filepath.Glob(filepath.Join(store.directory, "call-*.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		var record legacycall.Record
		if err := json.Unmarshal(data, &record); err != nil {
			t.Fatal(err)
		}
		if record.Workspace != workspace || record.History.ExecutingThread != thread {
			continue
		}
		for _, host := range record.History.HostResults {
			if host.CallID == item {
				retained, found, err := store.publication.ReadHostCall(workspace, record.CallID, false)
				if err != nil || !found {
					t.Fatalf("retained shell record: %v, %v", found, err)
				}
				return retained, true
			}
		}
	}
	return legacycall.Record{}, false
}

func shellTestRecord(t *testing.T, store *mekugiReplayStore, workspace, thread, item string) legacycall.Record {
	t.Helper()
	record, found := lookupShellTestRecord(t, store, workspace, thread, item)
	if !found {
		t.Fatalf("missing shell record for %s/%s", thread, item)
	}
	return record
}
