package executionobservation

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/yusing/mekugi/modules/changehistory/changes"

	"github.com/yusing/mekugi/libraries/commandargs"

	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/outputbudget/tokenizer"
)

func TestMChangesFrontendReadsAcrossAgentsAndPages(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	replayDirectory := registry.ReplayDirectory
	directory := replayDirectory
	store, err := openMekugiReplayStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	ctx, release, err := store.beginSession(t.Context(), "reviewer-thread", "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	workspace := t.TempDir()
	invocation := newShellWorkerTestInvocation(workspace,
		"XDG_STATE_HOME="+t.TempDir(), "MEKUGI_RUNTIME_DIR="+t.TempDir(), "CODEX_THREAD_ID=reviewer-thread",
		routerTestWorkerUnscopedEnvironment+"=0")
	id, err := store.changes.Reserve(ctx, store.scoped(ctx).changeScope(), workspace, "implementer-thread", "edited")
	if err != nil {
		t.Fatal(err)
	}
	history := mekugiHistory{
		ChangeID: id, CorrelationID: "edited", ReviewFiles: []diffmodel.ReviewFile{
			{AfterPath: "file.txt", Diff: "add \"\" -> \"file.txt\"\n--- /dev/null\n+++ \"file.txt\"\n@@ -0,0 +1,12 @@\n" + strings.Repeat("+line π changed\n", 12)},
			{BeforePath: "old name.txt", AfterPath: "new name.txt", Diff: "move \"old name.txt\" -> \"new name.txt\"\n"},
			{AfterPath: "--summary", Diff: "add \"\" -> \"--summary\"\n"},
			{AfterPath: "apple2", Diff: "add \"\" -> \"apple2\"\n"},
			{AfterPath: "excluded.txt", Diff: "add \"\" -> \"excluded.txt\"\n"},
		},
	}
	if err := store.put(ctx, workspace, map[string]mekugiHistory{"edited": history}); err != nil {
		t.Fatal(err)
	}
	if stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil,
		"mchanges --list", nil, invocation); status != 0 || stderr != "" || stdout != "" {
		t.Fatalf("other thread's changes leaked into list: %q, %q, %d", stdout, stderr, status)
	}
	ownID, err := store.changes.Reserve(ctx, store.scoped(ctx).changeScope(), workspace, "reviewer-thread", "own-edit")
	if err != nil {
		t.Fatal(err)
	}
	if stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil,
		"mchanges --list", nil, invocation); status != 0 || stderr != "" || stdout != ownID+" pending\n" {
		t.Fatalf("own pending change not discoverable: %q, %q, %d", stdout, stderr, status)
	}
	frontend, ok := registry.Frontends["mchanges"]
	if !ok {
		t.Fatal("mchanges frontend is unavailable")
	}
	command := exec.CommandContext(t.Context(), frontend, "--summary", id, "--", "file.txt")
	command.Dir = workspace
	command.Env = append(os.Environ(), "XDG_STATE_HOME="+t.TempDir(), "MEKUGI_RUNTIME_DIR="+t.TempDir(), "CODEX_THREAD_ID=reviewer-thread", routerTestWorkerEnvironment+"=1")
	var directOut, directErr bytes.Buffer
	command.Stdout, command.Stderr = &directOut, &directErr
	if err := command.Run(); err != nil || directOut.String() != "A\t12\t0\tfile.txt\n" || directErr.Len() != 0 {
		t.Fatalf("stock frontend: output %q, stderr %q, error %v", directOut.String(), directErr.String(), err)
	}
	want, err := store.changes.Read(ctx, store.scoped(ctx).changeScope(), changes.Query{Workspace: workspace, IDs: []string{id}, Paths: []string{"file.txt", "new name.txt"}, View: "net"})
	if err != nil {
		t.Fatal(err)
	}
	codec, err := tokenizer.New()
	if err != nil {
		t.Fatal(err)
	}
	for _, interpreter := range []string{"bash", "sh"} {
		t.Run(interpreter, func(t *testing.T) {
			t.Parallel()
			var all strings.Builder
			cursor := ""
			for page := range 100 {
				command := "mchanges --max-tokens 32 " + id + " -- 'old name.txt' file.txt 'new name.txt' ./file.txt"
				if cursor != "" {
					command = "mread " + cursor + " --stdout --max-tokens 32"
				}
				stdout, stderr, status := runShellWorkerTest(t, registry, interpreter, nil, command, nil, invocation)
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
				const notice = "read: incomplete; next_call: mread "
				if !strings.HasPrefix(stderr, notice) || stdout == "" || page == 99 {
					t.Fatalf("read failed: %q, %q, %d", stdout, stderr, status)
				}
				cursor = strings.Fields(strings.TrimPrefix(stderr, notice))[0]
			}
		})
	}
	if err := os.Mkdir(filepath.Join(workspace, "child"), 0700); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil,
		"cd child\nmchanges --summary "+id, nil, invocation)
	if status != 0 || stderr != "" || !strings.Contains(stdout, "A\t12\t0\tfile.txt\n") || strings.Contains(stdout, "+line") {
		t.Fatalf("summary from subdirectory: %q, %q, %d", stdout, stderr, status)
	}
	for _, command := range []string{
		"mchanges " + id + " --summary -- file.txt",
		"mchanges --summary " + id + " -- " + filepath.Join(workspace, "file.txt"),
		"mchanges " + id + " --summary " + id + " -- ./file.txt",
	} {
		stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil, command, nil, invocation)
		if status != 0 || stderr != "" || stdout != "A\t12\t0\tfile.txt\n" {
			t.Fatalf("mixed flags: %q: %q, %q, %d", command, stdout, stderr, status)
		}
	}
	for _, interpreter := range []string{"bash", "sh"} {
		for _, view := range []string{"", "--summary", "--history"} {
			filtered, err := store.changes.Read(ctx, store.scoped(ctx).changeScope(), changes.Query{
				Workspace: workspace, IDs: []string{id}, Paths: []string{"file.txt", "new name.txt"},
				View: map[string]string{"": "net", "--summary": "summary", "--history": "history"}[view],
			})
			if err != nil {
				t.Fatal(err)
			}
			command := "mchanges " + id + " " + view +
				" -- 'old name.txt' " + commandargs.QuoteArgument(filepath.Join(workspace, "file.txt")) +
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
		"mchanges "+id+" -- absent.txt 'also absent.txt'", nil, invocation)
	if status != 0 || stderr != "" || !strings.Contains(stdout, `no captured files match paths after -- in workspace`) {
		t.Fatalf("unmatched union: %q, %q, %d", stdout, stderr, status)
	}
	stdout, stderr, status = runShellWorkerTest(t, registry, "bash", nil,
		"mchanges "+id+" --summary -- missing.txt", nil, invocation)
	if status != 0 || stderr != "" || !strings.Contains(stdout, `no files match paths after --: "missing.txt"`) {
		t.Fatalf("unmatched path: %q, %q, %d", stdout, stderr, status)
	}
	stdout, stderr, status = runShellWorkerTest(t, registry, "sh", nil,
		"mchanges "+id+" --history --max-tokens 15500 -- file.txt", nil, invocation)
	if status != 0 || stderr != "" || !strings.Contains(stdout, "input:") ||
		strings.Count(stdout, "--- /dev/null") != 1 || strings.Contains(stdout, `add "" ->`) {
		t.Fatalf("history: %q, %q, %d", stdout, stderr, status)
	}
	stdout, stderr, status = runShellWorkerTest(t, registry, "bash", nil,
		"mchanges "+id+" -- --summary apple2", nil, invocation)
	if status != 0 || stderr != "" || stdout != "add \"\" -> \"--summary\"\nadd \"\" -> \"apple2\"\n" {
		t.Fatalf("literal flag and ID paths: %q, %q, %d", stdout, stderr, status)
	}
	for _, arguments := range []string{id + " --path file.txt", "amber1..apple2", "--max-tokens 0 amber1", "--history --summary amber1"} {
		stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil, "mchanges "+arguments, nil, invocation)
		if status == 0 || stdout != "" || stderr == "" {
			t.Fatalf("%q did not reject: %q, %q, %d", arguments, stdout, stderr, status)
		}
	}
}

