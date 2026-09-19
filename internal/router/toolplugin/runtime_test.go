package toolplugin

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestLoadBoundsPluginHostOutput verifies that plugin validation rejects excessive host output.
func TestLoadBoundsPluginHostOutput(t *testing.T) {
	t.Parallel()
	pluginDirectory := t.TempDir()
	declaration := `process.stdout.write("x".repeat(16 * 1024 * 1024 + 1));
export default {
  apiVersion: "mekugi-tool-plugin/v1",
  id: "output.test",
  tools: [{
    specification: {type: "custom", name: "output_test", description: "test tool"},
    parse(input) { return input; },
    argv(input) { return [input]; },
    translate(_input, api) { return api.exec(); },
    execute() { return {stdout: "", exitCode: 0}; }
  }]
};
`
	if err := os.WriteFile(filepath.Join(pluginDirectory, "output.mjs"), []byte(declaration), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load(t.Context(), pluginDirectory, filepath.Join(t.TempDir(), "snapshot"))
	if err == nil || !strings.Contains(err.Error(), "plugin runtime output exceeds") {
		t.Fatalf("Load() error = %v", err)
	}
}

// TestLoadProvidesSharedCoreToConfiguredPlugin verifies that plugins can import and use mekugi:core/v1.
func TestLoadProvidesSharedCoreToConfiguredPlugin(t *testing.T) {
	t.Parallel()
	pluginDirectory := t.TempDir()
	declaration := `import {hashLine, parseRowReference} from "mekugi:core/v1";
const row = parseRowReference("12:abcd");
export default {
  apiVersion: "mekugi-tool-plugin/v1",
  id: "shared-core.test",
  tools: [{
    specification: {
      type: "custom",
      name: "shared_core_test",
      description: hashLine("hello") + ":" + row.line + ":" + row.hash,
    },
    parse(input) { return input; },
    argv(input) { return [input]; },
    translate(_input, api) { return api.exec(); },
    execute() { return {stdout: "", exitCode: 0}; }
  }]
};
`
	if err := os.WriteFile(filepath.Join(pluginDirectory, "shared-core.mjs"), []byte(declaration), 0o600); err != nil {
		t.Fatal(err)
	}

	// Validate a supported shared-core consumer alongside an unsupported
	// version in one real registry. A bad sibling must not hide valid tools.
	if err := os.WriteFile(filepath.Join(pluginDirectory, "future.mjs"), []byte(`import "mekugi:core/v2";
export default {apiVersion: "mekugi-tool-plugin/v1", id: "future.test", tools: []};
`), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := Load(t.Context(), pluginDirectory, filepath.Join(t.TempDir(), "snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(snapshot.Diagnostics, "\n"), "mekugi plugin module is not supported: mekugi:core/v2") {
		t.Fatalf("Load() diagnostics = %v", snapshot.Diagnostics)
	}
	for _, plugin := range snapshot.Plugins {
		if plugin.ID != "shared-core.test" {
			continue
		}
		if len(plugin.Tools) != 1 || !strings.Contains(string(plugin.Tools[0].Specification), `"description":"2cf2:12:abcd"`) {
			t.Fatalf("shared-core plugin = %+v", plugin)
		}
		return
	}
	t.Fatal("configured shared-core plugin was not loaded")
}

// TestExecutionHostOutputBoundCoversJSONExpansion verifies the host output buffer accommodates JSON encoding overhead.
func TestExecutionHostOutputBoundCoversJSONExpansion(t *testing.T) {
	const minimum = 6*ExecutionOutputBudgetBytes + 1024
	if maxEncodedExecutionHostOutputBytes < minimum {
		t.Fatalf("execution output bound = %d, want at least %d", maxEncodedExecutionHostOutputBytes, minimum)
	}
}

// TestLoadTimesOutPluginValidation verifies the runtime's own deadline and
// confirms Load waits for the live plugin host to terminate.
func TestLoadTimesOutPluginValidation(t *testing.T) {
	t.Parallel()
	pluginDirectory := t.TempDir()
	pidPath := filepath.Join(t.TempDir(), "validation.pid")
	node, err := resolveNodeRuntime(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	hostExited := false
	t.Cleanup(func() {
		if hostExited {
			return
		}
		if data, err := os.ReadFile(pidPath); err == nil {
			if pid, err := strconv.Atoi(string(data)); err == nil {
				if process, err := os.FindProcess(pid); err == nil {
					_ = process.Kill()
					_ = process.Release()
				}
			}
		}
	})
	declaration := `import {writeFileSync} from "node:fs";
writeFileSync(` + strconv.Quote(pidPath) + `, String(process.pid));
await new Promise((resolve) => setTimeout(resolve, 60_000));
export default {
  apiVersion: "mekugi-tool-plugin/v1",
  id: "timeout.test",
  tools: []
};
`
	if err := os.WriteFile(filepath.Join(pluginDirectory, "timeout.mjs"), []byte(declaration), 0o600); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, err = Load(t.Context(), pluginDirectory, filepath.Join(t.TempDir(), "snapshot"))
	if err == nil || !strings.Contains(err.Error(), "plugin validation exceeded "+pluginInvocationTimeout.String()) {
		t.Fatalf("Load() error = %v", err)
	}
	if elapsed := time.Since(started); elapsed < pluginInvocationTimeout || elapsed > 8*time.Second {
		t.Fatalf("plugin validation took %s, want the runtime deadline %s with startup headroom", elapsed, pluginInvocationTimeout)
	}
	data, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("plugin did not reach its hanging validation: %v", err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil || pid <= 0 {
		t.Fatalf("invalid plugin host PID %q: %v", data, err)
	}
	// Node provides the same process-existence probe on supported platforms.
	probe := `try { process.kill(Number(process.argv[1]), 0); process.exit(1); }
catch (error) { if (error.code !== "ESRCH") throw error; }`
	if output, err := exec.CommandContext(t.Context(), node, "-e", probe, strconv.Itoa(pid)).CombinedOutput(); err != nil {
		t.Fatalf("validation host %d survived Load: %v: %s", pid, err, output)
	}
	hostExited = true
}
