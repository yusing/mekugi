package router

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/router/toolplugin"
)

func retainedShellTestOutput(t *testing.T, diagnostic string) (string, string) {
	t.Helper()
	match := regexp.MustCompile(`hread (r_[A-Za-z0-9_-]{22})`).FindStringSubmatch(diagnostic)
	if len(match) != 2 {
		t.Fatalf("missing output recovery receipt: %s", diagnostic)
	}
	registry := sharedProxyTestRegistry(t)
	manifest, err := readToolWorkerManifest(filepath.Join(registry.SnapshotDir, toolPluginManifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	store, err := shellOutputStore(manifest)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.readShellOutput(t.Context(), match[1])
	if err != nil {
		t.Fatal(err)
	}
	return record.Stdout, record.Stderr
}

func TestShellReadOutputBudgetRetainsWholeBatchAndExitStatus(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	for _, interpreter := range []string{"bash", "sh"} {
		t.Run(interpreter, func(t *testing.T) {
			directory := t.TempDir()
			if err := os.WriteFile(filepath.Join(directory, "first"), []byte(strings.Repeat("alpha row\n", 400)), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "second"), []byte(strings.Repeat("beta row\n", 400)), 0o600); err != nil {
				t.Fatal(err)
			}
			script := "#!params={\"max_output_tokens\":1600}\nhcat first\nhcat second\nprintf done > finished\nprintf 'command error\\n' >&2\nexit 7"
			stdout, stderr, status := runShellWorkerTest(t, registry, interpreter, nil, script, nil,
				newShellWorkerTestInvocation(directory))
			if status != 7 || !strings.Contains(stderr, "hread ") || !strings.HasSuffix(stdout, "\n") {
				t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout, stderr)
			}
			allStdout, allStderr := retainedShellTestOutput(t, stderr)
			if strings.Count(stdout+allStdout, "alpha row") != 400 || strings.Count(stdout+allStdout, "beta row") != 400 ||
				allStderr != "command error\n" {
				t.Fatalf("retained output lost command data: %d bytes, %q", len(allStdout), allStderr)
			}
			if _, err := os.Stat(filepath.Join(directory, "finished")); err != nil {
				t.Fatalf("display budget stopped the producer: %v", err)
			}
			if allStdout == "" {
				t.Fatal("batch output was not bounded")
			}
		})
	}
}

func TestShellReadOutputBudgetDoesNotChangePipelinesOrRedirections(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "input"), []byte(strings.Repeat("row\n", 400)), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil,
		"#!params={\"max_output_tokens\":1200}\nhcat input > saved\nhcat input | wc -l\n", nil, newShellWorkerTestInvocation(directory))
	if status != 0 || strings.TrimSpace(stdout) != "400" || stderr != "" {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout, stderr)
	}
	data, err := os.ReadFile(filepath.Join(directory, "saved"))
	if err != nil || strings.Count(string(data), "\n") != 400 {
		t.Fatalf("redirected output was budgeted: %v", err)
	}
}

func TestShellOutputBudgetPreservesExecutionWithSmallBudget(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	_, stderr, status := runShellWorkerTest(t, registry, "bash", nil,
		"#!params={\"max_output_tokens\":20}\nprintf changed > marker\nprintf visible", nil, newShellWorkerTestInvocation(directory))
	if status != 0 {
		t.Fatalf("status=%d stderr=%q", status, stderr)
	}
	if data, err := os.ReadFile(filepath.Join(directory, "marker")); err != nil || string(data) != "changed" {
		t.Fatalf("small budget changed execution: %v", err)
	}
	stdout, retainedStderr := retainedShellTestOutput(t, stderr)
	if stdout != "visible" || retainedStderr != "" {
		t.Fatalf("small budget lost output: %q %q", stdout, retainedStderr)
	}
}

func TestShellOutputBudgetPreservesTemplateData(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil,
		"#!params={\"max_output_tokens\":256}\n#!cmd={.} | wc -l\nfor i in {1..2000}; do printf 'row\\n'; done",
		nil, newShellWorkerTestInvocation(directory))
	if status != 0 || stderr != "" || strings.Count(stdout, "\n") != 2000 {
		t.Fatalf("template input truncated: status=%d rows=%d stderr=%q", status, strings.Count(stdout, "\n"), stderr)
	}
}

func TestShellBudgetCarrierHasNoAddedFlagsOrEnvironment(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	contribution, _ := registry.contribution("shell")
	source := "#!params={\"max_output_tokens\":1600}\nhcat first\nhcat second"
	command, err := registry.execCarrierCommand(contribution, source, []string{"bash", source}, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if command != workerCommand("shell", []string{"bash", source}) {
		t.Fatalf("budget changed shell command shape: %s", command)
	}
	batch, err := registry.execCarrierCommand(contribution, source, []string{"bash", source}, "", 1000)
	if err != nil {
		t.Fatal(err)
	}
	expected := "#!params={\"max_output_tokens\":1000}\nhcat first\nhcat second"
	if batch != workerCommand("shell", []string{"bash", expected}) {
		t.Fatalf("batch budget used flags or environment: %s", batch)
	}
}

func TestShellOutputBudgetPreservesNonBashBodyShebang(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	stdout, stderr, status := runShellWorkerTest(t, registry, "cat", nil,
		"#!cat\n#!/anything\nhello\n", nil)
	if status != 0 || stderr != "" || stdout != "#!/anything\nhello\n" {
		t.Fatalf("body parsed twice: status=%d stdout=%q stderr=%q", status, stdout, stderr)
	}
}

func TestShellOutputBudgetFitsFramedResults(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	manifest, err := readToolWorkerManifest(filepath.Join(registry.SnapshotDir, toolPluginManifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	runtimeRoot := filepath.Join(registry.SnapshotDir, manifest.RuntimeRoot)
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "input"), []byte(strings.Repeat("\"quoted\" \t🙂 row\\value\n", 3000)), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, budget := range []int{256, 1600, 10000} {
		t.Run(strconv.Itoa(budget), func(t *testing.T) {
			script := "#!params={\"max_output_tokens\":" + strconv.Itoa(budget) + "}\nhcat input\nprintf 'command error\\n' >&2\nexit 7"
			stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil, script, nil,
				newShellWorkerTestInvocation(directory))
			if status != 7 {
				t.Fatalf("status=%d stderr=%q", status, stderr)
			}
			retainedShellTestOutput(t, stderr)
			native := string(mustMarshalJSON(map[string]any{
				"chunk_id": "012345", "wall_time_seconds": 1.23, "exit_code": status,
				"original_token_count": 30000, "output": stdout + stderr,
				"retained": true, "script_ref": "@shell/call_012345678901234567890123",
			}))
			for _, framed := range []string{native, string(mustMarshalJSON(native))} {
				selected, err := toolplugin.FormatOutput(t.Context(), manifest.NodeExecutable,
					runtimeRoot, []string{strconv.Itoa(budget), "head", framed, ""})
				if err != nil {
					t.Fatal(err)
				}
				if selected.ExitCode != 0 || selected.Stdout != framed {
					t.Fatalf("framed output exceeds %d tokens: %d bytes", budget, len(framed))
				}
			}
		})
	}
}