func TestParseChangeRead(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	for _, arguments := range [][]string{
		{"amber1", "--summary", "--", "first", "--history", "apple2", "first"},
		{"--summary", "amber1", "--", "first", "--history", "apple2", "first"},
	} {
		options, err := testChanges().Parse(arguments, workspace)
		if err != nil || strings.Join(options.Paths, ",") != "first,--history,apple2,first" ||
			strings.Join(options.IDs, ",") != "amber1" || options.View != "summary" {
			t.Fatalf("paths after --: %+v, %v", options, err)
		}
	}
	for _, arguments := range [][]string{{"amber1"}, {"amber1", "--"}} {
		options, err := testChanges().Parse(arguments, workspace)
		if err != nil || len(options.Paths) != 0 || strings.Join(options.IDs, ",") != "amber1" || options.View != "net" {
			t.Fatalf("unfiltered read: %+v, %v", options, err)
		}
	}
	if options, err := testChanges().Parse([]string{"--list", "--max-tokens", "32"}, workspace); err != nil || options.View != "list" || len(options.IDs) != 0 {
		t.Fatalf("list options: %+v, %v", options, err)
	}
	if options, err := testChanges().Parse([]string{"--max-tokens", "15501", "amber1"}, workspace); err != nil || options.MaxTokens != commandargs.MaxOutputTokens {
		t.Fatalf("capped budget: %+v, %v", options, err)
	}
	options, err := testChanges().Parse([]string{"amber1..amber2", "--summary", "apple1", "--", "file"}, workspace)
	if err != nil || strings.Join(options.IDs, ",") != "amber1,amber2,apple1" {
		t.Fatalf("range and flags: %+v, %v", options, err)
	}
	for _, arguments := range [][]string{
		{"amber1", "--path", "file"}, {"amber1", "--", ""},
		{"--summary", "--summary", "amber1"}, {"--max-tokens", "01", "amber1"},
		{"--workspace", workspace, "amber1"},
		{"--cursor"}, {"--cursor", "", "amber1"}, {"--unknown", "amber1"},
		{"--list", "--", "file"}, {"--list", "--summary"},
	} {
		if _, err := testChanges().Parse(arguments, workspace); err == nil {
			t.Fatalf("accepted %q", arguments)
		}
	}
}

