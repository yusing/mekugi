package codexbinding

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestMCPSpawnInvokingSettings(t *testing.T) {
	// Host source at c82708f85803d21fe9046af407107a1ee105b6e5:
	// mcp_tool_call.rs builds metadata from StepContext.settings, while
	// session/step_context.rs to_turn_context_item delegates to the initial
	// TurnContext. Repeated inferences can therefore retain old turn settings.
	for _, tc := range []struct {
		name, model, effort, retainedModel, retainedEffort string
		reset, oldTurn, conflictingSession, foreignRollout bool
		unknownItem, laterInference                        bool
		captured                                           *invokingSettings
		override, wantError                                string
	}{
		{name: "first inference", model: "parent", effort: "low"},
		{name: "next inference", model: "parent", effort: "low"},
		{name: "model changed", model: "selected", effort: "low"},
		{name: "effort changed", model: "parent", effort: "high"},
		{name: "next inference still stale", model: "selected", effort: "high"},
		{name: "unset retained effort", model: "parent", effort: "low", retainedEffort: "null"},
		{name: "no effective effort", model: "parent", retainedEffort: "null"},
		{name: "older yielded cell", model: "parent", effort: "low", retainedModel: "selected", retainedEffort: `"high"`, laterInference: true},
		{name: "conflicting inference model", model: "selected", effort: "low", captured: &invokingSettings{model: "parent", effort: "low"}, wantError: `MCP model="selected" effort="low"; captured model="parent" effort="low"`},
		{name: "conflicting inference effort", model: "parent", effort: "high", captured: &invokingSettings{model: "parent", effort: "low"}, wantError: `MCP model="parent" effort="high"; captured model="parent" effort="low"`},
		{name: "stale MCP matches retained turn", model: "parent", effort: "low", captured: &invokingSettings{model: "selected", effort: "high"}, wantError: `MCP model="parent" effort="low"; captured model="selected" effort="high"`},
		{name: "missing MCP effort", model: "parent", captured: &invokingSettings{model: "parent", effort: "high"}, wantError: `MCP model="parent" effort=""; captured model="parent" effort="high"`},
		{name: "unknown execution item", model: "parent", effort: "low", unknownItem: true, wantError: "no matching invoking inference"},
		{name: "refreshed context", model: "selected", effort: "high", retainedModel: "selected", retainedEffort: `"high"`},
		{name: "new turn", model: "selected", effort: "high", retainedModel: "selected", retainedEffort: `"high"`, reset: true},
		{name: "old caller after reset", model: "selected", effort: "high", retainedModel: "selected", retainedEffort: `"high"`, reset: true, oldTurn: true, wantError: "caller"},
		{name: "conflicting session", model: "parent", effort: "low", conflictingSession: true, wantError: "conflicting host MCP identity"},
		{name: "foreign retained identity", model: "parent", effort: "low", foreignRollout: true, wantError: "admission settings are unavailable"},
		{name: "supported override", model: "parent", effort: "low", override: `,"model":"selected","reasoning_effort":"high"`},
		{name: "unsupported effort", model: "parent", effort: "low", override: `,"model":"selected","reasoning_effort":"invalid"`, wantError: "unsupported reasoning effort"},
		{name: "unsupported model and effort", model: "parent", effort: "low", override: `,"model":"unknown","reasoning_effort":"high"`, wantError: "unknown agent model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			retained, err := history.NewService(control.RecordKinds())
			if err != nil {
				t.Fatal(err)
			}
			store, err := retained.Open(t.Context(), t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			session, err := store.SelectSession(t.Context(), nil, history.Limits{Total: history.DefaultStorageBytes}, "root")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(session.Close)
			workspace := t.TempDir()
			path := filepath.Join(workspace, "root.jsonl")
			header := fmt.Sprintf(`{"type":"session_meta","payload":{"id":"root"}}
{"type":"event_msg","payload":{"type":"thread_settings_applied","thread_id":"root","thread_settings":{"model":"parent","model_provider_id":"provider","cwd":%q,"permission_profile":{"type":"disabled"},"approval_policy":"never"}}}
`, workspace)
			contextRecord := func(turn, model, effort string) string {
				return fmt.Sprintf(`{"type":"turn_context","payload":{"turn_id":%q,"model":%q,"effort":%s,"cwd":%q,"permission_profile":{"type":"disabled"},"approval_policy":"never"}}`+"\n", turn, model, effort, workspace)
			}
			initial := header + contextRecord("root-turn", "parent", `"low"`)
			roleTestFile(t, path, initial)
			service := control.New()
			driver, err := NewDriver(service)
			if err != nil {
				t.Fatal(err)
			}
			host := &reconciliationHost{}
			created := 0
			native := uniqueChildren{instructionNative: instructionNative{Native: host, owner: instructionRuntime{info: control.ThreadInfo{Thread: "root", Session: "root-session", Path: path}}}, created: &created}
			if err := driver.Connect(native, host); err != nil {
				t.Fatal(err)
			}
			caller := control.Identity{Thread: "root", Session: "root-session", Turn: "root-turn", Call: "spawn", Item: "exec"}
			if err := driver.AdmitRequest(t.Context(), session, caller, true); err != nil {
				t.Fatal(err)
			}
			if tc.reset {
				if err := service.HostCompleted(t.Context(), caller.Thread, caller.Turn, control.Completed, control.HostFailure{}); err != nil {
					t.Fatal(err)
				}
				caller.Turn = "reset-turn"
			}
			if tc.retainedModel == "" {
				tc.retainedModel = "parent"
			}
			if tc.retainedEffort == "" {
				tc.retainedEffort = `"low"`
			}
			// A new inference appends context, or a reset replaces history. Neither
			// alters the native MCP call's captured model/effort below.
			if tc.reset {
				initial = header
			}
			roleTestFile(t, path, initial+contextRecord(caller.Turn, tc.retainedModel, tc.retainedEffort))
			if tc.name != "first inference" {
				if err := driver.AdmitRequest(t.Context(), session, caller, true); err != nil {
					t.Fatal(err)
				}
			}
			captured := invokingSettings{model: tc.model, effort: tc.effort}
			if tc.captured != nil {
				captured = *tc.captured
			}
			if err := driver.CaptureInvocation(caller, captured.model, captured.effort); err != nil {
				t.Fatal(err)
			}
			if tc.laterInference {
				next := caller
				next.Item = "later-exec"
				if err := driver.CaptureInvocation(next, "selected", "high"); err != nil {
					t.Fatal(err)
				}
			}
			if tc.unknownItem {
				caller.Item = "unknown-exec"
			}
			if tc.oldTurn {
				caller.Turn = "root-turn"
			}
			if tc.foreignRollout {
				roleTestFile(t, path, strings.Replace(initial, `"id":"root"`, `"id":"foreign"`, 1))
			}
			driver.models = []Model{{Name: "parent", Efforts: []string{"low", "high"}}, {Name: "selected", Efforts: []string{"low", "high"}}}
			contribution := Codex.MCP(driver)
			left, right := net.Pipe()
			server, err := contribution.Server.Connect(t.Context(), &mcp.IOTransport{Reader: left, Writer: left}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			client, err := mcp.NewClient(&mcp.Implementation{Name: "fixture", Version: "1"}, nil).Connect(t.Context(), &mcp.IOTransport{Reader: right, Writer: right}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			meta := mcp.Meta{"threadId": caller.Thread, "sessionId": caller.Session, "callId": caller.Call, "itemId": caller.Item, "windowId": "window",
				"x-codex-turn-metadata": map[string]any{"thread_id": caller.Thread, "session_id": caller.Session, "turn_id": caller.Turn, "codex_version": "fixture", "model": tc.model, "reasoning_effort": tc.effort}}
			if tc.conflictingSession {
				meta["sessionId"] = "foreign"
			}
			before, _ := service.Member(caller.Thread)
			result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "spawn_agent", Meta: meta, Arguments: jsontext.Value(`{"task_name":"worker","message":"assignment","fork_turns":"none"` + tc.override + `}`)})
			if err != nil || len(result.Content) != 1 {
				t.Fatalf("MCP result=%+v error=%v", result, err)
			}
			text := result.Content[0].(*mcp.TextContent).Text
			if tc.wantError != "" {
				if !result.IsError || !strings.Contains(text, tc.wantError) {
					t.Fatalf("expected %q, got %+v: %s", tc.wantError, result, text)
				}
				after, _ := service.Member(caller.Thread)
				effect, err := service.Effect(caller.Thread, caller.Call)
				if created != 0 || effect != nil || err != nil || !reflect.DeepEqual(before, after) {
					t.Fatalf("rejected spawn mutated parent or reserved/created a child: created=%d effect=%+v err=%v", created, effect, err)
				}
				return
			}
			if result.IsError || created != 1 {
				t.Fatalf("fresh spawn failed: created=%d result=%s", created, text)
			}
			child, ok := service.Member("child-1")
			model, effort := tc.model, tc.effort
			if tc.override != "" {
				model, effort = "selected", "high"
			}
			var expectedEffort *string
			if effort != "" {
				expectedEffort = new(effort)
			}
			if !ok || child.Settings.Model != model || !sameEffort(child.Settings.Effort, expectedEffort) || child.Settings.Provider != "provider" || string(child.Settings.ApprovalPolicy) != `"never"` {
				t.Fatalf("child did not inherit captured authority: %+v", child)
			}
			assertForkJSON(t, child.Settings.PermissionProfile, `{"type":"disabled"}`)
		})
	}
}

