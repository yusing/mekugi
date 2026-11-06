package compact

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/modules/fileread/read"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func TestMain(m *testing.M) {
	if os.Getenv("MEKUGI_TEST_MCAT") == "1" {
		result, err := (read.Service{}).Execute(context.Background(), command.Request{Args: os.Args[1:]})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Fprint(os.Stdout, result.Stdout)
		fmt.Fprint(os.Stderr, result.Stderr)
		os.Exit(result.ExitCode)
	}
	os.Exit(m.Run())
}

func testMcat(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(executable, filepath.Join(dir, "mcat")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MEKUGI_TEST_MCAT", "1")
}

func TestReloadRejectsOversizedCapture(t *testing.T) {
	testMcat(t)
	path := filepath.Join(t.TempDir(), "oversized.json")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(history.MaxRecordBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	selection, err := json.Marshal(map[string]any{"sources": []Source{{Path: path}}})
	if err != nil {
		t.Fatal(err)
	}
	output, err := Capture(t.Context(), command.Request{Args: []string{string(selection)}}, ChangeReader{})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := output.Delivery.Prepare([]string{"amber"})
	if err != nil {
		t.Fatal(err)
	}
	var captured Snapshot
	if err := json.Unmarshal([]byte(prepared.Recovery[0].Stdout), &captured); err != nil {
		t.Fatal(err)
	}
	if captured.Sources[0].ExitCode == 0 || captured.Sources[0].Stderr == "" {
		t.Fatal("oversized source did not report incomplete capture")
	}
}

func TestReloadSelectionHasNoSourceCountLimit(t *testing.T) {
	sources := make([]Source, 40)
	for i := range sources {
		sources[i] = Source{Path: strings.Repeat("a/", 250) + "guide.md"}
	}
	data, err := json.Marshal(map[string]any{"sources": sources})
	if err != nil {
		t.Fatal(err)
	}
	commands, err := ReadCommands(string(data))
	if err != nil || len(commands) != len(sources) {
		t.Fatalf("selected %d sources: %d commands, %v", len(sources), len(commands), err)
	}
	for _, command := range commands {
		if strings.Contains(command, "--number") || !strings.HasPrefix(command, "'mcat'") {
			t.Fatalf("reload must retain raw source rows: %s", command)
		}
	}
}

func TestReloadReportsIncompleteSelectedContent(t *testing.T) {
	testMcat(t)
	path := filepath.Join(t.TempDir(), "full.txt")
	body := strings.Repeat("full source content beyond the former reader token budget\n", 3000)
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	selection, _ := json.Marshal(map[string]any{"sources": []Source{{Path: path}, {Path: path + ".missing"}}})
	output, err := Capture(t.Context(), command.Request{Args: []string{string(selection)}}, ChangeReader{})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := output.Delivery.Prepare([]string{"amber"})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Referenced.Stdout != SnapshotPrefix+"amber\n" || strings.Contains(prepared.Referenced.Stdout, body) {
		t.Fatal("source body entered capped tool output")
	}
	var captured Snapshot
	if err := json.Unmarshal([]byte(prepared.Recovery[0].Stdout), &captured); err != nil {
		t.Fatal(err)
	}
	sources := captured.Sources
	if len(sources) != 2 || sources[0].Stdout == body || sources[0].ExitCode == 0 || sources[1].ExitCode == 0 || sources[1].Stderr == "" {
		t.Fatal("capture did not report incomplete source or independent failure")
	}
	if prepared.Unreferenced.ExitCode == 0 || strings.Contains(prepared.Unreferenced.Stdout, SnapshotPrefix) {
		t.Fatal("failed publication exposed a reference")
	}
}

func TestReloadExcludesHostInstructionsAndPreservesSourceOrder(t *testing.T) {
	testMcat(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "selected.txt")
	if err := os.WriteFile(path, []byte("selected body\n"), 0600); err != nil {
		t.Fatal(err)
	}
	selection, _ := json.Marshal(map[string]any{"sources": []Source{
		{Path: "AGENTS.md"}, {Path: path},
		{Path: filepath.Join(dir, "AGENTS.md"), Ranges: [][2]int{{1, 10}}},
		{Path: path + ".missing"}, {Path: "nested/../nested/AGENTS.md"},
	}})
	output, err := Capture(t.Context(), command.Request{Args: []string{string(selection)}}, ChangeReader{})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := output.Delivery.Prepare([]string{"amber"})
	if err != nil {
		t.Fatal(err)
	}
	var snapshot Snapshot
	if err := json.Unmarshal([]byte(prepared.Recovery[0].Stdout), &snapshot); err != nil {
		t.Fatal(err)
	}
	selected, err := Sources(snapshot.Selection)
	if err != nil || len(selected) != 5 || len(snapshot.Sources) != 5 || snapshot.Selection != string(selection) {
		t.Fatalf("selection/results correlation: %+v, %v", snapshot, err)
	}
	if selected[1].Path != path || snapshot.Sources[1].Stdout != "selected body\n" || snapshot.Sources[1].ExitCode != 0 || selected[3].Path != path+".missing" || snapshot.Sources[3].ExitCode == 0 {
		t.Fatalf("filtered results lost source identity: %+v", snapshot)
	}
	for _, i := range []int{0, 2, 4} {
		if snapshot.Sources[i] != (CapturedSource{}) {
			t.Fatalf("host-owned source acquired: %+v", snapshot.Sources[i])
		}
	}
	commands, err := ReadCommands(`{"sources":[{"path":"AGENTS.md"}]}`)
	if err != nil || len(commands) != 0 {
		t.Fatalf("host-only selection must acquire nothing: %v, %v", commands, err)
	}
}

func TestReloadReadsDashFile(t *testing.T) {
	testMcat(t)
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "-"), []byte("first\nsecond\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		ranges [][2]int
		want   string
	}{{"whole", nil, "first\nsecond\n"}, {"range", [][2]int{{2, 2}}, "second\n"}} {
		t.Run(tc.name, func(t *testing.T) {
			selection, _ := json.Marshal(map[string]any{"sources": []Source{{Path: "-", Ranges: tc.ranges}}})
			commands, err := ReadCommands(string(selection))
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(t.Context(), "bash", "-c", commands[0])
			cmd.Dir = directory
			got, err := cmd.Output()
			if err != nil || string(got) != tc.want {
				t.Fatalf("dash file: got %q, want %q: %v", got, tc.want, err)
			}
		})
	}
}

func TestReloadRefreshesManagedMetadataSource(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "SKILL.md")
	if err := os.WriteFile(path, []byte("metadata placeholder"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, ".skills-mgr-placeholder"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	manager := filepath.Join(directory, "skills-mgr")
	if err := os.WriteFile(manager, []byte("#!/bin/sh\ntest \"$1 $2 $3\" = 'get --codex selected' || exit 2\ncat -- \""+filepath.Join(directory, "current")+"\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	selection, _ := json.Marshal(map[string]any{"sources": []Source{{Skill: "selected", Path: path}}})
	for _, body := range []string{"first instructions", "current instructions"} {
		if err := os.WriteFile(filepath.Join(directory, "current"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		result, err := ReadSources(t.Context(), string(selection), directory, os.Environ(), ChangeReader{})
		if err != nil || len(result.Sources) != 1 || result.Sources[0].ExitCode != 0 || result.Sources[0].Stdout != body {
			t.Fatalf("current managed instructions unavailable: %+v, %v", result, err)
		}
	}
}

func TestReloadMcatQuotedRangesAndLimits(t *testing.T) {
	testMcat(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "-selected 'file'.txt")
	if err := os.WriteFile(path, []byte("first\nsecond\nthird\n"), 0600); err != nil {
		t.Fatal(err)
	}
	many := make([][2]int, 17)
	for i := range many {
		many[i] = [2]int{1, 1}
	}
	selection, err := json.Marshal(map[string]any{"sources": []Source{
		{Path: path, Ranges: [][2]int{{1, 1}, {3, 3}}},
		{Path: path, Ranges: many},
		{Path: path, Ranges: [][2]int{{3, 1}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := ReadSources(t.Context(), string(selection), dir, os.Environ(), ChangeReader{})
	if err != nil {
		t.Fatal(err)
	}
	selected := snapshot.Sources[0]
	if selected.ExitCode != 0 || !strings.HasPrefix(selected.Command, "'mcat'") || !strings.Contains(selected.Command, "'1:1'") || !strings.Contains(selected.Command, "'3:3'") || !strings.Contains(selected.Stdout, "first\n") || !strings.Contains(selected.Stdout, "third\n") || strings.Contains(selected.Stdout, "second\n") {
		t.Fatalf("quoted range selection: %+v", selected)
	}
	if snapshot.Sources[1].ExitCode == 0 || !strings.Contains(snapshot.Sources[1].Stderr, "16-read limit") || snapshot.Sources[2].ExitCode == 0 {
		t.Fatalf("invalid ranges accepted: %+v", snapshot.Sources)
	}
}

func TestReloadNestedSelectedText(t *testing.T) {
	testMcat(t)
	dir := t.TempDir()
	for name, body := range map[string]string{
		"state.json": "{\"next\":\"@child\"}\n@not-selected\n",
		"child":      "child body @leaf @AGENTS.md", "leaf": "leaf body @too-deep",
		"too-deep": "FORBIDDEN_DEPTH", "AGENTS.md": "HOST_ONLY", "not-selected": "UNSELECTED",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	selection, _ := json.Marshal(map[string]any{"sources": []Source{{Path: "state.json", Ranges: [][2]int{{1, 1}}}, {Path: "missing"}}})
	snapshot, err := ReadSources(t.Context(), string(selection), dir, os.Environ(), ChangeReader{})
	if err != nil {
		t.Fatal(err)
	}
	body := snapshot.Sources[0].Text()
	if !strings.Contains(body, "child body") || !strings.Contains(body, "leaf body") || !strings.Contains(body, "depth limit 3 exceeded") || snapshot.Sources[0].ExitCode == 0 || snapshot.Sources[1].ExitCode == 0 {
		t.Fatal(body)
	}
	for _, excluded := range []string{"FORBIDDEN_DEPTH", "UNSELECTED", "HOST_ONLY"} {
		if strings.Contains(body, excluded) {
			t.Fatal(body)
		}
	}
	if snapshot.Selection != string(selection) || len(snapshot.Sources) != 2 {
		t.Fatal("root selection identity changed")
	}
}

func TestReloadSkillReferences(t *testing.T) {
	testMcat(t)
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "references"), 0700); err != nil {
		t.Fatal(err)
	}
	reference := filepath.Join(dir, "references", "guide.md")
	if err := os.WriteFile(reference, []byte("first\nselected reference\n"), 0600); err != nil {
		t.Fatal(err)
	}
	manager := "#!/bin/sh\ntest \"$1 $2 $3 $4\" = 'get --codex chosen/references/guide.md 2:2' || exit 2\nprintf 'managed reference\\n'\n"
	if err := os.WriteFile(filepath.Join(dir, "skills-mgr"), []byte(manager), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, source := range []Source{
		{Skill: "chosen/references/guide.md", Ranges: [][2]int{{2, 2}}},
		{Skill: "chosen", Path: reference, Ranges: [][2]int{{2, 2}}},
		{Skill: "chosen/references/guide.md", Path: filepath.Join(dir, "SKILL.md"), Ranges: [][2]int{{2, 2}}},
	} {
		selection, _ := json.Marshal(map[string]any{"sources": []Source{source}})
		snapshot, err := ReadSources(t.Context(), string(selection), dir, os.Environ(), ChangeReader{})
		if err != nil || snapshot.Sources[0].ExitCode != 0 || !strings.Contains(snapshot.Sources[0].Stdout, "reference") || strings.Contains(snapshot.Sources[0].Stdout, "first") {
			t.Fatalf("%+v: %+v %v", source, snapshot, err)
		}
	}
	for _, skill := range []string{"chosen/../secret", "chosen//reference", "chosen/./reference", "-flag", "/absolute"} {
		selection, _ := json.Marshal(map[string]any{"sources": []Source{{Skill: skill}}})
		commands, err := ReadCommands(string(selection))
		if err != nil || !strings.Contains(commands[0], "CONTENT NOT ATTACHED") {
			t.Fatalf("unsafe skill %q: %v %v", skill, commands, err)
		}
	}
}

func TestReloadNestedShallowerBranch(t *testing.T) {
	testMcat(t)
	dir := t.TempDir()
	for name, body := range map[string]string{"root": "@a @b", "a": "@b", "b": "branch body @c", "c": "reachable leaf"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := ReadSources(t.Context(), `{"sources":[{"path":"root"}]}`, dir, os.Environ(), ChangeReader{})
	if err != nil {
		t.Fatal(err)
	}
	body := snapshot.Sources[0].Stdout
	if !strings.Contains(body, "reachable leaf") || strings.Count(body, "branch body") != 1 {
		t.Fatal(body)
	}
}

func TestReloadManagedNestedWorkspaceAndNativeHostExclusion(t *testing.T) {
	testMcat(t)
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "placeholder"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"placeholder/SKILL.md": "metadata", "placeholder/.skills-mgr-placeholder": "",
		"placeholder/child": "WRONG body", "child": "workspace body",
		"skills-mgr": "#!/bin/sh\nprintf '@child'\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	selection, _ := json.Marshal(map[string]any{"sources": []Source{
		{Skill: "chosen", Path: "placeholder/SKILL.md"},
		{Skill: "chosen/references/AGENTS.md", Path: "SKILL.md"},
	}})
	snapshot, err := ReadSources(t.Context(), string(selection), dir, os.Environ(), ChangeReader{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(snapshot.Sources[0].Stdout, "workspace body") || strings.Contains(snapshot.Sources[0].Stdout, "WRONG body") || snapshot.Sources[1] != (CapturedSource{}) {
		t.Fatalf("%+v", snapshot)
	}
}
