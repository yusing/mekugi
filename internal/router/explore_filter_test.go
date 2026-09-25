package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

type fakeExploreJudge struct {
	calls atomic.Int32
	err   error
	// score returns the probability for one results entry.
	score func(exploreUnitState) float64
}

func (j *fakeExploreJudge) nouls(_ context.Context, state any, questions map[string]typesafeNoul) (map[string]float64, typesafeUsage, error) {
	j.calls.Add(1)
	if j.err != nil {
		return nil, typesafeUsage{}, j.err
	}
	results := state.(map[string]any)["results"].([]exploreUnitState)
	answers := make(map[string]float64, len(questions))
	for id := range questions {
		var index int
		fmt.Sscanf(id, "r%d", &index)
		answers[id] = j.score(results[index])
	}
	return answers, typesafeUsage{InputTokens: 10, Requests: 1}, nil
}

func TestExploreCommand(t *testing.T) {
	for _, test := range []struct {
		command string
		family  exploreFamily
		list    bool
	}{
		{`rg -n snapshot internal`, explorePaths, false},
		{`rg -n "func .*Token" --type go`, explorePaths, false},
		{`/usr/bin/grep -rn 'needle' .`, explorePaths, false},
		{`find . -name '*.json' -not -path './.git/*'`, exploreListing, false},
		{`fd -e go router`, exploreListing, false},
		{`git -C repo --no-pager grep -n snapshot`, explorePaths, false},
		{`git ls-files internal`, exploreListing, false},
		{`rg -l snapshot`, exploreListing, false},
		{`grep -rl snapshot .`, exploreListing, false},
		{`rg -n -g*.lock snapshot`, explorePaths, false},
		{`git log --stat -n 80`, exploreCommits, false},
		{`git log --oneline -n 300`, exploreCommits, false},
		{`git diff HEAD~8 HEAD`, exploreDiffs, false},
		{`git show b57a998`, exploreDiffs, false},
		{`go vet ./...`, exploreDiagnostics, false},
		{`golangci-lint run ./internal/...`, exploreDiagnostics, false},
		{`npx tsc --noEmit`, exploreDiagnostics, false},
		{`rg --help`, exploreHelp, false},
		{`git help log`, exploreHelp, false},
		{`man find`, exploreHelp, false},
		// Globs, a leading tilde, and $HOME expand to paths.
		{`rg snapshot internal/*.go`, explorePaths, false},
		{`find ~/src -name x`, exploreListing, false},
		{`rg -n x "$HOME/.codex"`, explorePaths, false},
		// Filters that keep rows intact, and stderr redirections.
		{`rg -n snapshot | head`, explorePaths, false},
		{`rg -n snapshot | head -n 40`, explorePaths, false},
		{`rg -n snapshot | sort -u | head -80`, explorePaths, false},
		{`rg --files internal | rg -v '_test'`, exploreListing, false},
		{`rg -n snapshot 2>&1`, explorePaths, false},
		{`go vet ./... 2>/dev/null`, exploreDiagnostics, false},
		// Lists combine the families of their statements.
		{`rg -n a internal | head -65; mcat internal/a.go 1:40`, explorePaths, true},
		{"git diff --check; git diff -- a.go\ngit status --short", exploreDiffs, true},
		{`git log --oneline -5 && rg -n a`, exploreCommits | explorePaths, true},
		{`mcat a.go 1:20; cat b.md`, exploreNone, true},
		{`ls internal; rg --files internal`, explorePaths, true},
		{`rg -n -thtml snapshot`, explorePaths, false},
		{`git show HEAD | head -40`, exploreDiffs, false},
		// A cut file diff or log entry cannot bound its rows before the next statement.
		{`git diff | head -20; cat config.yaml`, exploreNone, true},
		{`git log --stat | grep fix`, exploreNone, false},
		// Rows of unknown shape, dynamic programs, and directory changes.
		{`rg -n foo; go test ./...`, exploreNone, true},
		{`rg -n foo; ./script.sh`, exploreNone, true},
		{`cd "$(git rev-parse --show-toplevel)" && rg -n foo`, exploreNone, false},
		{`builtin cd /tmp; rg -n foo`, exploreNone, false},
		{`cd $DIR; rg -n foo`, exploreNone, false},
		{`source env.sh; rg -n foo`, exploreNone, false},
		{`$TOOL -n foo`, exploreNone, false},
		{`rg -n snapshot | wc -l`, exploreNone, false},
		{`rg -n snapshot | head notes.txt`, exploreNone, false},
		{`rg -n snapshot | rg -n render`, exploreNone, false},
		{`rg -n snapshot | grep render other.txt`, exploreNone, false},
		{`cd internal && rg snapshot`, exploreNone, false},
		{`(cd internal && rg snapshot); rg x`, exploreNone, false},
		{`rg snapshot & rg x`, exploreNone, false},
		{`rg snapshot > out.txt`, exploreNone, false},
		{`rg snapshot 2>errors.txt`, exploreNone, false},
		{`rg "$PATTERN" .`, exploreNone, false},
		{`rg "$(cat p)" .`, exploreNone, false},
		{`rg --json snapshot`, exploreNone, false},
		{`find . -name x -exec cat {} +`, exploreNone, false},
		{`git log --graph --oneline`, exploreNone, false},
		{`git status`, exploreNone, false},
		{`go test ./...`, exploreNone, false},
		{`golangci-lint fmt`, exploreNone, false},
		{`sed -n 1,20p file.go`, exploreNone, false},
		{`FOO=1 rg snapshot`, exploreNone, false},
	} {
		if family, list := exploreCommand(test.command); family != test.family || family != exploreNone && list != test.list {
			t.Errorf("exploreCommand(%q) = %v, %v; want %v, %v", test.command, family, list, test.family, test.list)
		}
	}
}

