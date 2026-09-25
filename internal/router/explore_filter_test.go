package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
	return answers, typesafeUsage{InputTokens: 10}, nil
}

func TestExploreCommand(t *testing.T) {
	for _, test := range []struct {
		command string
		family  exploreFamily
	}{
		{`rg -n snapshot internal`, explorePaths},
		{`rg -n "func .*Token" --type go`, explorePaths},
		{`/usr/bin/grep -rn 'needle' .`, explorePaths},
		{`find . -name '*.json' -not -path './.git/*'`, explorePaths},
		{`fd -e go router`, explorePaths},
		{`git -C repo --no-pager grep -n snapshot`, explorePaths},
		{`git ls-files internal`, explorePaths},
		{`git log --stat -n 80`, exploreCommits},
		{`git log --oneline -n 300`, exploreCommits},
		{`git diff HEAD~8 HEAD`, exploreDiffs},
		{`git show b57a998`, exploreDiffs},
		{`go vet ./...`, exploreDiagnostics},
		{`golangci-lint run ./internal/...`, exploreDiagnostics},
		{`npx tsc --noEmit`, exploreDiagnostics},
		{`rg --help`, exploreHelp},
		{`git help log`, exploreHelp},
		{`man find`, exploreHelp},
		{`rg -n snapshot | head`, exploreNone},
		{`cd internal && rg snapshot`, exploreNone},
		{`rg snapshot > out.txt`, exploreNone},
		{`rg "$PATTERN" .`, exploreNone},
		{`rg snapshot internal/*.go`, exploreNone},
		{`rg --json snapshot`, exploreNone},
		{`find . -name x -exec cat {} +`, exploreNone},
		{`git log --graph --oneline`, exploreNone},
		{`git status`, exploreNone},
		{`go test ./...`, exploreNone},
		{`golangci-lint fmt`, exploreNone},
		{`sed -n 1,20p file.go`, exploreNone},
		{`FOO=1 rg snapshot`, exploreNone},
		{`find ~/src -name x`, exploreNone},
	} {
		if got := exploreCommand(test.command); got != test.family {
			t.Errorf("exploreCommand(%q) = %v, want %v", test.command, got, test.family)
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
		"pipeline":       {relevant, "rg -n snapshot | head -100", header + body},
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
	units, byDirectory := explorePathUnits(rows, dir)
	if !byDirectory || len(units) != 3 || units[0].key != "g0" || len(units[0].rows) != 50 {
		t.Fatalf("units = %d byDirectory=%v first=%+v", len(units), byDirectory, units[0].key)
	}
	state := units[0].state(exploreDirectoryUnit)
	if state.Directory != "g0" || state.Entries != 50 || len(state.Sample) != exploreSampleLines || state.Sample[0] != "f00.json" {
		t.Fatalf("state = %+v", state)
	}
}

func TestExploreSplitFamilies(t *testing.T) {
	rows := func(text string) []string { return strings.SplitAfter(strings.TrimSuffix(text, "\n")+"\n", "\n") }

	log := rows("commit 1111111111111111111111111111111111111111\nAuthor: a\nDate: d\n\n    fix(router): first\n\n a.go | 2 +-\n\ncommit 2222222222222222222222222222222222222222\nAuthor: b\n\n    feat: second\n")
	units, kind := exploreSplit(exploreCommits, log, "")
	if kind != exploreCommitUnit || len(units) != 2 || len(units[0].rows) != 8 || units[1].label(kind, log) != "222222222222 feat: second" {
		t.Fatalf("commit units = %+v", units)
	}
	if state := units[0].state(kind); state.Label == "" || state.Sample[0] != "Author: a" || state.LineCount != 8 {
		t.Fatalf("commit state = %+v", state)
	}
	oneline := rows("abc1234 first\ndef5678 second\n")
	if units, _ := exploreSplit(exploreCommits, oneline, ""); len(units) != 2 || units[1].label(exploreCommitUnit, oneline) != "def5678 second" {
		t.Fatalf("oneline units = %+v", units)
	}

	show := rows("commit 1111111111111111111111111111111111111111\n    subject\ndiff --git a/x.go b/x.go\nindex 1..2\n--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-old\n+new\ndiff --git a/y.md b/y.md\n+doc\n")
	units, kind = exploreSplit(exploreDiffs, show, "")
	if kind != exploreDiffUnit || len(units) != 2 || units[0].rows[0] != 2 || units[0].label(kind, show) != "x.go" {
		t.Fatalf("diff units = %+v", units)
	}
	if state := units[0].state(kind); strings.Join(state.Sample, "|") != "@@ -1 +1 @@|-old|+new" {
		t.Fatalf("diff sample = %q", state.Sample)
	}

	help := rows("Usage: rg [OPTIONS]\n\nOPTIONS:\n    -., --hidden\n        Search hidden files.\n\n    --no-ignore\n        Ignore nothing.\nFOOTER\n")
	units, kind = exploreSplit(exploreHelp, help, "")
	if kind != exploreHelpUnit || len(units) != 2 || units[0].label(kind, help) != "-., --hidden" || len(units[1].rows) != 2 {
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
	units, _ = exploreSplit(exploreDiagnostics, diagnostics, dir)
	if len(units) != 2 || len(units[0].rows) != 3 || units[1].key != "b.ts" {
		t.Fatalf("diagnostic units = %+v", units)
	}
}

func TestExploreExitAccepted(t *testing.T) {
	if !exploreExitAccepted(exploreDiagnostics, "Process exited with code 1") || exploreExitAccepted(explorePaths, "Process exited with code 1") ||
		exploreExitAccepted(exploreDiagnostics, "Process exited with code 3") || !exploreExitAccepted(exploreCommits, "Process exited with code 0") {
		t.Fatal("unexpected exit acceptance")
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
