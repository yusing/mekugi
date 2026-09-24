package router

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/tokenizer"
)

func TestMCatDefaultBudgetFitsSixteenTinyFiles(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	paths := make([]string, 0, 16)
	for index := range 16 {
		name := fmt.Sprintf("tiny-%02d", index+1)
		paths = append(paths, name)
		if err := os.WriteFile(filepath.Join(directory, name), []byte(name+" tiny row\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil,
		"mcat "+strings.Join(paths, " "), nil, mcatBudgetTestInvocation(directory))
	if status != 0 || stderr != "" {
		t.Fatalf("16-file default bundle: status=%d stderr=%q", status, stderr)
	}
	if got := strings.Count(stdout, "--- file "); got != len(paths) {
		t.Fatalf("rendered %d file bodies, want %d: %s", got, len(paths), stdout)
	}
	for _, path := range paths {
		if !strings.Contains(stdout, path+" tiny row\n") {
			t.Errorf("default bundle omitted %s: %s", path, stdout)
		}
	}
	assertMCatOutputWithinBudget(t, 6000, stdout, stderr)
}

func TestMCatBundleRedistributesUnusedShareAndKeepsOneContinuation(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "small"), []byte("small row\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var large strings.Builder
	for index := range 1200 {
		fmt.Fprintf(&large, "large-row-%04d alpha beta gamma delta epsilon zeta eta theta iota kappa lambda\n", index+1)
	}
	if err := os.WriteFile(filepath.Join(directory, "large"), []byte(large.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	const budget = 1000
	stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil,
		"mcat --max-tokens 1000 small large", nil, mcatBudgetTestInvocation(directory))
	if status != 1 {
		t.Fatalf("large bundle status=%d, want an incomplete but recoverable result: %s", status, stdout)
	}
	if strings.Contains(stderr, "output incomplete") || strings.Contains(stderr, "next_call") {
		t.Fatalf("per-file omission diagnostics escaped the bundle: %q", stderr)
	}
	if got := strings.Count(stdout, "next_call:"); got != 1 {
		t.Fatalf("bundle has %d continuation footers, want one: %s", got, stdout)
	}
	footer := regexp.MustCompile(`(?m)^next_call: mread ([a-z]+[0-9]*) --max-tokens 1000$`).FindStringSubmatch(stdout)
	if len(footer) != 2 {
		t.Fatalf("missing combined bounded continuation footer: %s", stdout)
	}

	codec, err := tokenizer.New()
	if err != nil {
		t.Fatal(err)
	}
	reserve := 16
	for _, path := range []string{"small", "large"} {
		pathTokens, err := codec.Count(string(mustMarshalJSON(path)))
		if err != nil {
			t.Fatal(err)
		}
		reserve += 2*pathTokens + 48
	}
	equalShare := (budget - reserve) / 2
	largeBody := mcatBudgetTestBody(t, stdout, 2, "large")
	largeTokens, err := codec.Count(largeBody)
	if err != nil {
		t.Fatal(err)
	}
	if largeTokens <= equalShare {
		t.Fatalf("large file received %d body tokens, no more than its equal share %d", largeTokens, equalShare)
	}
	assertMCatOutputWithinBudget(t, budget, stdout, stderr)
}

func TestMCatLineLimitDefaultBudgetAndSourceRemovalRecovery(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	invocation := mcatBudgetTestInvocation(directory)
	const rowCount = 1000
	full := mcatBudgetTestRows(t, directory, "rows-default", rowCount)
	stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil,
		"mcat -n 1000 rows-default", nil, invocation)
	if status != 1 {
		t.Fatalf("truncated line-limited mcat status=%d, want 1", status)
	}
	ref := checkMCatLimitedOutput(t, stdout, stderr, 6000, rowCount)
	if err := os.Remove(filepath.Join(directory, "rows-default")); err != nil {
		t.Fatal(err)
	}
	if got := recoverMCatRows(t, registry, invocation, stdout, ref); got != full {
		t.Fatalf("mread recovery differs after source removal: got %d bytes, want %d; first difference %s",
			len(got), len(full), mcatBudgetTestDifference(got, full))
	}
}

func TestMCatNondefaultBudgetContinuesWithOriginalLimit(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	const rowCount = 1000
	mcatBudgetTestRows(t, directory, "rows-custom", rowCount)
	stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil,
		"mcat --max-tokens 3000 rows-custom", nil, mcatBudgetTestInvocation(directory))
	if status != 1 {
		t.Fatalf("truncated nondefault mcat status=%d, want 1", status)
	}
	ref := checkMCatLimitedOutput(t, stdout, stderr, 3000, rowCount)
	if !strings.Contains(stderr, "next_call: mread "+ref+" --max-tokens 3000") {
		t.Fatalf("next_call dropped the mcat token budget: %q", stderr)
	}
}

func TestMCatTailSelectionRecoversExactSourceAfterSourceRemoval(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	invocation := mcatBudgetTestInvocation(directory)
	const rowCount = 1000
	full := mcatBudgetTestRows(t, directory, "rows-tail", rowCount)
	stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil,
		"mcat --tail --max-tokens 3000 rows-tail", nil, invocation)
	if status != 1 {
		t.Fatalf("truncated tail mcat status=%d, want 1", status)
	}
	shownStart, shownEnd, reference := checkMCatLimitedRange(t, stdout, stderr, 3000, rowCount)
	if shownStart <= 1 || shownEnd != rowCount {
		t.Fatalf("tail output range = %d:%d, want a suffix ending at source row %d", shownStart, shownEnd, rowCount)
	}
	if err := os.Remove(filepath.Join(directory, "rows-tail")); err != nil {
		t.Fatal(err)
	}
	prefix := mreadMCatRows(t, registry, invocation, reference, 1)
	if got := prefix + stdout; got != full {
		t.Fatalf("tail plus recovered prefix differs from source: got %d bytes, want %d; first difference %s",
			len(got), len(full), mcatBudgetTestDifference(got, full))
	}
}