func exploreFixture(t *testing.T) (dir string, body string) {
	t.Helper()
	dir = t.TempDir()
	var rows strings.Builder
	for _, name := range []string{"live/diff.go", "live/view.go", "tokenizer/a.go", "tokenizer/b.go", "catalog/c.go", "catalog/d.go"} {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		for line := range 8 {
			fmt.Fprintf(&rows, "%s:%d:\tsnapshot %d := render(frame, colors, theme)\n", name, line+1, line)
		}
	}
	rows.WriteString("rg: missing: No such file or directory\n")
	return dir, rows.String()
}

func exploreRequest(t *testing.T, dir, command, output string) *parsedResponsesRequest {
	t.Helper()
	arguments := string(mustTestJSON(t, map[string]string{"cmd": command, "workdir": dir}))
	input := []map[string]any{
		{"type": "message", "role": "user", "content": []map[string]string{{"type": "input_text", "text": "Fix live diff snapshot colors."}}},
		{"type": "message", "role": "assistant", "content": []map[string]string{{"type": "output_text", "text": "Looking for where live diff snapshots render."}}},
		{"type": "function_call", "name": "exec_command", "call_id": "call-1", "arguments": arguments},
		{"type": "function_call_output", "call_id": "call-1", "output": output},
	}
	return &parsedResponsesRequest{fields: map[string]json.RawMessage{"input": mustTestJSON(t, input)}}
}

func exploreOutput(t *testing.T, request *parsedResponsesRequest) string {
	t.Helper()
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(request.fields["input"], &items); err != nil {
		t.Fatal(err)
	}
	text, ok := decodeJSONString(items[len(items)-1]["output"])
	if !ok {
		t.Fatalf("output is not a string: %s", items[len(items)-1]["output"])
	}
	return text
}

