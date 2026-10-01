package router

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	abs := func(path string) string {
		value, err := filepath.Abs(path)
		if err != nil {
			b.Fatal(err)
		}
		return value
	}
	inspectBatch := []string{
		abs("app_server_ui.go"), abs("live_activity_view.go"), abs("../ui/activity/paint.go"),
		abs("vcs_display.go"), abs("server.go"), abs("native_journal.go"),
		abs("storage_retention.go"), abs("mchanges_apply.go"),
	}
	docs := []string{abs("../../doc/architecture/ui.md"), abs("../../doc/architecture/activity.md"), abs("../../doc/architecture/boundary.md")}
	rows := filepath.Join(directory, "rows.txt")
	if err := os.WriteFile(rows, []byte(strings.Repeat("word\n", 3000)), 0o600); err != nil {
		b.Fatal(err)
	}
	for _, tc := range []struct {
		name, tool string
		args       []string
		incomplete bool
	}{
		{"mcat", "mcat", []string{source}, false},
		{"mcat_batch", "mcat", []string{source, second}, false},
		{"inspect_go", "inspect_file", []string{source}, false},
		{"mcat_3000_rows", "mcat", []string{rows}, false},
		{"mcat_docs", "mcat", docs, false},
		{"inspect_go_batch", "inspect_file", inspectBatch, true},
	} {
		b.Run(tc.name, func(b *testing.B) {
			for b.Loop() {
				cmd := exec.CommandContext(b.Context(), registry.frontends[tc.tool], tc.args...)
				cmd.Env = append(os.Environ(), routerTestWorkerEnvironment+"=1", "CODEX_THREAD_ID=")
				output, err := cmd.CombinedOutput()
				if tc.incomplete && cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == 1 && strings.Contains(string(output), "next_call: mread ") {
					continue
				}
				if err != nil {
					b.Fatalf("frontend: %v\n%s", err, output)
				}
			}
		})
	}
}
