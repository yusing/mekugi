package frontends

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/agentmetrics/evidence"
	"github.com/yusing/mekugi/modules/boundedcommand/run"
	"github.com/yusing/mekugi/modules/changehistory"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/fileread/read"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/sourceoutline/outline"
	"github.com/yusing/mekugi/modules/symbolquery/query"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

const testWorkerEnvironment = "MEKUGI_FRONTEND_TEST_WORKER"

var testThreadSequence atomic.Uint64

func uniqueTestThread(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, testThreadSequence.Add(1))
}

func testHistory() *history.Service {
	service, err := history.NewService(slices.Concat(changehistory.Module.Contributes.RecordKinds, recovery.RecordKinds()))
	if err != nil {
		panic(err)
	}
	return service
}
func testRecovery() *recovery.Service {
	service, err := recovery.New(testHistory(), changehistory.Module.Contributes.References)
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
func testCommands() []command.Contribution {
	return []command.Contribution{moduletest.BindCommand(changes.Command, testChanges()), moduletest.BindCommand(recovery.Command, testRecovery()), moduletest.BindCommand(run.Command, run.Service{}), moduletest.BindCommand(outline.Command, outline.Service{}), moduletest.BindCommand(read.Command, read.Service{}), moduletest.BindCommand(query.Command, query.Service{})}
}
func buildRegistryForTest(t *testing.T, ctx context.Context) (*Registry, error) {
	t.Helper()
	directory := t.TempDir()
	replay := filepath.Join(directory, "replay")
	if _, err := testHistory().Open(ctx, replay); err != nil {
		return nil, err
	}
	return New(ctx, Config{Commands: testCommands(), Guidance: testGuidance(), RuntimeDirectory: filepath.Join(directory, "runtime"), ReplayDirectory: replay})
}

var sharedRegistry *Registry
var sharedDirectory string
var sharedRegistryOnce sync.Once
var sharedRegistryError error

func sharedTestRegistry(t *testing.T) *Registry {
	t.Helper()
	sharedRegistryOnce.Do(func() {
		sharedDirectory, sharedRegistryError = os.MkdirTemp("", "mekugi-frontend-tests-")
		if sharedRegistryError != nil {
			return
		}
		replay := filepath.Join(sharedDirectory, "replay")
		if _, sharedRegistryError = testHistory().Open(t.Context(), replay); sharedRegistryError != nil {
			return
		}
		sharedRegistry, sharedRegistryError = New(t.Context(), Config{Commands: testCommands(), Guidance: testGuidance(), RuntimeDirectory: filepath.Join(sharedDirectory, "runtime"), ReplayDirectory: replay})
	})
	if sharedRegistryError != nil {
		t.Fatal(sharedRegistryError)
	}
	return sharedRegistry
}
func runToolPluginWorkerInHostProcess(service *history.Service, recovered *recovery.Service, commands []command.Contribution, ctx context.Context, argv0 string, args []string, stdin *os.File, stdout, stderr io.Writer) (bool, int) {
	return runTestWorker(service, recovered, commands, command.HostProcessGroup, ctx, argv0, args, stdin, stdout, stderr)
}
func runOwnedToolPluginWorker(service *history.Service, recovered *recovery.Service, commands []command.Contribution, ctx context.Context, argv0 string, args []string, stdin *os.File, stdout, stderr io.Writer) (bool, int) {
	return runTestWorker(service, recovered, commands, command.DedicatedHostProcess, ctx, argv0, args, stdin, stdout, stderr)
}
func runTestWorker(service *history.Service, recovered *recovery.Service, commands []command.Contribution, processScope command.ProcessScope, ctx context.Context, argv0 string, args []string, stdin *os.File, stdout, stderr io.Writer) (bool, int) {
	invocation, err := Recognize(argv0)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return true, 1
	}
	if invocation == nil {
		return false, 0
	}
	return true, invocation.Run(service, recovered, new(evidence.Service), commands, processScope, ctx, args, stdin, stdout, stderr)
}
func TestMain(m *testing.M) {
	if os.Getenv(testWorkerEnvironment) == "1" {
		if handled, status := runToolPluginWorkerInHostProcess(testHistory(), testRecovery(), testCommands(), context.Background(), os.Args[0], os.Args[1:], os.Stdin, os.Stdout, os.Stderr); handled {
			os.Exit(status)
		}
	}
	if err := os.Unsetenv(evidence.AXReadOutputEnvironment); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	var err error
	if sharedRegistry != nil {
		err = sharedRegistry.Close()
	}
	if sharedDirectory != "" {
		err = errors.Join(err, os.RemoveAll(sharedDirectory))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		code = 1
	}
	os.Exit(code)
}
