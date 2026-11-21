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
	"github.com/yusing/mekugi/contracts/recordkind"
	"github.com/yusing/mekugi/modules/agentguidance"
	"github.com/yusing/mekugi/modules/agentmetrics/evidence"
	"github.com/yusing/mekugi/modules/changehistory"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

const testWorkerEnvironment = "MEKUGI_FRONTEND_TEST_WORKER"

var testThreadSequence atomic.Uint64

func uniqueTestThread(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, testThreadSequence.Add(1))
}

func testHistory() *history.Service {
	service, err := history.NewService(slices.Concat(
		changehistory.Module.Contributes.RecordKinds,
		[]recordkind.Spec{history.FrozenFileRecord, history.FrozenResourceRecord},
	))
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
	hist := testHistory()
	chg, err := changehistory.Module.New(changehistory.Deps{History: hist}, module.Slots{})
	if err != nil {
		panic(err)
	}
	guidance, err := agentguidance.Module.New(agentguidance.Deps{Changes: chg, History: hist}, module.Slots{})
	if err != nil {
		panic(err)
	}
	return []command.Contribution{
		moduletest.BindCommand(changehistory.Module.Contributes.Commands[0], chg),
		moduletest.BindCommand(agentguidance.Module.Contributes.Commands[0], guidance),
	}
}

func buildRegistryForTest(t *testing.T, ctx context.Context) (*Registry, error) {
	t.Helper()
	directory := t.TempDir()
	replay := filepath.Join(directory, "replay")
	if _, err := testHistory().Open(ctx, replay); err != nil {
		return nil, err
	}
	return New(ctx, Config{Commands: testCommands(), ReplayDirectory: replay})
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
		sharedRegistry, sharedRegistryError = New(t.Context(), Config{Commands: testCommands(), ReplayDirectory: replay})
	})
	if sharedRegistryError != nil {
		t.Fatal(sharedRegistryError)
	}
	return sharedRegistry
}

func runToolPluginWorkerInHostProcess(service *history.Service, commands []command.Contribution, ctx context.Context, argv0 string, args []string, stdin *os.File, stdout, stderr io.Writer) (bool, int) {
	return runTestWorker(service, commands, command.HostProcessGroup, ctx, argv0, args, stdin, stdout, stderr)
}

func runOwnedToolPluginWorker(service *history.Service, commands []command.Contribution, ctx context.Context, argv0 string, args []string, stdin *os.File, stdout, stderr io.Writer) (bool, int) {
	return runTestWorker(service, commands, command.DedicatedHostProcess, ctx, argv0, args, stdin, stdout, stderr)
}

func runTestWorker(service *history.Service, commands []command.Contribution, processScope command.ProcessScope, ctx context.Context, argv0 string, args []string, stdin *os.File, stdout, stderr io.Writer) (bool, int) {
	invocation, err := Recognize(argv0)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return true, 1
	}
	if invocation == nil {
		return false, 0
	}
	return true, invocation.Run(service, new(evidence.Service), commands, processScope, ctx, args, stdin, stdout, stderr)
}

func TestMain(m *testing.M) {
	if os.Getenv(testWorkerEnvironment) == "1" {
		if handled, status := runToolPluginWorkerInHostProcess(testHistory(), testCommands(), context.Background(), os.Args[0], os.Args[1:], os.Stdin, os.Stdout, os.Stderr); handled {
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
