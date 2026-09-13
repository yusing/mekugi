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
		ReviewFiles: []mekugi.ReviewFile{{AfterPath: "file.txt", Diff: "add \"\" -> \"file.txt\"\n--- /dev/null\n+++ \"file.txt\"\n@@ -0,0 +1,12 @@\n" + strings.Repeat("+line π changed\n", 12)}},
	}
	if err := store.put(t.Context(), workspace, map[string]mekugiHistory{"edited": history}); err != nil {
		t.Fatal(err)
	}
	// Child environment changes cannot redirect the authenticated store.
	if _, direct := registry.directBashExecCommand([]string{"bash", "hchanges read " + id}); direct {
		t.Fatal("hchanges escaped the private runner")
	}
	want, err := store.readChanges(t.Context(), changeReadOptions{workspace: workspace, ids: []string{id}})
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
				command := "hchanges read --max-tokens 32 "
				if cursor != "" {
					command += "--cursor " + cursor + " "
				}
				stdout, stderr, status := runShellWorkerTest(t, registry, interpreter, nil, command+id, nil, invocation)
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
				const notice = "hchanges: incomplete; repeat this read with --cursor "
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
		"cd child\nhchanges read --workspace .. --summary "+id, nil, invocation)
	if status != 0 || stderr != "" || !strings.Contains(stdout, `add "file.txt" +12 -0`) || strings.Contains(stdout, "+line") {
		t.Fatalf("summary from subdirectory: %q, %q, %d", stdout, stderr, status)
	}
	for _, command := range []string{
		"hchanges read " + id + " --summary --path file.txt",
		"hchanges read --summary " + id + " --path " + filepath.Join(workspace, "file.txt"),
		"hchanges read " + id + " --path ./file.txt --summary " + id,
	} {
		stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil, command, nil, invocation)
		if status != 0 || stderr != "" || stdout != id+" applied\nadd \"file.txt\" +12 -0\n" {
			t.Fatalf("mixed flags: %q: %q, %q, %d", command, stdout, stderr, status)
		}
	}
	stdout, stderr, status = runShellWorkerTest(t, registry, "bash", nil,
		"hchanges read "+id+" --summary --path missing.txt", nil, invocation)
	if status != 0 || stderr != "" || !strings.Contains(stdout, `no files match --path "missing.txt"`) {
		t.Fatalf("unmatched path: %q, %q, %d", stdout, stderr, status)
	}
	stdout, stderr, status = runShellWorkerTest(t, registry, "sh", nil,
		"hchanges read "+id+" --history --path file.txt --max-tokens 15500", nil, invocation)
	if status != 0 || stderr != "" || !strings.Contains(stdout, "input:") ||
		strings.Count(stdout, "--- /dev/null") != 1 || strings.Contains(stdout, `add "" ->`) {
		t.Fatalf("history: %q, %q, %d", stdout, stderr, status)
	}
	for _, arguments := range []string{"read hp_a99", "read hp_a1..hp_b2", "read --max-tokens 0 hp_a1", "read --history --summary hp_a1"} {
		stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil, "hchanges "+arguments, nil, invocation)
		if status == 0 || stdout != "" || stderr == "" {
			t.Fatalf("%q did not reject: %q, %q, %d", arguments, stdout, stderr, status)
		}
	}
}

func TestParseChangeRead(t *testing.T) {
	workspace := t.TempDir()
	for _, arguments := range [][]string{
		{}, {"write", "hp_a1"}, {"read"}, {"read", "--path", "", "hp_a1"},
		{"read", "--summary", "--summary", "hp_a1"}, {"read", "--max-tokens", "01", "hp_a1"},
		{"read", "--max-tokens", strconv.Itoa(hrunMaxTokens + 1), "hp_a1"},
		{"read", "--cursor"}, {"read", "--cursor", "", "hp_a1"}, {"read", "--unknown", "hp_a1"},
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
		got := changePathMatches(changeReadOptions{workspace: workspace, path: test.path}, test.recorded, test.retained)
		if got != test.want {
			t.Errorf("%+v: got %v", test, got)
		}
	}
	options, err := parseChangeRead([]string{"read", "hp_a1", "--workspace", ""}, workspace)
	if err != nil || options.workspace != "" {
		t.Fatalf("no-directory selection: %+v, %v", options, err)
	}
	if changePathMatches(options, "file.txt", false) {
		t.Fatal("empty selection unexpectedly matched")
	}
}