func TestExploreFilterOmitsUnrelatedFiles(t *testing.T) {
	dir, body := exploreFixture(t)
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	header := "Chunk ID: 1\nWall time: 0.1 seconds\nProcess exited with code 0\nOutput:\n"
	judge := &fakeExploreJudge{score: func(unit exploreUnitState) float64 {
		if strings.HasPrefix(unit.Path, "live/") {
			return 0.9
		}
		if unit.Path == "catalog/c.go" {
			return 0.2 // within the kept top three
		}
		return 0.05
	}}
	filter := newExploreFilter(judge)
	request := exploreRequest(t, dir, "rg -n snapshot", header+body)
	filter.project(t.Context(), request, nil, dir, "", "/root", store)

	got := exploreOutput(t, request)
	if !strings.HasPrefix(got, header) {
		t.Fatalf("header changed:\n%s", got)
	}
	for _, want := range []string{"live/diff.go:1:\tsnapshot 0", "live/view.go:8:\tsnapshot 7", "catalog/c.go:3:", "rg: missing: No such file or directory"} {
		if !strings.Contains(got, want) {
			t.Errorf("filtered output lacks %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"tokenizer/a.go:1:", "catalog/d.go:1:"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("filtered output kept %q", unwanted)
		}
	}
	footer := regexp.MustCompile(`\[mekugi explore filter: omitted 3 of 6 files \(24 of 49 lines\) judged unrelated to the task: tokenizer/a.go \(8\), tokenizer/b.go \(8\), catalog/d.go \(8\)\. Full output: mread ([a-z]+[0-9]*)\]\n$`).FindStringSubmatch(got)
	if footer == nil {
		t.Fatalf("missing footer:\n%s", got)
	}
	record, err := store.readShellOutput(t.Context(), footer[1])
	if err != nil {
		t.Fatal(err)
	}
	if record.Stdout != body {
		t.Fatalf("retained output differs:\n%s", record.Stdout)
	}

	// A replayed request forwards the same bytes without judging again.
	calls := judge.calls.Load()
	again := exploreRequest(t, dir, "rg -n snapshot", header+body)
	filter.project(t.Context(), again, nil, dir, "", "/root", store)
	if judge.calls.Load() != calls || exploreOutput(t, again) != got {
		t.Fatal("replayed output was judged again or changed")
	}
}

func TestExploreFilterKeepsStockOutput(t *testing.T) {
	dir, body := exploreFixture(t)
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	header := "Wall time: 0.1 seconds\nProcess exited with code 0\nOutput:\n"
	relevant := &fakeExploreJudge{score: func(exploreUnitState) float64 { return 0.8 }}
	failing := &fakeExploreJudge{err: errors.New("unavailable")}
	for name, test := range map[string]struct {
		judge   *fakeExploreJudge
		command string
		output  string
	}{
		"all relevant":   {relevant, "rg -n snapshot", header + body},
		"judge failure":  {failing, "rg -n snapshot", header + body},
		"counting":       {relevant, "rg -n snapshot | wc -l", header + body},
		"nonzero exit":   {relevant, "rg -n snapshot", strings.Replace(header, "code 0", "code 2", 1) + body},
		"short output":   {relevant, "rg -n snapshot", header + body[:200]},
		"running":        {relevant, "rg -n snapshot", "Wall time: 0.1 seconds\nProcess running with session ID 4\nOutput:\n" + body},
		"other programs": {relevant, "cat big.txt", header + body},
	} {
		t.Run(name, func(t *testing.T) {
			request := exploreRequest(t, dir, test.command, test.output)
			newExploreFilter(test.judge).project(t.Context(), request, nil, dir, "", "/root", store)
			if got := exploreOutput(t, request); got != test.output {
				t.Fatalf("output changed:\n%s", got)
			}
		})
	}
}

func TestExploreFilterJudgesOnlyNewOutputs(t *testing.T) {
	dir, body := exploreFixture(t)
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	output := "Wall time: 0.1 seconds\nProcess exited with code 0\nOutput:\n" + body
	request := exploreRequest(t, dir, "rg -n snapshot", output)
	var items []map[string]any
	if err := json.Unmarshal(request.fields["input"], &items); err != nil {
		t.Fatal(err)
	}
	// A later assistant message makes the output historical.
	items = append(items, map[string]any{"type": "message", "role": "assistant", "content": []map[string]string{{"type": "output_text", "text": "done"}}})
	request.fields["input"] = mustTestJSON(t, items)
	judge := &fakeExploreJudge{score: func(exploreUnitState) float64 { return 0 }}
	newExploreFilter(judge).project(t.Context(), request, nil, dir, "", "/root", store)
	if judge.calls.Load() != 0 {
		t.Fatal("historical output was judged")
	}
}

func TestExploreUnitsGroupsDirectories(t *testing.T) {
	dir := t.TempDir()
	var rows []string
	for group := range 3 {
		for entry := range 50 {
			path := filepath.Join(fmt.Sprintf("g%d", group), fmt.Sprintf("f%02d.json", entry))
			if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(path)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, path), nil, 0o644); err != nil {
				t.Fatal(err)
			}
			rows = append(rows, "./"+path+"\n")
		}
	}
	units := explorePathUnits(rows, dir)
	if len(units) != 3 || units[0].kind != exploreDirectoryUnit || units[0].key != "g0" || len(units[0].rows) != 50 {
		t.Fatalf("units = %d first=%+v", len(units), units[0].key)
	}
	state := units[0].state()
	if state.Directory != "g0" || state.Entries != 50 || len(state.Sample) != exploreSampleLines || state.Sample[0] != "f00.json" {
		t.Fatalf("state = %+v", state)
	}
}

