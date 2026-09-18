package router

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/syntax"
)

func TestShellHpatchSemantics(t *testing.T) {
	registry := sharedProxyTestRegistry(t)
	for _, interpreter := range []string{"bash", "sh"} {
		for _, test := range []struct {
			name, script, want string
		}{
			{"argument", `hpatch 'new result.txt
type "argument"'`, "argument"},
			{"heredoc", "hpatch <<PATCH\nnew result.txt\ntype \"foo $(printf expanded) $HPATCH_TEST bar\"\nPATCH\n", "foo expanded variable bar"},
			{"quoted", "hpatch <<'PATCH'\nnew result.txt\ntype \"$(printf literal) $HPATCH_TEST\"\nPATCH\n", "$(printf literal) $HPATCH_TEST"},
			{"stdin", "hpatch < input.patch > report.txt", "redirected"},
		} {
			t.Run(interpreter+"/"+test.name, func(t *testing.T) {
				directory := t.TempDir()
				if err := os.WriteFile(filepath.Join(directory, "input.patch"), []byte("new result.txt\ntype \"redirected\""), 0o600); err != nil {
					t.Fatal(err)
				}
				stdout, stderr, code := runShellWorkerTest(t, registry, interpreter, nil, test.script, nil,
					newShellWorkerTestInvocation(directory, "HPATCH_TEST=variable"))
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
		`touch marker && hpatch 'new result.txt'`,
		"touch marker\nhpatch 'new result.txt'",
		`hpatch 'new result.txt' | cat`,
		`(hpatch 'new result.txt')`,
		`hpatch 'new result.txt' &`,
	} {
		t.Run(script, func(t *testing.T) {
			directory := t.TempDir()
			_, stderr, code := runShellWorkerTest(t, registry, "bash", nil, script, nil, newShellWorkerTestInvocation(directory))
			if code != 2 || !strings.Contains(stderr, "standalone") {
				t.Fatalf("code=%d stderr=%s", code, stderr)
			}
			for _, name := range []string{"marker", "result.txt"} {
				if _, err := os.Stat(filepath.Join(directory, name)); !os.IsNotExist(err) {
					t.Fatalf("%s exists: %v", name, err)
				}
			}
		})
	}
}

func TestShellHpatchAtomicFailure(t *testing.T) {
	directory := t.TempDir()
	registry := sharedProxyTestRegistry(t)
	_, stderr, code := runShellWorkerTest(t, registry, "bash", nil, `hpatch 'new result.txt
type "first"
in missing.txt
type "old" "new"'`, nil, newShellWorkerTestInvocation(directory))
	if code == 0 || stderr == "" {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(directory, "result.txt")); !os.IsNotExist(err) {
		t.Fatalf("partial edit: %v", err)
	}
}

func TestShellHpatchRecoveryAndReview(t *testing.T) {
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "sample.txt"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	invocation := newShellWorkerTestInvocation(directory)
	_, rejected, code := runShellWorkerTest(t, registry, "bash", nil,
		`hpatch 'in sample.txt
type "missing" "new"'`, nil, invocation)
	if code == 0 {
		t.Fatal("invalid target succeeded")
	}
	marker := "Recover this rejected edit with hpatch --recover "
	_, tail, ok := strings.Cut(rejected, marker)
	if !ok {
		t.Fatalf("missing recovery: %s", rejected)
	}
	handle, _, _ := strings.Cut(tail, ".")
	recovery := "hpatch --recover " + handle + ` 'type "missing" "old"'`
	report, stderr, code := runShellWorkerTest(t, registry, "bash", nil, recovery, nil, invocation)
	if code != 0 {
		t.Fatalf("recovery: %d %s %s", code, report, stderr)
	}
	data, err := os.ReadFile(filepath.Join(directory, "sample.txt"))
	if err != nil || string(data) != "new\n" {
		t.Fatalf("actual recovery: %q %v", data, err)
	}
	_, after, _ := strings.Cut(report, "change ")
	id, _, _ := strings.Cut(after, "\n")
	review, stderr, code := runShellWorkerTest(t, registry, "bash", nil,
		"hchanges "+id+" --history", nil, invocation)
	if code != 0 || !strings.Contains(review, "applied") || !strings.Contains(review, "+new") {
		t.Fatalf("review: %d %s %s", code, review, stderr)
	}
	// A successful recovery does not mutate the explicitly named original
	// baseline. Another correction still rebuilds from its original payload.
	_, stderr, code = runShellWorkerTest(t, registry, "bash", nil,
		"hpatch --recover "+handle+` 'type "missing" "new"'`, nil, invocation)
	if code != 0 {
		t.Fatalf("immutable recovery baseline: %d %s", code, stderr)
	}
}

func TestShellHpatchCatProjectionKeepsCompositionGuard(t *testing.T) {
	source := "cat > marker <<'DATA'\ncontent\nDATA\nhpatch 'new result.txt'\n"
	for _, variant := range []syntax.LangVariant{syntax.LangBash, syntax.LangPOSIX} {
		if _, projected := splitShellCatWrites(source, t.TempDir(), variant); projected {
			t.Fatal("cat projection bypassed standalone edit validation")
		}
	}
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
	source := "hpatch " + shellQuoteArgument(strings.Repeat("x", maxMekugiScriptBytes+1))
	_, stderr, code := runShellWorkerTest(t, sharedProxyTestRegistry(t), "bash", nil, source, nil, newShellWorkerTestInvocation(directory))
	if code == 0 || !strings.Contains(stderr, "script exceeds") {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
}

func TestShellHpatchRecoveryRestartIsolationAndControlBytes(t *testing.T) {
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	invocation := newShellWorkerTestInvocation(directory, "CODEX_THREAD_ID=recovery-test")
	base := "new sample.go\ntype \"package p\\nvar =\\n\"\nnew control.txt\ntype \"old\"\n"
	_, rejected, code := runShellWorkerTest(t, registry, "bash", nil, "hpatch "+shellQuoteArgument(base), nil, invocation)
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
	// Open a fresh store, as a restarted router or worker does.
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
	commands := recoveryCommands(base, history.RecoveryHandles)
	payload := commands[1].handle + ` value "package p\n"` + "\n" + commands[3].handle + ` value "\u001b[31m\u0000\r\t"`
	_, stderr, code := runShellWorkerTest(t, registry, "bash", nil, "hpatch --recover "+handle+" "+shellQuoteArgument(payload), nil, invocation)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	data, err := os.ReadFile(filepath.Join(directory, "control.txt"))
	if err != nil || string(data) != "\x1b[31m\x00\r\t" {
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
	thread := "edit-publication"
	_, events, _ := liveDiffTestBroker(t, store, liveDiffScope{Workspaces: map[string]map[string]bool{directory: {thread: true}}})
	sub := events.subscribe()
	broker := newCommentaryBroker()
	broker.editPublisher = store.publishEditReceipt
	server := httptest.NewServer(http.HandlerFunc(broker.serveHTTP))
	defer server.Close()
	token := broker.subscribeThread(directory+"\x00session", thread, "/root")
	sink := &httpShellCommentarySink{endpoint: server.URL, token: token, client: server.Client()}
	shell, _ := registry.contribution("shell")
	result, err := executeShellTool(t.Context(), manifest, registry.RuntimeRoot, &shell,
		[]string{"bash", "hpatch 'new result.txt\ntype \"published\"'"}, nil, directory,
		append(os.Environ(), "CODEX_THREAD_ID="+thread), sink, nil, nil)
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("execution=%+v err=%v", result, err)
	}
	event := waitLiveDiffChange(t, sub, true)
	if event.Thread != thread || len(event.Change.Calls) != 1 {
		t.Fatalf("%+v", event)
	}
	callID := event.Change.Calls[0].ID
	if err := store.publishEditReceipt(t.Context(), directory, "other-thread", callID); err == nil {
		t.Fatal("another thread published edit evidence")
	}
	files, err := store.liveDiffSnapshotFiles(t.Context(), liveDiffScope{Workspaces: map[string]map[string]bool{directory: {thread: true}}})
	if err != nil || len(files) != 1 || files[0].path != filepath.Join(directory, "result.txt") {
		t.Fatalf("live diff files=%+v err=%v", files, err)
	}
	// A fork may recover the original change without inheriting its stream owner.
	rejected, err := executeShellTool(t.Context(), manifest, registry.RuntimeRoot, &shell,
		[]string{"bash", "hpatch 'in result.txt\ntype \"missing\" \"forked\"'"}, nil, directory,
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
	if err := store.publishEditReceipt(t.Context(), directory, thread, forkEvent.Change.Calls[0].ID); err == nil {
		t.Fatal("original thread impersonated the fork's recovery attempt")
	}
	// A broken auxiliary publisher cannot replace successful edit output.
	broker.editPublisher = func(context.Context, string, string, string) error { return fmt.Errorf("offline") }
	result, err = executeShellTool(t.Context(), manifest, registry.RuntimeRoot, &shell,
		[]string{"bash", "hpatch 'new offline.txt\ntype \"kept\"'"}, nil, directory,
		append(os.Environ(), "CODEX_THREAD_ID="+thread), sink, nil, nil)
	if err != nil || result.ExitCode != 0 || !strings.Contains(result.Stdout, "change ") {
		t.Fatalf("auxiliary failure affected edit: %+v %v", result, err)
	}
}

func TestShellHpatchConfiguredOutcomeHook(t *testing.T) {
	directory := t.TempDir()
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
		"hpatch 'new result.txt\ntype \"done\"'", nil, newShellWorkerTestInvocation(directory, "CODEX_THREAD_ID=hook-thread"))
	if code != 0 {
		t.Fatalf("%d %s %s", code, report, stderr)
	}
	content, err := os.ReadFile(resultPath)
	if err != nil || string(content) != "hpatch|applied|succeeded" {
		t.Fatalf("hook result=%q err=%v report=%s stderr=%s", content, err, report, stderr)
	}
}
