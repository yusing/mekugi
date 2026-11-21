package launch

import (
	"context"
	"slices"
	"testing"

	"github.com/yusing/mekugi/modules/diagnostics/diagnostic"

	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/activity"
	"github.com/yusing/mekugi/modules/agentguidance"
	"github.com/yusing/mekugi/modules/changehistory"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/commandsegments/reports"
	"github.com/yusing/mekugi/modules/sessionhistory"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/usage/accounting"
)

func retainedScope(ctx context.Context, directory string) scope.Retained {
	identity, _ := ctx.Value(storageSessionKey{}).(storageSessionIdentity)
	return scope.Retained{Directory: directory, Namespace: identity.Namespace, Thread: identity.Thread}
}

func testHistory() *history.Service {
	service, err := history.NewService(slices.Concat(
		sessionhistory.Module.Contributes.RecordKinds,
		activity.Module.Contributes.RecordKinds,
		agentguidance.Module.Contributes.RecordKinds,
		diagnostic.RecordKinds(),
		accounting.RecordKinds(),
		changehistory.Module.Contributes.RecordKinds,
		reports.RecordKinds(),
	))
	if err != nil {
		panic(err)
	}
	return service
}

func openMekugiReplayStore(directory string) (*mekugiReplayStore, error) {
	captured, err := newTestCapture(context.Background(), directory)
	if err != nil {
		return nil, err
	}
	store, err := openReplayStore(context.Background(), testHistory(), captured, directory)
	if err != nil {
		_ = captured.Stop(context.Background())
	}
	return store, err
}

func testReplayStore(store mekugiReplayStore) *mekugiReplayStore {
	store.publication = testHistory().Attach(store.directory)
	store.changes = testChanges()
	if store.captured == nil {
		var err error
		store.captured, err = changecapture.New(testHistory(), "")
		if err != nil {
			panic(err)
		}
	}
	return &store
}

func testReports() *reports.Service {
	service, err := reports.New(testHistory())
	if err != nil {
		panic(err)
	}
	return service
}

func testChanges() changes.Service {
	service, err := changehistory.Module.New(changehistory.Deps{History: testHistory()}, module.Slots{})
	if err != nil {
		panic(err)
	}
	return service
}

func newTestCapture(ctx context.Context, directory string) (*changecapture.Service, error) {
	service, err := changehistory.Module.New(changehistory.Deps{History: testHistory(), Directory: directory}, module.Slots{})
	if err == nil {
		err = changehistory.Module.Start(ctx, service)
	}
	return service, err
}

func testCapture(t *testing.T, directory string) *changecapture.Service {
	t.Helper()
	service, err := newTestCapture(t.Context(), directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := service.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return service
}