func TestMCatExplicitRangeRecoversExactSelectionAfterSourceRemoval(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	invocation := mcatBudgetTestInvocation(directory)
	const sourceRows = 1000
	const rangeStart = 125
	const rangeEnd = 900
	full := mcatBudgetTestRows(t, directory, "rows-range", sourceRows)
	stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil,
		"mcat --max-tokens 3000 rows-range 125:900", nil, invocation)
	if status != 1 {
		t.Fatalf("truncated explicit-range mcat status=%d, want 1", status)
	}
	shownStart, shownEnd, reference := checkMCatLimitedRange(t, stdout, stderr, 3000, rangeEnd-rangeStart+1)
	if shownStart != rangeStart || shownEnd >= rangeEnd {
		t.Fatalf("initial range output = %d:%d, want a prefix of %d:%d", shownStart, shownEnd, rangeStart, rangeEnd)
	}
	selected := strings.Split(strings.TrimSuffix(full, "\n"), "\n")[rangeStart-1 : rangeEnd]
	want := strings.Join(selected, "\n") + "\n"
	if err := os.Remove(filepath.Join(directory, "rows-range")); err != nil {
		t.Fatal(err)
	}
	remainder := mreadMCatRows(t, registry, invocation, reference, shownEnd+1)
	if got := stdout + remainder; got != want {
		t.Fatalf("range plus recovered remainder differs from selected source rows: got %d bytes, want %d; first difference %s",
			len(got), len(want), mcatBudgetTestDifference(got, want))
	}
}

func checkMCatLimitedOutput(t *testing.T, stdout, stderr string, budget, rowCount int) string {
	shownStart, _, reference := checkMCatLimitedRange(t, stdout, stderr, budget, rowCount)
	if shownStart != 1 {
		t.Fatalf("initial mcat range starts at row %d, want row 1", shownStart)
	}
	return reference
}

func checkMCatLimitedRange(t *testing.T, stdout, stderr string, budget, rowCount int) (int, int, string) {
	t.Helper()
	if !strings.HasSuffix(stderr, "\n") {
		t.Fatalf("single-file omission notice is not newline-terminated: %q", stderr)
	}
	lines := strings.Split(strings.TrimSuffix(stderr, "\n"), "\n")
	pattern := fmt.Sprintf(`^mcat: shown ([0-9]+):([0-9]+) of %d rows \(%d-token limit\); next_call: mread ([a-z]+[0-9]*)( --max-tokens ([0-9]+))?$`, rowCount, budget)
	match := regexp.MustCompile(pattern).FindStringSubmatch(lines[0])
	if len(match) != 6 {
		t.Fatalf("unexpected single-file continuation notice: %q", stderr)
	}
	if budget == 6000 && match[4] != "" || budget != 6000 && match[5] != strconv.Itoa(budget) {
		t.Fatalf("next_call has the wrong token-budget spelling: %q", lines[0])
	}
	for _, warning := range lines[1:] {
		if !regexp.MustCompile(`^mcat: rows [0-9]+:[0-9]+ past EOF \([0-9]+ rows\)$`).MatchString(warning) {
			t.Fatalf("unexpected line after single-file continuation notice: %q", warning)
		}
	}
	shownStart, startErr := strconv.Atoi(match[1])
	shownEnd, endErr := strconv.Atoi(match[2])
	shownRows := strings.Count(stdout, "\n")
	if startErr != nil || endErr != nil || shownStart < 1 || shownEnd-shownStart+1 != shownRows || shownRows == 0 || !strings.HasSuffix(stdout, "\n") {
		t.Fatalf("notice range %d:%d does not match %d complete output rows (errors=%v, %v)", shownStart, shownEnd, shownRows, startErr, endErr)
	}
	if !strings.HasPrefix(stdout, fmt.Sprintf("row-%04d ", shownStart)) || !strings.Contains(stdout, fmt.Sprintf("row-%04d ", shownEnd)) {
		t.Fatal("initial mcat page does not match its reported source-row range")
	}
	assertMCatOutputWithinBudget(t, budget, stdout, "")
	return shownStart, shownEnd, match[3]
}