func TestCapturedInvocationIdentityAndTurnLifetime(t *testing.T) {
	driver, _, _, caller := reconciliationFixture(t)
	if err := driver.CaptureInvocation(caller, "model", "low"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, model, effort string
		caller              control.Identity
	}{
		{"changed model", "other", "low", caller},
		{"changed effort", "model", "high", caller},
		{"missing model", "", "low", caller},
		{"missing item", "model", "low", control.Identity{Thread: caller.Thread, Session: caller.Session, Turn: caller.Turn}},
		{"foreign thread", "model", "low", control.Identity{Thread: "foreign", Session: caller.Session, Turn: caller.Turn, Item: caller.Item}},
		{"foreign session", "model", "low", control.Identity{Thread: caller.Thread, Session: "foreign", Turn: caller.Turn, Item: caller.Item}},
		{"foreign turn", "model", "low", control.Identity{Thread: caller.Thread, Session: caller.Session, Turn: "foreign", Item: caller.Item}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := driver.CaptureInvocation(tc.caller, tc.model, tc.effort); err == nil {
				t.Fatal("conflicting capture accepted")
			}
			if got := driver.invocations[caller.Thread].items[caller.Item]; got != (invokingSettings{model: "model", effort: "low"}) {
				t.Fatalf("rejection replaced the original invoking settings: %+v", got)
			}
		})
	}
	if err := driver.CaptureInvocation(caller, "model", "low"); err != nil {
		t.Fatal("idempotent completion:", err)
	}
	for index := range 8 {
		if err := driver.control.HostCompleted(t.Context(), caller.Thread, caller.Turn, control.Completed, control.HostFailure{}); err != nil {
			t.Fatal(err)
		}
		caller.Turn = fmt.Sprintf("next-%d", index)
		if err := driver.control.RequestStarted(t.Context(), caller, true); err != nil {
			t.Fatal(err)
		}
		if err := driver.CaptureInvocation(caller, "model", "high"); err != nil {
			t.Fatal(err)
		}
		if len(driver.invocations) != 1 || len(driver.invocations[caller.Thread].items) != 1 || driver.invocations[caller.Thread].turn != caller.Turn {
			t.Fatal("capture retained retired turns")
		}
	}
}

