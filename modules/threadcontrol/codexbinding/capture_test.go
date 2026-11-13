package codexbinding

import (
	"encoding/json/jsontext"
	"fmt"
	"net"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
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
