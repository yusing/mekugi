package router

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/router/toolplugin"
)

func TestShellHpatchActivityPreviewSuppressesStandaloneEdits(t *testing.T) {
	tests := []struct {
		name, input string
	}{
		{"shell", "hpatch 'new result.txt\ntype \"argument\"'"},
		{"shell", "hpatch <<'PATCH'\nnew result.txt\ntype \"heredoc\"\nPATCH\n"},
		{"shell", `hpatch --recover maple 'maple value "fixed"'`},
		{"shell", "hpatch --recover maple <<'PATCH'\nmaple value \"fixed\"\nPATCH\n"},
		{"exec", `await tools.exec_command({cmd:"hpatch 'new result.txt\\ntype \\\"code mode\\\"'"})`},
		{"exec", `text(await tools.exec_command({cmd:"hpatch <<'PATCH'\nnew result.txt\ntype \"static heredoc\"\nPATCH\n"}))`},
	}
	for _, test := range tests {
		t.Run(test.name+"/"+test.input, func(t *testing.T) {
			item := map[string]json.RawMessage{"name": mustMarshalJSON(test.name), "input": mustMarshalJSON(test.input)}
			if got := subagentToolActivityText(item, test.name); got != "" {
				t.Fatalf("standalone hpatch preview = %q", got)
			}
		})
	}

	for _, test := range []struct {
		source, kept, omitted string
	}{
		{"printf before\nhpatch 'new result.txt'", "printf before", "new result.txt"},
		{"hpatch <<'PATCH'\nnew result.txt\ntype \"hidden\"\nPATCH\ncat visible.txt", "Read `visible.txt`", "hidden"},
		{"cat visible.txt\nhpatch <<'PATCH'\nnew result.txt\ntype \"hidden\"\nPATCH\n", "Read `visible.txt`", "hidden"},
		{"cat <<'DATA'\nvisible body\nDATA\nhpatch <<'PATCH'\nnew result.txt\nPATCH\n", "visible body", "new result.txt"},
		{"#!params={\"max_output_tokens\":100}\nprintf batch\n#!bash\nprintf second", "printf batch", ""},
	} {
		got := toolActivityShell(test.source)
		if !strings.Contains(got, test.kept) || test.omitted != "" && strings.Contains(got, test.omitted) {
			t.Fatalf("ordinary shell preview = %q, want kept %q and omitted %q", got, test.kept, test.omitted)
		}
	}
}

func TestShellHpatchSemantics(t *testing.T) {
	registry := sharedProxyTestRegistry(t)
	for _, interpreter := range []string{"bash", "sh"} {
		for _, test := range []struct{ name, script, want string }{
			{"argument", `hpatch result.txt 'type "old" "argument"'`, "argument\n"},
			{"heredoc", "hpatch result.txt <<PATCH\ntype \"old\" \"foo $(printf expanded) $HPATCH_TEST bar\"\nPATCH\n", "foo expanded variable bar\n"},
			{"quoted", "hpatch result.txt <<'PATCH'\ntype \"old\" \"$(printf literal) $HPATCH_TEST\"\nPATCH\n", "$(printf literal) $HPATCH_TEST\n"},
			{"stdin", "hpatch result.txt < input.patch > report.txt", "redirected\n"},
		} {
			t.Run(interpreter+"/"+test.name, func(t *testing.T) {
				directory := t.TempDir()
				if err := os.WriteFile(filepath.Join(directory, "result.txt"), []byte("old\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(directory, "input.patch"), []byte("type \"old\" \"redirected\""), 0o600); err != nil {
					t.Fatal(err)
				}
				stdout, stderr, code := runShellWorkerTest(t, registry, interpreter, nil, test.script, nil, newShellWorkerTestInvocation(directory, "HPATCH_TEST=variable"))
				if code != 0 {
					t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout, stderr)
				}
				content, err := os.ReadFile(filepath.Join(directory, "result.txt"))
				if err != nil || string(content) != test.want {
					t.Fatalf("content=%q err=%v, want %q", content, err, test.want)
				}
				if test.name == "stdin" {
					report, err := os.ReadFile(filepath.Join(directory, "report.txt"))
					if err != nil || !strings.Contains(string(report), "change ") {
						t.Fatalf("redirected report=%q err=%v", report, err)
					}
				} else if !strings.Contains(stdout, "change ") {
					t.Fatalf("missing report: %s", stdout)
				}
			})
		}
	}
}