func TestPrepareSpawnClaudeNativeForkAndRetainedConfiguration(t *testing.T) {
	retained, err := history.NewService(control.RecordKinds())
	if err != nil {
		t.Fatal(err)
	}
	store, err := retained.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.SelectSession(t.Context(), nil, history.Limits{Total: history.DefaultStorageBytes}, "root")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(session.Close)
	workspace := t.TempDir()
	path := filepath.Join(workspace, "root.jsonl")
	roleTestFile(t, path, fmt.Sprintf(`{"type":"session_meta","payload":{"id":"root"}}
{"type":"event_msg","payload":{"type":"thread_settings_applied","thread_id":"root","thread_settings":{"model":"codex","model_provider_id":"provider","cwd":%q,"permission_profile":{"type":"disabled"},"approval_policy":"never"}}}
{"type":"turn_context","payload":{"turn_id":"root-turn","model":"codex","cwd":%q,"permission_profile":{"type":"disabled"},"approval_policy":"never"}}
`, workspace, workspace))
	config := jsontext.Value(`{"layers":[{"name":{},"config":{"agents":{"review-correctness":{"description":"Review correctness."}}}}],"config":{"model_reasoning_summary":"auto","service_tier":"default","agents":{"default_subagent_model":"codex","default_subagent_reasoning_effort":"ultra"},"features":{"multi_agent_v2":{"subagent_developer_instructions":"frozen child instructions"}}},"host_extension":{"retained":true}}`)
	service := control.New()
	driver, err := NewDriver(service)
	if err != nil {
		t.Fatal(err)
	}
	runtime := instructionRuntime{info: control.ThreadInfo{Thread: "root", Session: "root-session", Path: path}, config: config}
	host := &reconciliationHost{}
	if err := driver.Connect(instructionNative{Native: host, owner: runtime}, host); err != nil {
		t.Fatal(err)
	}
	caller := control.Identity{Thread: "root", Session: "root-session", Turn: "root-turn", Call: "spawn", Item: "exec"}
	if err := driver.AdmitRequest(t.Context(), session, caller, true); err != nil {
		t.Fatal(err)
	}
	child, err := driver.PrepareSpawn(t.Context(), binding.MCPIdentity{Thread: caller.Thread, Session: caller.Session, Turn: caller.Turn, Call: caller.Call, Item: caller.Item, Model: "codex"}, SpawnOptions{TaskName: "claude", Message: "assignment", ForkTurns: new("none"), Model: new("claude:parent")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertForkJSON(t, child.Settings.Preparation, string(config))
	spawn, err := service.ReserveSpawn(t.Context(), caller, control.SpawnRequest{Name: child.TaskName, Message: child.Message, Settings: child.Settings, Limits: child.Limits, Arguments: child.Arguments})
	if err != nil {
		t.Fatal(err)
	}
	// A Claude path is an opaque native identity, not a readable Codex rollout.
	claudePath := filepath.Join(workspace, "nonexistent-claude-session")
	for _, phase := range []string{"create", "assignment_input", "admit", "initial"} {
		claimed, err := service.ClaimEffect(t.Context(), caller.Thread, spawn.ID)
		if err != nil || claimed == nil || claimed.Phase != phase {
			t.Fatalf("claim %s: %+v, %v", phase, claimed, err)
		}
		if err := service.EffectAcknowledged(t.Context(), caller.Thread, spawn.ID, control.Receipt{Phase: phase, Thread: "claude", Session: "claude-session", Path: claudePath, Turn: "claude-turn"}); err != nil {
			t.Fatal(err)
		}
	}
	runtime = instructionRuntime{info: control.ThreadInfo{Thread: "claude", Session: "claude-session", Path: claudePath}, config: child.Settings.Preparation}
	driver.host = instructionNative{Native: host, owner: runtime}
	driver.CaptureCatalog([]byte(`{"models":[{"slug":"codex-selected"}]}`))
	claudeDriver, err := NewDriver(service)
	if err != nil {
		t.Fatal(err)
	}
	claudeDriver.InheritCatalog(driver)
	claudeCaller := control.Identity{Thread: "claude", Session: "claude-session", Turn: "claude-turn", Call: "models", Item: "catalog-tool"}
	catalog, err := claudeDriver.Call(t.Context(), claudeCaller, "list_models", jsontext.Value(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	assertForkJSON(t, catalog, `{"models":[{"id":"codex-selected"}]}`)
	claudeDriver.CaptureCatalog([]byte(`{"models":[]}`))
	if _, err := claudeDriver.Call(t.Context(), claudeCaller, "list_models", jsontext.Value(`{}`)); err == nil {
		t.Fatal("missing selected Codex catalog silently succeeded")
	}
	fork, err := driver.PrepareSpawn(t.Context(), binding.MCPIdentity{Thread: "claude", Session: "claude-session", Turn: "claude-turn", Call: "native-fork", Item: "native-tool", Model: "claude:parent"}, SpawnOptions{TaskName: "fork", Message: "nested assignment"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !fork.FullHistory || fork.Import == "" || fork.Settings.Model != "claude:parent" {
		t.Fatalf("native fork = %+v", fork)
	}
	importPath, err := service.ImportPath("claude", fork.Import)
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := os.ReadFile(importPath)
	if err != nil {
		t.Fatal(err)
	}
	assertForkJSON(t, descriptor, fmt.Sprintf(`{"backend":"claude","session":"claude-session","path":%q}`, claudePath))
	codex, err := driver.PrepareSpawn(t.Context(), binding.MCPIdentity{Thread: "claude", Session: "claude-session", Turn: "claude-turn", Call: "codex-child", Item: "codex-tool", Model: "claude:parent"}, SpawnOptions{TaskName: "review", Message: "review assignment", ForkTurns: new("none"), Model: new("codex-selected"), ReasoningEffort: new("medium"), AgentType: new("review-correctness")}, []Model{{Name: "codex-selected", Efforts: []string{"medium"}}})
	if err != nil {
		t.Fatal(err)
	}
	if codex.FullHistory || codex.Settings.Model != "codex-selected" || !sameEffort(codex.Settings.Summary, new("auto")) || !sameEffort(codex.Settings.Tier, new("default")) {
		t.Fatalf("Claude-to-Codex preparation = %+v", codex)
	}
	// Restore uses retained preparation. Configuration exposes that snapshot to
	// later nested preparation without consulting the original Codex runtime.
	restored := control.New()
	if err := restored.Restore(t.Context(), session); err != nil {
		t.Fatal(err)
	}
	member, ok := restored.Member("claude")
	if !ok {
		t.Fatal("Claude member was not retained")
	}
	assertForkJSON(t, member.Settings.Preparation, string(config))
	driver.control = restored
	runtime.config = member.Settings.Preparation
	driver.host = instructionNative{Native: host, owner: runtime}
	settings, captured, _, err := driver.capture(t.Context(), control.Identity{Thread: "claude", Session: "claude-session", Turn: "later-turn"})
	if err != nil {
		t.Fatal(err)
	}
	nested, err := prepareChild(settings, member.Limits, SpawnOptions{Message: "nested fresh assignment", ForkTurns: new("none")}, nil, captured.Layers, [3]*string{captured.Config.Agents.Model, captured.Config.Agents.Effort, captured.Config.Features.MultiAgent.ChildInstructions})
	if err != nil {
		t.Fatal(err)
	}
	if nested.Settings.Model != "claude:parent" || nested.Settings.DeveloperInstructions == nil || *nested.Settings.DeveloperInstructions != "frozen child instructions" {
		t.Fatalf("nested preparation = %+v", nested.Settings)
	}
	assertForkJSON(t, nested.Settings.Preparation, string(config))
	var frozen map[string]jsontext.Value
	if err := json.Unmarshal(nested.Settings.Preparation, &frozen); err != nil || frozen["host_extension"] == nil {
		t.Fatalf("unknown effective configuration was lost: %s, %v", nested.Settings.Preparation, err)
	}
}
