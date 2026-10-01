package router

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Includes a fresh authenticated worker, snapshot verification, source I/O,
// output formatting and process exit. Registry setup is outside measurement.
func BenchmarkNativeFrontends(b *testing.B) {
	directory := b.TempDir()
	replay := filepath.Join(directory, "replay")
	if _, err := openMekugiReplayStore(replay); err != nil {
		b.Fatal(err)
	}
	registry, err := buildToolRegistryAt(b.Context(), filepath.Join(directory, "data"), false, filepath.Join(directory, "runtime"), replay)
	if err != nil {
		b.Fatal(err)
	}
	defer registry.Close()
	if err = registry.installFrontends(); err != nil {
		b.Fatal(err)
	}
	source, err := filepath.Abs("../gooutline/outline.go")
	if err != nil {
		b.Fatal(err)
	}
	second, err := filepath.Abs("../logicalrow/row.go")
	if err != nil {
		b.Fatal(err)
	}
	for _, tc := range []struct {
		name, tool string
		args       []string
	}{
		{"mcat", "mcat", []string{source}},
		{"mcat_batch", "mcat", []string{source, second}},
		{"inspect_go", "inspect_file", []string{source}},
	} {
		b.Run(tc.name, func(b *testing.B) {
			for b.Loop() {
				cmd := exec.CommandContext(b.Context(), registry.frontends[tc.tool], tc.args...)
				cmd.Env = append(os.Environ(), routerTestWorkerEnvironment+"=1", "CODEX_THREAD_ID=")
				if output, err := cmd.CombinedOutput(); err != nil {
					b.Fatalf("frontend: %v\n%s", err, output)
				}
			}
		})
	}
}
