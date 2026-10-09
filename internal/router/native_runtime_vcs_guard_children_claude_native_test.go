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
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/claude"
	"github.com/yusing/mekugi/internal/session"
	"github.com/yusing/mekugi/internal/shellsyntax"
)

// The provider supplies tools, while installed Claude owns both background
// children, their original hooks, StopTask and shell shutdown/resume.
func TestNativeRuntimeVCSGuardChildrenClaudeNative(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_NATIVE_CLAUDE") != "1" {
		t.Skip("requires installed native Claude and built bridge; no inference")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Second)
	defer cancel()
	t.Setenv(routerTestWorkerEnvironment, "1")
	t.Setenv("MEKUGI_RUNTIME_DIR", t.TempDir())
	config := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	t.Setenv("ANTHROPIC_API_KEY", "native-guard-children-fixture")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_SHELL", "/bin/bash")
	service, binding, _ := observationHTTPFixture(t)
	trace := traceNativeObservation(t, service)
	presentation, err := service.PrepareCompanion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	agents := filepath.Join(presentation.Plugin, "agents")
	if err := os.Mkdir(agents, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agents, "guard-child.md"), []byte("---\nname: guard-child\ndescription: Native guard isolation fixture.\ntools: Bash\nmodel: haiku\n---\nExecute the assigned fixture exactly once.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"cancelled", "surviving", ".claude"} {
		if err := os.Mkdir(filepath.Join(binding.Workspace, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	settings := `{"permissions":{"defaultMode":"default","ask":["Bash"]}}`
	settingsPath := filepath.Join(binding.Workspace, ".claude", "settings.local.json")
	if err := os.WriteFile(settingsPath, []byte(settings), 0600); err != nil {
		t.Fatal(err)
	}
	helper, err := execTrackHelper()
	if err != nil {
		t.Fatal(err)
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	fake := t.TempDir()
	effects := filepath.Join(binding.Workspace, "effects.txt")
	script := "#!/bin/sh\n[ \"$1\" = push ] || exec " + shellsyntax.Quote(realGit) + " \"$@\"\nprintf '%s:%s\\n' \"$PWD\" \"$*\" >> " + shellsyntax.Quote(effects) + "\n"
	if err := os.WriteFile(filepath.Join(fake, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fake+string(os.PathListSeparator)+os.Getenv("PATH"))
	bashEnv, err := service.PrepareCommandTracking(ctx, helper, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.PrepareVCSGuard(ctx, helper); err != nil {
		t.Fatal(err)
	}
	commands := map[string]string{"cancel": "cd cancelled && git push cancelled", "survive": "cd surviving && git push surviving", "follow": "git push surviving"}
	var mu sync.Mutex
	phases := make(map[string]int)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			w.WriteHeader(401)
			return
		}
		var packet map[string]any
		if err := json.UnmarshalRead(http.MaxBytesReader(w, r.Body, 8<<20), &packet); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		text := nativeGuidanceRequestText(packet["messages"])
		content := []any{map[string]any{"type": "text", "text": "GUARD_CHILDREN_SETTLED"}}
		stop := "end_turn"
		tools, _ := packet["tools"].([]any)
		if len(tools) > 0 {
			phase := "root"
			if strings.Contains(text, "GUARD_CHILD_FOLLOW!") {
				phase = "follow"
			} else if strings.Contains(text, "GUARD_CHILD_REPEAT!") {
				phase = "repeat"
			} else if strings.Contains(text, "GUARD_CHILDREN_ROOT!") {
				phase = "root"
			} else if strings.Contains(text, "GUARD_CHILD_CANCEL!") {
				phase = "cancel"
			} else if strings.Contains(text, "GUARD_CHILD_SURVIVE!") {
				phase = "survive"
			}
			mu.Lock()
			phases[phase]++
			count := phases[phase]
			mu.Unlock()
			if count > 8 {
				t.Error("native child provider exceeded phase budget")
				w.WriteHeader(400)
				return
			}
			if count == 1 {
				stop = "tool_use"
				if phase == "root" {
					content = nil
					for _, child := range []string{"cancel", "survive"} {
						prompt := "GUARD_CHILD_CANCEL!"
						if child == "survive" {
							prompt = "GUARD_CHILD_SURVIVE!"
						}
						content = append(content, map[string]any{"type": "tool_use", "id": "guard-spawn-" + child, "name": "Agent", "input": map[string]any{"description": "Guard child " + child, "subagent_type": "mekugi:guard-child", "prompt": prompt, "run_in_background": true}})
					}
				} else {
					command := commands[phase]
					if phase == "repeat" {
						command = commands["survive"]
					}
					content = []any{map[string]any{"type": "tool_use", "id": "guard-child-" + phase, "name": "Bash", "input": map[string]any{"command": command, "description": "Guard child " + phase}}}
				}
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
	endpoint := service.Endpoint()
	client, err := claude.Start(ctx, "node", bridge, claude.Config{Cwd: binding.Workspace, Executable: executable, Model: "haiku", Companion: &claude.ObservationEndpoint{Socket: endpoint.Socket, Token: endpoint.Token, Plugin: presentation.Plugin, FrontendDirectory: presentation.FrontendDirectory, JournalSchema: presentation.JournalSchema, BashEnv: bashEnv, VCSGuard: true, VCSGuardHelper: helper}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	u := newRuntimeUI(ctx, client, "Claude Code", binding.Workspace)
	u.attachRuntimeObservation(service)
	defer u.shell.diff.close()
	defer u.shell.diffScreen.Close()
	requests := make(map[string]*vcsApproval)
	tasks := make(map[string]string)
	terminal := make(map[string]string)
	results := make(map[string]bool)
	ready, mainDone, shellDone := false, false, false
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	await := func(label string, match func() bool) {
		t.Helper()
		for !match() {
			select {
			case <-ctx.Done():
				t.Fatalf("%s timed out: tasks=%v terminal=%v frame=%s", label, tasks, terminal, runtimeFrame(t, u, 100, 28))
			case <-tick.C:
				u.expireApprovals()
			case request := <-service.owner.execTrack.approvals:
				u.runtimeGuardApproval(request)
				requests[request.item] = request
				if request.thread != u.thread || request.cwd != filepath.Join(binding.Workspace, map[string]string{"guard-child-cancel": "cancelled", "guard-child-survive": "surviving", "guard-child-repeat": "surviving", "guard-child-follow": "surviving"}[request.item]) || request.executable != filepath.Join(fake, "git") {
					t.Fatalf("guard changed native identity: thread=%q item=%q cwd=%q executable=%q", request.thread, request.item, request.cwd, request.executable)
				}
				arg := "surviving"
				if request.item == "guard-child-cancel" {
					arg = "cancelled"
				}
				if !reflect.DeepEqual(request.argv, []string{"git", "push", arg}) {
					t.Fatalf("expanded argv=%q", request.argv)
				}
			case e, ok := <-client.Events():
				if !ok {
					t.Fatalf("bridge closed during %s", label)
				}
				if err := u.runtimeEvent(e); err != nil {
					t.Fatal(err)
				}
				switch e.Kind {
				case "session":
					t.Logf("%s: native session=%q cwd=%q launch=%q", label, e.SessionID, e.Cwd, binding.Workspace)
				case "ready":
					ready = true
				case "prompt":
					if e.Prompt == nil || !strings.HasPrefix(e.Prompt.ToolID, "guard-child-") {
						t.Fatalf("unexpected native prompt: %+v", e.Prompt)
					}
					if err := client.Respond(ctx, session.Decision{ID: e.Prompt.ID, Allow: true}); err != nil {
						t.Fatal(err)
					}
				case "task":
					if e.Task != nil {
						if strings.HasPrefix(e.Task.ToolID, "guard-spawn-") {
							tasks[strings.TrimPrefix(e.Task.ToolID, "guard-spawn-")] = e.Task.ID
						}
						if runtimeTaskTerminal(e.Task.Status) {
							terminal[e.Task.ID] = e.Task.Status
						}
					}
				case "tool_result":
					results[e.ID] = true
				case "done":
					mainDone = true
				case "shell_done":
					if e.Shell == nil || !e.Shell.Retained || e.Shell.ExitCode == nil || *e.Shell.ExitCode == 0 {
						t.Fatalf("native cancelled shell receipt=%+v", e.Shell)
					}
					shellDone = true
				case "error":
					t.Fatal(e.Text)
				case "notice":
					if strings.Contains(strings.ToLower(e.Text), "capture unavailable") {
						trace.mu.Lock()
						for id, call := range trace.before {
							t.Logf("native before %s: %+v", id, call)
						}
						trace.mu.Unlock()
						t.Fatal(e.Text)
					}
				}
			}
		}
	}
	await("ready", func() bool { return ready })
	runtimeKeys(t, u, "GUARD_CHILDREN_ROOT!\r")
	await("two concurrent reached writes", func() bool { return len(requests) == 2 && tasks["cancel"] != "" && tasks["survive"] != "" && mainDone })
	cancelled, surviving := requests["guard-child-cancel"], requests["guard-child-survive"]
	if cancelled == nil || surviving == nil || cancelled.finished() || surviving.finished() || len(u.approvals.pending) != 2 {
		t.Fatal("children did not independently wait together")
	}
	trace.mu.Lock()
	beforeCancel, beforeSurvive := trace.before["guard-child-cancel"], trace.before["guard-child-survive"]
	trace.mu.Unlock()
	for id, call := range map[string]ObservationCall{"guard-child-cancel": beforeCancel, "guard-child-survive": beforeSurvive} {
		phase := strings.TrimPrefix(id, "guard-child-")
		if call.ID != id || call.Command != commands[phase] || call.Binding.Session != u.thread || call.Binding.Workspace != binding.Workspace || call.Binding.Agent != tasks[phase] || call.Tool != "Bash" {
			t.Fatalf("original child hook tuple=%+v task=%q", call, tasks[phase])
		}
		var input struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal([]byte(call.Input), &input); err != nil || input.Command != commands[phase] {
			t.Fatalf("original child input=%q error=%v", call.Input, err)
		}
	}
	if beforeCancel.Binding.Agent == beforeSurvive.Binding.Agent {
		t.Fatal("concurrent children share capture authority")
	}
	if err := client.StopTask(ctx, tasks["cancel"]); err != nil {
		t.Fatal(err)
	}
	await("owned request withdrawal and native child stop", func() bool { return cancelled.finished() && terminal[tasks["cancel"]] != "" })
	u.expireApprovals()
	if status := terminal[tasks["cancel"]]; status != "stopped" && status != "killed" {
		t.Fatalf("StopTask lacks native stop receipt: %q", status)
	}
	if cancelled.outcome != "withdrawn" || surviving.finished() || terminal[tasks["survive"]] != "" || len(u.approvals.pending) != 1 || u.approvals.pending[0].guard != surviving {
		t.Fatal("child cancellation released or retired sibling authority")
	}
	if _, err := os.Stat(effects); !os.IsNotExist(err) {
		t.Fatalf("unapproved child write took effect: %v", err)
	}
	a := u.approvals.pending[0]
	if err := u.answerApproval(a, a.choices[1]); err != nil {
		t.Fatal(err)
	}
	await("surviving child execution and native terminal receipt", func() bool { return terminal[tasks["survive"]] != "" && results["guard-child-survive"] })
	if terminal[tasks["survive"]] != "completed" {
		t.Fatalf("surviving child did not complete: %q", terminal[tasks["survive"]])
	}
	want := filepath.Join(binding.Workspace, "surviving") + ":push surviving\n"
	if data, err := os.ReadFile(effects); err != nil || string(data) != want {
		t.Fatalf("isolated effects=%q error=%v", data, err)
	}
	trace.mu.Lock()
	after := trace.after["guard-child-survive"]
	trace.mu.Unlock()
	if !sameObservationCall(&beforeSurvive, &after) {
		t.Fatalf("surviving original hook tuple drifted: before=%+v after=%+v", beforeSurvive, after)
	}

	// User-shell cancellation makes the installed bridge shut down and resume
	// its native query. The UI-lifetime exact grant must outlive that handoff.
	shell := "printf ready > guard-shell-ready; while [ ! -f guard-shell-release ]; do sleep 0.05; done; printf forbidden >> effects.txt"
	runtimeKeys(t, u, "!"+shell+"\r")
	await("native shell process", func() bool {
		_, err := os.Stat(filepath.Join(binding.Workspace, "guard-shell-ready"))
		return err == nil
	})
	if err := client.Interrupt(ctx); err != nil {
		t.Fatal(err)
	}
	await("native shell shutdown/resume receipt", func() bool { return shellDone })
	mainDone = false
	runtimeKeys(t, u, "GUARD_CHILD_REPEAT!\r")
	await("follow-up reused exact UI grant", func() bool { return mainDone && results["guard-child-repeat"] && requests["guard-child-repeat"] != nil })
	u.expireApprovals()
	if len(u.approvals.pending) != 0 {
		t.Fatal("same-session shell handoff lost exact grant")
	}
	if data, err := os.ReadFile(effects); err != nil || string(data) != want+want {
		t.Fatalf("handoff replayed or lost effects=%q error=%v", data, err)
	}
	runtimeKeys(t, u, "GUARD_CHILD_FOLLOW!\r")
	await("following command reused changed-directory grant", func() bool { return results["guard-child-follow"] && requests["guard-child-follow"] != nil })
	u.expireApprovals()
	if len(u.approvals.pending) != 0 {
		t.Fatal("following command lost exact changed-directory grant")
	}
	if data, err := os.ReadFile(effects); err != nil || string(data) != want+want+want {
		t.Fatalf("following command changed native cwd or replayed effects=%q error=%v", data, err)
	}
	trace.mu.Lock()
	following, terminalFollowing := trace.before["guard-child-follow"], trace.after["guard-child-follow"]
	trace.mu.Unlock()
	if following.Binding != (ObservationBinding{Runtime: "claude", Session: u.thread, Workspace: binding.Workspace}) || following.Workdir != filepath.Join(binding.Workspace, "surviving") || !sameObservationCall(&following, &terminalFollowing) {
		t.Fatalf("following command conflated operational cwd with selected scope: before=%+v after=%+v", following, terminalFollowing)
	}
	if data, err := os.ReadFile(settingsPath); err != nil || string(data) != settings {
		t.Fatal("native project permissions changed")
	}
	t.Log("two native children retained distinct original hook authority; StopTask withdrew only its request; sibling executed once; shell shutdown/resume retained exact UI grant without replay")
}
