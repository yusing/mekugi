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
	want := []string{"inspect_file", "mcat", "mchanges", "mread", "mrun", "msymbol"}
	var got []string
	for name := range registry.Frontends {
		got = append(got, name)
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("frontends = %v, want %v", got, want)
	}
	for _, name := range []string{"mcat", "inspect_file", "msymbol", "mread", "mrun", "mchanges"} {
		found := false
		for _, contribution := range registry.ordered {
			if contribution.Name == name {
				found = contribution.NativeExecutor == name
			}
		}
		if !found {
			t.Fatalf("%s is not a compiled tool with its native backend", name)
		}
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
	source := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(source, []byte("native contents\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run := func() (string, string, error) {
		command := exec.CommandContext(t.Context(), registry.Frontends["mcat"], source)
		command.Env = append(os.Environ(), testWorkerEnvironment+"=1", "CODEX_THREAD_ID=")
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		err := command.Run()
		return stdout.String(), stderr.String(), err
	}
	if stdout, stderr, err := run(); err != nil || stdout != "native contents\n" || stderr != "" {
		t.Fatalf("native frontend: stdout %q stderr %q error %v", stdout, stderr, err)
	}
	manifest, err := readToolWorkerManifest(filepath.Join(registry.SnapshotDir, manifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	manifest.GuidanceSHA256 = "altered"
	if err := writeToolWorkerManifest(registry.SnapshotDir, manifest); err != nil {
		t.Fatal(err)
	}
	if stdout, stderr, err := run(); err == nil || stdout != "" || !strings.Contains(stderr, "registry identity mismatch") {
		t.Fatalf("altered catalog: stdout %q stderr %q error %v", stdout, stderr, err)
	}
}
