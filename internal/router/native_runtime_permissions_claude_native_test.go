//go:build unix

package router

import (
	"context"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/claude"
)

// Only provider responses are scripted. Installed Claude owns permission policy,
// tool execution and cancellation; real decoded receipts reach the shared UI.
func TestNativeRuntimePermissionsClaudeNative(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_NATIVE_CLAUDE") != "1" {
		t.Skip("requires installed native Claude and built bridge")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	t.Setenv(routerTestWorkerEnvironment, "1")
	t.Setenv("MEKUGI_RUNTIME_DIR", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "native-permissions-fixture")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_SHELL", "/bin/bash")
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, ".claude"), 0700); err != nil {
		t.Fatal(err)
	}
	settings := []byte(`{"permissions":{"defaultMode":"default","ask":["Bash"],"deny":["Bash(sh ./automatic.sh)"]}}`)
	settingsPath := filepath.Join(workspace, ".claude", "settings.local.json")
	if err := os.WriteFile(settingsPath, settings, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "automatic.sh"), []byte("printf 'automatic\\n' >> effects.txt\n"), 0600); err != nil {
		t.Fatal(err)
	}
	agents := filepath.Join(workspace, ".claude", "agents")
	if err := os.Mkdir(agents, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agents, "permission-worker.md"), []byte("---\nname: permission-worker\ndescription: Native child permission fixture.\ntools: Bash\nmodel: haiku\n---\nExecute the assigned fixture once.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	phases := []string{"ALLOW", "DENY", "CANCEL", "AUTOMATIC", "CHILD"}
	command := "printf 'once\\n' >> effects.txt"
	const childCommand = "printf 'child-once\\n' >> child-effects.txt"
	var mu sync.Mutex
	requests := make(map[string]int)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var packet map[string]any
		if err := json.UnmarshalRead(http.MaxBytesReader(w, r.Body, 8<<20), &packet); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		content := []any{map[string]any{"type": "text", "text": "PERMISSION_SETTLED"}}
		stop := "end_turn"
		if tools, _ := packet["tools"].([]any); len(tools) > 0 {
			text := nativeGuidanceRequestText(packet["messages"])
			phase, latest := "", -1
			for _, candidate := range phases {
				if at := strings.LastIndex(text, "NATIVE_PERMISSION_"+candidate); at > latest {
					phase, latest = candidate, at
				}
			}
			if strings.Contains(text, "PERMISSION_NATIVE_WORKER") && !strings.Contains(text, "NATIVE_PERMISSION_CHILD") {
				phase = "WORKER"
			}
			mu.Lock()
			requests[phase]++
			count := requests[phase]
			mu.Unlock()
			if phase == "" {
				t.Errorf("unexpected provider phase %q request %d", phase, count)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if count == 1 {
				id, name := "permission-"+phase, "Bash"
				arguments := map[string]any{"command": command, "description": "Native permission " + phase}
				if phase == "AUTOMATIC" {
					arguments["command"] = "sh ./automatic.sh"
				} else if phase == "CHILD" {
					id, name = "permission-agent", "Agent"
					arguments = map[string]any{"description": "Native child permission", "subagent_type": "permission-worker", "prompt": "PERMISSION_NATIVE_WORKER", "run_in_background": false}
				} else if phase == "WORKER" {
					id = "permission-CHILD"
					arguments["command"] = childCommand
				}
				content = []any{map[string]any{"type": "tool_use", "id": id, "name": name, "input": arguments}}
				stop = "tool_use"
			}
		}
		nativeGuidanceProviderReply(w, packet, content, stop)
	}))
	defer provider.Close()
	t.Setenv("ANTHROPIC_BASE_URL", provider.URL)
	executable, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := filepath.Abs("../claude/bridge/dist/bridge.js")
	if err != nil {
		t.Fatal(err)
	}
	client, err := claude.Start(ctx, "node", bridge, claude.Config{Cwd: workspace, Executable: executable, Model: "haiku"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	u := newRuntimeUI(ctx, client, "Claude Code", workspace)
	defer u.shell.diff.close()
	defer u.shell.diffScreen.Close()
	state := func(id, want string) { t.Helper(); runtimePermissionState(t, u, id, want) }
	ready := false
	for !ready {
		select {
		case <-ctx.Done():
			t.Fatal("native permission startup timed out")
		case e, ok := <-client.Events():
			if !ok {
				t.Fatal("native bridge closed during startup")
			}
			if err := u.runtimeEvent(e); err != nil {
				t.Fatal(err)
			}
			ready = e.Kind == "ready"
		}
	}
	completed := make(map[string]string)
	for _, phase := range phases {
		id := "permission-" + phase
		runtimeKeys(t, u, "NATIVE_PERMISSION_"+phase+"\r")
		prompts, decisions, denials, dismissals, tools, results := 0, 0, 0, 0, 0, 0
		promptID := ""
		childID, childCaller := "", ""
		agentResults := 0
		settled := false
		for !settled {
			select {
			case <-ctx.Done():
				t.Fatalf("native %s timed out: %s", phase, runtimeFrame(t, u, 100, 28))
			case e, ok := <-client.Events():
				if !ok {
					t.Fatalf("native bridge closed during %s", phase)
				}
				if err := u.runtimeEvent(e); err != nil {
					t.Fatal(err)
				}
				switch e.Kind {
				case "tool":
					if e.ID == id && e.Text != "{}" {
						tools++
						childCaller = e.Caller
						var input struct {
							Command string `json:"command"`
						}
						wantCommand := command
						if phase == "AUTOMATIC" {
							wantCommand = "sh ./automatic.sh"
						} else if phase == "CHILD" {
							wantCommand = childCommand
						}
						if err := json.Unmarshal([]byte(e.Text), &input); err != nil || input.Command != wantCommand {
							t.Fatalf("native complete tool input changed: %+v error=%v", e, err)
						}
					}
				case "prompt":
					prompts++
					if e.Prompt == nil || e.Prompt.ToolID != id || phase == "AUTOMATIC" {
						t.Fatalf("unexpected native permission: %+v", e.Prompt)
					}
					if phase == "CHILD" {
						childID = e.Prompt.Caller
						if childID == "" || childCaller != "permission-agent" {
							t.Fatalf("native child identity missing: prompt=%+v tool caller=%q", e.Prompt, childCaller)
						}
					} else if e.Prompt.Caller != "" {
						t.Fatalf("Main prompt acquired child caller: %+v", e.Prompt)
					}
					promptID = e.Prompt.ID
					state(id, "Pending Approval")
					frame := runtimeFrame(t, u, 100, 28)
					if !strings.Contains(frame, "Allow once") || !strings.Contains(frame, "Deny") {
						t.Fatalf("missing shared permission dock: %s", frame)
					}
					if phase == "CANCEL" {
						// Foreground cancellation, not a manufactured denial/receipt.
						if err := client.Interrupt(ctx); err != nil {
							t.Fatal(err)
						}
					} else if phase == "ALLOW" || phase == "CHILD" {
						runtimeKeys(t, u, "1\r")
					} else {
						runtimeKeys(t, u, "\x03")
					}
					state(id, "Pending Approval")
					for _, row := range u.view.entries {
						if row.CallID == id {
							u.shell.openEntry(u.view, row.Seq)
						}
					}
				case "permission_decision":
					decisions++
					if e.ID != id || e.Failed != (phase == "DENY") {
						t.Fatalf("wrong submitted decision: %+v", e)
					}
					// Resolving canUseTool precedes the native control-response write.
					// Submission alone does not confirm the engine consumed it.
					state(id, "Pending Approval")
				case "permission_denied":
					denials++
					if e.ID != id {
						t.Fatalf("wrong native denial: %+v", e)
					}
				case "dismiss":
					dismissals++
					if phase != "CANCEL" || e.ID != promptID {
						t.Fatalf("wrong native cancellation: %+v", e)
					}
				case "tool_result":
					if e.ID == "permission-agent" {
						agentResults++
						if e.Failed {
							t.Fatalf("native Agent failed: %s", e.Text)
						}
					}
					if e.ID == id {
						results++
						if e.Failed != (phase != "ALLOW" && phase != "CHILD") {
							t.Fatalf("wrong native tool outcome: %+v", e)
						}
					}
				case "done":
					settled = true
				}
			}
		}
		want := map[string]string{"ALLOW": "Approved", "DENY": "Declined", "AUTOMATIC": "Auto Denied", "CANCEL": "Cancelled", "CHILD": "Approved"}[phase]
		state(id, want)
		if phase != "AUTOMATIC" {
			u.shell.paintOutput(make([]string, 18), 80, 18)
			display := want
			if phase == "DENY" {
				display = "Denied"
			}
			if frame := drawOutputDialog(u.shell); !strings.Contains(frame, "Approval: "+display) {
				t.Fatalf("open dialog missed %s receipt: %s", phase, frame)
			}
		}
		u.shell.output = nil
		for previous, approval := range completed {
			state(previous, approval) // Same command text, distinct native IDs.
		}
		completed[id] = want
		badReceipts := tools == 0 || results > 1
		switch phase {
		case "AUTOMATIC":
			badReceipts = badReceipts || prompts != 0 || decisions != 0 || denials == 0 || results != 1
		case "CANCEL":
			badReceipts = badReceipts || prompts != 1 || decisions != 0 || dismissals != 1
		default:
			badReceipts = badReceipts || prompts != 1 || decisions != 1 || results != 1 || dismissals != 0
		}
		if badReceipts {
			t.Fatalf("%s receipts: tools=%d results=%d prompts=%d decisions=%d denials=%d dismissals=%d", phase, tools, results, prompts, decisions, denials, dismissals)
		}
		if effects, err := os.ReadFile(filepath.Join(workspace, "effects.txt")); err != nil || string(effects) != "once\n" {
			t.Fatalf("%s effects=%q error=%v", phase, effects, err)
		}
		if u.questions.active != nil {
			t.Fatalf("%s left a permission dock active", phase)
		}
		if phase == "CHILD" {
			task := u.runtime.tasks[childID]
			if task.ID != childID || u.runtimeCallerLane(childCaller) != runtimeTaskLane(childID) || agentResults != 1 {
				t.Fatalf("native child metadata mismatch: child=%q caller=%q task=%+v Agent results=%d", childID, childCaller, task, agentResults)
			}
			for _, view := range []*liveActivityView{u.view, u.agents} {
				commandRows, questionRows := 0, 0
				for _, row := range view.entries {
					if _, previous := completed[row.CallID]; previous && row.CallID != id && row.Agent != "Main" {
						t.Fatalf("prior Main command %s moved to %q", row.CallID, row.Agent)
					}
					if row.CallID == id || row.native != nil && row.native.item == "permission/"+promptID {
						if row.Agent != runtimeTaskLane(childID) {
							t.Fatalf("child command/question moved to %q, want native child %q", row.Agent, childID)
						}
						if row.CallID == id {
							commandRows++
						} else {
							questionRows++
						}
					}
				}
				if commandRows != 1 || questionRows != 1 {
					t.Fatalf("child command/question rows = %d/%d", commandRows, questionRows)
				}
			}
			if effects, err := os.ReadFile(filepath.Join(workspace, "child-effects.txt")); err != nil || string(effects) != "child-once\n" {
				t.Fatalf("child effects=%q error=%v", effects, err)
			}
			t.Logf("native child %s: permission caller, Agent parent %s and shared command/question rows correlated; one Agent result and child effect", childID, childCaller)
		}
		t.Logf("%s: native %s, exact ID %s; one allow effect retained", phase, want, id)
	}
	if after, err := os.ReadFile(settingsPath); err != nil || string(after) != string(settings) {
		t.Fatalf("native fixture settings changed: %v", err)
	}
}
