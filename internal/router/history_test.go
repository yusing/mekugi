package router

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/changehistory"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/commandsegments/reports"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/usage/accounting"
)

func testHistory() *history.Service {
	service, err := history.NewService(append(append(append(append(RetainedRecordKinds(), accounting.RecordKinds()...), changehistory.Module.Contributes.RecordKinds...), recovery.RecordKinds()...), reports.RecordKinds()...))
	if err != nil {
		panic(err)
	}
	return service
}

func openMekugiReplayStore(directory string) (*mekugiReplayStore, error) {
	store, err := openReplayStore(context.Background(), testHistory(), directory)
	if store != nil {
		store.recovery, store.changes = testRecovery(), testChanges()
	}
	return store, err
}

func testReplayStore(store mekugiReplayStore) *mekugiReplayStore {
	store.publication = testHistory().Attach(store.directory)
	store.recovery, store.changes = testRecovery(), testChanges()
	return &store
}

func testRecovery() *recovery.Service {
	service, err := recovery.New(testHistory(), changehistory.Module.Contributes.References)
	if err != nil {
		panic(err)
	}
	return service
}

func (s *mekugiReplayStore) publishOutput(ctx context.Context, output command.OmittedOutput, exit int) (string, error) {
	session, err := s.recoverySession(ctx)
	if err != nil {
		return "", err
	}
	return session.Publish(ctx, output, exit)
}
func (s *mekugiReplayStore) recoverOutput(ctx context.Context, id string) (command.Output, error) {
	output, err := s.recovery.Execute(ctx, command.Request{Args: []string{id, "--max-tokens", "15500"}, Scope: retainedScope(ctx, s.directory)})
	if err == nil && output.ExitCode != 0 {
		err = fmt.Errorf("recovery failed: %s", output.Stderr)
	}
	return output, err
}
func recoveryCursor(t *testing.T, store *mekugiReplayStore, ctx context.Context, id string) string {
	t.Helper()
	output, err := store.recovery.Execute(ctx, command.Request{Args: []string{id, "--max-tokens", "4"}, Scope: retainedScope(ctx, store.directory)})
	_, next, ok := strings.Cut(output.Stderr, "next_call: mread ")
	if err != nil || output.ExitCode != 1 || !ok {
		t.Fatalf("cursor publication: %+v %v", output, err)
	}
	return strings.Fields(next)[0]
}
func executeRecovery(ctx context.Context, manifest toolWorkerManifest, args []string) command.Output {
	output, err := testRecovery().Execute(ctx, command.Request{Args: args, Scope: retainedScope(ctx, manifest.ReplayDirectory)})
	if err != nil {
		panic(err)
	}
	return output
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
