package launch

import (
	"bufio"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/executionobservation"
	"github.com/yusing/mekugi/apps/mekugi/internal/frontends"
	launchoptions "github.com/yusing/mekugi/apps/mekugi/internal/launch/options"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/codex/binding"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/conversation/prompt"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	threadbinding "github.com/yusing/mekugi/modules/threadcontrol/codexbinding"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestClaudeInvocationRetainedOwners(t *testing.T) {
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "CODEX_HOME", "MEKUGI_RUNTIME_DIR"} {
		t.Setenv(key, t.TempDir())
	}
	options, err := launchoptions.Parse([]string{"--ansi-faint=off", "--shell-guard=false"})
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	threads := &threadExecution{shared: control.New(), setup: &threadSetup{options: options, executable: executable, environment: os.Environ()}}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if code, err := runOwnedInvocation(ctx, nil, nil, nil, t.TempDir(), threads); err != nil || code != 0 {
		t.Fatalf("invocation: code=%d error=%v", code, err)
	}
	directory, err := history.ReplayDirectory()
	if err != nil {
		t.Fatal(err)
	}
	if threads.retained.Store == nil {
		t.Fatal("invocation did not supply retained execution owners for Claude delegates")
	}
	if threads.retained.Store.Directory() != directory || threads.retained.Limits.Total != history.DefaultStorageBytes || threads.retained.Limits.Kinds["commentary"] != 16<<20 {
		t.Fatalf("delegate retained scope differs from invocation storage: %+v", threads.retained)
	}
}

type claudeNativeParent struct {
	control.Runtime
	final chan control.NativeInput
}

func (p *claudeNativeParent) Input(_ context.Context, input control.NativeInput) (string, error) {
	if input.Kind == "FINAL_ANSWER" {
		p.final <- input
	}
	return "parent-turn", nil
}
func (*claudeNativeParent) Inspect(context.Context, string) (control.SessionDetails, error) {
	return control.SessionDetails{}, nil
}

type claudeNativeLauncher struct{ parent *claudeNativeParent }

func (*claudeNativeLauncher) StartOptions(context.Context) (sessionapi.StartOptions, error) {
	return sessionapi.StartOptions{Models: []sessionapi.ModelGroup{{Models: []sessionapi.Model{{ID: "codex"}}}}}, nil
}
func (*claudeNativeLauncher) PrepareInput(context.Context, string, sessionapi.InputRequest) (*sessionapi.PreparedInput, error) {
	return nil, nil
}
func (l *claudeNativeLauncher) Run(ctx context.Context, start sessionapi.Start, api *sessionapi.Server) error {
	api.BindRequests(l.parent)
	if err := api.Publish(sessionapi.Frame{Session: sessionapi.Session{Thread: "parent", Workspace: start.Workspace}, FamilyRoot: "parent"}); err != nil {
		return err
	}
	if err := api.SetSummary(sessionapi.Summary{Thread: "parent", Workspace: start.Workspace, Ready: true}); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

// The installed native CLI and official SDK use the same loopback provider as
// backend acceptance. The scripted parent only supplies rooted Hub admission.
func TestClaudeNativePreparedRuntime(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_CLAUDE_NATIVE") != "1" {
		t.Skip("set MEKUGI_TEST_CLAUDE_NATIVE=1 with the bundled SDK, Bun and Claude installed")
	}
	for _, tc := range []struct{ name, approval, reviewer string }{
		{"user", `"on-request"`, "user"},
		{"never_auto_review", `"never"`, "auto_review"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testClaudeNativePreparedRuntime(t, tc.approval, tc.reviewer, "")
		})
	}
}

func TestClaudeNativeAgentCompaction(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_CLAUDE_NATIVE") != "1" {
		t.Skip("set MEKUGI_TEST_CLAUDE_NATIVE=1 with bundled SDK, Bun and Claude")
	}
	for _, outcome := range []string{"success", "failed"} {
		t.Run(outcome, func(t *testing.T) { testClaudeNativePreparedRuntime(t, `"never"`, "auto_review", outcome) })
	}
}