func TestShellHpatchRejectsCompositionBeforeEffects(t *testing.T) {
	registry := sharedProxyTestRegistry(t)
	for _, script := range []string{
		`touch marker && hpatch result.txt 'type "old" "new"'`,
		"touch marker\nhpatch result.txt 'type \"old\" \"new\"'",
		`hpatch result.txt 'type "old" "new"' | cat`,
		`(hpatch result.txt 'type "old" "new"')`,
		`hpatch result.txt 'type "old" "new"' &`,
	} {
		t.Run(script, func(t *testing.T) {
			directory := t.TempDir()
			if err := os.WriteFile(filepath.Join(directory, "result.txt"), []byte("old\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			_, stderr, code := runShellWorkerTest(t, registry, "bash", nil, script, nil, newShellWorkerTestInvocation(directory))
			if code != 2 || !strings.Contains(stderr, "standalone") {
				t.Fatalf("code=%d stderr=%s", code, stderr)
			}
			if _, err := os.Stat(filepath.Join(directory, "marker")); !os.IsNotExist(err) {
				t.Fatalf("marker exists: %v", err)
			}
			data, err := os.ReadFile(filepath.Join(directory, "result.txt"))
			if err != nil || string(data) != "old\n" {
				t.Fatalf("result changed: %q, %v", data, err)
			}
		})
	}
}

func TestShellHpatchAtomicFailure(t *testing.T) {
	directory := t.TempDir()
	registry := sharedProxyTestRegistry(t)
	if err := os.WriteFile(filepath.Join(directory, "result.txt"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runShellWorkerTest(t, registry, "bash", nil, `hpatch result.txt 'type "old" "first"' missing.txt 'type "old" "new"'`, nil, newShellWorkerTestInvocation(directory))
	if code == 0 || stderr == "" {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	data, err := os.ReadFile(filepath.Join(directory, "result.txt"))
	if err != nil || string(data) != "old\n" {
		t.Fatalf("partial edit: %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(directory, "missing.txt")); !os.IsNotExist(err) {
		t.Fatalf("missing path unexpectedly created: %v", err)
	}
}

func TestShellHpatchRecoveryAndReview(t *testing.T) {
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "sample.txt"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	invocation := newShellWorkerTestInvocation(directory)
	_, rejected, code := runShellWorkerTest(t, registry, "bash", nil, `hpatch sample.txt 'type "missing" "new"'`, nil, invocation)
	if code == 0 {
		t.Fatal("invalid target succeeded")
	}
	marker := "Recover this rejected edit with hpatch --recover "
	_, tail, ok := strings.Cut(rejected, marker)
	if !ok {
		t.Fatalf("missing recovery: %s", rejected)
	}
	handle, _, _ := strings.Cut(tail, ".")
	report, stderr, code := runShellWorkerTest(t, registry, "bash", nil, "hpatch --recover "+handle+` 'type "missing" "old"'`, nil, invocation)
	if code != 0 {
		t.Fatalf("recovery: %d %s %s", code, report, stderr)
	}
	data, err := os.ReadFile(filepath.Join(directory, "sample.txt"))
	if err != nil || string(data) != "new\n" {
		t.Fatalf("actual recovery: %q %v", data, err)
	}
	_, after, _ := strings.Cut(report, "change ")
	id, _, _ := strings.Cut(after, "\n")
	review, stderr, code := runShellWorkerTest(t, registry, "bash", nil, "hchanges "+id+" --history", nil, invocation)
	if code != 0 || !strings.Contains(review, "applied") || !strings.Contains(review, "+new") || !strings.Contains(review, "file \"sample.txt\"") ||
		strings.Count(review, "evaluated script:") != 1 || strings.Count(review, `type "old" "new"`) != 1 {
		t.Fatalf("review: %d %s %s", code, review, stderr)
	}
	_, stderr, code = runShellWorkerTest(t, registry, "bash", nil, "hpatch --recover "+handle+` 'type "missing" "new"'`, nil, invocation)
	if code != 0 {
		t.Fatalf("immutable recovery baseline: %d %s", code, stderr)
	}
}

func TestShellHpatchCatProjectionKeepsCompositionGuard(t *testing.T) {
	source := "cat > marker <<'DATA'\ncontent\nDATA\nhpatch result.txt 'type \"old\" \"new\"' \n"
	directory := t.TempDir()
	_, stderr, code := runShellWorkerTest(t, sharedProxyTestRegistry(t), "bash", nil, source, nil, newShellWorkerTestInvocation(directory))
	if code != 2 || !strings.Contains(stderr, "standalone") {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(directory, "marker")); !os.IsNotExist(err) {
		t.Fatalf("composed command ran: %v", err)
	}
}

func TestShellHpatchScriptCapacity(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "result.txt"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := "hpatch result.txt " + shellQuoteArgument(strings.Repeat("x", maxMekugiScriptBytes+1))
	_, stderr, code := runShellWorkerTest(t, sharedProxyTestRegistry(t), "bash", nil, source, nil, newShellWorkerTestInvocation(directory))
	if code == 0 || !strings.Contains(stderr, "script exceeds") {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
}

func TestShellHpatchRecoveryRestartIsolationAndControlBytes(t *testing.T) {
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "sample.go"), []byte("package p\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "control.txt"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	invocation := newShellWorkerTestInvocation(directory, "CODEX_THREAD_ID=recovery-test")
	sampleScript := `type "package p" "package p\nvar =\n"`
	controlScript := `type "old" "\u001b[31m\u0000\r\t"`
	command := "hpatch sample.go " + shellQuoteArgument(sampleScript) + " control.txt " + shellQuoteArgument(controlScript)
	_, rejected, code := runShellWorkerTest(t, registry, "bash", nil, command, nil, invocation)
	if code == 0 {
		t.Fatal("invalid Go source succeeded")
	}
	_, tail, found := strings.Cut(rejected, "Recover this rejected edit with hpatch --recover ")
	if !found {
		t.Fatal(rejected)
	}
	handle, _, _ := strings.Cut(tail, ".")
	manifest, err := readToolWorkerManifest(filepath.Join(registry.SnapshotDir, toolPluginManifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	store, err := openMekugiReplayStore(manifest.ReplayDirectory)
	if err != nil {
		t.Fatal(err)
	}
	history, err := store.rejectedEdit(t.Context(), directory, handle)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.rejectedEdit(t.Context(), t.TempDir(), handle); err == nil {
		t.Fatal("recovered another execution directory's edit")
	}
	commands := recoveryBatchCommands(history.Edits, history.RecoveryHandles)
	payload := commands[0].handle + ` value "package p"`
	recovery := "hpatch --recover " + handle + " --script 1 " + shellQuoteArgument(payload)
	report, stderr, code := runShellWorkerTest(t, registry, "bash", nil, recovery, nil, invocation)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	_, after, _ := strings.Cut(report, "change ")
	changeID, _, _ := strings.Cut(after, "\n")
	review, reviewErr, reviewCode := runShellWorkerTest(t, registry, "bash", nil, "hchanges "+changeID+" --history", nil, invocation)
	if reviewCode != 0 || reviewErr != "" || !strings.Contains(review, `recovery script 1 file "sample.go":`) ||
		strings.Count(review, "evaluated script:") != 1 || strings.Count(review, payload) != 1 {
		t.Fatalf("recovery history lost selection or duplicated input: %d %s %s", reviewCode, review, reviewErr)
	}
	data, err := os.ReadFile(filepath.Join(directory, "control.txt"))
	if err != nil || string(data) != "\x1b[31m\x00\r\t\n" {
		t.Fatalf("control bytes = %q, %v", data, err)
	}
	if _, err := store.rejectedEdit(t.Context(), directory, handle); err != nil {
		t.Fatalf("successful correction mutated original rejection: %v", err)
	}
	for _, binding := range []string{"", "changed"} {
		bad := history
		bad.RecoveryBinding = binding
		id := "hpatch-" + binding + "bad"
		if err := store.put(t.Context(), directory, map[string]mekugiHistory{id: bad}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.rejectedEdit(t.Context(), directory, binding+"bad"); err == nil {
			t.Fatal("accepted invalid recovery binding")
		}
	}
}

func TestShellHpatchPublishesCommittedEditReceipt(t *testing.T) {
	registry := sharedProxyTestRegistry(t)
	manifest, err := readToolWorkerManifest(filepath.Join(registry.SnapshotDir, toolPluginManifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	store, err := openMekugiReplayStore(manifest.ReplayDirectory)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "result.txt"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	thread := "edit-publication"
	_, events, _ := liveDiffTestBroker(t, store, liveDiffScope{Workspaces: map[string]map[string]bool{directory: {thread: true}}})
	sub := events.subscribe()
	broker := newCommentaryBroker()
	broker.editPublisher = func(ctx context.Context, workspace, thread, callID string) error {
		return store.publishEditReceipt(ctx, workspace, thread, callID, nil)
	}
	server := httptest.NewServer(http.HandlerFunc(broker.serveHTTP))
	defer server.Close()
	token := broker.subscribeThread(directory+"\x00session", thread, "/root")
	sink := &httpShellCommentarySink{endpoint: server.URL, token: token, client: server.Client()}
	shell, _ := registry.contribution("shell")
	result, err := executeShellTool(t.Context(), manifest, registry.RuntimeRoot, &shell,
		[]string{"bash", "hpatch result.txt 'type \"old\" \"published\"'"}, nil, directory,
		append(os.Environ(), "CODEX_THREAD_ID="+thread), sink, nil, nil)
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("execution=%+v err=%v", result, err)
	}
	event := waitLiveDiffChange(t, sub, true)
	if event.Thread != thread || len(event.Change.Calls) != 1 {
		t.Fatalf("%+v", event)
	}
	callID := event.Change.Calls[0].ID
	if err := store.publishEditReceipt(t.Context(), directory, "other-thread", callID, nil); err == nil {
		t.Fatal("another thread published edit evidence")
	}
	files, err := store.liveDiffSnapshotFiles(t.Context(), liveDiffScope{Workspaces: map[string]map[string]bool{directory: {thread: true}}})
	if err != nil || len(files) != 1 || files[0].Path != filepath.Join(directory, "result.txt") {
		t.Fatalf("live diff files=%+v err=%v", files, err)
	}
	// A fork may recover the original change without inheriting its stream owner.
	rejected, err := executeShellTool(t.Context(), manifest, registry.RuntimeRoot, &shell,
		[]string{"bash", "hpatch result.txt 'type \"missing\" \"forked\"'"}, nil, directory,
		append(os.Environ(), "CODEX_THREAD_ID="+thread), sink, nil, nil)
	if err != nil || rejected.ExitCode == 0 {
		t.Fatalf("%+v %v", rejected, err)
	}
	_, tail, ok := strings.Cut(rejected.Stderr, "Recover this rejected edit with hpatch --recover ")
	if !ok {
		t.Fatal(rejected.Stderr)
	}
	handle, _, _ := strings.Cut(tail, ".")
	forkThread := "edit-fork"
	events.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{directory: {forkThread: true}}})
	forkSub := events.subscribe()
	forkSink := &httpShellCommentarySink{endpoint: server.URL,
		token: broker.subscribeThread(directory+"\x00fork", forkThread, "/root/fork"), client: server.Client()}
	fixed, err := executeShellTool(t.Context(), manifest, registry.RuntimeRoot, &shell,
		[]string{"bash", "hpatch --recover " + handle + ` 'type "missing" "published"'`}, nil, directory,
		append(os.Environ(), "CODEX_THREAD_ID="+forkThread), forkSink, nil, nil)
	if err != nil || fixed.ExitCode != 0 {
		t.Fatalf("%+v %v", fixed, err)
	}
	forkEvent := waitLiveDiffChange(t, forkSub, true)
	if forkEvent.Thread != forkThread {
		t.Fatalf("fork event=%+v", forkEvent)
	}
	forkFiles, err := store.liveDiffSnapshotFiles(t.Context(), liveDiffScope{Workspaces: map[string]map[string]bool{directory: {forkThread: true}}})
	if err != nil || len(forkFiles) != 1 {
		t.Fatalf("fork snapshot=%+v err=%v", forkFiles, err)
	}
	if err := store.publishEditReceipt(t.Context(), directory, thread, forkEvent.Change.Calls[0].ID, nil); err == nil {
		t.Fatal("original thread impersonated the fork's recovery attempt")
	}
	// A broken auxiliary publisher cannot replace successful edit output.
	broker.editPublisher = func(context.Context, string, string, string) error { return fmt.Errorf("offline") }
	result, err = executeShellTool(t.Context(), manifest, registry.RuntimeRoot, &shell,
		[]string{"bash", "hpatch result.txt 'type \"forked\" \"kept\"'"}, nil, directory,
		append(os.Environ(), "CODEX_THREAD_ID="+thread), sink, nil, nil)
	if err != nil || result.ExitCode != 0 || !strings.Contains(result.Stdout, "change ") {
		t.Fatalf("auxiliary failure affected edit: %+v %v", result, err)
	}
}

func TestShellHpatchConfiguredOutcomeHook(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "result.txt"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hooks := t.TempDir()
	resultPath := filepath.Join(directory, "hook.txt")
	settings := map[string]any{"hooks": map[string]any{"outcome": []string{
		"printf '%s' {{shellquote .ToolName}}'|'{{shellquote .Stage}}'|'{{shellquote .Outcome}} > " + shellQuoteArgument(resultPath),
	}}}
	if err := os.WriteFile(filepath.Join(hooks, "settings.json"), mustMarshalJSON(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := buildToolRegistryForTest(t, t.Context(), hooks, false)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	report, stderr, code := runShellWorkerTest(t, registry, "bash", nil,
		"hpatch result.txt 'type \"old\" \"done\"'", nil, newShellWorkerTestInvocation(directory, "CODEX_THREAD_ID=hook-thread"))
	if code != 0 {
		t.Fatalf("%d %s %s", code, report, stderr)
	}
	content, err := os.ReadFile(resultPath)
	if err != nil || string(content) != "hpatch|applied|succeeded" {
		t.Fatalf("hook result=%q err=%v report=%s stderr=%s", content, err, report, stderr)
	}
}

func TestShellHpatchGeneratedScriptHistoryAndLiveDiff(t *testing.T) {
	registry := sharedProxyTestRegistry(t)
	manifest, err := readToolWorkerManifest(filepath.Join(registry.SnapshotDir, toolPluginManifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	store, err := openMekugiReplayStore(manifest.ReplayDirectory)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	for path, content := range map[string]string{
		"result.txt":      "old\n",
		"generator.py":    "print('type \"old\" \"command\"')\n",
		"generated.patch": "type \"command\" \"redirected\"\n",
	} {
		if err := os.WriteFile(filepath.Join(directory, path), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}

	thread := "generated-edit"
	_, events, _ := liveDiffTestBroker(t, store, liveDiffScope{
		Workspaces: map[string]map[string]bool{directory: {thread: true}},
	})
	sub := events.subscribe()
	commentary := newCommentaryBroker()
	commentary.editPublisher = func(ctx context.Context, workspace, thread, callID string) error {
		return store.publishEditReceipt(ctx, workspace, thread, callID, nil)
	}
	server := httptest.NewServer(http.HandlerFunc(commentary.serveHTTP))
	defer server.Close()
	token := commentary.subscribeThread(directory+"\x00generated", thread, "/root")
	sink := &httpShellCommentarySink{endpoint: server.URL, token: token, client: server.Client()}
	shell, ok := registry.contribution("shell")
	if !ok {
		t.Fatal("shell contribution is unavailable")
	}
	run := func(command string) toolplugin.ExecutionOutput {
		result, err := executeShellTool(t.Context(), manifest, registry.RuntimeRoot, &shell,
			[]string{"bash", command}, nil, directory,
			append(os.Environ(), "CODEX_THREAD_ID="+thread), sink, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}

	first := run(`hpatch result.txt "$(python3 generator.py)"`)
	if first.ExitCode != 0 || !strings.Contains(first.Stdout, "change ") {
		t.Fatalf("generated command substitution: %+v", first)
	}
	if content, err := os.ReadFile(filepath.Join(directory, "result.txt")); err != nil || string(content) != "command\n" {
		t.Fatalf("command-generated edit = %q, %v", content, err)
	}
	firstEvent := waitLiveDiffChange(t, sub, true)
	if firstEvent.Thread != thread || len(firstEvent.Change.Calls) != 1 {
		t.Fatalf("committed command-generated event = %+v", firstEvent)
	}

	_, tail, ok := strings.Cut(first.Stdout, "change ")
	if !ok {
		t.Fatalf("missing change id: %s", first.Stdout)
	}
	changeID, _, _ := strings.Cut(tail, "\n")
	review, reviewErr, code := runShellWorkerTest(t, registry, "bash", nil,
		"hchanges "+shellQuoteArgument(changeID)+" --history", nil,
		newShellWorkerTestInvocation(directory, "CODEX_THREAD_ID="+thread))
	if code != 0 || reviewErr != "" || !strings.Contains(review, "applied") ||
		!strings.Contains(review, "+command") || !strings.Contains(review, "file \"result.txt\"") ||
		strings.Count(review, "type \"old\" \"command\"") != 1 {
		t.Fatalf("hchanges generated command = %d %s %s", code, review, reviewErr)
	}

	second := run("hpatch result.txt < generated.patch")
	if second.ExitCode != 0 || !strings.Contains(second.Stdout, "change ") {
		t.Fatalf("generated redirect: %+v", second)
	}
	if content, err := os.ReadFile(filepath.Join(directory, "result.txt")); err != nil || string(content) != "redirected\n" {
		t.Fatalf("redirect-generated edit = %q, %v", content, err)
	}
	secondEvent := waitLiveDiffChange(t, sub, true)
	if secondEvent.Thread != thread || len(secondEvent.Change.Calls) != 1 {
		t.Fatalf("committed redirect-generated event = %+v", secondEvent)
	}
}
