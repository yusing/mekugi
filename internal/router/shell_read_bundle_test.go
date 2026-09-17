package router

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/router/toolplugin"
)

func TestReadBundleValidation(t *testing.T) {
	for _, args := range [][]string{nil, {"--max-tokens", "0", "a"}, {"a", "--"}, {"a", "1:2", "extra"}} {
		if _, _, err := parseReadBundle(args); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
	specs, budget, err := parseReadBundle([]string{"--max-tokens", "2000", "a b", "1:20", "--", "other"})
	if err != nil || budget != 2000 || len(specs) != 2 || specs[0].path != "a b" || specs[0].span != "1:20" {
		t.Fatalf("%+v %d %v", specs, budget, err)
	}
}

func TestReadBundleRetainsPerFileOmissions(t *testing.T) {
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	for _, name := range []string{"first", "second"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(strings.Repeat(name+" row\n", 200)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil,
		"#!params={\"max_output_tokens\":10000}\nhcat --batch --max-tokens 2000 first -- second", nil,
		newShellWorkerTestInvocation(directory))
	if status != 1 || stderr != "" || !strings.Contains(stdout, `path="first" shown=1:`) || !strings.Contains(stdout, `path="second" shown=1:`) {
		t.Fatalf("status=%d out=%s err=%s", status, stdout, stderr)
	}
	refs := regexp.MustCompile(`next_call="(hread [a-z]+[0-9]*)"`).FindAllStringSubmatch(stdout, -1)
	if len(refs) != 2 {
		t.Fatalf("missing per-file receipts: %s", stdout)
	}
	for i, name := range []string{"first", "second"} {
		if err := os.Remove(filepath.Join(directory, name)); err != nil {
			t.Fatal(err)
		}
		out, _, _ := runShellWorkerTest(t, registry, "bash", nil, refs[i][1]+" --max-tokens 10000", nil, newShellWorkerTestInvocation(directory))
		if !strings.Contains(out, name+" row") || !strings.Contains(out, "200:") {
			t.Fatalf("snapshot not recoverable: %s", out)
		}
	}
}

func TestReadBundleEmptyFailureAndPipeline(t *testing.T) {
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "empty"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	stdout, _, status := runShellWorkerTest(t, registry, "sh", nil,
		"hcat --batch empty -- missing", nil, newShellWorkerTestInvocation(directory))
	if status != 1 || !strings.Contains(stdout, `path="empty" shown=none omitted=none status=complete`) || !strings.Contains(stdout, "status=failed") {
		t.Fatalf("status=%d out=%s", status, stdout)
	}
	stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil,
		"hcat --batch empty | wc -l", nil, newShellWorkerTestInvocation(directory))
	if status != 0 || strings.TrimSpace(stdout) != "1" || stderr != "" {
		t.Fatalf("%d %q %q", status, stdout, stderr)
	}
}

func TestReadBundleBudgetAndMissingFileFailure(t *testing.T) {
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "a"), []byte(strings.Repeat("a row\n", 300)), 0600); err != nil {
		t.Fatal(err)
	}
	out, diagnostic, status := runShellWorkerTest(t, registry, "bash", nil,
		"#!params={\"max_output_tokens\":15000}\nhcat --batch --max-tokens 1037 a -- b", nil, newShellWorkerTestInvocation(directory))
	if status != 1 || out != "" || !strings.Contains(diagnostic, "budget cannot fit") {
		t.Fatalf("%d %q %q", status, out, diagnostic)
	}
	out, diagnostic, status = runShellWorkerTest(t, registry, "bash", nil,
		"#!params={\"max_output_tokens\":15000}\nhcat --batch --max-tokens 2500 a -- missing -- a", nil, newShellWorkerTestInvocation(directory))
	if status != 1 || diagnostic != "" || !strings.Contains(out, `path="missing" shown=none omitted=none status=failed`) ||
		!strings.Contains(out, "3 path=\"a\" shown=1:") {
		t.Fatalf("%d %q %q", status, out, diagnostic)
	}
	manifest, err := readToolWorkerManifest(filepath.Join(registry.SnapshotDir, toolPluginManifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	selected, err := toolplugin.FormatOutput(t.Context(), manifest.NodeExecutable,
		filepath.Join(registry.SnapshotDir, manifest.RuntimeRoot), []string{"2500", "head", out, ""})
	if err != nil || selected.ExitCode != 0 || selected.Stdout != out {
		t.Fatalf("bundle exceeds budget: %v", err)
	}
}

func TestHcatBatchPreservesSingleFileMode(t *testing.T) {
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	for _, name := range []string{"first", "--batch", "space name"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("first row\nsecond row\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, command := range []string{"hcat first 2:2", "hcat -- --batch 2:2"} {
		out, diagnostic, status := runShellWorkerTest(t, registry, "bash", nil,
			command, nil, newShellWorkerTestInvocation(directory))
		if status != 0 || diagnostic != "" || !strings.Contains(out, "second row") ||
			strings.Contains(out, "first row") || strings.Contains(out, "path=") {
			t.Fatalf("%s: %d %q %q", command, status, out, diagnostic)
		}
	}
	out, diagnostic, status := runShellWorkerTest(t, registry, "bash", nil,
		"hcat --batch first 1:1 -- 'space name' 2:2", nil, newShellWorkerTestInvocation(directory))
	if status != 0 || diagnostic != "" || !strings.Contains(out, `path="first" shown=1:1`) ||
		!strings.Contains(out, `path="space name" shown=2:2`) {
		t.Fatalf("%d %q %q", status, out, diagnostic)
	}
}

func TestReadBundleFitsOuterDisplay(t *testing.T) {
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	for _, name := range []string{"first", "second"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(strings.Repeat(name+" row \"escaped\" \t界\n", 200)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, prefix := range []string{"", "printf 'preceding output\\n'; ", "printf 'preceding error\\n' >&2; "} {
		out, diagnostic, status := runShellWorkerTest(t, registry, "bash", nil,
			"#!params={\"max_output_tokens\":2000}\n"+prefix+"hcat --batch --max-tokens 2000 first -- second",
			nil, newShellWorkerTestInvocation(directory))
		if status != 1 || strings.Contains(diagnostic, "hread ") {
			t.Fatalf("status=%d out=%s err=%s", status, out, diagnostic)
		}
		receipts := regexp.MustCompile(`shown=(1:\d+).*next_call="(hread [a-z]+[0-9]*)"`).FindAllStringSubmatch(out, -1)
		bodies := strings.Split(out, "--- file ")
		if len(receipts) != 2 || len(bodies) != 3 {
			t.Fatalf("missing previews or receipts: %s", out)
		}
		for i, receipt := range receipts {
			if got := bundleRowSpan(bodies[i+1]); got != receipt[1] {
				t.Fatalf("advertised %s but delivered %s: %s", receipt[1], got, out)
			}
			recovered, _, _ := runShellWorkerTest(t, registry, "bash", nil,
				receipt[2]+" --max-tokens 10000", nil, newShellWorkerTestInvocation(directory))
			if !strings.Contains(recovered, "200:") {
				t.Fatalf("missing retained suffix: %s", recovered)
			}
		}
	}
	for _, route := range []string{"> saved", "| cat > saved"} {
		var saved []string
		for _, limit := range []string{"2000", "10000"} {
			_, diagnostic, _ := runShellWorkerTest(t, registry, "bash", nil,
				"#!params={\"max_output_tokens\":"+limit+"}\nhcat --batch --max-tokens 2000 first -- second "+route,
				nil, newShellWorkerTestInvocation(directory))
			if diagnostic != "" {
				t.Fatal(diagnostic)
			}
			data, err := os.ReadFile(filepath.Join(directory, "saved"))
			if err != nil {
				t.Fatal(err)
			}
			saved = append(saved, regexp.MustCompile(`hread [a-z]+[0-9]*`).ReplaceAllString(string(data), "hread REFERENCE"))
		}
		if saved[0] != saved[1] {
			t.Fatalf("%s changed redirected bytes with display budget", route)
		}
	}
}

func TestReadBundleDisplayTrimmingPreservesSuccess(t *testing.T) {
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "small"), []byte(strings.Repeat("small row\n", 50)), 0600); err != nil {
		t.Fatal(err)
	}
	out, diagnostic, status := runShellWorkerTest(t, registry, "bash", nil,
		"#!params={\"max_output_tokens\":1400}\nset -e; hcat --batch --max-tokens 4000 small && printf success > marker",
		nil, newShellWorkerTestInvocation(directory))
	if status != 0 || diagnostic != "" || !strings.Contains(out, "status=incomplete") {
		t.Fatalf("status=%d out=%s err=%s", status, out, diagnostic)
	}
	if data, err := os.ReadFile(filepath.Join(directory, "marker")); err != nil || string(data) != "success" {
		t.Fatalf("display budget changed shell control flow: %q %v", data, err)
	}
}
