package router

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/tokenizer"
)

func TestReadBundleValidation(t *testing.T) {
	for _, args := range [][]string{nil, {"--max-tokens", "0", "a"}, {"a", "2:1"}, {"a", "01:2"}, {"a", "1:9007199254740992"}, {"a", ""}, {"a", "--max-tokens", "2000", "--max-tokens", "2000"}, {"-n", "1", "a", "b"}, {"-n", "0", "a"}, {"-n", "1", "-n", "2", "a"}, {"--tail", "a"}, {"--tail", "--tail", "-n", "1", "a"}, {"--tail", "a", "b"}, {"--max-tokens"}, {"-n"}, slices.Repeat([]string{"a"}, 17)} {
		if _, _, err := parseReadBundle(args); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
	specs, budget, err := parseReadBundle([]string{"a b", "1:20", "--max-tokens", "2000", "other", "third", "200:300"})
	if err != nil || budget != 2000 || len(specs) != 3 || specs[0].path != "a b" || specs[0].span != "1:20" || specs[1].span != "" || specs[2].span != "200:300" {
		t.Fatalf("%+v %d %v", specs, budget, err)
	}
}

func TestReadBundleDefaultMultiFileBudget(t *testing.T) {
	specs, budget, err := parseReadBundle([]string{"first.go", "second.go"})
	if err != nil || budget != 6000 || len(specs) != 2 {
		t.Fatalf("parse default multi-file bundle: specs=%+v budget=%d err=%v", specs, budget, err)
	}
}

func TestReadBundleLimitReportsFullReadCount(t *testing.T) {
	for _, count := range []int{16, 17, 21, 33} {
		args := append([]string{"--max-tokens=2000"}, slices.Repeat([]string{"source"}, count)...)
		specs, budget, err := parseReadBundle(args)
		if count == 16 {
			if err != nil || len(specs) != count || budget != 2000 {
				t.Fatalf("limit boundary: specs=%+v budget=%d err=%v", specs, budget, err)
			}
			continue
		}
		want := fmt.Sprintf("received %d reads after argument expansion, exceeding the 16-read limit; split into %d mcat calls of at most 16 reads each; repeat the path when splitting its ranges", count, (count+15)/16)
		if err == nil || err.Error() != want {
			t.Fatalf("%d paths: error=%v, want %q", count, err, want)
		}
	}
	args := []string{"first", "1:1", "2:2"}
	args = append(args, slices.Repeat([]string{"other"}, 19)...)
	_, _, err := parseReadBundle(args)
	if err == nil || !strings.HasPrefix(err.Error(), "received 21 reads after argument expansion,") {
		t.Fatalf("mixed paths and ranges: error=%v", err)
	}
}

func TestMCatExpandedGlobLimitAndBatchedRecovery(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	for i := range 21 {
		name := fmt.Sprintf("snapshot-%02d.txt", i)
		if err := os.WriteFile(filepath.Join(directory, name), fmt.Appendf(nil, "payload %d\n", i), 0600); err != nil {
			t.Fatal(err)
		}
	}
	out, diagnostic, status := runShellWorkerTest(t, registry, "bash", nil,
		"mcat *.txt", nil, newShellWorkerTestInvocation(directory))
	want := "mcat: received 21 reads after argument expansion, exceeding the 16-read limit; split into 2 mcat calls of at most 16 reads each; repeat the path when splitting its ranges\n"
	if status != 1 || out != "" || diagnostic != want {
		t.Fatalf("expanded glob: status=%d stdout=%q stderr=%q", status, out, diagnostic)
	}
	out, diagnostic, status = runShellWorkerTest(t, registry, "bash", nil,
		`files=( *.txt ); mcat "${files[@]:0:16}" && mcat "${files[@]:16}"`, nil, newShellWorkerTestInvocation(directory))
	if status != 0 || diagnostic != "" || strings.Count(out, "payload ") != 21 {
		t.Fatalf("batched recovery: status=%d stdout=%q stderr=%q", status, out, diagnostic)
	}
}

func TestMCatMissingPathDiagnostics(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "good"), []byte("available source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, missing := range []string{"missing", "missing file", filepath.Join(directory, "missing file"), "missing\u2028file"} {
		want := "mcat: " + string(mustMarshalJSON(missing)) + ": ENOENT: no such file or directory\n"
		// JSON.stringify keeps U+2028 literal; Go's protocol encoder escapes it.
		want = strings.ReplaceAll(want, `\u2028`, "\u2028")
		for _, args := range [][]string{{missing}, {missing, "good"}} {
			out, diagnostic, status := runShellWorkerTest(t, registry, "bash", nil,
				workerCommand("mcat", args), nil, newShellWorkerTestInvocation(directory))
			if status != 1 || diagnostic != want || (len(args) == 1 && out != "") ||
				(len(args) == 2 && !strings.Contains(out, "available source\n")) {
				t.Fatalf("%q: status=%d stdout=%q stderr=%q, want stderr=%q", args, status, out, diagnostic, want)
			}
		}
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
		if err := os.WriteFile(filepath.Join(directory, name), []byte(strings.Repeat(name+" row\n", 2000)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil,
		"#!params={\"max_output_tokens\":10000}\nmcat --max-tokens 2000 first second", nil,
		newShellWorkerTestInvocation(directory))
	if status != 1 || stderr != "" ||
		!strings.Contains(stdout, `path="first" shown=1:`) || !strings.Contains(stdout, `path="second" shown=1:`) {
		t.Fatalf("status=%d out=%s err=%s", status, stdout, stderr)
	}
	_, continuation, found := strings.Cut(stdout, "next_call: mread ")
	if !found {
		t.Fatalf("missing combined continuation: %s", stdout)
	}
	refs := strings.Fields(strings.Split(continuation, " --max-tokens")[0])
	if len(refs) != 2 {
		t.Fatalf("missing per-file receipts: %s", stdout)
	}
	for i, name := range []string{"first", "second"} {
		if err := os.Remove(filepath.Join(directory, name)); err != nil {
			t.Fatal(err)
		}
		out, _, _ := runShellWorkerTest(t, registry, "bash", nil, "mread "+refs[i]+" --max-tokens 10000", nil, newShellWorkerTestInvocation(directory))
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
		"#!params={\"max_output_tokens\":15000}\nmcat --max-tokens 100 a b", nil, newShellWorkerTestInvocation(directory))
	if status != 1 || out != "" || !strings.Contains(diagnostic, "budget cannot fit") {
		t.Fatalf("%d %q %q", status, out, diagnostic)
	}
	out, diagnostic, status = runShellWorkerTest(t, registry, "bash", nil,
		"#!params={\"max_output_tokens\":15000}\nmcat --max-tokens 2500 a missing a", nil, newShellWorkerTestInvocation(directory))
	if status != 1 || !strings.Contains(diagnostic, `mcat: "missing": `) || strings.Count(diagnostic, `mcat: "missing": `) != 1 ||
		!strings.Contains(out, "2 path=\"missing\" shown=none omitted=none status=failed\n") ||
		!strings.Contains(out, "3 path=\"a\" shown=1:") {
		t.Fatalf("%d %q %q", status, out, diagnostic)
	}
	codec, err := tokenizer.New()
	if err != nil {
		t.Fatal(err)
	}
	if count, err := codec.Count(out); err != nil || count > 2500 {
		t.Fatalf("bundle uses %d tokens, budget 2500: %v", count, err)
	}
}

func TestReadBundleOmitsCompleteRowsAndShowsDiagnostics(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	for _, name := range []string{"first", "second"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(name+" one\n"+name+" two\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	out, diagnostic, status := runShellWorkerTest(t, registry, "bash", nil,
		"mcat first second 2:2", nil, newShellWorkerTestInvocation(directory))
	want := "--- file 1 path=\"first\" shown=1:2 ---\nfirst one\nfirst two\n\n--- file 2 path=\"second\" shown=2:2 ---\nsecond two\n"
	if status != 0 || diagnostic != "" || out != want {
		t.Fatalf("complete bundle: %d %q %q", status, out, diagnostic)
	}
	out, diagnostic, status = runShellWorkerTest(t, registry, "bash", nil,
		"mcat first 1:9 second 2:9", nil, newShellWorkerTestInvocation(directory))
	if status != 0 || diagnostic != "" || out != want {
		t.Fatalf("clamped-end bundle: %d %q %q", status, out, diagnostic)
	}
	out, diagnostic, status = runShellWorkerTest(t, registry, "bash", nil,
		"mcat first missing second 2:3", nil, newShellWorkerTestInvocation(directory))
	want = "2 path=\"missing\" shown=none omitted=none status=failed\n" +
		"\n--- file 1 path=\"first\" shown=1:2 ---\nfirst one\nfirst two\n\n--- file 3 path=\"second\" shown=2:2 ---\nsecond two\n"
	if status != 1 || out != want || strings.Contains(out, "next_call") ||
		!regexp.MustCompile(`(?m)^mcat: "missing": ENOENT`).MatchString(diagnostic) ||
		strings.Contains(diagnostic, "past EOF") {
		t.Fatalf("failed bundle: %d %q %q", status, out, diagnostic)
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
