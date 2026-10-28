package codexbinding

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

type instructionNative struct {
	control.Native
	owner control.Runtime
}

func (n instructionNative) Runtime(string) (control.Runtime, error) { return n.owner, nil }

func (n instructionNative) Inspect(ctx context.Context, thread string) (control.SessionDetails, error) {
	return n.owner.Inspect(ctx, thread)
}

type instructionRuntime struct {
	control.Runtime
	info   control.ThreadInfo
	config jsontext.Value
}

func (r instructionRuntime) Thread(context.Context, string) (control.ThreadInfo, error) {
	return r.info, nil
}

func (r instructionRuntime) Configuration(context.Context, string, string) (jsontext.Value, error) {
	if r.config != nil {
		return r.config, nil
	}
	return jsontext.Value(`{"layers":[]}`), nil
}

func TestInstructionsRequireAdmittedRoleOrUsageProvenance(t *testing.T) {
	const header = `{"type":"session_meta","payload":{"id":"root"}}` + "\n"
	const generated = `{"type":"message","id":"generated","role":"developer","content":[{"type":"input_text","text":"native role"},{"type":"input_text","text":"native usage"},{"type":"input_text","text":"native mode"},{"type":"input_text","text":"custom policy"}],"internal_chat_message_metadata_passthrough":{"content_item_kinds":["multi_agent.role_instructions","multi_agent.usage_hint","multi_agent.mode_instructions","managed_config.developer_instructions"]}}`
	const modeOnly = `{"type":"message","id":"mode","role":"developer","content":[{"type":"input_text","text":"native mode"}],"internal_chat_message_metadata_passthrough":{"content_item_kinds":["multi_agent.mode_instructions"]}}`
	const lookalike = `{"type":"message","id":"custom","role":"developer","content":[{"type":"input_text","text":"native role"}],"internal_chat_message_metadata_passthrough":{"content_item_kinds":["multi_agent.role_instructions.extra"]}}`
	for _, tc := range []struct {
		name, source, retained string
		want                   int
	}{
		{name: "missing history"},
		{name: "malformed history", source: "{"},
		{name: "foreign history", source: strings.Replace(header, `"root"`, `"foreign"`, 1) + `{"type":"response_item","payload":` + generated + "}\n"},
		{name: "mode alone is insufficient", source: header + `{"type":"response_item","payload":` + modeOnly + "}\n"},
		{name: "retained kind lookalike is insufficient", retained: lookalike},
		{name: "authenticated history", source: header + `{"type":"response_item","payload":` + generated + "}\n", want: 2},
		{name: "retained fragments satisfy unavailable history", retained: generated, want: 2},
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
			path := filepath.Join(workspace, "rollout.jsonl")
			if tc.source != "" {
				roleTestFile(t, path, tc.source)
			}
			service := control.New()
			if err := service.AdmitRoot(t.Context(), session, &control.Member{Thread: "root", Session: "session", Task: "/root", Path: path, Settings: control.Settings{Workspace: workspace}}); err != nil {
				t.Fatal(err)
			}
			if tc.retained != "" {
				parts, err := binding.NativeInstructionFragments("root", jsontext.Value(tc.retained))
				if err != nil {
					t.Fatal(err)
				}
				if err := service.RetainInstructions(t.Context(), "root", path, parts); err != nil {
					t.Fatal(err)
				}
			}
			driver, err := NewDriver(service)
			if err != nil {
				t.Fatal(err)
			}
			driver.host = instructionNative{owner: instructionRuntime{}}
			replacements, err := driver.Instructions(t.Context(), "root", "")
			if tc.want == 0 {
				if err == nil || !strings.Contains(err.Error(), "provenance") || len(replacements) != 0 {
					t.Fatalf("unavailable required provenance: replacements=%+v err=%v", replacements, err)
				}
				return
			}
			if err != nil || len(replacements) != tc.want {
				t.Fatalf("replacements=%+v err=%v", replacements, err)
			}
			for _, replacement := range replacements {
				switch replacement.Fragment.Kind {
				case "multi_agent.role_instructions":
					if replacement.Fragment.Text != "native role" || !strings.Contains(replacement.Text, "tools.mcp__threadcontrol__<name>") {
						t.Fatalf("role projection = %+v", replacement)
					}
				case "multi_agent.usage_hint":
					if replacement.Fragment.Text != "native usage" || replacement.Text != "" {
						t.Fatalf("duplicate usage projection = %+v", replacement)
					}
				default:
					t.Fatalf("unrelated content kind adapted: %+v", replacement)
				}
			}
		})
	}
}