func testClaudeNativePreparedRuntime(t *testing.T, approval, reviewer, compact string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	f := newClaudeHookFixture(t)
	b := f.bridge
	workspace := b.settings.Workspace
	claudeConfig := t.TempDir()
	excluded := filepath.Join(workspace, "AGENTS.md")
	nativeSettings, err := json.Marshal(map[string]any{
		"autoMemoryEnabled": true,
		"attribution":       map[string]string{"commit": "", "pr": ""},
		"claudeMdExcludes":  []string{excluded, filepath.Join(claudeConfig, "CLAUDE.md")},
	})
	if err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string][]byte{
		filepath.Join(claudeConfig, "settings.json"): nativeSettings,
		filepath.Join(claudeConfig, "CLAUDE.md"):     []byte("excluded-native-instruction-fixture"),
		excluded:                                     []byte("excluded-native-instruction-fixture"),
		filepath.Join(workspace, "CLAUDE.md"):        []byte("included-native-instruction-fixture"),
	} {
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	parentScope := b.selected
	parentScope.Thread = "parent"
	operation := executionobservation.PrepareShell(ctx, parentScope, b.owners.Changes.Service, b.checkpoints, b.owners.Recovery, nil, &executionobservation.Windows{}, nil, nil,
		executionobservation.ShellInput{Thread: "parent", Namespace: "parent", Workspace: workspace, Caller: "/root", Command: "printf 'parent evidence\\n' > parent.txt", Shell: "bash"})
	if operation == nil {
		t.Fatal("parent edit was not observed")
	}
	operation.Begin(nil)
	command := exec.CommandContext(ctx, "/bin/bash", "-c", "printf 'parent evidence\\n' > parent.txt")
	command.Dir = workspace
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if err := operation.Complete(binding.ToolResult{Tool: "userShell", CallID: "parent-edit", Status: "completed", ExitCode: new(0)}, string(output)); err != nil {
		t.Fatal(err)
	}
	list, err := b.owners.Changes.Service.Read(ctx, parentScope, changes.Query{Workspace: workspace, View: "list"})
	if err != nil || len(strings.Fields(list)) == 0 {
		t.Fatalf("parent receipt: %q %v", list, err)
	}
	parentID := strings.Fields(list)[0]
	providerScript, err := filepath.Abs("../../../../backends/claude/testdata/mock-provider.mjs")
	if err != nil {
		t.Fatal(err)
	}
	provider := exec.CommandContext(ctx, "bun", providerScript)
	stdin, err := provider.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := provider.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	provider.Stderr = os.Stderr
	if err := provider.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stdin.Close()
		if err := provider.Wait(); err != nil && ctx.Err() == nil {
			t.Error(err)
		}
	}()
	plan := map[string]any{"responses": []any{
		map[string]any{"content": []any{map[string]any{"type": "tool_use", "id": "parent_range", "name": "Bash", "input": map[string]string{"command": "mchanges " + parentID + "; printf '\\nTHREAD=%s\\nPATH=%s\\n' \"$CODEX_THREAD_ID\" \"$PATH\"", "description": "Read parent capture through private frontend"}}}},
		map[string]any{"content": []any{map[string]any{"type": "tool_use", "id": "native_write", "name": "Bash", "input": map[string]string{"command": "printf 'native child evidence\\n' > native-child.txt", "description": "Write child evidence through the shell"}}}},
		map[string]any{"content": []any{map[string]any{"type": "tool_use", "id": "child_list", "name": "Bash", "input": map[string]string{"command": "mchanges --list", "description": "Read child captures"}}}},
		map[string]any{"content": []any{map[string]string{"type": "text", "text": "native child finished"}}},
	}}
	if compact != "" {
		responses := plan["responses"].([]any)
		summary := map[string]any{"content": []any{map[string]string{"type": "text", "text": "<summary>Retain native child checks and continue unfinished work. All prior checks succeeded. The agent read parent captures, wrote native-child.txt and listed child captures. The only remaining task is to report native child finished.</summary>"}}}
		if compact == "failed" {
			summary = map[string]any{"status": 400, "error": map[string]string{"type": "invalid_request_error", "message": "fixture compaction failure"}}
		}
		plan["responses"] = append(append(responses[:3:3], map[string]any{"content": []any{map[string]any{"type": "tool_use", "id": "compact_checkpoint", "name": "mcp__threadcontrol__compact", "input": map[string]any{}}}}), responses[3])
		plan["compaction"] = summary
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(stdin, "%s\n", encoded); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var address struct {
		Port int `json:"port"`
	}
	if err := json.Unmarshal(line, &address); err != nil {
		t.Fatal(err)
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", address.Port)
	environment := slices.DeleteFunc(os.Environ(), func(entry string) bool {
		return strings.HasPrefix(entry, "ANTHROPIC_") || strings.HasPrefix(entry, "CLAUDE_") || strings.HasPrefix(entry, "BASH_ENV=") || strings.HasPrefix(entry, "MEKUGI_EXEC_TRACK=") || strings.HasPrefix(entry, "CODEX_THREAD_ID=")
	})
	environment = append(environment, "ANTHROPIC_BASE_URL="+base, "ANTHROPIC_API_KEY=local-fixture", "ANTHROPIC_AUTH_TOKEN=", "CLAUDE_CODE_OAUTH_TOKEN=", "CLAUDE_CONFIG_DIR="+claudeConfig, "CLAUDE_CODE_DISABLE_AUTO_MEMORY=0", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "CLAUDE_CODE_USE_BEDROCK=0", "CLAUDE_CODE_USE_VERTEX=0", "CLAUDE_CODE_USE_FOUNDRY=0", routerTestWorkerEnvironment+"=1")
	parent := &claudeNativeParent{final: make(chan control.NativeInput, 1)}
	hub := sessionapi.NewHub(ctx, &claudeNativeLauncher{parent: parent})
	workers := new(frontends.WorkerImage)
	defer func() { hub.Close(); _ = workers.Close() }()
	server := httptest.NewServer(sessionapi.Versioned(hub.Handler()))
	defer server.Close()
	rpc, err := sessionapi.OpenRPC(ctx, server.Client(), "ws"+strings.TrimPrefix(server.URL, "http")+sessionapi.RPCPath, "")
	if err != nil {
		t.Fatal(err)
	}
	defer rpc.Close()
	start, _ := json.Marshal(sessionapi.Start{Workspace: workspace, Model: "codex"})
	var ready struct {
		ID string `json:"id"`
	}
	if err := rpc.Call(ctx, "session/start", sessionapi.RPCParams{Body: start}, &ready); err != nil || ready.ID != "parent" {
		t.Fatalf("root readiness: %+v %v", ready, err)
	}
	driver, err := threadbinding.NewDriver(b.execution.shared)
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer driver.Stop(context.WithoutCancel(ctx))
	approvals := sessioncontrol.NewDelegateApprovals(ctx)
	defer approvals.Close()
	go func() {
		for {
			select {
			case request := <-approvals.Requests():
				request.Reply(prompt.Choice{Approve: true}, "")
			case <-ctx.Done():
				return
			}
		}
	}()
	execution := &threadExecution{hub: hub, shared: b.execution.shared, driver: driver, workers: workers, retained: parentScope, windows: b.windows, approvals: approvals, setup: &threadSetup{environment: environment}}
	audience := sessioncontrol.NewAudience(nil)
	defer audience.Close()
	if err := driver.Connect(execution, audience); err != nil {
		t.Fatal(err)
	}
	settings := b.settings
	settings.Model = "claude:opus"
	settings.Effort = new("high")
	settings.DeveloperInstructions = new("selected-native-role-fixture")
	settings.ApprovalPolicy, settings.ApprovalsReviewer = jsontext.Value(approval), reviewer
	// Keep a parent shell window open while the real child runtime performs its
	// shell edit. Both files exist before the parent reaches terminal capture.
	concurrent := executionobservation.PrepareShell(ctx, parentScope, b.owners.Changes.Service, b.checkpoints, b.owners.Recovery, nil, b.windows, nil, nil,
		executionobservation.ShellInput{Thread: "parent", Namespace: "parent", Workspace: workspace, Caller: "/root", Command: "printf 'concurrent parent\\n' > concurrent-parent.txt", Shell: "bash"})
	if concurrent == nil {
		t.Fatal("concurrent parent edit was not observed")
	}
	concurrent.Begin(nil)
	defer concurrent.Discard()
	command = exec.CommandContext(ctx, execTrackShellExecutable(t, "bash"), "-c", "printf 'concurrent parent\\n' > concurrent-parent.txt")
	command.Dir = workspace
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("concurrent parent write: %s, %v", output, err)
	}
	result, err := driver.SpawnPrepared(ctx, control.Identity{Thread: "parent", Session: "parent-session", Turn: "parent-turn", Call: "native-spawn", Item: "native-spawn"}, threadbinding.PreparedChild{TaskName: "native", Message: "Execute the requested checks and finish.", Settings: settings})
	if err != nil {
		t.Fatalf("prepared native child: %+v %v", result, err)
	}
	member, ok := execution.shared.Member(result.Thread)
	if !ok || !member.Settings.NonAttachable || member.Session == "" || member.Session == member.Thread {
		t.Fatalf("native identity/capability: %+v", member)
	}
	if scope, found, err := parentScope.Store.ReadScope(result.Thread); err != nil || !found || scope.Namespace != "parent" || scope.Parent != "parent" {
		t.Fatalf("native shared scope: %+v %v", scope, err)
	}
	select {
	case final := <-parent.final:
		if !strings.Contains(final.Message, "native child finished") {
			t.Fatalf("native final delivery: %+v", final)
		}
	case <-ctx.Done():
		member, _ := execution.shared.Member(result.Thread)
		var checkpoint any
		if member.Wait != nil {
			checkpoint = member.Wait.Compaction
		}
		t.Fatalf("%v: status=%s wait=%+v compact=%+v errors=%v", ctx.Err(), member.Status, member.Wait, checkpoint, driver.Failures())
	}
	wantRounds, wantTurns := uint64(4), uint64(1)
	if compact != "" {
		wantRounds, wantTurns = 6, 2
	}
	// Final delivery precedes auxiliary context acquisition. Wait for the shared
	// roster publication, not another model turn, to observe both producers.
	for {
		family, _ := hub.Family(result.Thread)
		var metrics activitypresentation.AgentFacts
		if family != nil && family.Agents != nil {
			for _, row := range family.Agents.Roster {
				if row.Agent.Thread == result.Thread {
					metrics = row.Agent.AgentFacts
				}
			}
		}
		if metrics.ContextKnown && metrics.TokensKnown && metrics.WorkKnown {
			if metrics.InputTokens == 0 || metrics.OutputTokens == 0 || metrics.ContextWindow == 0 || !metrics.CostKnown || metrics.Cost <= 0 || metrics.Roundtrips != wantRounds || metrics.Turns != wantTurns || !metrics.NonAttachable {
				t.Fatalf("native SDK metrics did not reach family roster: %+v", metrics)
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("native roster metrics unavailable: %+v: %v", metrics, ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	childScope := parentScope
	childScope.Thread = result.Thread
	childList, err := b.owners.Changes.Service.Read(ctx, childScope, changes.Query{Workspace: workspace, View: "list"})
	if err != nil || len(strings.Fields(childList)) == 0 {
		t.Fatalf("native child receipts: %q %v", childList, err)
	}
	if content, err := os.ReadFile(filepath.Join(workspace, "native-child.txt")); err != nil || string(content) != "native child evidence\n" {
		t.Fatalf("native shell edit effect: %q %v", content, err)
	}
	if err := concurrent.Complete(binding.ToolResult{Tool: "userShell", CallID: "concurrent-parent", Status: "completed", ExitCode: new(0)}, ""); err != nil {
		t.Fatal(err)
	}
	parentDiff, err := b.owners.Changes.Service.Read(ctx, parentScope, changes.Query{Workspace: workspace, Mine: true})
	if err != nil || !strings.Contains(parentDiff, "+concurrent parent") || strings.Contains(parentDiff, "native-child.txt") {
		t.Fatalf("cross-runtime parent capture: %s, %v", parentDiff, err)
	}
	childDiff, err := b.owners.Changes.Service.Read(ctx, childScope, changes.Query{Workspace: workspace, Mine: true})
	if err != nil || !strings.Contains(childDiff, "+native child evidence") || strings.Contains(childDiff, "concurrent-parent.txt") {
		t.Fatalf("cross-runtime child capture: %s, %v", childDiff, err)
	}
	response, err := server.Client().Get(base + "/_fixture/requests")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "+parent evidence") || !strings.Contains(string(body), "THREAD="+result.Thread) || !strings.Contains(string(body), "mekugi-tools-") || !strings.Contains(string(body), "mchanges "+strings.Fields(childList)[0]) {
		t.Fatalf("native provider input omitted private frontend identity or shared receipts: %s", body)
	}
	var effortRequests []struct {
		Output struct {
			Effort string `json:"effort"`
		} `json:"output_config"`
		System []struct {
			Text string `json:"text"`
		} `json:"system"`
		Tools []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(body, &effortRequests); err != nil {
		t.Fatal(err)
	}
	if len(effortRequests) == 0 || effortRequests[0].Output.Effort != "high" {
		t.Fatalf("native provider effort not delivered: %+v", effortRequests)
	}
	for _, request := range effortRequests {
		var guidance strings.Builder
		for _, block := range request.System {
			guidance.WriteString(block.Text)
		}
		for _, tool := range request.Tools {
			if slices.Contains([]string{"Read", "Glob", "Grep", "Write", "Edit", "NotebookEdit"}, tool.Name) {
				t.Errorf("native provider exposes overlapping tool %q", tool.Name)
			}
			if tool.Name == "Bash" {
				guidance.WriteString(tool.Description)
			}
		}
		for _, advice := range []string{"Read tool", "Glob tool", "Grep tool", "Write tool", "Edit tool", "NotebookEdit tool"} {
			if strings.Contains(guidance.String(), advice) {
				t.Errorf("native provider retains unavailable-tool advice %q", advice)
			}
		}
	}
	details, err := execution.Inspect(ctx, result.Thread)
	if err != nil || details.ReasoningEffort == nil || *details.ReasoningEffort != "high" {
		t.Fatalf("applied native effort = %+v, %v", details, err)
	}
	if compact != "" {
		var requests []struct {
			Messages []struct {
				Role    string         `json:"role"`
				Content jsontext.Value `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(body, &requests); err != nil {
			t.Fatal(err)
		}
		generating := 0
		for _, request := range requests {
			raw, _ := json.Marshal(request.Messages)
			if !strings.Contains(string(raw), "CRITICAL: Respond with TEXT ONLY.") {
				generating++
			}
		}
		if generating != 5 {
			t.Fatalf("compaction ran unexpected inference: requests=%d", len(requests))
		}
		last, _ := json.Marshal(requests[len(requests)-1])
		if !strings.Contains(string(last), "Thread Control compact") || compact == "failed" && !strings.Contains(string(last), "failed") {
			t.Fatalf("missing retained compaction outcome: %s", last)
		}
	}
	for _, absent := range []string{"excluded-native-instruction-fixture", "# Memory", "Co-Authored-By:", "Generated with [Claude Code]", "tools.exec_command", "Promise.allSettled"} {
		if strings.Contains(string(body), absent) {
			t.Errorf("native provider request contains excluded guidance %q", absent)
		}
	}
	for _, present := range []string{"included-native-instruction-fixture", "selected-native-role-fixture", "Reuse completed edit-receipt IDs", "mcp__threadcontrol__spawn_agent"} {
		if !strings.Contains(string(body), present) {
			t.Errorf("native provider request omits required guidance %q", present)
		}
	}
	// Frontend guidance is SessionStart context, not the system tail, and is
	// delivered again after native compaction.
	var requests []struct {
		System   jsontext.Value `json:"system"`
		Messages jsontext.Value `json:"messages"`
	}
	if err := json.Unmarshal(body, &requests); err != nil || len(requests) == 0 {
		t.Fatalf("fixture requests: %v", err)
	}
	const workflow = "Reuse completed edit-receipt IDs"
	for i, request := range requests {
		if strings.Contains(string(request.System), workflow) {
			t.Errorf("request %d carries frontend guidance in the system prompt", i)
		}
	}
	if !strings.Contains(string(requests[0].Messages), workflow) || compact == "success" && !strings.Contains(string(requests[len(requests)-1].Messages), workflow) {
		t.Error("frontend guidance missing from initial or post-compaction context")
	}
}
