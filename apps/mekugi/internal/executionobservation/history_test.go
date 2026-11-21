package executionobservation

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

func testCaptureWorkspaces(t *testing.T) *changecapture.Workspaces {
	t.Helper()
	directory := t.TempDir()
	snapshots, err := testCapture(t, directory).Checkpoints(directory)
	if err != nil {
		t.Fatal(err)
	}
	return snapshots
}

func TestReplayStoreBorrowsCaptureLifetime(t *testing.T) {
	directory := t.TempDir()
	captured := testCapture(t, directory)
	snapshots, err := captured.Checkpoints(directory)
	if err != nil {
		t.Fatal(err)
	}
	store, err := openReplayStore(t.Context(), testHistory(), captured, directory)
	if err != nil {
		t.Fatal(err)
	}
	if store.snapshots != snapshots || store.changes != captured.Service {
		t.Fatal("replay store replaced the composed capture owner")
	}
	reopened, err := openReplayStore(t.Context(), testHistory(), captured, directory)
	if err != nil || reopened.snapshots != snapshots {
		t.Fatalf("reopened capture owner: %v", err)
	}
	if _, err := openReplayStore(t.Context(), testHistory(), captured, t.TempDir()); err == nil {
		t.Fatal("capture crossed retained directories")
	}
	retained, err := openReplayStore(t.Context(), testHistory(), nil, directory)
	if err != nil || retained.snapshots != nil || retained.changes != nil {
		t.Fatalf("unselected capture acquired resources: %+v %v", retained, err)
	}
}
