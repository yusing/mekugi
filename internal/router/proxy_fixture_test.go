package router

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/yusing/mekugi/capturer"
)

type proxyRegistryFixture struct {
	once      sync.Once
	registry  *toolRegistry
	directory string
	err       error
}

var proxyTestFixture, pluginProxyTestFixture proxyRegistryFixture

const routerTestWorkerEnvironment = "MEKUGI_ROUTER_TEST_WORKER"
const routerTestWorkerUnscopedEnvironment = "MEKUGI_ROUTER_TEST_WORKER_UNSCOPED"

// Ordinary proxy tests borrow the real, immutable built-in catalog and its
// stateless shell translator. Each proxy still owns its session state and shell
// storage. Tests of plugin loading, registry mutation, startup, or shutdown
// build and close their own registries instead.
func sharedProxyTestRegistry(t *testing.T) *toolRegistry {
	t.Helper()
	return proxyTestFixture.get(t, "")
}
func buildToolRegistryForTest(
	t *testing.T,
	ctx context.Context,
	dataDirectory string,
	diagnose bool,
) (*toolRegistry, error) {
	t.Helper()
	directory := t.TempDir()
	if _, err := openMekugiReplayStore(filepath.Join(directory, "replay")); err != nil {
		return nil, err
	}
	return buildToolRegistryAt(ctx, dataDirectory, diagnose,
		filepath.Join(directory, "runtime"), filepath.Join(directory, "replay"))
}

func (fixture *proxyRegistryFixture) get(t *testing.T, pluginSource string) *toolRegistry {
	t.Helper()
	fixture.once.Do(func() {
		fixture.directory, fixture.err = os.MkdirTemp("", "mekugi-proxy-tests-")
		if fixture.err != nil {
			return
		}
		dataDirectory := filepath.Join(fixture.directory, "data")
		if pluginSource != "" {
			pluginDirectory := filepath.Join(dataDirectory, "plugins")
			if fixture.err = os.MkdirAll(pluginDirectory, 0o700); fixture.err != nil {
				return
			}
			if fixture.err = os.WriteFile(filepath.Join(pluginDirectory, "proxy.mjs"), []byte(pluginSource), 0o600); fixture.err != nil {
				return
			}
		}
		if _, fixture.err = openMekugiReplayStore(filepath.Join(fixture.directory, "replay")); fixture.err != nil {
			return
		}
		fixture.registry, fixture.err = buildToolRegistryAt(
			t.Context(),
			dataDirectory,
			false,
			filepath.Join(fixture.directory, "runtime"),
			filepath.Join(fixture.directory, "replay"),
		)
		if fixture.err == nil {
			fixture.err = fixture.registry.installFrontends()
		}
	})
	if fixture.err != nil {
		t.Fatal(fixture.err)
	}
	return fixture.registry
}

func newProxyWithSharedTestRegistry(t *testing.T, registry *toolRegistry) *mekugiProxy {
	t.Helper()
	proxy := newMekugiProxy(registry)
	proxy.shellDirectory = t.TempDir()
	t.Cleanup(func() {
		if err := proxy.Close(); err != nil {
			t.Error(err)
		}
	})
	return proxy
}

func TestMain(m *testing.M) {
	if os.Getenv(routerTestWorkerEnvironment) == "1" {
		if filepath.Base(os.Args[0]) == "mread" && os.Getenv(routerTestWorkerUnscopedEnvironment) == "1" {
			_ = os.Unsetenv("CODEX_THREAD_ID")
		}
		if handled, code := RunToolPluginWorker(
			context.Background(), os.Args[0], os.Args[1:], os.Stdin, os.Stdout, os.Stderr,
		); handled {
			os.Exit(code)
		}
	}
	if err := os.Unsetenv(capturer.AXReadOutputEnvironment); err != nil {
		fmt.Fprintln(os.Stderr, "isolate AX test instrumentation:", err)
		os.Exit(1)
	}
	for _, name := range []string{
		"OPENCODE_API_KEY",
		"OPENCODE_GO_API_KEY",
		"OPENCODE_ZEN_API_KEY",
	} {
		if err := os.Setenv(name, ""); err != nil {
			fmt.Fprintf(os.Stderr, "isolate test environment variable %s: %v\n", name, err)
			os.Exit(1)
		}
	}
	code := m.Run()
	var err error
	for _, fixture := range []*proxyRegistryFixture{&proxyTestFixture, &pluginProxyTestFixture} {
		if fixture.registry != nil {
			err = errors.Join(err, fixture.registry.Close())
		}
		if fixture.directory != "" {
			err = errors.Join(err, os.RemoveAll(fixture.directory))
		}
	}
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "clean up proxy test fixture: %v\n", err)
		code = 1
	}
	os.Exit(code)
}
