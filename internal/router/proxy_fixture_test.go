package router

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/yusing/mekugi/capturer"
	"github.com/yusing/mekugi/internal/shellruntime"
)

type proxyRegistryFixture struct {
	once      sync.Once
	registry  *toolRegistry
	directory string
	err       error
}

var proxyTestFixture, realProxyTestFixture, pluginProxyTestFixture proxyRegistryFixture

// Ordinary proxy tests borrow the real, immutable built-in catalog and its
// stateless shell translator. Each proxy still owns its session state and shell
// storage. Tests of plugin loading, registry mutation, startup, or shutdown
// build and close their own registries instead.
func sharedProxyTestRegistry(t *testing.T) *toolRegistry {
	t.Helper()
	return proxyTestFixture.get(t, "", testMekugiToolDescription)
}

func (fixture *proxyRegistryFixture) get(t *testing.T, pluginSource, toolDescription string) *toolRegistry {
	t.Helper()
	fixture.once.Do(func() {
		fixture.directory, fixture.err = os.MkdirTemp("", "mekugi-proxy-tests-")
		if fixture.err != nil {
			return
		}
		t.Setenv(shellruntime.RuntimeDirectoryEnvironment, fixture.directory)
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
		fixture.registry, fixture.err = buildToolRegistry(t.Context(), dataDirectory, toolDescription, false)
	})
	if fixture.err != nil {
		t.Fatal(fixture.err)
	}
	// Workers must not put thread artifacts into the shared fixture directory.
	t.Setenv(shellruntime.RuntimeDirectoryEnvironment, t.TempDir())
	return fixture.registry
}

func newProxyWithSharedTestRegistry(t *testing.T, translator mekugiTranslator, registry *toolRegistry) *mekugiProxy {
	t.Helper()
	proxy := newMekugiProxy(translator, registry, false, false)
	proxy.shellDirectory = os.Getenv(shellruntime.RuntimeDirectoryEnvironment)
	t.Cleanup(func() {
		if err := proxy.Close(); err != nil {
			t.Error(err)
		}
	})
	return proxy
}

func TestMain(m *testing.M) {
	if err := os.Unsetenv(capturer.AXReadOutputEnvironment); err != nil {
		fmt.Fprintln(os.Stderr, "isolate AX test instrumentation:", err)
		os.Exit(1)
	}
	code := m.Run()
	var err error
	for _, fixture := range []*proxyRegistryFixture{&proxyTestFixture, &realProxyTestFixture, &pluginProxyTestFixture} {
		err = errors.Join(err, fixture.registry.Close())
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
