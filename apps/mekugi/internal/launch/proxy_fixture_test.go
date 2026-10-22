package launch

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/executionobservation"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	"github.com/yusing/mekugi/backends/codex/binding"
	previewbinding "github.com/yusing/mekugi/modules/livepreview/codexbinding"
	previewview "github.com/yusing/mekugi/modules/livepreview/terminalview"
	"github.com/yusing/mekugi/modules/usage/accounting"

	"github.com/yusing/mekugi/apps/mekugi/internal/frontends"
	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/modules/agentguidance"
	"github.com/yusing/mekugi/modules/agentmetrics/evidence"
	"github.com/yusing/mekugi/modules/boundedcommand/run"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/fileread/read"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/sourceoutline/outline"
	"github.com/yusing/mekugi/modules/symbolquery/query"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

type proxyRegistryFixture struct {
	once      sync.Once
	registry  *frontends.Registry
	directory string
	err       error
}

var proxyTestFixture proxyRegistryFixture

const routerTestWorkerEnvironment = "MEKUGI_ROUTER_TEST_WORKER"
const routerTestWorkerUnscopedEnvironment = "MEKUGI_ROUTER_TEST_WORKER_UNSCOPED"

var testThreadSequence atomic.Uint64

// uniqueTestThread names a thread no other run in this process uses. The shared
// registry keeps one replay store per process, so a fixed thread ID would see an
// earlier -count run's handles and fork points.
func uniqueTestThread(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, testThreadSequence.Add(1))
}

// Ordinary proxy tests borrow the real, immutable built-in frontend catalog.
func sharedProxyTestRegistry(t *testing.T) *frontends.Registry {
	t.Helper()
	return proxyTestFixture.get(t)
}
func buildToolRegistryForTest(
	t *testing.T,
	ctx context.Context,
) (*frontends.Registry, error) {
	t.Helper()
	directory := t.TempDir()
	if _, err := openMekugiReplayStore(filepath.Join(directory, "replay")); err != nil {
		return nil, err
	}
	var axReadOutput string
	if debug, _ := requestexecution.DebugRequest(ctx); debug != nil {
		axReadOutput = debug.ReadLogPath()
	}
	return frontends.New(ctx, frontends.Config{Commands: testCommands(), Guidance: testGuidance(), RuntimeDirectory: filepath.Join(directory, "runtime"), ReplayDirectory: filepath.Join(directory, "replay"), AXReadOutput: axReadOutput})
}

func (fixture *proxyRegistryFixture) get(t *testing.T) *frontends.Registry {
	t.Helper()
	fixture.once.Do(func() {
		fixture.directory, fixture.err = os.MkdirTemp("", "mekugi-proxy-tests-")
		if fixture.err != nil {
			return
		}
		if _, fixture.err = openMekugiReplayStore(filepath.Join(fixture.directory, "replay")); fixture.err != nil {
			return
		}
		fixture.registry, fixture.err = frontends.New(t.Context(), frontends.Config{Commands: testCommands(), Guidance: testGuidance(), RuntimeDirectory: filepath.Join(fixture.directory, "runtime"), ReplayDirectory: filepath.Join(fixture.directory, "replay")})
	})
	if fixture.err != nil {
		t.Fatal(fixture.err)
	}
	return fixture.registry
}

func newProxyWithSharedTestRegistry(t *testing.T, registry *frontends.Registry) *observationFixture {
	t.Helper()
	proxy := newMekugiProxy(registry, nil, newTestUsage(t), testHistory())
	t.Cleanup(func() {
		if err := proxy.Close(); err != nil {
			t.Error(err)
		}
	})
	return proxy
}

func testCommands() []command.Contribution {
	commands := []command.Contribution{moduletest.BindCommand(changes.Command, testChanges())}
	for _, definition := range agentguidance.Module.Contributes.Commands {
		commands = append(commands, moduletest.BindCommand(definition, nil))
	}
	return append(commands, moduletest.BindCommand(recovery.Command, testRecovery()), moduletest.BindCommand(run.Command, run.Service{}), moduletest.BindCommand(outline.Command, outline.Service{}), moduletest.BindCommand(read.Command, read.Service{}), moduletest.BindCommand(query.Command, query.Service{}))
}

func runToolPluginWorkerInHostProcess(service *history.Service, recovered *recovery.Service, commands []command.Contribution, ctx context.Context, argv0 string, args []string, stdin *os.File, stdout, stderr io.Writer) (bool, int) {
	return runToolPluginWorker(service, recovered, commands, command.HostProcessGroup, ctx, argv0, args, stdin, stdout, stderr)
}

func runToolPluginWorker(service *history.Service, recovered *recovery.Service, commands []command.Contribution, processScope command.ProcessScope, ctx context.Context, argv0 string, args []string, stdin *os.File, stdout, stderr io.Writer) (bool, int) {
	invocation, err := frontends.Recognize(argv0)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return true, 1
	}
	if invocation == nil {
		return false, 0
	}
	return true, invocation.Run(service, recovered, new(evidence.Service), commands, processScope, ctx, args, stdin, stdout, stderr)
}

func runOwnedToolPluginWorker(service *history.Service, recovered *recovery.Service, commands []command.Contribution, ctx context.Context, argv0 string, args []string, stdin *os.File, stdout, stderr io.Writer) (bool, int) {
	invocation, err := frontends.Recognize(argv0)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return true, 1
	}
	if invocation == nil {
		return false, 0
	}
	return true, invocation.Run(service, recovered, new(evidence.Service), commands, command.DedicatedHostProcess, ctx, args, stdin, stdout, stderr)
}

// Close releases the actual owners created by request fixtures.
func (p *observationFixture) Close() error {
	if p == nil {
		return nil
	}
	p.calls.StopAdmission()
	p.calls.Close()
	p.activity.Close()
	return nil
}

func newMekugiProxy(registry *frontends.Registry, projections []binding.InputProjection, usage *accounting.Service, service *history.Service) *observationFixture {
	if registry == nil {
		return nil
	}
	proxy := &observationFixture{
		registry:         registry,
		calls:            service.Calls(),
		review:           sessionreview.New(context.Background(), nil, nil),
		inputProjections: slices.Clone(projections),
		usage:            usage,
		control:          new(sessioncontrol.Observations),
		previewTiming:    previewbinding.Timing{Frame: previewview.PreviewFrameDelay, Unit: previewview.PreviewUnitDelay, Finish: previewview.PreviewFinishDrain},
		execWindows:      &executionobservation.Windows{},
	}
	proxy.activity = sessioncontrol.NewAudience(func(category, message string) { proxy.notice("", "", category, message) })
	return proxy
}
