package router

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestToolRegistryExposesOnlyExecutableFrontends(t *testing.T) {
	registry := sharedProxyTestRegistry(t)
	want := []string{"inspect_file", "mcat", "mchanges", "mcommentary", "mread", "mrun", "msymbol"}
	var got []string
	for name := range registry.frontends {
		got = append(got, name)
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("frontends = %v, want %v", got, want)
	}
	for _, name := range []string{"mread", "mrun", "mchanges"} {
		found := false
		for _, contribution := range registry.ordered {
			if contribution.Name == name {
				found = contribution.PluginID == builtinToolsPluginID && contribution.NativeExecutor == name && !contribution.Builtin
			}
		}
		if !found {
			t.Fatalf("%s is not a bundled plugin tool with its native backend", name)
		}
	}
}

func TestConfiguredPluginRunsThroughAuthenticatedFrontend(t *testing.T) {
	registry := pluginProxyTestFixture.get(t, testToolPluginDeclaration)
	frontend, ok := registry.frontends["plugin_tool"]
	if !ok {
		t.Fatal("configured plugin frontend is unavailable")
	}
	command := exec.CommandContext(t.Context(), frontend, "one", "two")
	command.Env = append(os.Environ(), routerTestWorkerEnvironment+"=1")
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil || stdout.String() != "one|two" || stderr.Len() != 0 {
		t.Fatalf("plugin frontend = stdout %q stderr %q err %v", stdout.String(), stderr.String(), err)
	}
}

func TestConfiguredPluginCannotClaimBundledFrontendName(t *testing.T) {
	data := t.TempDir()
	pluginDirectory := filepath.Join(data, "plugins")
	if err := os.MkdirAll(pluginDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	source := strings.Replace(testToolPluginDeclaration, "plugin_tool", "mrun", 1)
	if err := os.WriteFile(filepath.Join(pluginDirectory, "collision.mjs"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := buildToolRegistryForTest(t, t.Context(), data, false)
	if registry != nil {
		_ = registry.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "tool name \"mrun\" is owned by both") {
		t.Fatalf("configured plugin claimed bundled mrun: %v", err)
	}
}
