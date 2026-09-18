package mekugi

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestMekugi2IndentationOnlyReplacementRejectsWithoutSuggestion(t *testing.T) {
	rootPath := t.TempDir()
	writeTestFile(t, rootPath, "script.sh", "header\n\texit \"$status\"\n", 0o644)
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	command := "type " + row(2, "\texit \"$status\"") + ` "exit \"$status\"\n"`
	edits := []FileEdit{{Path: "script.sh", Script: command}}
	result, err := translateForHostForTest(t.Context(), Workspace{Root: root}, edits, t.TempDir())
	if err == nil {
		t.Fatal("indentation-only replacement unexpectedly succeeded")
	}
	wantRejections := []HostRejection{{
		Command: 1, SourceLine: 1, Operation: "type", Target: "line",
		Reason: "edit-conflict", Path: "script.sh",
	}}
	if !reflect.DeepEqual(result.Rejections, wantRejections) {
		t.Fatalf("rejections = %#v, want %#v", result.Rejections, wantRejections)
	}
	if !strings.Contains(result.Diagnostic, "indentation-only change") {
		t.Fatalf("diagnostic = %q", result.Diagnostic)
	}
	if strings.Contains(result.Diagnostic, "1:"+hashLine(`exit "$status"`)) {
		t.Fatalf("diagnostic contains fabricated target row: %q", result.Diagnostic)
	}
}

func TestHostRejectionsPreserveGroupedCommandIdentityWithoutSourceText(t *testing.T) {
	rootPath := t.TempDir()
	writeTestFile(t, rootPath, "file.txt", "", 0o644)
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	result, err := translateForHostForTest(t.Context(), Workspace{Root: root}, []FileEdit{{Path: "file.txt", Script: "bad\nworse\n"}}, t.TempDir())
	if err == nil {
		t.Fatal("invalid commands unexpectedly succeeded")
	}
	want := []HostRejection{
		{Command: 1, SourceLine: 1, Operation: "bad", Reason: "script-syntax", Path: "file.txt"},
		{Command: 2, SourceLine: 2, Operation: "worse", Reason: "script-syntax", Path: "file.txt"},
	}
	if !reflect.DeepEqual(result.Rejections, want) {
		t.Fatalf("rejections = %#v, want %#v", result.Rejections, want)
	}
}

