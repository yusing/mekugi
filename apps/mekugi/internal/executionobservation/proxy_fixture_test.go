package executionobservation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	previewbinding "github.com/yusing/mekugi/modules/livepreview/codexbinding"
	previewview "github.com/yusing/mekugi/modules/livepreview/terminalview"
	"github.com/yusing/mekugi/modules/usage/accounting"

	"github.com/dlclark/regexp2/v2"
	"github.com/yusing/mekugi/apps/mekugi/internal/frontends"
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/agentguidance"
	"github.com/yusing/mekugi/modules/agentmetrics/evidence"
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

func newManagedMekugiProxy(t *testing.T) *Preparer {
	t.Helper()
	proxy := newMekugiProxy(newTestUsage(t), testHistory())
	t.Cleanup(func() {
		if err := proxy.Close(); err != nil {
			t.Error(err)
		}
	})
	return proxy
}

func TestMain(m *testing.M) {
	if os.Getenv(routerTestWorkerEnvironment) == "1" {
		if handled, code := runToolPluginWorkerInHostProcess(testHistory(), testCommands(),
			context.Background(), os.Args[0], os.Args[1:], os.Stdin, os.Stdout, os.Stderr,
		); handled {
			os.Exit(code)
		}
	}
	// Keep test publications and configuration outside the user's directories,
	// including parallel cases. Tests that need settings supply their own config.
	var stateDirectory string
	var err error
	// Child test executables must retain their parent's authenticated store path.
	if os.Getenv("MEKUGI_ROUTER_TEST_STATE_ISOLATED") == "" {
		stateDirectory, err = os.MkdirTemp("", "mekugi-test-state-")
		if err == nil {
			err = os.Setenv("XDG_STATE_HOME", stateDirectory)
		}
		if err == nil {
			err = os.Setenv("XDG_CONFIG_HOME", filepath.Join(stateDirectory, "config"))
		}
		if err == nil {
			err = os.Setenv("MEKUGI_ROUTER_TEST_STATE_ISOLATED", "1")
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	// Chroma lexers set regexp2 match timeouts, which lazily start one shared
	// clock goroutine. Start it here so a synctest bubble never owns it.
	clock := regexp2.MustCompile("x", regexp2.None)
	clock.MatchTimeout = 1000 * time.Hour
	if _, err := clock.MatchString("x"); err != nil {
		fmt.Fprintln(os.Stderr, "start regexp2 timeout clock:", err)
		os.Exit(1)
	}
	if err := os.Unsetenv(evidence.AXReadOutputEnvironment); err != nil {
		fmt.Fprintln(os.Stderr, "isolate AX test instrumentation:", err)
		os.Exit(1)
	}
	code := m.Run()
	if stateDirectory != "" {
		err = os.RemoveAll(stateDirectory)
	}
	for _, fixture := range []*proxyRegistryFixture{&proxyTestFixture} {
		if fixture.registry != nil {
			err = errors.Join(err, fixture.registry.Close())
		}
		if fixture.directory != "" {
			err = errors.Join(err, os.RemoveAll(fixture.directory))
		}
	}
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "clean up router test fixtures: %v\n", err)
		code = 1
	}
	os.Exit(code)
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
func (p *Preparer) Close() error {
	if p == nil {
		return nil
	}
	p.Calls.StopAdmission()
	p.Calls.Close()
	p.Audience.Close()
	return nil
}

func newMekugiProxy(usage *accounting.Service, service *history.Service) *Preparer {
	proxy := &Preparer{Config: Config{Calls: service.Calls(), Review: sessionreview.New(context.Background(), nil, nil), Usage: usage, Control: new(sessioncontrol.Observations), PreviewTiming: previewbinding.Timing{Frame: previewview.PreviewFrameDelay, Unit: previewview.PreviewUnitDelay, Finish: previewview.PreviewFinishDrain}, Windows: &Windows{}}}
	proxy.Audience = sessioncontrol.NewAudience(func(category, message string) { proxy.notice("", "", category, message) })
	return proxy
}
