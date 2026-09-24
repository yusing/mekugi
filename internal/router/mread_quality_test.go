package router

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/router/toolplugin"
	"github.com/yusing/mekugi/internal/tokenizer"
)

func mreadQualitySession(t *testing.T, registry *toolRegistry, thread string) (*mekugiReplayStore, context.Context) {
	t.Helper()
	manifest, err := readToolWorkerManifest(filepath.Join(registry.SnapshotDir, toolPluginManifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	store, err := openMekugiReplayStore(manifest.ReplayDirectory)
	if err != nil {
		t.Fatal(err)
	}
	ctx, release, err := store.beginSession(t.Context(), thread, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	return store, bindTestHandleScope(t, store, ctx, "", "")
}

func mreadQualityBranch(t *testing.T, store *mekugiReplayStore, thread, parent, fork string) context.Context {
	t.Helper()
	ctx, release, err := store.beginSession(t.Context(), thread, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	return bindTestHandleScope(t, store, ctx, parent, fork)
}

func mreadQualityInvocation(t *testing.T, thread string) shellWorkerTestInvocation {
	t.Helper()
	return newShellWorkerTestInvocation(t.TempDir(), "CODEX_THREAD_ID="+thread, "BASH_ENV=")
}

func TestMReadMultipleHandlesShareBudgetAndReturnOneCombinedContinuation(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	thread := "mread-quality-" + filepath.Base(t.TempDir())
	store, ctx := mreadQualitySession(t, registry, thread)
	first := strings.Repeat("alpha ", 70) + "\n" + strings.Repeat("oversized ", 100) + "\n"
	second := strings.Repeat("beta row\n", 100)
	firstID, err := store.putTypedOutput(ctx, toolplugin.OmittedOutput{Stdout: first, StdoutKind: "rows"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := store.putTypedOutput(ctx, toolplugin.OmittedOutput{Stdout: second, StdoutKind: "rows"}, 0)
	if err != nil {
		t.Fatal(err)
	}

	const budget = 120
	stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil,
		fmt.Sprintf("mread %s %s --max-tokens %d", firstID, secondID, budget), nil,
		mreadQualityInvocation(t, thread))
	if status != 1 || strings.Count(stderr, "next_call:") != 1 ||
		!strings.Contains(stdout, "--- "+firstID+" ---\n") || !strings.Contains(stdout, "--- "+secondID+" ---\n") {
		t.Fatalf("shared read: status=%d stdout=%q stderr=%q", status, stdout, stderr)
	}
	codec, err := tokenizer.New()
	if err != nil {
		t.Fatal(err)
	}
	if count, err := codec.Count(stdout); err != nil || count > budget {
		t.Fatalf("shared output uses %d tokens, budget=%d err=%v", count, budget, err)
	}
	match := regexp.MustCompile(`(?m)^read: incomplete; next_call: mread ([a-z]+[0-9]*) ([a-z]+[0-9]*) --max-tokens 120$`).FindStringSubmatch(strings.TrimSuffix(stderr, "\n"))
	if len(match) != 3 {
		t.Fatalf("continuation was not one combined call for both handles: %q", stderr)
	}
}

func TestMReadSourcePagesReportTheirAbsoluteRows(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	thread := "mread-quality-" + filepath.Base(t.TempDir())
	store, ctx := mreadQualitySession(t, registry, thread)
	var source strings.Builder
	for row := 42; row < 342; row++ {
		fmt.Fprintf(&source, "row-%03d alpha beta gamma\n", row)
	}
	id, err := store.putTypedOutput(ctx, toolplugin.OmittedOutput{
		Stdout: source.String(), StdoutKind: "rows", SourceRow: 42,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}

	stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil,
		"mread "+id+" --max-tokens 48", nil, mreadQualityInvocation(t, thread))
	if status != 1 || stderr == "" {
		t.Fatalf("partial source page: status=%d stdout=%q stderr=%q", status, stdout, stderr)
	}
	match := regexp.MustCompile(`(?m)^\[rows ([0-9]+):([0-9]+)\]$`).FindStringSubmatch(stdout)
	if len(match) != 3 {
		t.Fatalf("missing source row range: %q", stdout)
	}
	var start, end int
	if _, err := fmt.Sscanf(match[0], "[rows %d:%d]", &start, &end); err != nil {
		t.Fatal(err)
	}
	page, ok := strings.CutPrefix(stdout, match[0]+"\n")
	if !ok || !strings.HasPrefix(page, fmt.Sprintf("row-%03d ", start)) ||
		end-start+1 != strings.Count(page, "\n") {
		t.Fatalf("source row range does not identify delivered rows: %q", stdout)
	}
}

func TestMReadPreservesCommandStderrThatResemblesALimitNotice(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	thread := "mread-quality-" + filepath.Base(t.TempDir())
	store, ctx := mreadQualitySession(t, registry, thread)
	const diagnostic = "mcat: output incomplete: compiler emitted this diagnostic verbatim\n"
	id, err := store.putTypedOutput(ctx, toolplugin.OmittedOutput{
		Stdout: "saved row\n", StdoutKind: "rows", Stderr: diagnostic,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil,
		"mread "+id, nil, mreadQualityInvocation(t, thread))
	want := "[stdout rows]\nsaved row\n\n[/stdout]\n[stderr bytes]\n" + diagnostic + "\n[/stderr]\n"
	if status != 0 || stdout != want || stderr != "" {
		t.Fatalf("mread dropped or altered retained command stderr: status=%d stdout=%q stderr=%q want=%q", status, stdout, stderr, want)
	}
}

func TestMReadTinyBudgetAndMalformedOperandsHaveExactMessages(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	thread := "mread-quality-" + filepath.Base(t.TempDir())
	store, ctx := mreadQualitySession(t, registry, thread)
	const row = "one complete row\n"
	id, err := store.putTypedOutput(ctx, toolplugin.OmittedOutput{Stdout: row, StdoutKind: "rows"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	codec, err := tokenizer.New()
	if err != nil {
		t.Fatal(err)
	}
	needed, err := codec.Count(row)
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil,
		"mread "+id+" --max-tokens 1", nil, mreadQualityInvocation(t, thread))
	want := fmt.Sprintf("mread: next row needs ~%d tokens; retry: mread %s --max-tokens %d\n", needed, id, needed)
	if status != 1 || stdout != "" || stderr != want {
		t.Fatalf("tiny-budget diagnostic: status=%d stdout=%q stderr=%q want=%q", status, stdout, stderr, want)
	}

	const malformed = "mread: REF must be a returned handle such as amber; paths and ranges belong to mcat\n"
	for _, command := range []string{"mread big.go", "mread amber 1:20", "mread nosuch"} {
		stdout, stderr, status = runShellWorkerTest(t, registry, "bash", nil,
			command, nil, mreadQualityInvocation(t, thread))
		if status != 1 || stdout != "" || stderr != malformed {
			t.Errorf("%s: status=%d stdout=%q stderr=%q want=%q", command, status, stdout, stderr, malformed)
		}
	}
}

func TestMReadFreshStoreForkAndSideContinuityStayInTheirHandleScopes(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	root := "mread-root-" + filepath.Base(t.TempDir())
	store, rootCtx := mreadQualitySession(t, registry, root)
	makeRows := func(prefix string, mediumWords int) string {
		medium := "alpha"
		if prefix == "root-b" {
			medium = "beta"
		}
		return prefix + "-first\n" + strings.Repeat(medium+" ", mediumWords) + "\n" +
			strings.Repeat("oversized ", 200) + "\n" + strings.Repeat("later\n", 100)
	}
	firstID, err := store.putTypedOutput(rootCtx, toolplugin.OmittedOutput{
		Stdout: makeRows("root-a", 70), StdoutKind: "rows",
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := store.putTypedOutput(rootCtx, toolplugin.OmittedOutput{
		Stdout: makeRows("root-b", 20), StdoutKind: "rows",
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	initialCursor := func(id string) string {
		t.Helper()
		page, diagnostic, status := runShellWorkerTest(t, registry, "bash", nil,
			"mread "+id+" --max-tokens 10", nil, mreadQualityInvocation(t, root))
		if status != 1 || page == "" {
			t.Fatalf("root continuation setup for %s: status=%d stdout=%q stderr=%q", id, status, page, diagnostic)
		}
		match := regexp.MustCompile(`(?m)^read: incomplete; next_call: mread ([a-z]+[0-9]*) --max-tokens 10$`).FindStringSubmatch(strings.TrimSuffix(diagnostic, "\n"))
		if len(match) != 2 {
			t.Fatalf("missing root continuation for %s: %q", id, diagnostic)
		}
		return match[1]
	}
	firstCursor, secondCursor := initialCursor(firstID), initialCursor(secondID)
	assertPairRead := func(thread, first, second string) {
		t.Helper()
		page, diagnostic, status := runShellWorkerTest(t, registry, "bash", nil,
			fmt.Sprintf("mread %s %s --max-tokens 120", first, second), nil, mreadQualityInvocation(t, thread))
		if status != 1 || !strings.Contains(page, "--- "+first+" ---\n") || !strings.Contains(page, "--- "+second+" ---\n") {
			t.Fatalf("two-handle continuation in %s: status=%d stdout=%q stderr=%q", thread, status, page, diagnostic)
		}
		match := regexp.MustCompile(`(?m)^read: incomplete; next_call: mread ([a-z]+[0-9]*) ([a-z]+[0-9]*) --max-tokens 120$`).FindStringSubmatch(strings.TrimSuffix(diagnostic, "\n"))
		if len(match) != 3 {
			t.Fatalf("%s did not retain both continuations in one call: %q", thread, diagnostic)
		}
	}

	// Every worker invocation is a fresh process. Reopen the durable store before
	// establishing fork and side identities as well.
	assertPairRead(root, firstCursor, secondCursor)
	freshStore, err := openMekugiReplayStore(store.directory)
	if err != nil {
		t.Fatal(err)
	}
	fork := "mread-fork-" + filepath.Base(t.TempDir())
	forkCtx := mreadQualityBranch(t, freshStore, fork, "", root)
	assertPairRead(fork, firstCursor, secondCursor)
	forkID, err := freshStore.putTypedOutput(forkCtx, toolplugin.OmittedOutput{
		Stdout: "fork-owned row\n", StdoutKind: "rows",
	}, 0)
	if err != nil {
		t.Fatal(err)
	}

	side := "mread-side-" + filepath.Base(t.TempDir())
	_ = mreadQualityBranch(t, freshStore, side, root, "")
	assertPairRead(side, firstCursor, secondCursor)
	borrowed, borrowErr, borrowStatus := runShellWorkerTest(t, registry, "bash", nil,
		fmt.Sprintf("mread %s %s --max-tokens 120", firstCursor, forkID), nil, mreadQualityInvocation(t, side))
	if borrowStatus != 1 || borrowed != "" || !strings.Contains(borrowErr, "unavailable in this session") ||
		strings.Contains(borrowErr, "root-a-") || strings.Contains(borrowErr, "alpha") ||
		strings.Contains(borrowErr, "beta") || strings.Contains(borrowErr, "oversized") || strings.Contains(borrowErr, "fork-owned") {
		t.Fatalf("mixed authorized/foreign read exposed partial data: status=%d stdout=%q stderr=%q", borrowStatus, borrowed, borrowErr)
	}
}
