package router

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReportIssueDescriptionIsNonInstructional(t *testing.T) {
	const want = "Free-form Markdown issue report for an observed mekugi-related tool interaction."
	if reportIssueToolDescription != want {
		t.Fatalf("reportIssueToolDescription = %q, want %q", reportIssueToolDescription, want)
	}
}

func TestWorkerFrontendSymlinkLifecycle(t *testing.T) {
	directory := t.TempDir()
	executable := filepath.Join(directory, "mekugi")
	if err := os.WriteFile(executable, []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	name := "plugin_tool"
	link := filepath.Join(directory, name)
	wrapper := filepath.Join(t.TempDir(), name)

	if err := os.WriteFile(link, []byte("unrelated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureWorkerSymlinkInDirectory(wrapper, directory, name); err == nil ||
		!strings.Contains(err.Error(), "not a symlink") {
		t.Fatalf("unrelated frontend error = %v", err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(executable, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureWorkerSymlinkInDirectory(wrapper, directory, name); err == nil ||
		!strings.Contains(err.Error(), "points to") {
		t.Fatalf("direct executable frontend error = %v", err)
	}
	if target, err := os.Readlink(link); err != nil || target != executable {
		t.Fatalf("direct executable frontend changed to %q: %v", target, err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureWorkerSymlinkInDirectory(wrapper, directory, name); err != nil {
		t.Fatalf("create frontend: %v", err)
	}

	if got, err := ensureWorkerSymlinkInDirectory(wrapper, directory, name); err != nil || got != link {
		t.Fatalf("reuse frontend = %q, %v", got, err)
	}
	otherWrapper := filepath.Join(t.TempDir(), name)
	if err := removeWorkerFrontendSymlink(link, otherWrapper); err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(link); err != nil || target != wrapper {
		t.Fatalf("foreign frontend changed to %q: %v", target, err)
	}
	if err := removeWorkerFrontendSymlink(link, wrapper); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatalf("owned frontend remains: %v", err)
	}

}

func TestToolRegistryStartup(t *testing.T) {
	declaration := func(pluginID, toolName, format string) string {
		t.Helper()
		formatField := ""
		if format != "" {
			formatField = ", format: " + format
		}
		return fmt.Sprintf(`export default {
  apiVersion: "mekugi-tool-plugin/v1",
  id: %q,
  tools: [{
    specification: {type: "custom", name: %q, description: "test tool"%s},
    parse(input) { return input; },
    argv(input) { return [input]; },
    translate(_input, api) { return api.exec(); },
    execute(argv) { return {stdout: argv.join(" "), exitCode: 0}; }
  }]
};
`, pluginID, toolName, formatField)
	}
	writePlugin := func(t *testing.T, directory, name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("missing directory loads embedded built-ins", func(t *testing.T) {
		t.Parallel()
		registry, err := buildToolRegistryForTest(t, t.Context(), t.TempDir(), testMekugiToolDescription, false)
		if err != nil {
			t.Fatal(err)
		}
		snapshot := registry.SnapshotDir
		if registry.NodeExecutable == "" || len(registry.ordered) != 8 {
			t.Fatalf("registry = %+v", registry)
		}
		if err := registry.installFrontends(); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"hchanges", "hcat", "hgrep", "hsymbol", "inspect_file", "shell"} {
			_, ok := registry.contribution(name)
			if !ok {
				t.Fatalf("built-in %q is unavailable", name)
			}
			if wrapper, ok := registry.wrapper(name); ok {
				t.Fatalf("built-in %q unexpectedly has wrapper %q", name, wrapper)
			}
			if frontend, ok := registry.frontends[name]; ok {
				t.Fatalf("built-in %q unexpectedly has frontend %q", name, frontend)
			}
		}
		specifications, err := registry.specifications()
		if err != nil {
			t.Fatal(err)
		}
		if len(specifications) != 3 ||
			specifications[0].Name != "hpatch" ||
			specifications[1].Name != "hpatch_recover" ||
			specifications[2].Name != "shell" {
			t.Fatalf("model-visible specifications = %#v", specifications)
		}
		if err := registry.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(snapshot); !os.IsNotExist(err) {
			t.Fatalf("snapshot remains after close: %v", err)
		}
		for name, frontend := range registry.frontends {
			if _, err := os.Lstat(frontend); !os.IsNotExist(err) {
				t.Fatalf("frontend %q remains after close: %v", name, err)
			}
		}
	})

	t.Run("diagnose mode adds router-native report issue", func(t *testing.T) {
		t.Parallel()
		registry, err := buildToolRegistryForTest(t, t.Context(), t.TempDir(), testMekugiToolDescription, true)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := registry.Close(); err != nil {
				t.Error(err)
			}
		}()

		contribution, ok := registry.contribution(reportIssueToolName)
		if !ok || contribution.PluginID != "builtin.mekugi" || !contribution.Builtin || !contribution.ModelVisible {
			t.Fatalf("report issue contribution = %+v, available %t", contribution, ok)
		}
		var specification map[string]json.RawMessage
		if err := json.Unmarshal(contribution.Specification, &specification); err != nil {
			t.Fatal(err)
		}
		if _, formatted := specification["format"]; formatted {
			t.Fatalf("report issue specification is not freeform: %s", contribution.Specification)
		}
		if _, ok := registry.wrapper(reportIssueToolName); ok {
			t.Fatal("router-native report issue unexpectedly has a worker wrapper")
		}
		specifications, err := registry.specifications()
		if err != nil {
			t.Fatal(err)
		}
		if len(specifications) != 4 ||
			specifications[0].Name != mekugiToolName ||
			specifications[1].Name != mekugiRecoveryToolName ||
			specifications[2].Name != reportIssueToolName ||
			specifications[3].Name != "shell" {
			t.Fatalf("model-visible specifications = %#v", specifications)
		}
	})

	t.Run("lexical declarations are pinned and wrapped", func(t *testing.T) {
		t.Parallel()
		dataDirectory := t.TempDir()
		pluginDirectory := filepath.Join(dataDirectory, "plugins")
		if err := os.Mkdir(pluginDirectory, 0o700); err != nil {
			t.Fatal(err)
		}
		writePlugin(t, pluginDirectory, "zeta.mjs", declaration(
			"zeta.plugin",
			"zeta_tool",
			`{type: "grammar", syntax: "regex", definition: "^[a-z]+$"}`,
		))
		writePlugin(t, pluginDirectory, "alpha.js", declaration(
			"alpha.plugin",
			"alpha_tool",
			"{type: \"grammar\", syntax: \"lark\", definition: \"start: \\\"ok\\\"\"}",
		))
		if err := os.Symlink(filepath.Join(pluginDirectory, "alpha.js"), filepath.Join(pluginDirectory, "ignored.mjs")); err != nil {
			t.Fatal(err)
		}

		registry, err := buildToolRegistryForTest(t, t.Context(), dataDirectory, testMekugiToolDescription, false)
		if err != nil {
			t.Fatal(err)
		}
		snapshot := registry.SnapshotDir
		if len(registry.ordered) != 10 ||
			registry.ordered[8].PluginID != "alpha.plugin" ||
			registry.ordered[9].PluginID != "zeta.plugin" {
			t.Fatalf("registration order = %+v", registry.ordered)
		}
		for _, name := range []string{"alpha_tool", "zeta_tool"} {
			wrapper, ok := registry.wrapper(name)
			if !ok {
				t.Fatalf("wrapper %q is unavailable", name)
			}
			target, err := os.Readlink(wrapper)
			if err != nil {
				t.Fatal(err)
			}
			if target == "" || filepath.Base(wrapper) != name {
				t.Fatalf("wrapper %q targets %q", wrapper, target)
			}
		}
		for _, name := range []string{mekugiToolName, "hcat", "hgrep", "hsymbol", "inspect_file", "shell"} {
			if _, ok := registry.wrapper(name); ok {
				t.Fatalf("built-in %q unexpectedly has an executor wrapper", name)
			}
		}
		snapshotModule := filepath.Join(snapshot, "runtime", "plugins", "user", "alpha.js")
		pinned, err := os.ReadFile(snapshotModule)
		if err != nil {
			t.Fatal(err)
		}
		writePlugin(t, pluginDirectory, "alpha.js", "throw new Error('changed live declaration');\n")
		after, err := os.ReadFile(snapshotModule)
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != string(pinned) {
			t.Fatal("snapshot changed with live declaration")
		}
		var manifest toolWorkerManifest
		encodedManifest, err := os.ReadFile(filepath.Join(snapshot, toolPluginManifestFilename))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(encodedManifest, &manifest); err != nil {
			t.Fatal(err)
		}
		registryID, authenticated := toolRegistryIDFromDirectory(snapshot)
		if !authenticated || manifest.RegistryID != registryID || len(manifest.Tools) != 10 {
			t.Fatalf("manifest = %+v, snapshot registry ID %q, authenticated %t", manifest, registryID, authenticated)
		}
		if err := registry.Close(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("declaration and ownership errors aggregate", func(t *testing.T) {
		t.Parallel()
		dataDirectory := t.TempDir()
		pluginDirectory := filepath.Join(dataDirectory, "plugins")
		if err := os.Mkdir(pluginDirectory, 0o700); err != nil {
			t.Fatal(err)
		}
		writePlugin(t, pluginDirectory, "bad.mjs", "export default {apiVersion: 'wrong'};\n")
		writePlugin(t, pluginDirectory, "duplicate-a.mjs", declaration("duplicate.plugin", mekugiToolName, ""))
		writePlugin(t, pluginDirectory, "duplicate-b.mjs", declaration("duplicate.plugin", "other_tool", ""))
		writePlugin(t, pluginDirectory, "shell.mjs", declaration("shell.plugin", "eval", ""))
		writePlugin(t, pluginDirectory, "configured-shell.mjs", declaration("example.shell", "shell", ""))
		registry, err := buildToolRegistryForTest(t, t.Context(), dataDirectory, testMekugiToolDescription, false)
		if registry != nil || err == nil {
			t.Fatalf("registry = %+v, error = %v", registry, err)
		}
		diagnostic := err.Error()
		for _, fragment := range []string{
			"bad.mjs: default export",
			`plugin identity "duplicate.plugin"`,
			`tool name "hpatch"`,
			"collides with a shell keyword or built-in",
			`tool name "shell" is owned by both builtin.shell and example.shell`,
		} {
			if !strings.Contains(diagnostic, fragment) {
				t.Fatalf("startup diagnostic lacks %q:\n%s", fragment, diagnostic)
			}
		}
	})

	t.Run("invalid registry fails the real entry point before listening", func(t *testing.T) {
		configRoot := t.TempDir()
		switch runtime.GOOS {
		case "windows":
			t.Setenv("AppData", configRoot)
		case "darwin", "ios":
			t.Setenv("HOME", configRoot)
		case "plan9":
			t.Setenv("home", configRoot)
		default:
			t.Setenv("XDG_CONFIG_HOME", configRoot)
		}
		dataDirectory, err := mekugiDataDirectory()
		if err != nil {
			t.Fatal(err)
		}
		pluginDirectory := filepath.Join(dataDirectory, "plugins")
		if err := os.MkdirAll(pluginDirectory, 0o700); err != nil {
			t.Fatal(err)
		}
		writePlugin(t, pluginDirectory, "invalid.mjs", "export default null;\n")
		var stderr strings.Builder
		err = RunSession(t.Context(), nil, nil, func(Session) { t.Error("invalid registry reached readiness") }, nil)
		if err == nil || !strings.Contains(err.Error(), "initialize tool registry") {
			t.Fatalf("Run() error = %v", err)
		}
		if strings.Contains(stderr.String(), "listening") {
			t.Fatalf("router listened with invalid registry: %s", stderr.String())
		}
	})
}
