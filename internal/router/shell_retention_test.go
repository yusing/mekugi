package router

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/shellruntime"
)

func newShellStorageTestProxy(t *testing.T) (*mekugiProxy, string) {
	t.Helper()
	proxy := &mekugiProxy{
		shellDirectory: t.TempDir(),
		shellSessions:  make(map[string]*shellSession),
		registry:       &toolRegistry{shellRuntime: "/unused-test-worker"},
	}
	t.Cleanup(func() { _ = proxy.Close() })
	directory, err := proxy.storeShellRuntime("thread-id")
	if err != nil {
		t.Fatal(err)
	}
	return proxy, directory
}

func testShellRuntimePath(t *testing.T, root, threadID string) string {
	t.Helper()
	path, err := shellruntime.Path(root, threadID)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func shellDescriptorCount(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Skipf("descriptor enumeration unavailable: %v", err)
	}
	return len(entries)
}

func TestShellRuntimeHistoricalThreadsDoNotRetainDescriptors(t *testing.T) {
	proxy, _ := newShellStorageTestProxy(t)
	before := shellDescriptorCount(t)
	for i := range 80 {
		if _, err := proxy.storeShellRuntime(fmt.Sprintf("thread-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if growth := shellDescriptorCount(t) - before; growth > 2 {
		t.Fatalf("80 launcher-only threads retained %d descriptors", growth)
	}
}

func TestShellShutdownRemovesLauncherWithoutScripts(t *testing.T) {
	proxy, directory := newShellStorageTestProxy(t)
	if _, err := os.Stat(directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("launcher preparation created script storage: %v", err)
	}
	runtimePath := testShellRuntimePath(t, proxy.shellDirectory, "thread-id")
	if err := proxy.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(runtimePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("launcher survived shutdown without scripts: %v", err)
	}
}

func TestShellDoesNotRetainSource(t *testing.T) {
	for name, source := range map[string]string{
		"short":       "printf ok",
		"multiline":   "printf one\nprintf two\nprintf three\nprintf four\n",
		"interpreter": "#!python3\nprint('ok')\n",
		"batch":       "printf one\n#!bash\nprintf two\n",
	} {
		t.Run(name, func(t *testing.T) {
			transform, proxy, _, _ := newMekugiTestTransform(t)
			contribution, _ := proxy.registry.contribution("shell")
			history, err := transform.translateRegisteredTool(contribution, name, source, nil)
			if err != nil || history.TranslationError != "" {
				t.Fatalf("translate = %+v, %v", history, err)
			}
			var result map[string]json.RawMessage
			runShellCatJavaScript(t, proxy.registry.NodeExecutable, t.TempDir(), history.carrierInput(), &result,
				`tools.exec_command = async () => ({output:'ok',exit_code:0,future:'preserved'});`)
			for _, key := range []string{"retained", "retention", "script_ref"} {
				if _, exists := result[key]; exists {
					t.Fatalf("obsolete %s metadata: %s", key, mustMarshalJSON(result))
				}
			}
			if name != "batch" && (jsonString(result, "output") != "ok" || jsonString(result, "future") != "preserved") {
				t.Fatalf("native result fields lost: %s", mustMarshalJSON(result))
			}
			if _, err := os.Stat(transform.shellDirectory); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("ordinary shell call created private storage: %v", err)
			}
		})
	}
}

func TestShellRejectsRemovedScriptDirective(t *testing.T) {
	transform, proxy, _, _ := newMekugiTestTransform(t)
	contribution, _ := proxy.registry.contribution("shell")
	history, err := transform.translateRegisteredTool(contribution, "removed", "#!script=@shell/old\nprintf should-not-run", nil)
	if err != nil || !strings.Contains(history.TranslationError, "unsupported shell directive #!script") {
		t.Fatalf("removed directive = %+v, %v", history, err)
	}
	if strings.Contains(history.carrierInput(), "tools.exec_command") {
		t.Fatal("removed directive emitted an executable carrier")
	}
}