func recoverMCatRows(t *testing.T, registry *toolRegistry, invocation shellWorkerTestInvocation, firstPage, reference string) string {
	t.Helper()
	return firstPage + mreadMCatRows(t, registry, invocation, reference, strings.Count(firstPage, "\n")+1)
}

func mreadMCatRows(t *testing.T, registry *toolRegistry, invocation shellWorkerTestInvocation, reference string, nextRow int) string {
	t.Helper()
	var recovered strings.Builder
	rowLabel := regexp.MustCompile(`^\[rows ([0-9]+):([0-9]+)\]$`)
	for pageIndex := range 100 {
		page, stderr, status := runShellWorkerTest(t, registry, "bash", nil,
			"mread "+reference+" --max-tokens 15500", nil, invocation)
		if page == "" {
			t.Fatalf("mread returned no source rows: status=%d stderr=%q", status, stderr)
		}
		label, rows, found := strings.Cut(page, "\n")
		match := rowLabel.FindStringSubmatch(label)
		if !found || len(match) != 3 {
			t.Fatalf("mread source page omitted its row-range label: %q", page[:min(len(page), 160)])
		}
		start, startErr := strconv.Atoi(match[1])
		end, endErr := strconv.Atoi(match[2])
		rowCount := strings.Count(rows, "\n")
		if startErr != nil || endErr != nil || start != nextRow || rowCount == 0 || !strings.HasSuffix(rows, "\n") ||
			end != start+rowCount-1 || !strings.HasPrefix(rows, fmt.Sprintf("row-%04d ", start)) ||
			!strings.Contains(rows, fmt.Sprintf("row-%04d ", end)) {
			t.Fatalf("mread row range %q is not continuous from row %d: %q", label, nextRow, page[:min(len(page), 160)])
		}
		recovered.WriteString(rows)
		nextRow = end + 1
		if status == 0 {
			if stderr != "" {
				t.Fatalf("completed mread returned diagnostics: %q", stderr)
			}
			return recovered.String()
		}
		nextMatch := regexp.MustCompile(`read: incomplete; next_call: mread ([a-z]+[0-9]*)`).FindStringSubmatch(stderr)
		if len(nextMatch) != 2 {
			t.Fatalf("continued mread omitted its next handle: status=%d stderr=%q", status, stderr)
		}
		reference = nextMatch[1]
		if pageIndex == 99 {
			t.Fatal("mread did not finish recovery within 100 pages")
		}
	}
	t.Fatal("mread did not finish recovery within 100 pages")
	return ""
}

func mcatBudgetTestRows(t *testing.T, directory, path string, count int) string {
	t.Helper()
	var rows strings.Builder
	for index := range count {
		fmt.Fprintf(&rows, "row-%04d alpha beta gamma delta epsilon\n", index+1)
	}
	result := rows.String()
	if err := os.WriteFile(filepath.Join(directory, path), []byte(result), 0o600); err != nil {
		t.Fatal(err)
	}
	return result
}

func mcatBudgetTestPrefix(value string) string {
	return value[:min(len(value), 120)]
}

func mcatBudgetTestDifference(got, want string) string {
	limit := min(len(got), len(want))
	for index := range limit {
		if got[index] != want[index] {
			return fmt.Sprintf("at byte %d: got %q, want %q", index,
				got[max(0, index-24):min(len(got), index+64)],
				want[max(0, index-24):min(len(want), index+64)])
		}
	}
	return fmt.Sprintf("common prefix %d bytes; got tail %q", limit, mcatBudgetTestPrefix(got[limit:]))
}

func mcatBudgetTestBody(t *testing.T, output string, fileIndex int, path string) string {
	t.Helper()
	marker := fmt.Sprintf("--- file %d path=%q shown=", fileIndex, path)
	_, remaining, found := strings.Cut(output, marker)
	if !found {
		t.Fatalf("missing output body for %q: %s", path, output)
	}
	_, body, found := strings.Cut(remaining, "\n")
	if !found {
		t.Fatalf("incomplete output header for %q: %s", path, remaining)
	}
	if footer, _, found := strings.Cut(body, "\nnext_call:"); found {
		body = footer
	}
	if next, _, found := strings.Cut(body, "\n--- file "); found {
		body = next
	}
	return body
}

func assertMCatOutputWithinBudget(t *testing.T, budget int, stdout, stderr string) {
	t.Helper()
	codec, err := tokenizer.New()
	if err != nil {
		t.Fatal(err)
	}
	count, err := codec.Count(stdout + stderr)
	if err != nil || count > budget {
		t.Fatalf("mcat output uses %d tokens, budget=%d err=%v", count, budget, err)
	}
}

func mcatBudgetTestInvocation(directory string) shellWorkerTestInvocation {
	invocation := newShellWorkerTestInvocation(directory)
	invocation.environment = slices.DeleteFunc(invocation.environment, func(value string) bool {
		return strings.HasPrefix(value, "BASH_ENV=")
	})
	return invocation
}
