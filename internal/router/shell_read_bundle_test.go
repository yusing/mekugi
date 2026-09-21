package router

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/router/toolplugin"
)

func TestReadBundleValidation(t *testing.T) {
	for _, args := range [][]string{nil, {"--max-tokens", "0", "a"}, {"a", "1:2", "2:3"}, {"a", "2:1"}, {"a", "01:2"}, {"a", "1:9007199254740992"}, {"a", ""}, {"a", "--max-tokens", "2000", "--max-tokens", "2000"}, {"-n", "1", "a", "b"}, {"-n", "0", "a"}, {"-n", "1", "-n", "2", "a"}, {"--tail", "a"}, {"--tail", "--tail", "-n", "1", "a"}, {"--tail", "a", "b"}, {"--max-tokens"}, {"-n"}, slices.Repeat([]string{"a"}, 17)} {
		if _, _, err := parseReadBundle(args); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
	specs, budget, err := parseReadBundle([]string{"a b", "1:20", "--max-tokens", "2000", "other", "third", "200:300"})
	if err != nil || budget != 2000 || len(specs) != 3 || specs[0].path != "a b" || specs[0].span != "1:20" || specs[1].span != "" || specs[2].span != "200:300" {
		t.Fatalf("%+v %d %v", specs, budget, err)
	}
}

func TestMCatMixedPathsAndRanges(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	for _, name := range []string{"first", "second", "third", "200:300", "--tail"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(name+" one\n"+name+" two\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, command := range []string{
		"mcat first 1:1 second third 2:2",
		"mcat first 1:1 ./200:300 -- --tail 2:2",
	} {
		out, diagnostic, status := runShellWorkerTest(t, registry, "bash", nil, command, nil, newShellWorkerTestInvocation(directory))
		if status != 0 || diagnostic != "" || strings.Contains(out, "first two") ||
			!strings.Contains(out, "shown=1:1") || !strings.Contains(out, "shown=1:2") || !strings.Contains(out, "shown=2:2") {
			t.Fatalf("%s: %d %q %q", command, status, out, diagnostic)
		}
	}
}

func TestReadBundleRetainsPerFileOmissions(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	for _, name := range []string{"first", "second"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(strings.Repeat(name+" row\n", 200)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil,
		"#!params={\"max_output_tokens\":10000}\nmcat --max-tokens 2000 first second", nil,
		newShellWorkerTestInvocation(directory))
	if status != 1 || stderr != "" || !strings.Contains(stdout, `path="first" shown=1:`) || !strings.Contains(stdout, `path="second" shown=1:`) {
		t.Fatalf("status=%d out=%s err=%s", status, stdout, stderr)
	}
	refs := regexp.MustCompile(`next_call="(mread [a-z]+[0-9]*)"`).FindAllStringSubmatch(stdout, -1)
	if len(refs) != 2 {
		t.Fatalf("missing per-file receipts: %s", stdout)
	}
	for i, name := range []string{"first", "second"} {
		if err := os.Remove(filepath.Join(directory, name)); err != nil {
			t.Fatal(err)
		}
		out, _, _ := runShellWorkerTest(t, registry, "bash", nil, refs[i][1]+" --max-tokens 10000", nil, newShellWorkerTestInvocation(directory))
		if !strings.Contains(out, name+" row") {
			t.Fatalf("snapshot not recoverable: %s", out)
		}
	}
}

func TestReadBundleEmptyFailureAndPipeline(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "empty"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	stdout, _, status := runShellWorkerTest(t, registry, "sh", nil,
		"mcat empty missing", nil, newShellWorkerTestInvocation(directory))
	if status != 1 || !strings.Contains(stdout, `path="empty" shown=none omitted=none status=complete`) || !strings.Contains(stdout, "status=failed") {
		t.Fatalf("status=%d out=%s", status, stdout)
	}
	stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil,
		"mcat empty empty | wc -l", nil, newShellWorkerTestInvocation(directory))
	if status != 0 || strings.TrimSpace(stdout) != "2" || stderr != "" {
		t.Fatalf("%d %q %q", status, stdout, stderr)
	}
}

func TestReadBundleBudgetAndMissingFileFailure(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "a"), []byte(strings.Repeat("a row\n", 300)), 0600); err != nil {
		t.Fatal(err)
	}
	out, diagnostic, status := runShellWorkerTest(t, registry, "bash", nil,
		"#!params={\"max_output_tokens\":15000}\nmcat --max-tokens 1037 a b", nil, newShellWorkerTestInvocation(directory))
	if status != 1 || out != "" || !strings.Contains(diagnostic, "budget cannot fit") {
		t.Fatalf("%d %q %q", status, out, diagnostic)
	}
	out, diagnostic, status = runShellWorkerTest(t, registry, "bash", nil,
		"#!params={\"max_output_tokens\":15000}\nmcat --max-tokens 2500 a missing a", nil, newShellWorkerTestInvocation(directory))
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

func TestMCatMultiplePathsPreserveSingleFileMode(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	for _, name := range []string{"first", "--batch", "-source", "space name"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("first row\nsecond row\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, command := range []string{"mcat first 2:2", "mcat -- --batch 2:2", "mcat ./-source 2:2"} {
		out, diagnostic, status := runShellWorkerTest(t, registry, "bash", nil,
			command, nil, newShellWorkerTestInvocation(directory))
		if status != 0 || diagnostic != "" || !strings.Contains(out, "second row") ||
			strings.Contains(out, "first row") || strings.Contains(out, "path=") {
			t.Fatalf("%s: %d %q %q", command, status, out, diagnostic)
		}
	}
	out, diagnostic, status := runShellWorkerTest(t, registry, "bash", nil,
		"mcat first 1:1 'space name' 2:2", nil, newShellWorkerTestInvocation(directory))
	if status != 0 || diagnostic != "" || !strings.Contains(out, `path="first" shown=1:1`) ||
		!strings.Contains(out, `path="space name" shown=2:2`) {
		t.Fatalf("%d %q %q", status, out, diagnostic)
	}
}

var outerShellReadNotice = regexp.MustCompile(`\nread: incomplete; next_call: mread [a-z]+[0-9]*\n$`)

func TestOuterShellReadNoticePreservesVisibleDiagnostics(t *testing.T) {
	t.Parallel()
	for _, visible := range []string{"", "unexpected visible diagnostic\n", "read: incomplete; next_call: mread not-a-reference\n"} {
		shown := visible + "\nread: incomplete; next_call: mread amber\n"
		retained := "unexpected retained diagnostic\n"
		got := outerShellReadNotice.ReplaceAllString(shown, "") + retained
		if got != visible+retained {
			t.Fatalf("recovery discarded diagnostic bytes: got %q, want %q", got, visible+retained)
		}
	}
}
