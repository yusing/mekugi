package codexbinding

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

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
	config := jsontext.Value(`{"layers":[],"config":{"agents":{"default_subagent_model":"codex","default_subagent_reasoning_effort":"ultra"},"features":{"multi_agent_v2":{"subagent_developer_instructions":"frozen child instructions"}}},"host_extension":{"retained":true}}`)
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