func TestInstructionsUseCurrentModelAndPreserveCustomHints(t *testing.T) {
	for _, tc := range []struct {
		name          string
		child, custom bool
		want          string
	}{
		{"root model change", false, false, "NEW_ROOT_BASELINE"},
		{"root custom hint", false, true, "CUSTOM_HINT"},
		{"child model change", true, false, "NEW_CHILD_BASELINE"},
		{"child custom hint", true, true, "CUSTOM_HINT"},
		{"custom filesystem text", false, true, "You are `/root`, the primary agent in a team of agents collaborating to fulfill the user's goals.\nKeep /root/files unchanged."},
		{"root empty custom hint", false, true, ""},
		{"child empty custom hint", true, true, ""},
		{"root stock fallback", false, false, strings.ReplaceAll(defaultRootUsageHint, "/root", "agent:main")},
		{"child stock fallback", true, false, strings.ReplaceAll(defaultSubagentUsageHint, "/root", "agent:main")},
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
			path := filepath.Join(workspace, "rollout.jsonl")
			cwd, err := json.Marshal(workspace)
			if err != nil {
				t.Fatal(err)
			}
			roleTestFile(t, path, fmt.Sprintf(`{"type":"session_meta","payload":{"id":"root"}}
{"type":"event_msg","payload":{"type":"thread_settings_applied","thread_id":"root","thread_settings":{"model":"old-model","model_provider_id":"provider","cwd":%s,"permission_profile":{"type":"disabled"},"approval_policy":"never"}}}
{"type":"turn_context","payload":{"turn_id":"root-turn","model":"old-model","cwd":%s,"permission_profile":{"type":"disabled"},"approval_policy":"never"}}
`, cwd, cwd))
			settings := control.Settings{Workspace: workspace, Model: "old-model"}
			if tc.custom {
				settings.Config, err = json.Marshal(map[string]string{"features.multi_agent_v2.root_agent_usage_hint_text": tc.want})
				if err != nil {
					t.Fatal(err)
				}
			}
			service := control.New()
			limits := control.Limits{}
			if err := service.AdmitRoot(t.Context(), session, &control.Member{Thread: "root", Session: "session", Task: "/root", Path: path, AudienceAdmitted: true, Settings: settings, Limits: limits}); err != nil {
				t.Fatal(err)
			}
			driver, err := NewDriver(service)
			if err != nil {
				t.Fatal(err)
			}
			runtime := instructionRuntime{info: control.ThreadInfo{Thread: "root", Session: "session", Path: path}}
			if tc.custom {
				runtime.config = jsontext.Value(fmt.Sprintf(`{"layers":[],"config":{"features":{"multi_agent_v2":{"root_agent_usage_hint_text":"PARENT_CUSTOM_HINT","subagent_usage_hint_text":%q}}}}`, tc.want))
			}
			host := &reconciliationHost{}
			if err := driver.Connect(instructionNative{Native: host, owner: runtime}, host); err != nil {
				t.Fatal(err)
			}
			driver.models = []Model{
				{Name: "old-model", Root: new("OLD_ROOT_BASELINE"), Child: new("OLD_CHILD_BASELINE")},
				{Name: "new-model", Root: new("NEW_ROOT_BASELINE"), Child: new("NEW_CHILD_BASELINE")},
			}
			if strings.Contains(tc.name, "stock fallback") {
				driver.models = nil
			}
			caller := control.Identity{Thread: "root", Session: "session", Turn: "root-turn", Call: "spawn", Item: "exec"}
			if err := driver.AdmitRequest(t.Context(), session, caller, true); err != nil {
				t.Fatal(err)
			}
			thread := "root"
			if tc.child {
				prepared, err := driver.PrepareSpawn(t.Context(), binding.MCPIdentity{Thread: caller.Thread, Session: caller.Session, Turn: caller.Turn, Call: caller.Call, Item: caller.Item, Model: "old-model"}, SpawnOptions{TaskName: "worker", Message: "assignment", ForkTurns: new("none")}, driver.models)
				if err != nil {
					t.Fatal(err)
				}
				result, err := driver.SpawnPrepared(t.Context(), caller, prepared)
				if err != nil {
					t.Fatal(err)
				}
				thread = result.Thread
			}
			fragments, err := binding.NativeInstructionFragments(thread, jsontext.Value(`{"type":"message","id":"new-role","role":"developer","content":[{"type":"input_text","text":"current native role"}],"internal_chat_message_metadata_passthrough":{"content_item_kinds":["multi_agent.role_instructions"]}}`))
			if err != nil {
				t.Fatal(err)
			}
			member, _ := service.Member(thread)
			if err := service.RetainInstructions(t.Context(), thread, member.Path, fragments); err != nil {
				t.Fatal(err)
			}
			replacements, err := driver.Instructions(t.Context(), thread, "new-model")
			if err != nil {
				t.Fatal(err)
			}
			if len(replacements) != 1 || !strings.HasPrefix(replacements[0].Text, tc.want+"\n\n") {
				t.Fatalf("current model guidance = %+v, want baseline %q", replacements, tc.want)
			}
			identity := "Your logical task is agent:main."
			if tc.child {
				identity = "Your logical task is agent:main/worker. Your parent logical task is agent:main."
			}
			if !strings.Contains(replacements[0].Text, identity+" Call threadcontrol tools inside functions.exec as tools.mcp__threadcontrol__<name>.") {
				t.Fatalf("missing logical identity: %s", replacements[0].Text)
			}
			if strings.Contains(replacements[0].Text, "an explicit delegation request in your authenticated parent assignment carries the parent's authority within that assignment, subject to higher-priority limits") != tc.child {
				t.Fatalf("parent delegation authority must be child-only: %s", replacements[0].Text)
			}
			if !strings.Contains(replacements[0].Text, "call wait_agent alone") || !strings.Contains(replacements[0].Text, "later wait_agent output") || !strings.Contains(replacements[0].Text, "Omit timeout_ms") {
				t.Fatalf("missing wait delivery guidance: %s", replacements[0].Text)
			}
			driver.host = nil // Auxiliary projection must not call a busy host.
			passive, err := driver.RetainedInstructions(thread, "new-model")
			if err != nil || len(passive) != 1 || !strings.HasPrefix(passive[0].Text, tc.want+"\n\n") || !strings.Contains(passive[0].Text, identity) {
				t.Fatalf("retained instruction projection = %+v, %v", passive, err)
			}
			for _, projection := range []string{replacements[0].Text, passive[0].Text} {
				for _, required := range []string{
					"When spawning with fork_turns=none, supply preload for known files or skills the child needs to begin.",
					"exact source content before the first inference, avoiding discovery/read turns without inheriting unrelated parent history",
					"Select only startup context relevant to the assignment; AGENTS.md is host-loaded.",
					"Omit preload for full-history forks or when no startup sources are known.",
				} {
					if strings.Count(projection, required) != 1 {
						t.Fatalf("preload guidance must appear once: missing or duplicated %q in %s", required, projection)
					}
				}
			}
			member, _ = service.Member(thread)
			if member.Settings.Model != "old-model" {
				t.Fatal("instruction projection changed frozen preparation")
			}
		})
	}
}
