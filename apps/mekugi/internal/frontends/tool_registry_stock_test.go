package frontends

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
	registry := sharedTestRegistry(t)
	want := []string{"mchanges", "mreload"}
	var got []string
	entries, err := os.ReadDir(registry.FrontendDirectory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		got = append(got, entry.Name())
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("frontends = %v, want %v", got, want)
	}
}

func TestNativeFrontendAuthenticatesCatalogWithoutExternalRuntime(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	registry, err := buildRegistryForTest(t, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := registry.Close(); err != nil {
			t.Error(err)
		}
	})
	run := func() (string, string, error) {
		command := exec.CommandContext(t.Context(), filepath.Join(registry.FrontendDirectory, "mchanges"), "--list")
		command.Env = append(os.Environ(), testWorkerEnvironment+"=1", "CODEX_THREAD_ID="+uniqueTestThread("auth"))
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		err := command.Run()
		return stdout.String(), stderr.String(), err
	}
	if stdout, stderr, err := run(); err != nil || stdout != "" || stderr != "" {
		t.Fatalf("native frontend: stdout %q stderr %q error %v", stdout, stderr, err)
	}
	manifest, err := readToolWorkerManifest(filepath.Join(registry.SnapshotDir, manifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	manifest.Tools = append(manifest.Tools, "altered")
	if err := writeToolWorkerManifest(registry.SnapshotDir, manifest); err != nil {
		t.Fatal(err)
	}
	if stdout, stderr, err := run(); err == nil || stdout != "" || !strings.Contains(stderr, "registry identity mismatch") {
		t.Fatalf("altered catalog: stdout %q stderr %q error %v", stdout, stderr, err)
	}
}