func TestExploreSplitFamilies(t *testing.T) {
	rows := func(text string) []string { return strings.SplitAfter(strings.TrimSuffix(text, "\n")+"\n", "\n") }

	log := rows("commit 1111111111111111111111111111111111111111\nAuthor: a\nDate: d\n\n    fix(router): first\n\n a.go | 2 +-\n\ncommit 2222222222222222222222222222222222222222\nAuthor: b\n\n    feat: second\n")
	units := exploreSplit(exploreCommits, log, "")
	if len(units) != 2 || units[0].kind != exploreCommitUnit || len(units[0].rows) != 8 || units[1].label() != "222222222222 feat: second" {
		t.Fatalf("commit units = %+v", units)
	}
	if state := units[0].state(); state.Label == "" || state.Sample[0] != "Author: a" || state.LineCount != 8 {
		t.Fatalf("commit state = %+v", state)
	}
	oneline := rows("abc1234 first\ndef5678 second\n")
	if units := exploreSplit(exploreCommits, oneline, ""); len(units) != 2 || units[1].label() != "def5678 second" {
		t.Fatalf("oneline units = %+v", units)
	}

	show := rows("commit 1111111111111111111111111111111111111111\n    subject\ndiff --git a/x.go b/x.go\nindex 1..2\n--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-old\n+new\ndiff --git a/y.md b/y.md\n+doc\n")
	units = exploreSplit(exploreDiffs, show, "")
	if len(units) != 2 || units[0].kind != exploreDiffUnit || units[0].rows[0] != 2 || units[0].label() != "x.go" {
		t.Fatalf("diff units = %+v", units)
	}
	if state := units[0].state(); strings.Join(state.Sample, "|") != "@@ -1 +1 @@|-old|+new" {
		t.Fatalf("diff sample = %q", state.Sample)
	}

	help := rows("Usage: rg [OPTIONS]\n\nOPTIONS:\n    -., --hidden\n        Search hidden files.\n\n    --no-ignore\n        Ignore nothing.\nFOOTER\n")
	units = exploreSplit(exploreHelp, help, "")
	if len(units) != 2 || units[0].kind != exploreHelpUnit || units[0].label() != "-., --hidden" || len(units[1].rows) != 2 {
		t.Fatalf("help units = %+v", units)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.ts"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	diagnostics := rows("# example.com/pkg\na.go:3:2: unused value\n\tx := 1\n\t^\nb.ts(4,5): error TS2322: bad\n2 issues.\n")
	units = exploreSplit(exploreDiagnostics, diagnostics, dir)
	if len(units) != 2 || len(units[0].rows) != 3 || units[1].key != "b.ts" {
		t.Fatalf("diagnostic units = %+v", units)
	}
}

func TestExploreExitAccepted(t *testing.T) {
	code := func(n int) *int { return &n }
	if !exploreExitAccepted(exploreDiagnostics, false, code(1)) || exploreExitAccepted(explorePaths, false, code(1)) ||
		exploreExitAccepted(exploreDiagnostics, false, code(3)) || !exploreExitAccepted(exploreCommits, false, code(0)) {
		t.Fatal("unexpected exit acceptance")
	}
	// A list's status is its last statement's, and printed stdout has none.
	if !exploreExitAccepted(explorePaths, true, code(1)) || !exploreExitAccepted(explorePaths, false, nil) {
		t.Fatal("list or unprinted status rejected")
	}
}

func TestExploreFilterShrinksPathLists(t *testing.T) {
	dir := t.TempDir()
	var body strings.Builder
	for i := range 40 {
		name := filepath.Join("node_modules", fmt.Sprintf("package-%02d", i), "package.json")
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		body.WriteString("./" + name + "\n")
	}
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	output := "Wall time: 0.1 seconds\nProcess exited with code 0\nOutput:\n" + body.String()
	request := exploreRequest(t, dir, "find . -name package.json", output)
	judge := &fakeExploreJudge{score: func(exploreUnitState) float64 { return 0.01 }}
	newExploreFilter(judge).project(t.Context(), request, nil, dir, "", "/root", store)
	got := exploreOutput(t, request)
	if got == output || len(got) >= len(output)*3/4 {
		t.Fatalf("filtered path list did not shrink: %d -> %d bytes\n%s", len(output), len(got), got)
	}
	if !strings.Contains(got, "+") || !strings.Contains(got, "more. Full output: mread ") {
		t.Fatalf("omission list is not summarized:\n%s", got)
	}
}

func TestExploreFilterListKeepsOtherStatements(t *testing.T) {
	dir, search := exploreFixture(t)
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// A large read makes the search rows a small share of the output; its
	// indented rows and path-like rows must not join a search unit.
	var read strings.Builder
	read.WriteString("--- file 1 path=\"notes.go\" shown=1:80 ---\n")
	for i := range 80 {
		fmt.Fprintf(&read, "\tfield%02d := render(frame) // keep this read intact\n", i)
	}
	read.WriteString("tokenizer/a.go\n")
	body := search + read.String() + " M tokenizer/b.go\n"
	header := "Wall time: 0.1 seconds\nProcess exited with code 1\nOutput:\n"
	judge := &fakeExploreJudge{score: func(unit exploreUnitState) float64 {
		if strings.HasPrefix(unit.Path, "live/") {
			return 0.9
		}
		if unit.Path == "catalog/c.go" {
			return 0.2 // within the kept top three
		}
		return 0.01
	}}
	request := exploreRequest(t, dir, "rg -n snapshot | head -60; mcat notes.go 1:80; git status --short", header+body)
	newExploreFilter(judge).project(t.Context(), request, nil, dir, "", "/root", store)
	got := exploreOutput(t, request)
	if !strings.HasPrefix(got, header) || !strings.Contains(got, "[mekugi explore filter: omitted 3 of 6 files") {
		t.Fatalf("list search rows were not filtered:\n%s", got)
	}
	if !strings.Contains(got, read.String()+" M tokenizer/b.go\n") {
		t.Fatalf("other statements' rows changed:\n%s", got)
	}
	if strings.Contains(got, "tokenizer/a.go:1:") || !strings.Contains(got, "live/diff.go:1:") {
		t.Fatalf("unexpected search rows kept or dropped:\n%s", got)
	}
}

func TestExploreListUnits(t *testing.T) {
	rows := func(text string) []string { return strings.SplitAfter(strings.TrimSuffix(text, "\n")+"\n", "\n") }
	dir := t.TempDir()
	for _, name := range []string{"a.go", "b.go"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Hunk line counts end a file diff, so rows of later statements stay out.
	diff := rows("diff --git a/a.go b/a.go\nindex 1..2 100644\n--- a/a.go\n+++ b/a.go\n@@ -1,2 +1,2 @@\n context\n-old\n+new\n\\ No newline at end of file\n" +
		"diff --git a/b.go b/b.go\nnew file mode 100644\n--- /dev/null\n+++ b/b.go\n@@ -0,0 +1 @@\n+package b\n M a.go\n?? b.go\n")
	units := exploreListUnits(exploreDiffs, diff, dir)
	if len(units) != 2 || len(units[0].rows) != 9 || len(units[1].rows) != 6 || units[1].label() != "b.go" {
		t.Fatalf("diff units = %+v", units)
	}

	// Commit entries end at the first row outside an entry.
	log := rows("commit 1111111111111111111111111111111111111111\nAuthor: a\nDate: d\n\n    fix: first\n\n a.go | 2 +-\n 1 file changed, 1 insertion(+)\n\n--- file 1 path=\"a.go\" ---\npackage a\n")
	units = exploreListUnits(exploreCommits, log, dir)
	if len(units) != 1 || len(units[0].rows) != 9 {
		t.Fatalf("commit units = %+v", units)
	}

	// One-line commits look like blame or checksum rows, so lists keep them whole.
	oneline := rows("1111111 fix: first\n2222222 (a 2026-09-25 1) package a\n")
	if units = exploreListUnits(exploreCommits, oneline, dir); len(units) != 0 {
		t.Fatalf("oneline units = %+v", units)
	}

	// Bare paths are units only when a statement lists paths.
	listing := rows("a.go\nb.go\na.go:3:\tneedle\n--\nb.go-4-\tcontext\n\tindented\n")
	units = exploreListUnits(explorePaths, listing, dir)
	if len(units) != 2 || !slices.Equal(units[0].rows, []int{2, 3}) || !slices.Equal(units[1].rows, []int{4}) {
		t.Fatalf("search units = %+v", units)
	}
	units = exploreListUnits(exploreListing, listing, dir)
	if len(units) != 2 || !slices.Equal(units[0].rows, []int{0, 2, 3}) || !slices.Equal(units[1].rows, []int{1, 4}) {
		t.Fatalf("listing units = %+v", units)
	}
}