func TestChangesSummaryAggregatesEvaluations(t *testing.T) {
	t.Parallel()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	var ids []string
	for i, files := range [][]diffmodel.ReviewFile{
		{diffmodel.RenderReviewFile("file", "file", "old\n", "middle\n"), diffmodel.RenderReviewFile("", "empty", "", "")},
		{diffmodel.RenderReviewFile("file", "file", "middle\n", "last\nextra\n"), diffmodel.RenderIncompleteReviewFile("unknown", "unknown", "missing capture")},
		{diffmodel.RenderReviewFile("unknown", "unknown", "a\n", "b\n")},
	} {
		call := strconv.Itoa(i)
		id, err := store.changes.Reserve(t.Context(), store.scoped(t.Context()).changeScope(), workspace, "author", call)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		if err := store.put(t.Context(), workspace, map[string]mekugiHistory{call: {
			ChangeID: id, CorrelationID: call, ReviewFiles: files,
		}}); err != nil {
			t.Fatal(err)
		}
	}
	options := changes.Query{Workspace: workspace, IDs: ids, View: "summary"}
	got, err := store.changes.Read(t.Context(), store.scoped(t.Context()).changeScope(), options)
	if want := "M\t2\t1\tfile\nA\t0\t0\tempty\n?\t-\t-\tunknown\t\"missing capture\"\n"; err != nil || got != want {
		t.Fatalf("summary = %q, %v; want %q", got, err, want)
	}
	options.Paths = []string{"file"}
	if got, err := store.changes.Read(t.Context(), store.scoped(t.Context()).changeScope(), options); err != nil || got != "M\t2\t1\tfile\n" {
		t.Fatalf("filtered summary = %q, %v", got, err)
	}
	pending, err := store.changes.Reserve(t.Context(), store.scoped(t.Context()).changeScope(), workspace, "author", "pending")
	if err != nil {
		t.Fatal(err)
	}
	options.IDs = append(ids, pending)
	if got, err := store.changes.Read(t.Context(), store.scoped(t.Context()).changeScope(), options); err != nil || got != pending+" pending (no completed result)\nM\t2\t1\tfile\n" {
		t.Fatalf("pending summary = %q, %v", got, err)
	}
}

func TestChangesSummaryRecoveryAndRetiredHistory(t *testing.T) {
	t.Parallel()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	id, err := store.changes.Reserve(t.Context(), store.scoped(t.Context()).changeScope(), workspace, "author", "original")
	if err != nil {
		t.Fatal(err)
	}
	for i, history := range []mekugiHistory{
		{TranslationError: "rejected"},
		{ReviewFiles: []diffmodel.ReviewFile{diffmodel.RenderReviewFile("file", "file", "a\n", "b\n")}},
		{ReviewFiles: []diffmodel.ReviewFile{diffmodel.RenderReviewFile("file", "file", "b\n", "c\nd\n")}},
	} {
		history.ChangeID, history.CorrelationID = id, "original"
		if err := store.put(t.Context(), workspace, map[string]mekugiHistory{strconv.Itoa(i): history}); err != nil {
			t.Fatal(err)
		}
	}
	options := changes.Query{Workspace: workspace, IDs: []string{id}, View: "summary"}
	if got, err := store.changes.Read(t.Context(), store.scoped(t.Context()).changeScope(), options); err != nil || got != "M\t2\t1\tfile\n" {
		t.Fatalf("recovery summary = %q, %v", got, err)
	}
	index, err := store.changes.Inspect(store.changeScope(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	change := index.Changes[id]
	change.RetiredCalls = 1
	index.Changes[id] = change
	if err := store.locked(t.Context(), func(s *mekugiReplayStore) error { return writeChangeIndexFixture(s, index) }); err != nil {
		t.Fatal(err)
	}
	if got, err := store.changes.Read(t.Context(), store.changeScope(), options); err != nil || got != id+" retired (partial history)\nM\t2\t1\tfile\n" {
		t.Fatalf("retired summary = %q, %v", got, err)
	}
}
