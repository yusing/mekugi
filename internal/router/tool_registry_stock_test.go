package router

import (
	"bytes"
	"os"
	"os/exec"
	"slices"
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
