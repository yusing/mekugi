package router

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/legacycall"

	"github.com/yusing/mekugi/apps/mekugi/internal/frontends"
)

type mchangesSliceFixture struct {
	registry   *frontends.Registry
	store      *mekugiReplayStore
	ctx        context.Context
	workspace  string
	thread     string
	invocation shellWorkerTestInvocation
}

func newMChangesSliceFixture(t *testing.T, thread string) *mchangesSliceFixture {
	t.Helper()
	registry := sharedProxyTestRegistry(t)
	thread = uniqueTestThread(thread)
	replayDirectory := registry.ReplayDirectory
	store, err := openMekugiReplayStore(replayDirectory)
	if err != nil {
		t.Fatal(err)
	}
	ctx, release, err := store.beginSession(t.Context(), thread, "mchanges-slices")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	workspace := t.TempDir()
	t.Cleanup(store.snapshots.Close)
	return &mchangesSliceFixture{
		registry:   registry,
		store:      store,
		ctx:        ctx,
		workspace:  workspace,
		thread:     thread,
		invocation: mchangesSliceInvocation(workspace, thread),
	}
}

func mchangesSliceInvocation(directory, thread string) shellWorkerTestInvocation {
	// BASH_ENV can replace fixture frontends before the test script starts.
	// Keep the test isolated from the interactive shell configuration.
	environment := make([]string, 0, len(os.Environ())+4)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "BASH_ENV=") || strings.HasPrefix(entry, "CODEX_THREAD_ID=") ||
			strings.HasPrefix(entry, routerTestWorkerEnvironment+"=") {
			continue
		}
		environment = append(environment, entry)
	}
	environment = append(environment,
		"XDG_STATE_HOME="+filepath.Join(directory, ".state"),
		"MEKUGI_RUNTIME_DIR="+filepath.Join(directory, ".runtime"),
		"CODEX_THREAD_ID="+thread,
		routerTestWorkerUnscopedEnvironment+"=0",
	)
	return shellWorkerTestInvocation{directory: directory, environment: environment}
}

func (f *mchangesSliceFixture) run(t *testing.T, command string) (stdout, stderr string, status int) {
	t.Helper()
	return runShellWorkerTest(t, f.registry, "bash", nil, command, nil, f.invocation)
}

func (f *mchangesSliceFixture) reserve(t *testing.T, thread, correlation string) string {
	t.Helper()
	id, err := f.store.changes.Reserve(f.ctx, f.store.scoped(f.ctx).changeScope(), f.workspace, thread, correlation)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *mchangesSliceFixture) publish(t *testing.T, id, correlation, callID string, history legacycall.History) {
	t.Helper()
	history.ChangeID, history.CorrelationID = id, correlation
	if history.ToolName == "" {
		history.ToolName = applyPatchToolName
	}
	if history.Script == "" {
		history.Script = callID
	}
	if err := f.store.put(f.ctx, f.workspace, map[string]legacycall.History{callID: history}); err != nil {
		t.Fatal(err)
	}
}
