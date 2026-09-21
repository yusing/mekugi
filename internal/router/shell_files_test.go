package router

import (
	"context"
	"errors"
	"io"
	"mvdan.cc/sh/v3/syntax"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/router/toolplugin"
	"mvdan.cc/sh/v3/interp"
)

func TestShellFileOperationsHistoryAndLiveDiff(t *testing.T) {
	registry := sharedProxyTestRegistry(t)
	manifest, err := readToolWorkerManifest(filepath.Join(registry.SnapshotDir, toolPluginManifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	store, err := openMekugiReplayStore(manifest.ReplayDirectory)
	if err != nil {
		t.Fatal(err)
	}
	directory, thread := t.TempDir(), "shell-files"
	ctx, release, err := store.beginSession(t.Context(), thread, "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	_, events, _ := liveDiffTestBroker(t, store, liveDiffScope{Workspaces: map[string]map[string]bool{directory: {thread: true}}})
	sub := events.subscribe()
	broker := newCommentaryBroker()
	broker.editPublisher = func(ctx context.Context, workspace, thread, callID string) error {
		session, release, err := store.beginSession(ctx, thread, "")
		if err != nil {
			return err
		}
		defer release()
		return store.publishEditReceipt(session, workspace, thread, callID, nil)
	}
	server := httptest.NewServer(http.HandlerFunc(broker.serveHTTP))
	defer server.Close()
	token := broker.subscribeThread(directory+"\x00session", thread, "/root")
	sink := &httpShellCommentarySink{endpoint: server.URL, token: token, client: server.Client()}
	shell, _ := registry.contribution("shell")
	run := func(command string) toolplugin.ExecutionOutput {
		t.Helper()
		environment := prependToolFrontendPath(append(os.Environ(),
			"CODEX_THREAD_ID="+thread, routerTestWorkerEnvironment+"=1"), registry.frontendDirectory)
		result, err := executeShellTool(ctx, manifest, registry.RuntimeRoot, &shell,
			[]string{"bash", command}, nil, directory,
			environment, sink, nil, nil)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("%s: %+v, %v", command, result, err)
		}
		return result
	}
	var ids []string
	for _, test := range []struct{ command, diff string }{
		{"touch empty", `add "" -> "`},
		{"cat > source <<'DATA'\nold\nDATA\n", "+old"},
		{"cat >> source <<'DATA'\nappend\nDATA\n", "+append"},
		{"cat > source <<'DATA'\nnew\nDATA\n", "-old"},
		{"mv source moved", "move "},
		{"rm moved", "-new"},
		{"mkdir tree; touch tree/empty; mv tree renamed", "move "},
		{"rm -rf renamed", "delete "},
	} {
		result := run(test.command)
		var lastID string
		for line := range strings.SplitSeq(result.Stderr, "\n") {
			if id, ok := strings.CutPrefix(line, "change "); ok {
				ids = append(ids, id)
				lastID = id
				event := waitLiveDiffChange(t, sub, true)
				if event.ID != id || event.Thread != thread {
					t.Fatalf("event=%+v, want %s/%s", event, id, thread)
				}
			}
		}
		if lastID == "" {
			t.Fatalf("no change notice: %+v", result)
		}
		review := run("mchanges " + lastID + " --history")
		if !strings.Contains(review.Stdout, test.diff) || !strings.Contains(review.Stdout, "shell input:") {
			t.Fatalf("%s: %s", test.command, review.Stdout)
		}
	}
	// Reopen the durable store, change the current file outside the agent, and
	// prove history still describes only the captured agent operations.
	if err := os.WriteFile(filepath.Join(directory, "source"), []byte("external"), 0600); err != nil {
		t.Fatal(err)
	}
	restarted, err := openMekugiReplayStore(manifest.ReplayDirectory)
	if err != nil {
		t.Fatal(err)
	}
	review, err := restarted.readChanges(ctx, changeReadOptions{workspace: directory, ids: ids})
	if err != nil || !strings.Contains(review, "-old") || strings.Contains(review, "external") {
		t.Fatalf("durable review: %s, %v", review, err)
	}
	files, err := restarted.liveDiffSnapshotFiles(ctx, liveDiffScope{Workspaces: map[string]map[string]bool{directory: {thread: true}}})
	if err != nil || len(files) == 0 {
		t.Fatalf("live view missing captured operations: %+v, %v", files, err)
	}
}

func TestShellCatCommittedActivityClassifiesCreateAndEdit(t *testing.T) {
	registry := sharedProxyTestRegistry(t)
	manifest, err := readToolWorkerManifest(filepath.Join(registry.SnapshotDir, toolPluginManifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	store, err := openMekugiReplayStore(manifest.ReplayDirectory)
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	const root, child = "cat-root", "cat-child"
	activity := newSubagentActivity()
	if !activity.observe(root, "", "/root", false) || !activity.observe(child, root, "/root/writer", true) {
		t.Fatal("failed to establish activity ancestry")
	}
	broker := newCommentaryBroker()
	broker.editPublisher = func(ctx context.Context, workspace, thread, callID string) error {
		return store.publishEditReceipt(ctx, workspace, thread, callID, activity)
	}
	server := httptest.NewServer(http.HandlerFunc(broker.serveHTTP))
	defer server.Close()
	sink := &httpShellCommentarySink{
		endpoint: server.URL,
		token:    broker.subscribeThread(workspace+"\x00cat", child, "/root/writer"),
		client:   server.Client(),
	}
	shell, _ := registry.contribution("shell")
	run := func(source, wantAction string) {
		t.Helper()
		result, err := executeShellTool(t.Context(), manifest, registry.RuntimeRoot, &shell,
			[]string{"bash", source}, nil, workspace,
			append(os.Environ(), "CODEX_THREAD_ID="+child), sink, nil, nil)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("execution=%+v err=%v", result, err)
		}
		messages := activity.drain(root, time.Time{}, maxCommentaryPublicationBytes)
		if len(messages) != 1 {
			t.Fatalf("activity messages=%d for %q", len(messages), source)
		}
		text := commentaryText(t, messages[0])
		if !strings.Contains(text, wantAction+" `target.txt`") {
			t.Fatalf("activity=%q; want %s label", text, wantAction)
		}
		var id string
		for line := range strings.SplitSeq(result.Stderr, "\n") {
			if value, ok := strings.CutPrefix(line, "change "); ok {
				id = value
			}
		}
		index, readErr := store.readChangeIndex(workspace)
		if readErr != nil || id == "" || len(index.Changes[id].Calls) != 1 {
			t.Fatalf("change index=%+v id=%q err=%v", index.Changes[id], id, readErr)
		}
		record, found, readErr := store.read(workspace, index.Changes[id].Calls[0].ID, false)
		if readErr != nil || !found || len(record.History.ReviewFiles) != 1 || record.History.ReviewFiles[0].Action().Title() != wantAction {
			t.Fatalf("durable evidence=%+v found=%t err=%v", record.History.ReviewFiles, found, readErr)
		}
		summary, readErr := store.readChanges(t.Context(), changeReadOptions{workspace: workspace, ids: []string{id}, view: "summary"})
		if readErr != nil || !strings.Contains(summary, "target.txt") {
			t.Fatalf("mchanges summary=%q; want classified target: %v", summary, readErr)
		}
	}

	run("cat >target.txt <<'DATA'\none\nDATA\n", "Create")
	run("cat >>target.txt <<'DATA'\ntwo\nDATA\n", "Edit")
	run("cat >target.txt <<'DATA'\nthree\nDATA\n", "Edit")
	failed, err := executeShellTool(t.Context(), manifest, registry.RuntimeRoot, &shell,
		[]string{"bash", "cat >missing/target.txt <<'DATA'\nnope\nDATA\n"}, nil, workspace,
		append(os.Environ(), "CODEX_THREAD_ID="+child), sink, nil, nil)
	if err != nil || failed.ExitCode == 0 {
		t.Fatalf("failed redirect execution=%+v err=%v", failed, err)
	}
	if messages := activity.drain(root, time.Time{}, maxCommentaryPublicationBytes); len(messages) != 0 {
		t.Fatalf("failed redirect published %d successful changes", len(messages))
	}
}

func TestShellFileWritesPreserveBytesInodesAndProgramData(t *testing.T) {
	registry := sharedProxyTestRegistry(t)
	for _, interpreter := range []string{"bash", "sh"} {
		t.Run(interpreter, func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, "file")
			if err := os.WriteFile(path, []byte("before"), 0640); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(path, filepath.Join(directory, "hard")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("file", filepath.Join(directory, "sym")); err != nil {
				t.Fatal(err)
			}
			before, _ := os.Stat(path)
			stdout, stderr, code := runShellWorkerTest(t, registry, interpreter, nil,
				`value=$(printf 'a\r\nb' | cat > sym; printf clean); printf '%s' "$value"; cat file >> hard`,
				nil, newShellWorkerTestInvocation(directory))
			// cat rejects self-copy. Use a different source for the append below.
			if stdout != "clean" || code == 0 {
				t.Fatalf("program data/exit changed: %d %q %q", code, stdout, stderr)
			}
			content, err := os.ReadFile(path)
			after, _ := os.Stat(path)
			if err != nil || string(content) != "a\r\nb" || !os.SameFile(before, after) || after.Mode().Perm() != 0640 {
				t.Fatalf("inode/bytes/mode: %q %v %v", content, after, err)
			}
			stdout, stderr, code = runShellWorkerTest(t, registry, interpreter, nil,
				`printf '\000tail' | cat >> hard; cat file`, nil, newShellWorkerTestInvocation(directory))
			if code != 0 || stdout != "a\r\nb\x00tail" || !strings.Contains(stderr, "change ") {
				t.Fatalf("append: %d %q %q", code, stdout, stderr)
			}
		})
	}
}

func TestShellFilePartialFailuresAndOverwriteMove(t *testing.T) {
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	for name, content := range map[string]string{"source": "new\n", "target": "old\n", "remove": "deleted\n"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	invocation := newShellWorkerTestInvocation(directory)
	_, stderr, code := runShellWorkerTest(t, registry, "bash", nil,
		"mv source target; rm remove missing", nil, invocation)
	if code == 0 {
		t.Fatalf("missing rm failure: %s", stderr)
	}
	var ids []string
	for line := range strings.SplitSeq(stderr, "\n") {
		if id, ok := strings.CutPrefix(line, "change "); ok {
			ids = append(ids, id)
		}
	}
	if len(ids) != 2 {
		t.Fatalf("lost partial results: %s", stderr)
	}
	review, readErr, code := runShellWorkerTest(t, registry, "bash", nil, "mchanges "+strings.Join(ids, " "), nil, invocation)
	if code != 0 || !strings.Contains(review, "-old") || !strings.Contains(review, "move ") || !strings.Contains(review, "-deleted") {
		t.Fatalf("partial review: %d %s %s", code, review, readErr)
	}
	content, _ := os.ReadFile(filepath.Join(directory, "target"))
	if string(content) != "new\n" {
		t.Fatalf("move destination=%q", content)
	}
}

func TestShellFileRedirectionLifecycle(t *testing.T) {
	registry := sharedProxyTestRegistry(t)
	for _, command := range []string{
		"exec >out; printf done",
		"false >out",
		"cat >out </missing/input",
		"printf a >out >other",
		"printf a >out & printf b >other & wait",
	} {
		t.Run(command, func(t *testing.T) {
			directory := t.TempDir()
			_, stderr, _ := runShellWorkerTest(t, registry, "bash", nil, command, nil, newShellWorkerTestInvocation(directory))
			if !strings.Contains(stderr, "change ") {
				t.Fatalf("lost redirection effects: %s", stderr)
			}
		})
	}
}

func TestShellFileRejectedOperandsHaveNoEffects(t *testing.T) {
	registry := sharedProxyTestRegistry(t)
	for _, command := range []string{
		"rm -rf .", "rm -rf child/..", "rm -rf child/.",
		"rm -n sentinel", "rm -T sentinel", "rm -t child sentinel",
		"mv -r sentinel moved", "mv -t missing sentinel", "mv -T -t child sentinel",
		"mv -t '' sentinel child/keep", "mv --target-directory= sentinel child/keep",
	} {
		t.Run(command, func(t *testing.T) {
			directory := t.TempDir()
			if err := os.Mkdir(filepath.Join(directory, "child"), 0700); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"sentinel", "child/keep"} {
				if err := os.WriteFile(filepath.Join(directory, name), []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			_, stderr, code := runShellWorkerTest(t, registry, "bash", nil, command, nil, newShellWorkerTestInvocation(directory))
			if code == 0 || strings.Contains(stderr, "change ") {
				t.Fatalf("invalid operation accepted: %d %s", code, stderr)
			}
			for _, name := range []string{"sentinel", "child/keep"} {
				content, err := os.ReadFile(filepath.Join(directory, name))
				if err != nil || string(content) != "keep" {
					t.Fatalf("rejected operation changed %s: %q, %v", name, content, err)
				}
			}
		})
	}
	// Validate root identity without ever invoking a deletion primitive on it.
	for _, path := range []string{"/", "/tmp/.."} {
		if _, err := shellRemovalInfo(path); err == nil {
			t.Fatalf("root operand accepted: %s", path)
		}
	}
}

func TestShellFileGroupedRedirectionOrder(t *testing.T) {
	registry := sharedProxyTestRegistry(t)
	for _, command := range []string{
		"{ printf new; mv out renamed; printf tail; } >out",
		"{ printf new; rm out; printf discarded; } >out",
		"{ printf new; mv link/../file renamed; printf tail; } >real/file",
		"{ printf new; rm link/../file; printf discarded; } >real/file",
	} {
		t.Run(command, func(t *testing.T) {
			directory := t.TempDir()
			if err := os.MkdirAll(filepath.Join(directory, "real/sub"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("real/sub", filepath.Join(directory, "link")); err != nil {
				t.Fatal(err)
			}
			oldName := "out"
			if strings.Contains(command, "real/file") {
				oldName = "real/file"
			}
			invocation := newShellWorkerTestInvocation(directory, "CODEX_THREAD_ID=grouped-redirection",
				routerTestWorkerUnscopedEnvironment+"=0")
			_, stderr, code := runShellWorkerTest(t, registry, "bash", nil, command, nil, invocation)
			if code != 0 {
				t.Fatalf("%d %s", code, stderr)
			}
			var ids []string
			for line := range strings.SplitSeq(stderr, "\n") {
				if id, ok := strings.CutPrefix(line, "change "); ok {
					ids = append(ids, id)
				}
			}
			want := 2
			if strings.Contains(command, "mv ") {
				want = 3
			}
			if len(ids) != want {
				t.Fatalf("unordered descriptor effects: %s", stderr)
			}
			review, readErr, code := runShellWorkerTest(t, registry, "bash", nil, "mchanges "+strings.Join(ids, " "), nil, invocation)
			if code != 0 || strings.Count(review, `+++ "`+directory+"/"+oldName+`"`) != 1 || strings.Contains(review, "+discarded") {
				t.Fatalf("descriptor history: %d %s %s", code, review, readErr)
			}
			manifest, err := readToolWorkerManifest(filepath.Join(registry.SnapshotDir, toolPluginManifestFilename))
			if err != nil {
				t.Fatal(err)
			}
			store, err := openMekugiReplayStore(manifest.ReplayDirectory)
			if err != nil {
				t.Fatal(err)
			}
			files, err := store.liveDiffSnapshotFiles(t.Context(), liveDiffScope{Workspaces: map[string]map[string]bool{directory: {"grouped-redirection": true}}})
			if err != nil {
				t.Fatal(err)
			}
			for _, file := range files {
				if file.Path != filepath.Join(directory, oldName) && file.Path != filepath.Join(directory, "renamed") {
					t.Fatalf("live diff grouped a lexical rather than resolved path: %s", file.Path)
				}
			}
			if _, err := os.Stat(filepath.Join(directory, oldName)); !os.IsNotExist(err) {
				t.Fatalf("old path exists: %v", err)
			}
		})
	}
}

func TestShellFileCommandsUseTrackedCarrier(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	transform, _, _, _ := newMekugiTestTransformWithProxy(t, proxy)
	directory := t.TempDir()
	transform.directory = directory
	contribution, _ := proxy.registry.contribution("shell")
	for _, source := range []string{
		"touch empty", "touch -- -notes", "mv source target", "rm file",
		"cat input >out", "cat input >>out", "cat input >|out",
	} {
		t.Run(source, func(t *testing.T) {
			if command, direct := proxy.registry.directBashExecCommand([]string{"bash", source}); direct {
				t.Fatalf("tracking bypass: %s", command)
			}
			workspace := t.TempDir()
			for _, name := range []string{"source", "target", "file", "input"} {
				if err := os.WriteFile(filepath.Join(workspace, name), []byte("content\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			transform.directory = workspace
			history, err := transform.translateRegisteredTool(contribution, "tracked-"+source, source, nil)
			if err != nil || history.TranslationError != "" || !strings.Contains(history.carrierInput(), "shell ") {
				t.Fatalf("carrier: %+v, %v", history, err)
			}
			command, err := proxy.registry.execCarrierCommand(contribution, source, []string{"bash", source}, "", 0)
			if err != nil {
				t.Fatal(err)
			}
			// Execute the actual carrier's expanded worker arguments. Unexpected
			// direct execution is rejected by this outer host boundary fixture.
			var notices strings.Builder
			runner, err := interp.New(interp.Dir(workspace), interp.ExecHandler(func(ctx context.Context, args []string) error {
				if len(args) < 3 || args[0] != "shell" {
					t.Fatalf("carrier bypassed worker: %q", args)
				}
				_, stderr, code := runShellWorkerTest(t, proxy.registry, args[1], args[2:len(args)-1], args[len(args)-1], nil, newShellWorkerTestInvocation(workspace))
				io.WriteString(&notices, stderr)
				if code != 0 {
					return interp.ExitStatus(code)
				}
				return nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			program, err := syntax.NewParser().Parse(strings.NewReader(command), "")
			if err != nil {
				t.Fatal(err)
			}
			if err := runner.Run(t.Context(), program); err != nil || !strings.Contains(notices.String(), "change ") {
				t.Fatalf("actual carrier effects: %s, %v", notices.String(), err)
			}
		})
	}
	// The consuming worker must also recognize the post-- literal operand.
	_, stderr, code := runShellWorkerTest(t, proxy.registry, "bash", nil, "touch -- -notes", nil, newShellWorkerTestInvocation(directory))
	if code != 0 || !strings.Contains(stderr, "change ") {
		t.Fatalf("dash-prefixed creation not tracked: %d %s", code, stderr)
	}
}

func TestLiveDiffTerminalShellFileOperations(t *testing.T) {
	registry := sharedProxyTestRegistry(t)
	manifest, err := readToolWorkerManifest(filepath.Join(registry.SnapshotDir, toolPluginManifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	store, err := openMekugiReplayStore(manifest.ReplayDirectory)
	if err != nil {
		t.Fatal(err)
	}
	directory, thread := t.TempDir(), "terminal-shell-files"
	connection, _, _ := liveDiffTestBroker(t, store, liveDiffScope{Workspaces: map[string]map[string]bool{directory: {thread: true}}})
	ui := startLiveDiffTerminal(t, directory, store.directory, connection, 22)
	ui.frame(t, func(frame string) bool { return strings.Contains(frame, "STREAM") })
	ui.write(t, "v")
	ui.frame(t, func(frame string) bool { return strings.Contains(frame, "FOLLOW") })
	broker := newCommentaryBroker()
	broker.editPublisher = func(ctx context.Context, workspace, thread, callID string) error {
		return store.publishEditReceipt(ctx, workspace, thread, callID, nil)
	}
	server := httptest.NewServer(http.HandlerFunc(broker.serveHTTP))
	defer server.Close()
	sink := &httpShellCommentarySink{
		endpoint: server.URL, client: server.Client(),
		token: broker.subscribeThread(directory+"\x00terminal", thread, "/root"),
	}
	shell, _ := registry.contribution("shell")
	result, err := executeShellTool(t.Context(), manifest, registry.RuntimeRoot, &shell,
		[]string{"bash", "cat >created.txt <<'DATA'\nSHELL_CAPTURE_VISIBLE\nDATA\n"}, nil, directory,
		append(os.Environ(), "CODEX_THREAD_ID="+thread), sink, nil, nil)
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("%+v %v", result, err)
	}
	ui.frame(t, func(frame string) bool {
		return strings.Contains(frame, "created.txt") && strings.Contains(frame, "SHELL_CAPTURE_VISIBLE")
	})
	ui.quit(t)
}

func TestShellFileRemovalTrailingSymlinkParity(t *testing.T) {
	registry := sharedProxyTestRegistry(t)
	for _, suffix := range []string{"", "/", "//"} {
		t.Run("suffix="+suffix, func(t *testing.T) {
			directory := t.TempDir()
			for _, kind := range []string{"native", "tracked"} {
				target := filepath.Join(directory, kind)
				if err := os.Mkdir(target, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(target, "sentinel"), []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(directory, kind+"-link")); err != nil {
					t.Fatal(err)
				}
			}
			// Only disposable links and their owned sentinel directories are operands.
			nativeErr := exec.CommandContext(t.Context(), "rm", "-rf", "--", filepath.Join(directory, "native-link")+suffix).Run()
			_, stderr, code := runShellWorkerTest(t, registry, "bash", nil,
				"rm -rf -- "+shellQuoteArgument(filepath.Join(directory, "tracked-link")+suffix),
				nil, newShellWorkerTestInvocation(directory))
			if (nativeErr == nil) != (code == 0) {
				t.Fatalf("native=%v tracked=%d %s", nativeErr, code, stderr)
			}
			for _, name := range []string{"", "/sentinel", "-link"} {
				native, nativeErr := os.Lstat(filepath.Join(directory, "native") + name)
				tracked, trackedErr := os.Lstat(filepath.Join(directory, "tracked") + name)
				if (nativeErr == nil) != (trackedErr == nil) ||
					nativeErr == nil && native.Mode().Type() != tracked.Mode().Type() {
					t.Fatalf("filesystem parity %s: native=%v/%v tracked=%v/%v", name, native, nativeErr, tracked, trackedErr)
				}
			}
		})
	}
}

func withoutShellChangeNotices(output string) string {
	var result strings.Builder
	for line := range strings.SplitAfterSeq(output, "\n") {
		id, notice := strings.CutPrefix(strings.TrimSuffix(line, "\n"), "change ")
		if notice {
			if _, _, err := parseChangeID(id); err == nil {
				continue
			}
		}
		result.WriteString(line)
	}
	return result.String()
}

func TestShellFileCrossDeviceMoves(t *testing.T) {
	alternate, err := os.MkdirTemp("/dev/shm", "mekugi-shell-move-")
	if err != nil {
		t.Skipf("separate temporary filesystem unavailable: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(alternate) })
	directory := t.TempDir()
	probe := filepath.Join(directory, "probe")
	if err := os.WriteFile(probe, []byte("probe"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(probe, filepath.Join(alternate, "probe")); !errors.Is(err, syscall.EXDEV) {
		t.Skipf("fixture requires different filesystems: %v", err)
	}
	registry := sharedProxyTestRegistry(t)
	invocation := newShellWorkerTestInvocation(directory)
	for _, name := range []string{"file", "tree"} {
		t.Run(name, func(t *testing.T) {
			source := filepath.Join(directory, name)
			target := filepath.Join(alternate, name)
			contentPath := source
			if name == "tree" {
				if err := os.Mkdir(source, 0700); err != nil {
					t.Fatal(err)
				}
				contentPath = filepath.Join(source, "child")
			} else if err := os.WriteFile(target, []byte("old destination\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(contentPath, []byte("moved contents\n"), 0750); err != nil {
				t.Fatal(err)
			}
			_, stderr, code := runShellWorkerTest(t, registry, "bash", nil,
				"mv -- "+shellQuoteArgument(source)+" "+shellQuoteArgument(target), nil, invocation)
			if code != 0 || !strings.Contains(stderr, "change ") {
				t.Fatalf("cross-device move: %d %s", code, stderr)
			}
			if _, err := os.Lstat(source); !os.IsNotExist(err) {
				t.Fatalf("source retained: %v", err)
			}
			if name == "tree" {
				target = filepath.Join(target, "child")
			}
			content, err := os.ReadFile(target)
			info, statErr := os.Stat(target)
			if err != nil || statErr != nil || string(content) != "moved contents\n" || info.Mode().Perm() != 0750 {
				t.Fatalf("destination: %q %v %v %v", content, info, err, statErr)
			}
			id := strings.TrimSpace(strings.TrimPrefix(stderr, "change "))
			review, readErr, code := runShellWorkerTest(t, registry, "bash", nil, "mchanges "+id, nil, invocation)
			if code != 0 || !strings.Contains(review, "move ") || name == "file" && !strings.Contains(review, "-old destination") {
				t.Fatalf("cross-device evidence: %d %s %s", code, review, readErr)
			}
		})
	}
	t.Run("source unlink failure retains copy evidence", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("requires unprivileged filesystem permissions")
		}
		parent := filepath.Join(directory, "protected")
		if err := os.Mkdir(parent, 0700); err != nil {
			t.Fatal(err)
		}
		source, target := filepath.Join(parent, "copy"), filepath.Join(alternate, "copy")
		if err := os.WriteFile(source, []byte("copied\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(parent, 0500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(parent, 0700) })
		_, stderr, code := runShellWorkerTest(t, registry, "bash", nil,
			"mv -- "+shellQuoteArgument(source)+" "+shellQuoteArgument(target), nil, invocation)
		if code == 0 || !strings.Contains(stderr, "change ") {
			t.Fatalf("missing partial-copy result: %d %s", code, stderr)
		}
		for _, path := range []string{source, target} {
			if data, err := os.ReadFile(path); err != nil || string(data) != "copied\n" {
				t.Fatalf("partial-copy bytes: %q %v", data, err)
			}
		}
		_, id, _ := strings.Cut(stderr, "change ")
		review, readErr, code := runShellWorkerTest(t, registry, "bash", nil,
			"mchanges "+strings.TrimSpace(id), nil, invocation)
		if code != 0 || !strings.Contains(review, "+copied") || strings.Contains(review, "move ") {
			t.Fatalf("partial-copy evidence: %d %s %s", code, review, readErr)
		}
	})
	// A failed install leaves the original source inode and target unchanged.
	source, target := filepath.Join(directory, "rollback"), filepath.Join(alternate, "occupied")
	if err := os.WriteFile(source, []byte("restore me"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(source, source+".link"); err != nil {
		t.Fatal(err)
	}
	original, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "keep"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runShellWorkerTest(t, registry, "bash", nil,
		"mv -T -- "+shellQuoteArgument(source)+" "+shellQuoteArgument(target), nil, invocation)
	content, err := os.ReadFile(source)
	if code == 0 || strings.Contains(stderr, "change ") || err != nil || string(content) != "restore me" {
		t.Fatalf("failed move rollback: %d %s %q %v", code, stderr, content, err)
	}
	_, stderr, code = runShellWorkerTest(t, registry, "bash", nil,
		"{ printf before; status=0; mv -T -- "+shellQuoteArgument(source)+" "+shellQuoteArgument(target)+" || status=$?; printf after; exit \"$status\"; } >"+shellQuoteArgument(source), nil, invocation)
	current, statErr := os.Stat(source)
	linked, linkErr := os.ReadFile(source + ".link")
	if code == 0 || statErr != nil || !os.SameFile(original, current) || linkErr != nil || string(linked) != "beforeafter" {
		t.Fatalf("failed move changed inode/descriptor: %d %s %q %v %v", code, stderr, linked, statErr, linkErr)
	}
	if leftovers, err := filepath.Glob(filepath.Join(alternate, ".mekugi-move-*")); err != nil || len(leftovers) != 0 {
		t.Fatalf("staging leftovers: %v %v", leftovers, err)
	}
}

func TestShellFileUnreadableHistory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires actual read permission denial")
	}
	registry := sharedProxyTestRegistry(t)
	manifest, err := readToolWorkerManifest(filepath.Join(registry.SnapshotDir, toolPluginManifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, command, want string }{
		{"overwrite", "printf new | cat > file", "new"},
		{"append", "printf new | cat >> file", "oldnew"},
		{"delete", "rm file", ""},
		{"move", "mv source file", "new"},
		{"cross-device move", "", "new"},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, "file")
			if err := os.WriteFile(path, []byte("old"), 0200); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "source"), []byte("new"), 0600); err != nil {
				t.Fatal(err)
			}
			if test.name == "cross-device move" {
				staging, err := os.MkdirTemp("/dev/shm", "mekugi-unreadable-")
				if err != nil {
					t.Skipf("alternate filesystem unavailable: %v", err)
				}
				t.Cleanup(func() { os.RemoveAll(staging) })
				source := filepath.Join(staging, "source")
				if err := os.WriteFile(source, []byte("new"), 0600); err != nil {
					t.Fatal(err)
				}
				test.command = "mv " + source + " file"
			}
			before, _ := os.Stat(path)
			if err := os.Link(path, filepath.Join(directory, "hard")); err != nil {
				t.Fatal(err)
			}
			const child = "unreadable-child"
			_, stderr, code := runShellWorkerTest(t, registry, "bash", nil, test.command, nil, newShellWorkerTestInvocation(directory, "CODEX_THREAD_ID="+child))
			if code != 0 || !strings.Contains(stderr, "incomplete history") {
				t.Fatalf("operation blocked or silent: %d %s", code, stderr)
			}
			var ids []string
			for line := range strings.SplitSeq(stderr, "\n") {
				if id, ok := strings.CutPrefix(line, "change "); ok {
					ids = append(ids, id)
				}
			}
			if len(ids) == 0 {
				t.Fatal("missing durable change")
			}
			if test.name == "delete" {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("not removed: %v", err)
				}
			} else {
				if err := os.Chmod(path, 0600); err != nil {
					t.Fatal(err)
				}
				content, err := os.ReadFile(path)
				after, _ := os.Stat(path)
				if err != nil || string(content) != test.want {
					t.Fatalf("contents: %q %v", content, err)
				}
				if (test.name == "overwrite" || test.name == "append") && !os.SameFile(before, after) {
					t.Fatal("redirection replaced inode")
				}
			}
			// A new store instance proves incomplete metadata survives replay.
			store, err := openMekugiReplayStore(manifest.ReplayDirectory)
			if err != nil {
				t.Fatal(err)
			}
			for _, option := range []string{"", " --history", " --summary"} {
				stdout, stderr, code := runShellWorkerTest(t, registry, "bash", nil, "mchanges "+strings.Join(ids, " ")+option, nil, newShellWorkerTestInvocation(directory))
				marker := "incomplete history"
				if option == " --summary" {
					marker = "-\t-\t"
				}
				if code != 0 || !strings.Contains(stdout, marker) || strings.Contains(stdout, "@@") {
					t.Fatalf("review %s: %d %s %s", option, code, stdout, stderr)
				}
			}
			activity := newSubagentActivity()
			activity.observe("unreadable-root", "", "/root", false)
			activity.observe(child, "unreadable-root", "/root/worker", true)
			index, err := store.readChangeIndex(directory)
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range ids {
				for _, call := range index.Changes[id].Calls {
					if err := store.publishEditReceipt(t.Context(), directory, child, call.ID, activity); err != nil {
						t.Fatal(err)
					}
				}
			}
			messages := activity.drain("unreadable-root", time.Time{}, maxCommentaryPublicationBytes)
			if len(messages) != 1 {
				t.Fatalf("incomplete capture activity messages=%d", len(messages))
			}
			text := commentaryText(t, messages[0])
			if !strings.Contains(text, "incomplete history; line counts unavailable") ||
				strings.Contains(text, "+-1") || strings.Contains(text, "--1") {
				t.Fatalf("incomplete capture activity=%q", text)
			}
			review, err := store.readChanges(t.Context(), changeReadOptions{workspace: directory, ids: ids})
			if err != nil || !strings.Contains(review, "incomplete history") {
				t.Fatalf("replay: %s %v", review, err)
			}
		})
	}
}
