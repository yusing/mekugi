package router

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/tiktoken-go/tokenizer"
	"github.com/yusing/mekugi"
)

func TestShellChangesReadAcrossAgentsAndPages(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	manifest, err := readToolWorkerManifest(filepath.Join(registry.SnapshotDir, toolPluginManifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	directory := manifest.ReplayDirectory
	store, err := openMekugiReplayStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	invocation := newShellWorkerTestInvocation(workspace,
		"XDG_STATE_HOME="+t.TempDir(), "MEKUGI_RUNTIME_DIR="+t.TempDir(), "CODEX_THREAD_ID=reviewer-thread")
	id, err := store.reserveChange(t.Context(), workspace, "implementer-thread", "edited")
	if err != nil {
		t.Fatal(err)
	}
	history := mekugiHistory{
		ChangeID: id, CorrelationID: "edited", Applied: true,
		ReviewFiles: []mekugi.ReviewFile{
			{AfterPath: "file.txt", Diff: "add \"\" -> \"file.txt\"\n--- /dev/null\n+++ \"file.txt\"\n@@ -0,0 +1,12 @@\n" + strings.Repeat("+line π changed\n", 12)},
			{BeforePath: "old name.txt", AfterPath: "new name.txt", Diff: "move \"old name.txt\" -> \"new name.txt\"\n"},
			{AfterPath: "--summary", Diff: "add \"\" -> \"--summary\"\n"},
			{AfterPath: "apple2", Diff: "add \"\" -> \"apple2\"\n"},
			{AfterPath: "excluded.txt", Diff: "add \"\" -> \"excluded.txt\"\n"},
		},
	}
	if err := store.put(t.Context(), workspace, map[string]mekugiHistory{"edited": history}); err != nil {
		t.Fatal(err)
	}
	// Child environment changes cannot redirect the authenticated store.
	if _, direct := registry.directBashExecCommand([]string{"bash", "hchanges " + id}); direct {
		t.Fatal("hchanges escaped the private runner")
	}
	want, err := store.readChanges(t.Context(), changeReadOptions{workspace: workspace, ids: []string{id}, paths: []string{"file.txt", "new name.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	codec, err := tokenizer.ForModel(tokenizer.GPT5)
	if err != nil {
		t.Fatal(err)
	}
	for _, interpreter := range []string{"bash", "sh"} {
		t.Run(interpreter, func(t *testing.T) {
			t.Parallel()
			var all strings.Builder
			cursor := ""
			for page := range 100 {
				command := "hchanges --max-tokens 32 " + id + " -- 'old name.txt' file.txt 'new name.txt' ./file.txt"
				if cursor != "" {
					command = "hread " + cursor + " --stdout --max-tokens 32"
				}
				stdout, stderr, status := runShellWorkerTest(t, registry, interpreter, nil, command, nil, invocation)
				if cursor != "" {
					stdout = strings.TrimSuffix(strings.TrimPrefix(stdout, "--- stdout [bytes] ---\n"), "\n")
				}
				count, err := codec.Count(stdout)
				if err != nil || count > 32 {
					t.Fatalf("page tokens = %d, %v", count, err)
				}
				all.WriteString(stdout)
				if status == 0 {
					if page < 2 {
						t.Fatal("fixture must exercise initial, intermediate, and final pages")
					}
					if stderr != "" || all.String() != want {
						t.Fatalf("pages differ: got %q stderr %q; want %q", all.String(), stderr, want)
					}
					break
				}
				const notice = "read: incomplete; next_call: hread "
				if !strings.HasPrefix(stderr, notice) || stdout == "" || page == 99 {
					t.Fatalf("read failed: %q, %q, %d", stdout, stderr, status)
				}
				cursor = strings.TrimSpace(strings.TrimPrefix(stderr, notice))
			}
		})
	}
	if err := os.Mkdir(filepath.Join(workspace, "child"), 0700); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil,
		"cd child\nhchanges --workspace .. --summary "+id, nil, invocation)
	if status != 0 || stderr != "" || !strings.Contains(stdout, "| 12 ++++++++++++") || strings.Contains(stdout, "+line") {
		t.Fatalf("summary from subdirectory: %q, %q, %d", stdout, stderr, status)
	}
	for _, command := range []string{
		"hchanges " + id + " --summary -- file.txt",
		"hchanges --summary " + id + " -- " + filepath.Join(workspace, "file.txt"),
		"hchanges " + id + " --summary " + id + " -- ./file.txt",
	} {
		stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil, command, nil, invocation)
		if status != 0 || stderr != "" || stdout != id+" applied\n file.txt | 12 ++++++++++++\n 1 file changed, 12 insertions(+)\n" {
			t.Fatalf("mixed flags: %q: %q, %q, %d", command, stdout, stderr, status)
		}
	}
	for _, interpreter := range []string{"bash", "sh"} {
		for _, view := range []string{"", "--summary", "--history"} {
			filtered, err := store.readChanges(t.Context(), changeReadOptions{
				workspace: workspace, ids: []string{id}, paths: []string{"file.txt", "new name.txt"},
				view: strings.TrimPrefix(view, "--"),
			})
			if err != nil {
				t.Fatal(err)
			}
			command := "hchanges " + id + " " + view +
				" -- 'old name.txt' " + shellQuoteArgument(filepath.Join(workspace, "file.txt")) +
				" ./file.txt 'new name.txt' absent.txt"
			stdout, stderr, status := runShellWorkerTest(t, registry, interpreter, nil, command, nil, invocation)
			move := `move "old name.txt"`
			if view == "--summary" {
				move = "old name.txt => new name.txt"
			}
			if status != 0 || stderr != "" || stdout != filtered ||
				strings.Contains(stdout, "excluded.txt") || !strings.Contains(stdout, "file.txt") ||
				strings.Index(stdout, "file.txt") > strings.Index(stdout, move) ||
				strings.Count(stdout, move) != 1 {
				t.Fatalf("union %s %s: %q, %q, %d", interpreter, view, stdout, stderr, status)
			}
		}
	}
	stdout, stderr, status = runShellWorkerTest(t, registry, "sh", nil,
		"hchanges "+id+" -- absent.txt 'also absent.txt'", nil, invocation)
	if status != 0 || stderr != "" || !strings.Contains(stdout, `no files match paths after --: "absent.txt" "also absent.txt"`) {
		t.Fatalf("unmatched union: %q, %q, %d", stdout, stderr, status)
	}
	stdout, stderr, status = runShellWorkerTest(t, registry, "bash", nil,
		"hchanges "+id+" --summary -- missing.txt", nil, invocation)
	if status != 0 || stderr != "" || !strings.Contains(stdout, `no files match paths after --: "missing.txt"`) {
		t.Fatalf("unmatched path: %q, %q, %d", stdout, stderr, status)
	}
	stdout, stderr, status = runShellWorkerTest(t, registry, "sh", nil,
		"hchanges "+id+" --history --max-tokens 15500 -- file.txt", nil, invocation)
	if status != 0 || stderr != "" || !strings.Contains(stdout, "input:") ||
		strings.Count(stdout, "--- /dev/null") != 1 || strings.Contains(stdout, `add "" ->`) {
		t.Fatalf("history: %q, %q, %d", stdout, stderr, status)
	}
	stdout, stderr, status = runShellWorkerTest(t, registry, "bash", nil,
		"hchanges "+id+" -- --summary apple2", nil, invocation)
	if status != 0 || stderr != "" || stdout != id+" applied\nadd \"\" -> \"--summary\"\nadd \"\" -> \"apple2\"\n" {
		t.Fatalf("literal flag and ID paths: %q, %q, %d", stdout, stderr, status)
	}
	for _, arguments := range []string{"", "-- file.txt", "--summary -- file.txt", "read " + id, id + " --path file.txt", "amber99", "amber1..apple2", "--max-tokens 0 amber1", "--history --summary amber1"} {
		stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil, "hchanges "+arguments, nil, invocation)
		if status == 0 || stdout != "" || stderr == "" {
			t.Fatalf("%q did not reject: %q, %q, %d", arguments, stdout, stderr, status)
		}
	}
}

func TestParseChangeRead(t *testing.T) {
	workspace := t.TempDir()
	for _, arguments := range [][]string{
		{"amber1", "--summary", "--", "first", "--history", "apple2", "first"},
		{"--summary", "amber1", "--", "first", "--history", "apple2", "first"},
	} {
		options, err := parseChangeRead(arguments, workspace)
		if err != nil || strings.Join(options.paths, ",") != "first,--history,apple2,first" ||
			strings.Join(options.ids, ",") != "amber1" || options.view != "summary" {
			t.Fatalf("paths after --: %+v, %v", options, err)
		}
	}
	for _, arguments := range [][]string{{"amber1"}, {"amber1", "--"}} {
		options, err := parseChangeRead(arguments, workspace)
		if err != nil || len(options.paths) != 0 || strings.Join(options.ids, ",") != "amber1" {
			t.Fatalf("unfiltered read: %+v, %v", options, err)
		}
	}
	options, err := parseChangeRead([]string{"amber1..amber2", "--summary", "apple1", "--", "file"}, workspace)
	if err != nil || strings.Join(options.ids, ",") != "amber1,amber2,apple1" {
		t.Fatalf("range and flags: %+v, %v", options, err)
	}
	for _, arguments := range [][]string{
		{}, {"--"}, {"--", "amber1"}, {"--summary"}, {"--summary", "--", "file"},
		{"read", "amber1"}, {"amber1", "--path", "file"}, {"amber1", "--", ""},
		{"--summary", "--summary", "amber1"}, {"--max-tokens", "01", "amber1"},
		{"--max-tokens", strconv.Itoa(hrunMaxTokens + 1), "amber1"},
		{"--workspace", workspace, "--workspace", workspace, "amber1"},
		{"--cursor"}, {"--cursor", "", "amber1"}, {"--unknown", "amber1"},
	} {
		if _, err := parseChangeRead(arguments, workspace); err == nil {
			t.Fatalf("accepted %q", arguments)
		}
	}
}

func TestChangePathSpellings(t *testing.T) {
	workspace := t.TempDir()
	for _, test := range []struct {
		path, recorded string
		retained, want bool
	}{
		{"file.txt", filepath.Join(workspace, "file.txt"), false, true},
		{filepath.Join(workspace, "file.txt"), "file.txt", false, true},
		{"./file.txt", "file.txt", false, true},
		{"other.txt", "file.txt", false, false},
		{"file.txt", "", false, false},
		{"./script", "script", true, false},
		{"script", "script", true, true},
	} {
		got := changePathMatches(changeReadOptions{workspace: workspace, paths: []string{test.path}}, test.recorded, test.retained)
		if got != test.want {
			t.Errorf("%+v: got %v", test, got)
		}
	}
	if !changePathMatches(changeReadOptions{workspace: workspace, paths: []string{"other", "script"}}, "script", true) ||
		changePathMatches(changeReadOptions{workspace: workspace, paths: []string{"./script", "/script"}}, "script", true) {
		t.Fatal("repeated retained-script paths must keep exact matching")
	}
	if changePathMatches(changeReadOptions{paths: []string{"./file.txt", "/file.txt"}}, "file.txt", false) {
		t.Fatal("no-directory selection must not invent a path base")
	}
	options, err := parseChangeRead([]string{"amber1", "--workspace", ""}, workspace)
	if err != nil || options.workspace != "" {
		t.Fatalf("no-directory selection: %+v, %v", options, err)
	}
	if changePathMatches(options, "file.txt", false) {
		t.Fatal("empty selection unexpectedly matched")
	}
}
