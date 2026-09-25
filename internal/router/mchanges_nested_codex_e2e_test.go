//go:build journal_e2e

package router

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// This acceptance test uses the installed Codex consumer with a deterministic
// local provider. Nested tool results are observed through Codex rollout trace,
// never through Code Mode's printed output.
type mchangesNestedCodexProvider struct {
	mu                        sync.Mutex
	program                   string
	callID                    string
	threadID                  string
	turns                     int
	resultSeen                bool
	finalMessage              string
	waitCall                  string
	expectPending, sawPending bool
	expectProcess             bool
	processWait               bool
	trace                     *nativeToolTrace
	store                     *mekugiReplayStore
	workspace                 string
}

func (p *mchangesNestedCodexProvider) forwardExecution(
	ctx context.Context,
	_ context.Context,
	body []byte,
	headers http.Header,
	_ string,
) (*http.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.turns++
	metadata, _ := decodeCodexTurnMetadata(headers)
	thread := headers.Get(threadIDHeader)
	if thread == "" {
		thread = metadata.ThreadID
	}
	if thread == "" {
		return nil, fmt.Errorf("Codex request omitted its stable thread identity")
	}
	if p.threadID == "" {
		p.threadID = thread
	} else if p.threadID != thread {
		return nil, fmt.Errorf("Codex request thread changed from %q to %q", p.threadID, thread)
	}

	if p.turns == 1 {
		if !bytes.Contains(body, []byte(applyPatchToolName)) || !bytes.Contains(body, []byte(nativeExecCommandToolName)) {
			return nil, fmt.Errorf("Codex request does not advertise both nested stock tools")
		}
		return mchangesNestedCodexResponse(p.turns, map[string]any{
			"type": "custom_tool_call", "id": p.callID + "-item", "call_id": p.callID,
			"name": "exec", "status": "completed", "input": p.program,
		}), nil
	}
	if p.turns > 8 {
		return nil, fmt.Errorf("unexpected provider turn %d", p.turns)
	}
	var request struct {
		Input []struct {
			Type   string         `json:"type"`
			CallID string         `json:"call_id"`
			Output jsontext.Value `json:"output"`
		} `json:"input"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, err
	}
	expected := p.callID
	if p.waitCall != "" {
		expected = p.waitCall
	}
	for _, item := range request.Input {
		if (item.Type != "custom_tool_call_output" && item.Type != "function_call_output") || item.CallID != expected {
			continue
		}
		if p.processWait {
			terminal, exit, _, _, _ := execResultState("write_stdin", item.Output)
			if !terminal || exit == nil || *exit != 0 {
				return nil, fmt.Errorf("native process continuation did not finish successfully")
			}
			p.resultSeen = true
			break
		}
		terminal, _, _, cell := stockPatchResultState("exec", item.Output)
		if terminal && p.expectProcess && !p.sawPending {
			evidence := p.trace.readCell(p.threadID, p.callID, p.program)
			if evidence == nil || !evidence.ended || !evidence.pending() || len(evidence.tools) != 1 {
				return nil, fmt.Errorf("expected a completed cell with a live nested process")
			}
			pendingCtx, release, err := p.store.beginSession(ctx, p.threadID, "")
			if err != nil {
				return nil, err
			}
			list, err := p.store.readChanges(pendingCtx, changeReadOptions{workspace: p.workspace, view: "list"})
			release()
			if err != nil || list != "" {
				return nil, fmt.Errorf("completed cell finalized a running process: %q, %v", list, err)
			}
			session, err := strconv.Atoi(evidence.tools[0].session)
			if err != nil {
				return nil, err
			}
			p.sawPending = true
			p.processWait = true
			p.waitCall = "nested-process-wait"
			return mchangesNestedCodexResponse(p.turns, map[string]any{"type": "function_call", "id": p.waitCall + "-item", "call_id": p.waitCall, "name": "write_stdin", "status": "completed", "arguments": string(mustMarshalJSON(map[string]any{"session_id": session, "chars": "", "yield_time_ms": 10000}))}), nil
		}
		if cell != "" {
			if p.expectPending {
				pendingCtx, release, err := p.store.beginSession(ctx, p.threadID, "")
				if err != nil {
					return nil, err
				}
				list, err := p.store.readChanges(pendingCtx, changeReadOptions{workspace: p.workspace, view: "list"})
				release()
				if err != nil || list != "" {
					return nil, fmt.Errorf("yielded edits finalized prematurely: %q, %v", list, err)
				}
				p.sawPending = true
			}
			p.waitCall = fmt.Sprintf("nested-wait-%d", p.turns)
			return mchangesNestedCodexResponse(p.turns, map[string]any{"type": "function_call", "id": p.waitCall + "-item", "call_id": p.waitCall, "name": "wait", "status": "completed", "arguments": string(mustMarshalJSON(map[string]any{"cell_id": cell, "yield_time_ms": 10000}))}), nil
		}
		p.resultSeen = terminal
		break
	}
	if !p.resultSeen {
		return nil, fmt.Errorf("Codex did not return the completed Code Mode cell result: %.3000s", body)
	}
	return mchangesNestedCodexResponse(p.turns, map[string]any{
		"type": "message", "id": p.callID + "-final", "role": "assistant", "status": "completed",
		"content": []any{map[string]any{"type": "output_text", "text": p.finalMessage}},
	}), nil
}

func mchangesNestedCodexResponse(turn int, item map[string]any) *http.Response {
	responseID := fmt.Sprintf("mchanges-nested-%d", turn)
	response := map[string]any{
		"id": responseID, "status": "completed", "output": []any{item},
		"usage": map[string]any{
			"input_tokens": 10, "input_tokens_details": map[string]any{"cached_tokens": 0},
			"output_tokens": 5, "output_tokens_details": map[string]any{"reasoning_tokens": 0}, "total_tokens": 15,
		},
	}
	var wire strings.Builder
	for _, event := range []map[string]any{
		{"type": "response.created", "response": map[string]any{"id": responseID, "status": "in_progress", "output": []any{}}},
		{"type": "response.output_item.added", "output_index": 0, "item": item},
		{"type": "response.output_item.done", "output_index": 0, "item": item},
		{"type": "response.completed", "response": response},
	} {
		wire.WriteString("data: ")
		wire.Write(mustMarshalJSON(event))
		wire.WriteString("\n\n")
	}
	result := serverHTTPResponse(wire.String())
	result.Header.Set("Content-Type", "text/event-stream")
	return result
}

func TestMChangesNestedNativeCodexE2E(t *testing.T) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal("installed Codex is required for the mchanges nested-tool acceptance gate")
	}
	dataDirectory := t.TempDir()
	registry, err := buildToolRegistryForTest(t, t.Context(), dataDirectory, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := registry.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := registry.installFrontends(); err != nil {
		t.Fatal(err)
	}
	frontend, ok := registry.frontends["mchanges"]
	if !ok {
		t.Fatal("mchanges executable frontend is unavailable")
	}
	manifest, err := readToolWorkerManifest(filepath.Join(registry.SnapshotDir, toolPluginManifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	store, err := openMekugiReplayStore(manifest.ReplayDirectory)
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	setCodexFrontendLoginEnvironment(t, filepath.Dir(frontend))

	patch := "*** Begin Patch\n*** Add File: nested-patch.txt\n+patch-result\n*** End Patch\n"
	shellCommand := "echo command-result > nested-command.txt"
	successProgram := "await tools.apply_patch(" + string(mustMarshalJSON(patch)) + ");\n" +
		"await tools.exec_command({cmd:" + string(mustMarshalJSON(shellCommand)) +
		",workdir:" + string(mustMarshalJSON(workspace)) + "});"
	successProvider := &mchangesNestedCodexProvider{
		program: successProgram, callID: "nested-success-call", finalMessage: "nested edits completed",
	}
	successThread := runMChangesNestedCodexCell(t, codex, registry, store, workspace, successProvider)
	// The helper has removed native traces. A fresh store must still expose
	// the host receipts, including to a reviewer in the shared namespace.
	store, err = openMekugiReplayStore(manifest.ReplayDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if !successProvider.resultSeen {
		t.Fatal("Codex did not acknowledge the successful Code Mode cell")
	}
	for name, want := range map[string]string{"nested-patch.txt": "patch-result\n", "nested-command.txt": "command-result\n"} {
		got, err := os.ReadFile(filepath.Join(workspace, name))
		if err != nil || string(got) != want {
			t.Fatalf("host file %s = %q, %v; want %q", name, got, err, want)
		}
	}

	rootList := runMChangesNestedShell(t, registry, workspace, successThread, "mchanges --mine --list")
	if rootList.status != 0 || rootList.stderr != "" {
		t.Fatalf("root --list: stdout=%q stderr=%q status=%d", rootList.stdout, rootList.stderr, rootList.status)
	}
	successIDs := mchangesThreadIDs(t, store, workspace, successThread)
	if len(successIDs) != 2 {
		t.Fatalf("nested success allocated %d change IDs, want one for apply_patch and one for exec_command: %q", len(successIDs), rootList.stdout)
	}
	for _, id := range successIDs {
		if !strings.Contains(rootList.stdout, id) {
			t.Errorf("--list omitted nested change ID %s: %q", id, rootList.stdout)
		}
	}
	if !strings.Contains(rootList.stdout, "+1 -0") || strings.Contains(rootList.stdout, "completed") ||
		strings.Contains(rootList.stdout, "pending") || strings.Contains(rootList.stdout, "unconfirmed") {
		t.Fatalf("nested change IDs and counts missing from --list: %q", rootList.stdout)
	}

	net := runMChangesNestedShell(t, registry, workspace, successThread, "mchanges --mine --net")
	if net.status != 0 || net.stderr != "" || !strings.Contains(net.stdout, "+patch-result") || !strings.Contains(net.stdout, "+command-result") ||
		!strings.Contains(net.stdout, "nested-patch.txt") || !strings.Contains(net.stdout, "nested-command.txt") {
		t.Fatalf("--mine --net did not compose confirmed nested effects: stdout=%q stderr=%q status=%d", net.stdout, net.stderr, net.status)
	}

	rootContext, releaseRoot, err := store.beginSession(t.Context(), successThread, "mchanges-nested-root")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseRoot()
	rootStore := store.scoped(rootContext)
	index, err := rootStore.readChangeIndex(workspace)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range successIDs {
		for _, call := range index.Changes[id].Calls {
			record, found, err := rootStore.read(workspace, call.ID, false)
			if err != nil || !found || len(record.History.HostResults) != 1 {
				t.Fatalf("nested success lacks durable native receipt: %s found=%t err=%v history=%+v", id, found, err, record.History)
			}
		}
	}
	inspectorThread := "mchanges-reviewer-" + filepath.Base(workspace)
	reviewerContext, releaseReviewer, err := store.beginSession(t.Context(), inspectorThread, "mchanges-nested-reviewer")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseReviewer()
	reviewerContext = bindTestHandleScope(t, store, reviewerContext, successThread, "")
	if err := store.scoped(reviewerContext).retainInput(reviewerContext, workspace, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	reviewerList := runMChangesNestedShell(t, registry, workspace, inspectorThread, "mchanges --list")
	if reviewerList.status != 0 || reviewerList.stderr != "" {
		t.Fatalf("reviewer --list: stdout=%q stderr=%q status=%d", reviewerList.stdout, reviewerList.stderr, reviewerList.status)
	}
	for _, id := range successIDs {
		if strings.Contains(reviewerList.stdout, id) {
			t.Errorf("reviewer own-thread list leaked author change ID %s: %q", id, reviewerList.stdout)
		}
	}
	explicit := runMChangesNestedShell(t, registry, workspace, inspectorThread,
		"mchanges "+strings.Join(successIDs, " ")+" --history")
	if explicit.status != 0 || explicit.stderr != "" || !strings.Contains(explicit.stdout, "nested-patch.txt") || !strings.Contains(explicit.stdout, "nested-command.txt") ||
		!strings.Contains(explicit.stdout, patch) || !strings.Contains(explicit.stdout, shellCommand) {
		t.Fatalf("reviewer explicit-ID read lost nested edit evidence: stdout=%q stderr=%q status=%d", explicit.stdout, explicit.stderr, explicit.status)
	}

	// A second real cell catches both nested host failures. Its outer Code Mode
	// result still completes, but neither nested change may become applied.
	failureWorkspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	failurePatch := "*** Begin Patch\n*** Update File: nested-patch.txt\n@@\n-absent-before-line\n+replacement\n*** End Patch\n"
	failureCommand := "echo partial-result > nested-partial.txt; exit 7"
	failureProgram := "try { await tools.apply_patch(" + string(mustMarshalJSON(failurePatch)) + "); } catch (_) {}\n" +
		"try { await tools.exec_command({cmd:" + string(mustMarshalJSON(failureCommand)) +
		",workdir:" + string(mustMarshalJSON(failureWorkspace)) + "}); } catch (_) {}"
	failureProvider := &mchangesNestedCodexProvider{
		program: failureProgram, callID: "nested-failure-call", finalMessage: "outer cell completed after caught failures",
	}
	failureThread := runMChangesNestedCodexCell(t, codex, registry, store, failureWorkspace, failureProvider)
	if !failureProvider.resultSeen {
		t.Fatal("Codex did not complete the outer Code Mode cell after caught nested failures")
	}
	failureList := runMChangesNestedShell(t, registry, failureWorkspace, failureThread, "mchanges --mine --list")
	if failureList.status != 0 || failureList.stderr != "" {
		t.Fatalf("failure --list: stdout=%q stderr=%q status=%d", failureList.stdout, failureList.stderr, failureList.status)
	}
	failureIDs := mchangesThreadIDs(t, store, failureWorkspace, failureThread)
	if len(failureIDs) != 1 {
		t.Fatalf("caught nested failures allocated %d change IDs, want only the shell's file effect: %q", len(failureIDs), failureList.stdout)
	}
	failedContext, releaseFailed, err := store.beginSession(t.Context(), failureThread, "mchanges-nested-failures")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseFailed()
	failedStore := store.scoped(failedContext)
	failedIndex, err := failedStore.readChangeIndex(failureWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	var sawPatch, sawNonzeroExec bool
	failedPatch, found, err := failedStore.lookup(failedContext, failureWorkspace, nativePatchDerivedCallID(failureProvider.callID, 0))
	if err != nil || !found || failedPatch.ChangeID != "" || len(failedPatch.HostResults) != 1 || failedPatch.HostResults[0].Status != "failed" {
		t.Fatalf("unchanged failed attempt was lost or allocated a change ID: %+v found=%t err=%v", failedPatch, found, err)
	}
	sawPatch = true
	for _, id := range failureIDs {
		change, ok := failedIndex.Changes[id]
		if !ok || len(change.Calls) == 0 {
			t.Fatalf("failed nested change %s has no retained call: %+v", id, change)
		}
		for _, call := range change.Calls {
			record, found, err := failedStore.read(failureWorkspace, call.ID, false)
			if err != nil || !found {
				t.Fatalf("read nested failure %s: found=%t err=%v", call.ID, found, err)
			}
			history := record.History
			if history.ChangeID == "" || len(history.ReviewFiles) == 0 {
				t.Errorf("caught nested edit lacks saved effect: id=%s call=%s history=%+v", id, call.ID, history)
			}
			if history.ToolName == applyPatchToolName && strings.Contains(history.Script, "absent-before-line") {
				sawPatch = true
			}
			if history.ExecOutcome != nil && history.ExecOutcome.Status == execStatusFailed &&
				strings.Contains(history.Script, "nested-partial.txt") {
				sawNonzeroExec = true
			}
		}
	}
	if !sawPatch || !sawNonzeroExec {
		t.Fatalf("nested failure receipts were not distinguished: patch=%t nonzero_exec=%t; list=%q", sawPatch, sawNonzeroExec, failureList.stdout)
	}
	failureNet := runMChangesNestedShell(t, registry, failureWorkspace, failureThread, "mchanges --mine --net")
	if failureNet.status != 0 || failureNet.stderr != "" || strings.Contains(failureNet.stdout, "composing observed effects") || !strings.Contains(failureNet.stdout, "+partial-result") {
		t.Fatalf("failed effects lost their diff or leaked outcome notice: stdout=%q stderr=%q status=%d", failureNet.stdout, failureNet.stderr, failureNet.status)
	}

	yieldWorkspace := t.TempDir()
	yieldProgram := "// @exec: {\"yield_time_ms\": 1}\nconst result = await tools.exec_command({cmd: \"sleep 2; echo yielded-result > yielded.txt\", workdir: " + string(mustMarshalJSON(yieldWorkspace)) + ", yield_time_ms: 1000});\nif (result.session_id) { await tools.write_stdin({session_id: result.session_id, chars: \"\", yield_time_ms: 10000}); }"
	yieldProvider := &mchangesNestedCodexProvider{program: yieldProgram, callID: "nested-yield-call", finalMessage: "yielded edit completed", expectPending: true, store: store, workspace: yieldWorkspace}
	yieldThread := runMChangesNestedCodexCell(t, codex, registry, store, yieldWorkspace, yieldProvider)
	if !yieldProvider.sawPending {
		t.Fatal("native fixture did not observe the running cell")
	}
	yieldIDs := mchangesThreadIDs(t, store, yieldWorkspace, yieldThread)
	if len(yieldIDs) != 1 {
		t.Fatalf("yielded command IDs = %v", yieldIDs)
	}
	yieldHistory := runMChangesNestedShell(t, registry, yieldWorkspace, yieldThread, "mchanges "+yieldIDs[0]+" --history")
	if yieldHistory.status != 0 || !strings.Contains(yieldHistory.stdout, "host tool ") || !strings.Contains(yieldHistory.stdout, "; exit 0") || !strings.Contains(yieldHistory.stdout, "+yielded-result") {
		t.Fatalf("yielded confirmation missing: %+v", yieldHistory)
	}
	processWorkspace := t.TempDir()
	processProgram := "await tools.exec_command({cmd: \"sleep 2; echo process-result > process.txt\", workdir: " + string(mustMarshalJSON(processWorkspace)) + ", yield_time_ms: 1000});"
	processProvider := &mchangesNestedCodexProvider{program: processProgram, callID: "nested-process-call", finalMessage: "nested process completed", expectProcess: true, store: store, workspace: processWorkspace}
	processThread := runMChangesNestedCodexCell(t, codex, registry, store, processWorkspace, processProvider)
	processIDs := mchangesThreadIDs(t, store, processWorkspace, processThread)
	if !processProvider.sawPending || len(processIDs) != 1 {
		t.Fatalf("completed-cell lifecycle: pending=%t IDs=%v", processProvider.sawPending, processIDs)
	}
	processHistory := runMChangesNestedShell(t, registry, processWorkspace, processThread, "mchanges "+processIDs[0]+" --history")
	if processHistory.status != 0 || !strings.Contains(processHistory.stdout, "host tool ") || !strings.Contains(processHistory.stdout, "; exit 0") || !strings.Contains(processHistory.stdout, "+process-result") {
		t.Fatalf("completed-cell process confirmation missing: %+v", processHistory)
	}
}

type mchangesNestedShellResult struct {
	stdout, stderr string
	status         int
}

func runMChangesNestedShell(t *testing.T, registry *toolRegistry, workspace, thread, script string) mchangesNestedShellResult {
	t.Helper()
	invocation := newShellWorkerTestInvocation(workspace,
		"XDG_STATE_HOME="+t.TempDir(), "MEKUGI_RUNTIME_DIR="+t.TempDir(),
		"CODEX_THREAD_ID="+thread, routerTestWorkerUnscopedEnvironment+"=0")
	stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil, script, nil, invocation)
	return mchangesNestedShellResult{stdout: stdout, stderr: stderr, status: status}
}

func mchangesThreadIDs(t *testing.T, store *mekugiReplayStore, workspace, thread string) []string {
	t.Helper()
	ctx, release, err := store.beginSession(t.Context(), thread, "mchanges-nested-ids")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	index, err := store.scoped(ctx).readChangeIndex(workspace)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := threadChangeIDs(index, thread)
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

func runMChangesNestedCodexCell(
	t *testing.T,
	codex string,
	registry *toolRegistry,
	store *mekugiReplayStore,
	workspace string,
	provider *mchangesNestedCodexProvider,
) string {
	t.Helper()
	// Codex declares repository workspaces in its routing metadata. A temp
	// directory without repository metadata deliberately has no relative scope.
	if output, err := exec.Command("git", "init", "--quiet", workspace).CombinedOutput(); err != nil {
		t.Fatalf("initialize isolated workspace: %v: %s", err, output)
	}
	traceRoot := t.TempDir()
	defer func() {
		if err := os.RemoveAll(traceRoot); err != nil {
			t.Error(err)
		}
	}()
	if err := os.Chmod(traceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_ROLLOUT_TRACE_ROOT", traceRoot)
	proxy := newProxyWithSharedTestRegistry(t, registry)
	proxy.replayStore = store
	proxy.nativeTrace = &nativeToolTrace{directory: traceRoot}
	provider.trace = proxy.nativeTrace
	server := httptest.NewServer(responsesHandler(t.Context(), time.Minute, provider, NewCriticalErrors(), proxy, nil))
	defer server.Close()
	config := `model_providers.mchanges_fixture={name="mchanges_fixture",base_url=` +
		strconv.Quote(server.URL+"/v1") + `,wire_api="responses",requires_openai_auth=false}`
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, codex,
		"-c", config, "-c", `model_provider="mchanges_fixture"`, "-c", "features.plugins=false",
		"--model", "gpt-6-astra", "--sandbox", "danger-full-access", "--ask-for-approval", "never",
		"exec", "--ignore-user-config", "--skip-git-repo-check", "--json", "--color", "never",
		"-C", workspace, "Exercise the supplied literal Code Mode operations exactly once.",
	)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("installed Codex nested-tool fixture: %v\nstdout: %.8000s\nstderr: %.8000s", err, stdout.String(), stderr.String())
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if !provider.resultSeen || !strings.Contains(stdout.String(), provider.finalMessage) || (!provider.expectPending && !provider.expectProcess && provider.turns != 2) || ((provider.expectPending || provider.expectProcess) && !provider.sawPending) {
		t.Fatalf("nested Code Mode acceptance: turns=%d result=%t\nstdout: %.8000s\nstderr: %.8000s",
			provider.turns, provider.resultSeen, stdout.String(), stderr.String())
	}
	cell := proxy.nativeTrace.readCell(provider.threadID, provider.callID, provider.program)
	if cell == nil {
		for name, bundle := range proxy.nativeTrace.bundles {
			t.Logf("trace bundle %s: seq=%d err=%v cells=%d", name, bundle.seq, bundle.err, len(bundle.cells))
		}
		t.Fatalf("native trace cell unavailable; stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
	return provider.threadID
}
