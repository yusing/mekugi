package router

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/claude"
	"github.com/yusing/mekugi/internal/session"
)

// Opt-in native acceptance sends one real prompt, with Claude owning the tool and
// permission lifecycle. It never routes inference or changes installed settings.
func TestNativeObservationClaudeSDKLive(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_CLAUDE_LIVE") != "1" {
		t.Skip("requires installed, authenticated Claude and explicitly enabled live inference")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	executable, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := filepath.Abs("../../bin/claude-bridge/dist/bridge.js")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bridge); err != nil {
		t.Fatal("run make build-claude first")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	service, binding, _ := observationHTTPFixture(t)
	endpoint := service.Endpoint()
	client, err := claude.Start(ctx, node, bridge, claude.Config{Cwd: binding.Workspace, Executable: executable, Companion: &claude.ObservationEndpoint{Socket: endpoint.Socket, Token: endpoint.Token}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	sent, finished := false, false
	writes := make(map[string]bool)
	results, permissions := 0, 0
	nativeID := ""
	for !finished {
		select {
		case <-ctx.Done():
			t.Fatal("native Claude acceptance timed out")
		case event, ok := <-client.Events():
			if !ok {
				t.Fatal("native runtime disconnected")
			}
			switch event.Kind {
			case "ready":
				if !sent {
					sent = true
					if err := client.Send(ctx, fmt.Sprintf("Use your native Write tool exactly once to create %s with the exact content companion-native-evidence followed by a newline. Do not use Bash, other tools or subagents. Then answer done.", filepath.Join(binding.Workspace, "native.txt"))); err != nil {
						t.Fatal(err)
					}
				}
			case "session":
				binding.Session = event.SessionID
			case "tool":
				if event.Role == "Write" {
					writes[event.ID] = true
					nativeID = event.ID
				}
			case "tool_result":
				if event.ID == nativeID {
					results++
				}
			case "prompt":
				if event.Prompt == nil {
					t.Fatal("missing native permission prompt")
				}
				allow := event.Prompt.Tool == "Write"
				if allow {
					permissions++
				}
				if err := client.Respond(ctx, session.Decision{ID: event.Prompt.ID, Allow: allow}); err != nil {
					t.Fatal(err)
				}
			case "notice":
				if strings.Contains(event.Text, "Companion capture unavailable") {
					t.Fatal(event.Text)
				}
			case "error":
				t.Fatal(event.Text)
			case "done":
				if event.Failed {
					t.Fatal("native turn failed")
				}
				finished = true
			}
		}
	}
	data, err := os.ReadFile(filepath.Join(binding.Workspace, "native.txt"))
	if err != nil || string(data) != "companion-native-evidence\n" {
		t.Fatalf("native write mismatch: %v", err)
	}
	if len(writes) != 1 || results != 1 {
		t.Fatalf("native execution count: Write starts=%d terminal results=%d", len(writes), results)
	}
	call := ObservationCall{Binding: binding, ID: nativeID}
	before, found, err := service.owner.store.lookup(ctx, binding.Workspace, observationKey(call)+"/before")
	if err != nil || !found {
		t.Fatalf("native PreToolUse baseline missing: %v", err)
	}
	after, found, err := service.owner.store.lookup(ctx, binding.Workspace, observationKey(call)+"/after")
	if err != nil || !found {
		t.Fatalf("native PostToolUse outcome missing: %v", err)
	}
	if after.ChangeID == "" || len(after.ReviewFiles) != 1 || before.ExecObservation == nil || before.ExecObservation.Files[0].Kind != execFileAbsent {
		t.Fatal("native capture did not preserve pre/post scope")
	}
	if before.NativeObservation.Call.ID != nativeID || after.NativeObservation.Call.ID != nativeID {
		t.Fatal("hook/SDK tool ID mismatch")
	}
	t.Logf("native exactly-once Write, hook/SDK ID equality, durable saved diff and %d native permission requests verified", permissions)
}

// TestNativeObservationClaudeHookAcceptance retains the native hook and SDK
// payloads, not invented IDs. Each case has a new service, store and workspace.
func TestNativeObservationClaudeHookAcceptance(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_CLAUDE_LIVE") != "1" {
		t.Skip("requires explicitly enabled installed-Claude inference")
	}
	for _, tc := range []struct {
		name, tool, instruction, content                                      string
		deny, disabled, failed, background, child, unavailable, coexist, stop bool
	}{
		{name: "disabled", tool: "Write", instruction: `Use Write exactly once to create native.txt containing exactly "native-once\n".`, content: "native-once\n", disabled: true},
		{name: "write", tool: "Write", instruction: `Use Write exactly once to create native.txt containing exactly "native-once\n".`, content: "native-once\n"},
		{name: "coexist", tool: "Write", instruction: `Use Write exactly once to create native.txt containing exactly "native-once\n".`, content: "native-once\n", coexist: true},
		{name: "service_unavailable", tool: "Write", instruction: `Use Write exactly once to create native.txt containing exactly "native-once\n".`, content: "native-once\n", unavailable: true},
		{name: "subagent", tool: "Write", instruction: `Use Agent exactly once with subagent_type general-purpose, asking it to use Write exactly once to create native.txt containing exactly "native-once\n" in the current workspace. The child must use no other tools and not delegate. Do not write the file yourself. Wait for the child.`, content: "native-once\n", child: true},
		{name: "denied", tool: "Write", instruction: `Use Write exactly once to create native.txt containing exactly "native-once\n". The user will deny it.`, deny: true, failed: true},
		{name: "failed", tool: "Bash", instruction: `Use Bash exactly once with command "exit 7".`, failed: true},
		{name: "partial", tool: "Bash", instruction: `Use Bash exactly once with command "printf 'native-once\n' >> native.txt; exit 7".`, content: "native-once\n", failed: true},
		{name: "no_effect", tool: "Bash", instruction: `Use Bash exactly once with command "printf native-no-effect".`},
		{name: "background_stopped", tool: "Bash", instruction: `Use Bash exactly once with run_in_background true and command "sleep 30; printf 'native-once\n' >> native.txt". Immediately use TaskStop exactly once to stop that task by its returned task ID.`, background: true, stop: true},
		{name: "background", tool: "Bash", instruction: `Use Bash exactly once with run_in_background true and command "sleep 8; printf 'native-once\n' >> native.txt".`, content: "native-once\n", background: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nativeAcceptanceSettingsUnchanged(t)
			service, binding, _ := observationHTTPFixture(t)
			bridge, err := filepath.Abs("../../bin/claude-bridge/dist/bridge.js")
			if err != nil {
				t.Fatal(err)
			}
			executable, err := exec.LookPath("claude")
			if err != nil {
				t.Fatal(err)
			}
			config := map[string]any{"cwd": binding.Workspace, "bridge": bridge, "executable": executable,
				"tools": []string{tc.tool}, "deny": tc.deny, "background": tc.background, "child": tc.child,
				"prompt": tc.instruction + " This is a bounded native acceptance test. Do not use other tools, do not retry or repair even after failure or denial. Then answer done."}
			if !tc.disabled {
				config["endpoint"] = service.Endpoint()
			}
			if tc.stop {
				config["tools"] = []string{"Bash", "TaskStop"}
			}
			if tc.child {
				config["tools"] = []string{"Write", "Agent"}
			}
			if tc.unavailable {
				config["endpoint"] = ObservationEndpoint{Socket: filepath.Join(t.TempDir(), "absent.sock"), Token: "unavailable-fixture"}
			}
			if tc.coexist {
				nativeAcceptanceCoexistence(t, binding.Workspace, config)
			}
			input, err := json.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 110*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "node", "testdata/claude-native-acceptance.mjs")
			cmd.Stdin = bytes.NewReader(input)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			data, err := cmd.Output()
			if err != nil {
				t.Fatalf("native probe failed: %v (stderr withheld)", err)
			}
			if err := nativeAcceptanceEvidence(os.Getenv("MEKUGI_CLAUDE_NATIVE_EVIDENCE"), tc.name, data); err != nil {
				t.Fatal(err)
			}
			var records []struct {
				Kind  string         `json:"kind"`
				Value jsontext.Value `json:"value"`
			}
			if err := json.Unmarshal(data, &records); err != nil {
				t.Fatal(err)
			}
			type nativeHook struct {
				Event   string         `json:"hook_event_name"`
				Session string         `json:"session_id"`
				Agent   string         `json:"agent_id"`
				ID      string         `json:"tool_use_id"`
				Tool    string         `json:"tool_name"`
				Input   jsontext.Value `json:"tool_input"`
			}
			var pre []nativeHook
			results := map[string]bool{}
			inputs := map[string]jsontext.Value{}
			permissions, terminals, notices := 0, 0, 0
			agents := map[string]bool{}
			parents := map[string]string{}
			taskParents := map[string]string{}
			historyAgents := map[string]bool{}
			rootResult := false
			userHooks := map[string][]string{}
			for _, record := range records {
				switch record.Kind {
				case "user_hook":
					var h struct {
						Owner string `json:"owner"`
						Input struct {
							Event string `json:"hook_event_name"`
							ID    string `json:"tool_use_id"`
						} `json:"input"`
					}
					if err := json.Unmarshal(record.Value, &h); err != nil {
						t.Fatal(err)
					}
					userHooks[h.Owner] = append(userHooks[h.Owner], h.Input.Event+":"+h.Input.ID)
				case "error":
					t.Errorf("native probe: %s", record.Value)
				case "notice":
					notices++
					if !tc.unavailable {
						t.Errorf("native probe: %s", record.Value)
					}
				case "unavailable":
					t.Logf("UNAVAILABLE: %s", record.Value)
				case "subagent_history":
					var h struct {
						Agent    string `json:"agent"`
						Messages []any  `json:"messages"`
					}
					if err := json.Unmarshal(record.Value, &h); err != nil {
						t.Fatal(err)
					}
					if len(h.Messages) > 0 {
						historyAgents[h.Agent] = true
					}
				case "permission":
					var p struct {
						Allow bool `json:"allow"`
					}
					if err := json.Unmarshal(record.Value, &p); err != nil {
						t.Fatal(err)
					}
					if p.Allow == tc.deny {
						t.Error("permission decision changed")
					}
					permissions++
				case "hook":
					var hook nativeHook
					if err := json.Unmarshal(record.Value, &hook); err != nil {
						t.Fatal(err)
					}
					if hook.Event == "SubagentStart" {
						agents[hook.Agent] = true
					}
					if hook.Event == "PreToolUse" && hook.Tool == tc.tool {
						pre = append(pre, hook)
					}
				case "hook_return":
					var ret struct {
						Output map[string]any `json:"output"`
					}
					if err := json.Unmarshal(record.Value, &ret); err != nil {
						t.Fatal(err)
					}
					if len(ret.Output) != 0 {
						t.Error("observation changed native hook output")
					}
				case "sdk":
					var event struct {
						Type    string `json:"type"`
						Failed  bool   `json:"is_error"`
						Parent  string `json:"parent_tool_use_id"`
						Task    string `json:"task_id"`
						ToolID  string `json:"tool_use_id"`
						Subtype string `json:"subtype"`
						Status  string `json:"status"`
						Patch   struct {
							Status string `json:"status"`
						} `json:"patch"`
						Message struct {
							Content jsontext.Value `json:"content"`
						} `json:"message"`
					}
					if err := json.Unmarshal(record.Value, &event); err != nil {
						t.Fatal(err)
					}
					if event.Type == "result" {
						rootResult = true
						if event.Failed {
							t.Error("native root turn failed")
						}
					}
					if event.Subtype == "task_started" {
						taskParents[event.Task] = event.ToolID
					}
					if event.Subtype == "task_notification" || event.Subtype == "task_updated" && (event.Patch.Status == "completed" || event.Patch.Status == "failed" || event.Patch.Status == "killed") {
						terminals++
						t.Logf("native terminal form: %s", event.Subtype)
					}
					if event.Message.Content.Kind() != '[' {
						continue
					}
					var blocks []struct {
						Type   string         `json:"type"`
						Name   string         `json:"name"`
						ID     string         `json:"id"`
						ToolID string         `json:"tool_use_id"`
						Input  jsontext.Value `json:"input"`
						Failed bool           `json:"is_error"`
					}
					if err := json.Unmarshal(event.Message.Content, &blocks); err != nil {
						t.Fatal(err)
					}
					for _, block := range blocks {
						if block.Type == "tool_use" && block.Name == tc.tool {
							inputs[block.ID] = block.Input
							parents[block.ID] = event.Parent
						}
						if block.Type == "tool_result" {
							if _, found := results[block.ToolID]; found {
								t.Error("duplicate native result")
							}
							results[block.ToolID] = block.Failed
						}
					}
				}
			}
			if len(pre) != 1 || len(inputs) != 1 || (!tc.child && !tc.stop && len(results) != 1) {
				t.Fatalf("native call counts: hooks=%d SDK calls=%d results=%d", len(pre), len(inputs), len(results))
			}
			if !rootResult {
				t.Error("native root completion unavailable")
			}
			hook := pre[0]
			if tc.coexist {
				for _, owner := range []string{"project", "plugin"} {
					if !reflect.DeepEqual(userHooks[owner], []string{"PreToolUse:" + hook.ID, "PostToolUse:" + hook.ID}) {
						t.Fatalf("%s native hooks missing or reordered: %v", owner, userHooks[owner])
					}
				}
			}
			if hook.Tool != tc.tool || !tc.child && (hook.Agent != "" || parents[hook.ID] != "") {
				t.Fatal("unexpected root tool identity")
			}
			if tc.child {
				if !agents[hook.Agent] || parents[hook.ID] == "" || taskParents[hook.Agent] != parents[hook.ID] {
					t.Fatal("native subagent identity unproven")
				}
				if !historyAgents[hook.Agent] {
					t.Log("UNAVAILABLE: native child history missing")
				} else {
					t.Log("verified native hook agent_id equals retained subagent history identity")
				}
			}
			var hookInput, sdkInput any
			if err := json.Unmarshal(hook.Input, &hookInput); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(inputs[hook.ID], &sdkInput); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(hookInput, sdkInput) {
				t.Fatal("native hook/SDK input mismatch")
			}
			if tc.deny && permissions == 0 {
				t.Skip("UNAVAILABLE: unchanged native permissions auto-approved the call; user denial was not requested")
			}
			if failed, found := results[hook.ID]; !found || failed != tc.failed {
				t.Fatalf("original native result: found=%v failed=%v", found, failed)
			}
			if tc.deny && permissions != 1 {
				t.Fatal("denied case requires one actual native permission request")
			}
			content, readErr := os.ReadFile(filepath.Join(binding.Workspace, "native.txt"))
			if tc.content == "" {
				if !os.IsNotExist(readErr) {
					t.Fatal("no-effect case wrote native.txt")
				}
			} else if readErr != nil || string(content) != tc.content {
				t.Fatalf("exactly-once file effect: %q %v", content, readErr)
			}
			binding.Session = hook.Session
			binding.Agent = hook.Agent
			call := ObservationCall{Binding: binding, ID: hook.ID}
			after, found, err := service.owner.store.lookup(ctx, binding.Workspace, observationKey(call)+"/after")
			if err != nil {
				t.Fatal(err)
			}
			if tc.disabled || tc.unavailable {
				if tc.unavailable && notices == 0 {
					t.Fatal("service failure not reported")
				}
				if found || service.owner.pendingCount.Load() != 0 {
					t.Fatal("disabled companion captured native call")
				}
				return
			}
			if tc.background && terminals == 0 {
				t.Log("UNAVAILABLE: no native background terminal event; capture must remain unfinished")
				if found {
					t.Error("root completion finalized background capture")
				}
				return
			}
			if tc.deny && !found {
				t.Log("UNAVAILABLE: native permission denial has no terminal observation hook; retained baseline remains unfinished")
				return
			}
			if !found || after.NativeObservation == nil {
				t.Fatal("native terminal capture missing")
			}
			before := nativeObservationHistory(t, service.owner.store, call, "before")
			if before.NativeObservation == nil || !reflect.DeepEqual(before.NativeObservation.Call, after.NativeObservation.Call) {
				t.Fatal("terminal hook changed original call")
			}
			var retainedInput any
			if err := json.Unmarshal([]byte(strings.ReplaceAll(after.NativeObservation.Call.Input, binding.Workspace, "<workspace>")), &retainedInput); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(retainedInput, hookInput) {
				t.Fatal("service changed native input")
			}
			if service.owner.pendingCount.Load() != 0 {
				t.Fatal("terminal native call left pending capture")
			}
			wantStatus := "completed"
			if tc.failed {
				wantStatus = "failed"
			}
			if tc.stop {
				wantStatus = "stopped"
			}
			if after.ExecOutcome.Status != wantStatus {
				t.Fatalf("native outcome changed: %s", after.ExecOutcome.Status)
			}
			if (after.ChangeID != "") != (tc.content != "") {
				t.Fatalf("effect/capture mismatch: change=%q", after.ChangeID)
			}
			// Reopen the durable owner and replay terminal delivery: no second ID,
			// no process revival, and exactly one saved-Diff chunk per actual edit.
			reader, err := openMekugiReplayStore(service.owner.store.directory)
			if err != nil {
				t.Fatal(err)
			}
			restored := nativeObservationHistory(t, reader, call, "after")
			if !reflect.DeepEqual(after, restored) {
				t.Fatal("restart changed native evidence")
			}
			id, err := service.owner.after(ctx, *after.NativeObservation.Call, *after.NativeObservation.Terminal)
			if err != nil || id != after.ChangeID {
				t.Fatal("duplicate completion changed capture")
			}
			files, err := reader.liveDiffSnapshotFiles(ctx, liveDiffScope{Workspaces: map[string]map[string]bool{binding.Workspace: {observationThread(binding): true}}})
			if err != nil {
				t.Fatal(err)
			}
			if tc.content != "" {
				path := filepath.Join(binding.Workspace, "native.txt")
				if len(files) != 1 || files[0].Path != path || len(files[0].Chunks) != 1 {
					t.Fatal("saved Diff missing, mis-scoped or duplicated native edit")
				}
				chunk := files[0].Chunks[0]
				expected := mekugi.RenderReviewFile("", path, "", tc.content)
				if chunk.Review.BeforePath != expected.BeforePath || chunk.Review.AfterPath != expected.AfterPath || chunk.Review.Diff != expected.Diff || chunk.Change != after.ChangeID || chunk.Caller != after.Caller {
					t.Fatal("reopened saved Diff lost native content or attribution")
				}
			} else if len(files) != 0 {
				t.Fatal("no-effect call appeared in saved Diff")
			}
			t.Logf("verified native %s: original input/result, one tool execution, %d permission requests, durable change=%t", tc.name, permissions, after.ChangeID != "")
		})
	}
}