func TestHostTranslationReportsAlreadySatisfiedOutcome(t *testing.T) {
	rootPath := t.TempDir()
	writeTestFile(t, rootPath, "file.txt", "same\n", 0o644)
	root := openTestRoot(t, rootPath)

	result, err := translateForHostForTest(
		t.Context(),
		Workspace{Root: root},
		[]FileEdit{{Path: "file.txt", Script: ""}},
		t.TempDir(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != (HostOutcome{Stage: "evaluated", Status: "already-satisfied"}) {
		t.Fatalf("outcome = %+v", result.Outcome)
	}
	if result.Change != (HostChange{AlreadySatisfied: true}) {
		t.Fatalf("change = %+v", result.Change)
	}
	if len(result.Patch) != 0 || result.PatchSummary != (HostPatchSummary{}) {
		t.Fatalf("patch = %q, summary = %+v", result.Patch, result.PatchSummary)
	}
}

func TestHostTranslationAggregatesIndependentStaleTargets(t *testing.T) {
	rootPath := t.TempDir()
	writeTestFile(t, rootPath, "first.txt", "first current\n", 0o644)
	writeTestFile(t, rootPath, "second.txt", "second current\n", 0o644)
	root := openTestRoot(t, rootPath)

	edits := []FileEdit{
		{Path: "first.txt", Script: `type 1:aaaa "first"`},
		{Path: "second.txt", Script: `type 1:bbbb "second"`},
	}
	result, err := translateForHostForTest(t.Context(), Workspace{Root: root}, edits, t.TempDir())
	if err == nil {
		t.Fatal("stale targets unexpectedly translated")
	}
	if len(result.Rejections) != 2 || result.Rejections[0].Command != 1 || result.Rejections[1].Command != 2 {
		t.Fatalf("rejections = %#v", result.Rejections)
	}
	if result.Change.AlreadySatisfied {
		t.Fatalf("rejected change marked already satisfied: %+v", result.Change)
	}
	if strings.Count(result.Diagnostic, "reason row-stale") != 2 ||
		!strings.Contains(result.Diagnostic, "current-line candidate (verify text)") ||
		!strings.Contains(result.Diagnostic, "hash is absent elsewhere") {
		t.Fatalf("diagnostic = %q", result.Diagnostic)
	}
}

func TestRangeStaleRepairIdentifiesCompleteCurrentCoordinateSpan(t *testing.T) {
	rootPath := t.TempDir()
	writeTestFile(t, rootPath, "file.txt", "start\nnear start\nstart edge\nmiddle hidden\nend edge\nnear end\nend changed\n", 0o644)
	root := openTestRoot(t, rootPath)
	edits := []FileEdit{{Path: "file.txt", Script: "type " + row(1, "start") + `..7:bbbb "replacement"`}}

	result, err := translateForHostForTest(t.Context(), Workspace{Root: root}, edits, t.TempDir())
	if err == nil {
		t.Fatal("stale range unexpectedly translated")
	}
	if !strings.Contains(result.Diagnostic, "range candidate at requested coordinates (verify both endpoint texts and complete 7-line span): "+row(1, "start")+".."+row(7, "end changed")) ||
		!strings.Contains(result.Diagnostic, "range start verified at "+row(1, "start")) ||
		!strings.Contains(result.Diagnostic, "range end expected 7:bbbb") ||
		!strings.Contains(result.Diagnostic, "current-line candidate (verify text): 7:") ||
		strings.Contains(result.Diagnostic, "middle hidden") {
		t.Fatalf("diagnostic = %q", result.Diagnostic)
	}
}

func TestHostFailuresClassifyLanguageAndConflictScopes(t *testing.T) {
	singleCommand := &commandError{
		Reason: reasonLanguageSyntax, Command: 2, Path: "file.go",
		Repair: "generated Go near 3:5", Locations: []commandErrorLocation{{}, {}},
	}
	failures := hostFailuresOf(singleCommand, "evaluated")
	if len(failures) != 1 || failures[0].Scope != "field-local" ||
		failures[0].Suggestion != "generated Go near 3:5" {
		t.Fatalf("single-command language failures = %+v", failures)
	}

	multipleCommands := &commandGroupError{commands: []*commandError{
		{Reason: reasonLanguageSyntax, Command: 2, Path: "file.go"},
		{Reason: reasonLanguageSyntax, Command: 4, Path: "file.go"},
	}}
	failures = hostFailuresOf(multipleCommands, "evaluated")
	if len(failures) != 2 || failures[0].Scope != "multi-command" || failures[1].Scope != "multi-command" {
		t.Fatalf("multi-command language failures = %+v", failures)
	}

	indentation := &commandError{
		Reason: reasonEditConflict, Command: 3, Path: "file.go", CorrectionScope: "field-local",
	}
	failures = hostFailuresOf(indentation, "evaluated")
	if len(failures) != 1 || failures[0].Scope != "field-local" {
		t.Fatalf("indentation failure = %+v", failures)
	}

	conflict := &commandError{Reason: reasonEditConflict, Command: 3, Path: "file.go"}
	failures = hostFailuresOf(conflict, "evaluated")
	if len(failures) != 1 || failures[0].Scope != "multi-command" {
		t.Fatalf("conflict failures = %+v", failures)
	}
}

func TestMekugi2ChangedGoFilesAreFormatted(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.go", "package p\n\nvar value=1\n", 0o644)
	edits := []FileEdit{{Path: "file.go", Script: "type " + row(3, "var value=1") + ` "var value=2"`}}
	result, err := applyForHostAtTest(t, root, edits, "")
	if err != nil {
		t.Fatalf("ApplyForHost() error = %v, diagnostic %q", err, result.Diagnostic)
	}
	if got, want := readTestFile(t, root, "file.go"), "package p\n\nvar value = 2\n"; got != want {
		t.Fatalf("file = %q, want %q", got, want)
	}
}

func TestMekugi2InvalidGoRejectsAtomically(t *testing.T) {
	rootPath := t.TempDir()
	before := "package p\n\nvar value = 1\n"
	writeTestFile(t, rootPath, "file.go", before, 0o644)
	edits := []FileEdit{{Path: "file.go", Script: "type " + row(3, "var value = 1") + ` "var ="`}}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	result, err := translateForHostForTest(t.Context(), Workspace{Root: root}, edits, t.TempDir())
	root.Close()
	if err == nil {
		t.Fatal("invalid Go unexpectedly translated")
	}
	want := []HostRejection{{
		Command: 1, SourceLine: 1, Operation: "type", Target: "line",
		Reason: "language-syntax", Path: "file.go", GeneratedLine: 3, GeneratedColumn: 5,
	}}
	if !reflect.DeepEqual(result.Rejections, want) {
		t.Fatalf("rejections = %#v, want %#v", result.Rejections, want)
	}

	result, err = applyForHostAtTest(t, rootPath, edits, "")
	if err == nil || !strings.Contains(result.Diagnostic, "language-syntax") {
		t.Fatalf("ApplyForHost() error = %v, diagnostic %q", err, result.Diagnostic)
	}
	if !strings.Contains(result.Diagnostic, "generated Go near 3:5\n") || !strings.Contains(result.Diagnostic, "> 3 | var =\n") {
		t.Fatalf("diagnostic lacks generated-source context: %q", result.Diagnostic)
	}
	if got := readTestFile(t, rootPath, "file.go"); got != before {
		t.Fatalf("file = %q, want unchanged", got)
	}
}

func TestMekugi2InvalidGoAttributesCausativeMutation(t *testing.T) {
	rootPath := t.TempDir()
	before := "package p\n\nvar first = 1\nvar second = 2\n"
	writeTestFile(t, rootPath, "file.go", before, 0o644)
	edits := []FileEdit{{Path: "file.go", Script: strings.Join([]string{
		"type " + row(3, "var first = 1") + ` "var ="`,
		"type " + row(4, "var second = 2") + ` "var second = 3"`,
	}, "\n")}}

	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	result, err := translateForHostForTest(t.Context(), Workspace{Root: root}, edits, t.TempDir())
	root.Close()
	if err == nil {
		t.Fatal("invalid Go unexpectedly translated")
	}
	want := []HostRejection{{
		Command: 1, SourceLine: 1, Operation: "type", Target: "line",
		Reason: "language-syntax", Path: "file.go", GeneratedLine: 3, GeneratedColumn: 5,
	}}
	if !reflect.DeepEqual(result.Rejections, want) {
		t.Fatalf("rejections = %#v, want %#v", result.Rejections, want)
	}
	if got := readTestFile(t, rootPath, "file.go"); got != before {
		t.Fatalf("file = %q, want unchanged", got)
	}
}

func TestMekugi2InvalidGoCollectsDistinctCommandsInOneFile(t *testing.T) {
	rootPath := t.TempDir()
	before := "package p\n\nvar first = 1\nvar second = 2\n"
	writeTestFile(t, rootPath, "file.go", before, 0o644)
	edits := []FileEdit{{Path: "file.go", Script: strings.Join([]string{
		"type " + row(3, "var first = 1") + ` "var ="`,
		"type " + row(4, "var second = 2") + ` "var ="`,
	}, "\n")}}

	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	result, err := translateForHostForTest(t.Context(), Workspace{Root: root}, edits, t.TempDir())
	root.Close()
	if err == nil {
		t.Fatal("invalid Go unexpectedly translated")
	}
	if got := result.Rejections; len(got) != 2 ||
		got[0].Command != 1 || got[0].GeneratedLine != 3 ||
		got[1].Command != 2 || got[1].GeneratedLine != 4 {
		t.Fatalf("rejections = %#v, want commands 1 and 2", got)
	}
	if got := readTestFile(t, rootPath, "file.go"); got != before {
		t.Fatalf("file = %q, want unchanged", got)
	}
}

func TestMekugi2InvalidGoPreservesSameLineCommandLocations(t *testing.T) {
	rootPath := t.TempDir()
	line := "var left = 1; var right = 2"
	before := "package p\n\n" + line + "\n"
	writeTestFile(t, rootPath, "file.go", before, 0o644)
	edits := []FileEdit{{Path: "file.go", Script: strings.Join([]string{
		"type " + row(3, line) + ` "1" ""`,
		"type " + row(3, line) + ` "2" ""`,
	}, "\n")}}

	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	result, err := translateForHostForTest(t.Context(), Workspace{Root: root}, edits, t.TempDir())
	root.Close()
	if err == nil {
		t.Fatal("invalid Go unexpectedly translated")
	}
	if got := result.Rejections; len(got) != 2 ||
		got[0].Command != 1 || got[1].Command != 2 ||
		got[0].GeneratedLine != 3 || got[1].GeneratedLine != 3 {
		t.Fatalf("rejections = %#v, want commands 1 and 2 on generated line 3", got)
	}
}

func TestMekugi2InvalidGoCollectsDistinctFiles(t *testing.T) {
	rootPath := t.TempDir()
	before := "package p\n\nvar value = 1\n"
	writeTestFile(t, rootPath, "first.go", before, 0o644)
	writeTestFile(t, rootPath, "second.go", before, 0o644)
	edits := []FileEdit{
		{Path: "first.go", Script: "type " + row(3, "var value = 1") + ` "var ="`},
		{Path: "second.go", Script: "type " + row(3, "var value = 1") + ` "var ="`},
	}

	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	result, err := translateForHostForTest(t.Context(), Workspace{Root: root}, edits, t.TempDir())
	root.Close()
	if err == nil {
		t.Fatal("invalid Go unexpectedly translated")
	}
	if got := result.Rejections; len(got) != 2 ||
		got[0].Command != 1 || got[0].Path != "first.go" ||
		got[1].Command != 2 || got[1].Path != "second.go" {
		t.Fatalf("rejections = %#v, want failures from both files", got)
	}
	for _, path := range []string{"first.go", "second.go"} {
		if got := readTestFile(t, rootPath, path); got != before {
			t.Fatalf("%s = %q, want unchanged", path, got)
		}
	}
}

func TestMekugi2InvalidGoReportsMultilineValueRow(t *testing.T) {
	rootPath := t.TempDir()
	before := "package p\n\nvar value = 1\n"
	writeTestFile(t, rootPath, "file.go", before, 0o644)
	edits := []FileEdit{{Path: "file.go", Script: "type " + row(3, "var value = 1") + " <<PATCH\n" +
		"var first = 1\n" +
		"var =\n" +
		"var third = 3\n" +
		"PATCH\n"}}

	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	result, err := translateForHostForTest(t.Context(), Workspace{Root: root}, edits, t.TempDir())
	root.Close()
	if err == nil {
		t.Fatal("invalid Go unexpectedly translated")
	}
	want := []HostRejection{{
		Command: 1, SourceLine: 1, Operation: "type", Target: "line",
		Reason: "language-syntax", Path: "file.go", GeneratedLine: 4, GeneratedColumn: 5, ValueLine: 2,
	}}
	if !reflect.DeepEqual(result.Rejections, want) {
		t.Fatalf("rejections = %#v, want %#v", result.Rejections, want)
	}
	result, err = applyForHostAtTest(t, rootPath, edits, "")
	if err == nil {
		t.Fatalf("ApplyForHost() error = %v, diagnostic %q", err, result.Diagnostic)
	}
	for _, fragment := range []string{
		"command 1 multiline value near row 2\n",
		"  value row 1 | var first = 1\n",
		"> value row 2 | var =\n",
		"  value row 3 | var third = 3\n",
	} {
		if !strings.Contains(result.Diagnostic, fragment) {
			t.Fatalf("diagnostic lacks %q:\n%s", fragment, result.Diagnostic)
		}
	}
	if got := readTestFile(t, rootPath, "file.go"); got != before {
		t.Fatalf("file = %q, want unchanged", got)
	}
}

func TestMekugi2InvalidGoCollectsFarApartHeredocLocations(t *testing.T) {
	body := "package p\nvar =\n" + strings.Repeat("var filler = 1\n", 100) + "var =\n"

	rootPath := t.TempDir()
	writeTestFile(t, rootPath, "file.go", "seed\n", 0o644)
	edits := []FileEdit{{Path: "file.go", Script: "type " + row(1, "seed") + " <<PATCH\n" + body + "PATCH\n"}}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	result, err := translateForHostForTest(t.Context(), Workspace{Root: root}, edits, t.TempDir())
	root.Close()
	if err == nil {
		t.Fatal("invalid Go unexpectedly translated")
	}
	if got := result.Rejections; len(got) != 2 ||
		got[0].Command != 1 || got[0].SourceLine != 1 || got[0].ValueLine != 2 ||
		got[1].Command != 1 || got[1].SourceLine != 1 || got[1].ValueLine != 103 {
		t.Fatalf("rejections = %#v, want heredoc value rows 2 and 103", got)
	}
	if count := strings.Count(result.Diagnostic, `type: command 1, path "file.go", reason language-syntax: 2 distinct syntax failures`); count != 1 {
		t.Fatalf("diagnostic command groups = %d, want 1:\n%s", count, result.Diagnostic)
	}
	if got := readTestFile(t, rootPath, "file.go"); got != "seed\n" {
		t.Fatalf("rejected translation mutated file.go: %q", got)
	}
}

func TestMekugi2InvalidGoCollectsBeyondFormatterErrorCutoff(t *testing.T) {
	const failureCount = 12
	body := "package p\n" + strings.Repeat("var =\n", failureCount)

	rootPath := t.TempDir()
	writeTestFile(t, rootPath, "file.go", "seed\n", 0o644)
	edits := []FileEdit{{Path: "file.go", Script: "type " + row(1, "seed") + " <<PATCH\n" + body + "PATCH\n"}}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	result, err := translateForHostForTest(t.Context(), Workspace{Root: root}, edits, t.TempDir())
	root.Close()
	if err == nil {
		t.Fatal("invalid Go unexpectedly translated")
	}
	if got := len(result.Rejections); got != failureCount {
		t.Fatalf("rejections = %d, want %d: %#v", got, failureCount, result.Rejections)
	}
	for index, rejection := range result.Rejections {
		if rejection.Command != 1 || rejection.SourceLine != 1 || rejection.ValueLine != index+2 {
			t.Fatalf("rejection %d = %#v, want command 1 value row %d", index, rejection, index+2)
		}
	}
}

type cancelAfterErrChecks struct {
	context.Context
	remaining int
}

func (c *cancelAfterErrChecks) Err() error {
	if c.remaining == 0 {
		return context.Canceled
	}
	c.remaining--
	return nil
}

func TestValidationCancellationPrecedesCollectedFailures(t *testing.T) {
	w := &workspace{files: []*fileState{{
		path:   "file.go",
		editor: editor{baseline: "package p\nvar =\n"},
	}}}
	ctx := &cancelAfterErrChecks{Context: t.Context(), remaining: 2}
	err := w.renderFinal(ctx)
	if err != context.Canceled {
		t.Fatalf("validation error = %v, want context.Canceled", err)
	}
	if commands := commandsOf(err); len(commands) != 0 {
		t.Fatalf("canceled validation exposed command failures: %#v", commands)
	}
}

func TestMekugi2MultilineValueRowsUsePhysicalFraming(t *testing.T) {
	rootPath := t.TempDir()
	writeTestFile(t, rootPath, "file.go", "seed\n", 0o644)
	edits := []FileEdit{{Path: "file.go", Script: "type " + row(1, "seed") + " <<PATCH\npackage p\rvar =\nPATCH\n"}}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	result, err := translateForHostForTest(t.Context(), Workspace{Root: root}, edits, t.TempDir())
	root.Close()
	if err == nil {
		t.Fatal("invalid Go unexpectedly translated")
	}
	if len(result.Rejections) != 1 || result.Rejections[0].Command != 1 ||
		result.Rejections[0].SourceLine != 1 || result.Rejections[0].ValueLine != 1 {
		t.Fatalf("rejections = %#v, want command 1 physical value row 1", result.Rejections)
	}

	result, err = applyForHostAtTest(t, rootPath, edits, "")
	if err == nil {
		t.Fatalf("ApplyForHost() error = %v, diagnostic %q", err, result.Diagnostic)
	}
	for _, fragment := range []string{
		"command 1 multiline value near row 1\n",
		"> value row 1 | package p\\rvar =\n",
	} {
		if !strings.Contains(result.Diagnostic, fragment) {
			t.Fatalf("diagnostic lacks %q:\n%s", fragment, result.Diagnostic)
		}
	}
	if strings.Contains(result.Diagnostic, "value row 2 |") {
		t.Fatalf("diagnostic split an embedded carriage return into another value row:\n%s", result.Diagnostic)
	}
	if got := readTestFile(t, rootPath, "file.go"); got != "seed\n" {
		t.Fatalf("rejected translation mutated file.go: %q", got)
	}
}
