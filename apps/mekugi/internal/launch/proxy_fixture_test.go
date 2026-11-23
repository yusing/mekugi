package launch

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/executionobservation"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	previewbinding "github.com/yusing/mekugi/modules/livepreview/codexbinding"
	previewview "github.com/yusing/mekugi/modules/livepreview/terminalview"
	"github.com/yusing/mekugi/modules/usage/accounting"

	"github.com/yusing/mekugi/apps/mekugi/internal/frontends"
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/agentguidance"
	"github.com/yusing/mekugi/modules/changehistory"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
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
func usePreloadTestFrontend(t *testing.T) {
	t.Helper()
	registry := sharedProxyTestRegistry(t)
	t.Setenv("PATH", registry.FrontendDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(routerTestWorkerEnvironment, "1")
	t.Setenv("CODEX_THREAD_ID", "")
}

func sharedProxyTestRegistry(t *testing.T) *frontends.Registry {
	t.Helper()
	return proxyTestFixture.get(t)
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
		fixture.registry, fixture.err = frontends.New(t.Context(), frontends.Config{Commands: testCommands(), ReplayDirectory: filepath.Join(fixture.directory, "replay")})
	})
	if fixture.err != nil {
		t.Fatal(fixture.err)
	}
	return fixture.registry
}

func newProxyWithSharedTestRegistry(t *testing.T, registry *frontends.Registry) *observationFixture {
	t.Helper()
	proxy := newMekugiProxy(registry, newTestUsage(t), testHistory())
	t.Cleanup(func() {
		if err := proxy.Close(); err != nil {
			t.Error(err)
		}
	})
	return proxy
}

func testCommands() []command.Contribution {
	hist := testHistory()
	chg, err := changehistory.Module.New(changehistory.Deps{History: hist}, module.Slots{})
	if err != nil {
		panic(err)
	}
	commands := []command.Contribution{moduletest.BindCommand(changehistory.Module.Contributes.Commands[0], chg)}
	guidance, err := agentguidance.Module.New(agentguidance.Deps{History: hist, Changes: chg}, module.Slots{})
	if err != nil {
		panic(err)
	}
	for _, definition := range agentguidance.Module.Contributes.Commands {
		commands = append(commands, moduletest.BindCommand(definition, guidance))
	}
	return commands
}

func runToolPluginWorkerInHostProcess(service *history.Service, commands []command.Contribution, ctx context.Context, argv0 string, args []string, stdin *os.File, stdout, stderr io.Writer) (bool, int) {
	return runToolPluginWorker(service, commands, command.HostProcessGroup, ctx, argv0, args, stdin, stdout, stderr)
}

func runToolPluginWorker(service *history.Service, commands []command.Contribution, processScope command.ProcessScope, ctx context.Context, argv0 string, args []string, stdin *os.File, stdout, stderr io.Writer) (bool, int) {
	invocation, err := frontends.Recognize(argv0)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return true, 1
	}
	if invocation == nil {
		return false, 0
	}
	return true, invocation.Run(service, commands, processScope, ctx, args, stdin, stdout, stderr)
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

func newMekugiProxy(registry *frontends.Registry, usage *accounting.Service, service *history.Service) *observationFixture {
	if registry == nil {
		return nil
	}
	proxy := &observationFixture{
		registry:      registry,
		calls:         service.Calls(),
		review:        sessionreview.New(context.Background(), nil, nil),
		usage:         usage,
		control:       new(sessioncontrol.Observations),
		previewTiming: previewbinding.Timing{Frame: previewview.PreviewFrameDelay, Unit: previewview.PreviewUnitDelay, Finish: previewview.PreviewFinishDrain},
		execWindows:   &executionobservation.Windows{},
	}
	proxy.activity = sessioncontrol.NewAudience(func(category, message string) { proxy.notice("", "", category, message) })
	return proxy
}