// Compare content hashes without ever logging configuration or credentials.
func nativeAcceptanceSettingsUnchanged(t *testing.T) {
	t.Helper()
	directory := os.Getenv("CLAUDE_CONFIG_DIR")
	if directory == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		directory = filepath.Join(home, ".claude")
	}
	for _, name := range []string{"settings.json", "settings.local.json", ".credentials.json"} {
		path := filepath.Join(directory, name)
		before, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal("cannot fingerprint native settings")
		}
		existed := err == nil
		hash := sha256.Sum256(before)
		t.Cleanup(func() {
			after, err := os.ReadFile(path)
			if err != nil && !os.IsNotExist(err) {
				t.Error("cannot verify native settings")
				return
			}
			if (err == nil) != existed || sha256.Sum256(after) != hash {
				t.Errorf("native %s changed during acceptance; no restoration attempted", name)
			}
		})
	}
}

func nativeAcceptanceCoexistence(t *testing.T, workspace string, config map[string]any) {
	t.Helper()
	directory := t.TempDir()
	log := filepath.Join(directory, "hooks.jsonl")
	script := filepath.Join(directory, "hook.mjs")
	nativeObservationWrite(t, script, `import {readFileSync, appendFileSync} from 'node:fs';
appendFileSync(process.argv[3], JSON.stringify({owner: process.argv[2], input: JSON.parse(readFileSync(0, 'utf8'))}) + '\n');
`)
	hooks := func(owner string) map[string]any {
		command := fmt.Sprintf("node '%s' %s '%s'", strings.ReplaceAll(script, "'", "'\"'\"'"), owner, strings.ReplaceAll(log, "'", "'\"'\"'"))
		matcher := []any{map[string]any{"matcher": "Write", "hooks": []any{map[string]any{"type": "command", "command": command}}}}
		return map[string]any{"hooks": map[string]any{"PreToolUse": matcher, "PostToolUse": matcher}}
	}
	writeJSON := func(path string, value any) {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		nativeObservationWrite(t, path, string(data))
	}
	writeJSON(filepath.Join(workspace, ".claude", "settings.json"), hooks("project"))
	plugin := filepath.Join(directory, "plugin")
	writeJSON(filepath.Join(plugin, ".claude-plugin", "plugin.json"), map[string]string{"name": "native-acceptance", "version": "1.0.0"})
	writeJSON(filepath.Join(plugin, "hooks", "hooks.json"), hooks("plugin"))
	config["plugin"], config["hookLog"] = plugin, log
}

// Validate before optional persistence: a missing task_notification can leave
// the probe without an authoritative output_file path to redact.
func nativeAcceptanceEvidence(directory, name string, data []byte) error {
	if bytes.Contains(data, []byte("/tmp/claude-")) {
		return errors.New("native task output path escaped redaction; evidence not saved")
	}
	if directory == "" {
		return nil
	}
	if !filepath.IsAbs(directory) {
		return errors.New("evidence directory must be absolute")
	}
	return os.WriteFile(filepath.Join(directory, name+".json"), data, 0600)
}

func TestNativeObservationClaudeEvidenceRedaction(t *testing.T) {
	directory := t.TempDir()
	unsafe := []byte(`[{"kind":"sdk","value":{"message":{"content":"Output is being written to: /tmp/claude-1000/session/tasks/task.output"}}}]`)
	if err := nativeAcceptanceEvidence(directory, "missing-notification", unsafe); err == nil {
		t.Fatal("accepted output path without terminal redaction evidence")
	}
	if _, err := os.Stat(filepath.Join(directory, "missing-notification.json")); !os.IsNotExist(err) {
		t.Fatal("unsafe native payload was persisted")
	}
	safe := bytes.ReplaceAll(unsafe, []byte("/tmp/claude-1000/session/tasks/task.output"), []byte("<native-task-output>"))
	if err := nativeAcceptanceEvidence(directory, "redacted", safe); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(filepath.Join(directory, "redacted.json"))
	if err != nil || !bytes.Equal(saved, safe) {
		t.Fatal("redacted evidence not preserved")
	}
	if err := nativeAcceptanceEvidence("", "not-saved", unsafe); err == nil {
		t.Fatal("unsafe evidence silently accepted without export")
	}
	if err := nativeAcceptanceEvidence("relative", "not-saved", safe); err == nil {
		t.Fatal("relative evidence directory accepted")
	}
}
