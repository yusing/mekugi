//go:build unix

package router

import (
	"context"
	json "encoding/json/v2"
	"fmt"
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
	"github.com/yusing/mekugi/internal/execsegment"
	"github.com/yusing/mekugi/internal/session"
	"github.com/yusing/mekugi/internal/shellsyntax"
)

// Installed Claude executes literal Bash inputs against a scripted local
// provider. The actual shared UI consumes the helper's reports; no inference
// or substitute shell executor supplies the command outcomes.
func TestRuntimeCommandSegmentsClaudeNative(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_NATIVE_CLAUDE") != "1" {
		t.Skip("requires installed native Claude and built bridge")
	}
	for _, background := range []bool{false, true} {
		for _, failure := range []bool{false, true} {
			t.Run(fmt.Sprintf("background=%v/failure=%v", background, failure), func(t *testing.T) {
				nativeCommandSegmentsClaude(t, background, failure, false)
			})
		}
	}
	t.Run("identical-concurrent-background", func(t *testing.T) {
		nativeCommandSegmentsClaude(t, true, false, true)
	})
}

func nativeCommandSegmentsClaude(t *testing.T, background, failure, ambiguous bool) {
	ctx, cancel := context.WithTimeout(t.Context(), 70*time.Second)
	defer cancel()
	t.Setenv(routerTestWorkerEnvironment, "1")
	t.Setenv("MEKUGI_RUNTIME_DIR", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "native-segments-fixture")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	// This fixture exercises native Bash startup, not the caller's shell choice.
	t.Setenv("CLAUDE_CODE_SHELL", "/bin/bash")
	// A fresh tracking launch must isolate the parent command's nested-shell
	// guard without changing the parent environment or other inherited values.
	t.Setenv(execsegment.Guard, "1")
	service, binding, _ := observationHTTPFixture(t)
	presentation, err := service.PrepareCompanion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	helper, err := execTrackHelper()
	if err != nil {
		t.Fatal(err)
	}
	// Preserve a caller-owned startup file and record the native execution
	// string before the observer is sourced. This proves BASH_ENV survived
	// installed Claude's setup/eval/cwd wrapper, rather than a synthetic one.
	previous := filepath.Join(t.TempDir(), "previous-bash-env")
	audit := filepath.Join(binding.Workspace, "native-wrapper.txt")
	startup := "export NATIVE_SEGMENT_STARTUP=STARTUP_SURVIVED\ncase $BASH_EXECUTION_STRING in *\"eval \"*\"pwd -P >| \"*) printf '%s\\n' \"$BASH_EXECUTION_STRING\" >> " + shellsyntax.Quote(audit) + " ;; esac\n"
	claimGate := filepath.Join(binding.Workspace, "claim.gate")
	if ambiguous {
		// Background launch returns natively before the shell finishes startup.
		// Hold both shells before claiming, until both native PreToolUse hooks
		// have registered their indistinguishable literal inputs.
		startup += "case $BASH_EXECUTION_STRING in *\"eval \"*\"pwd -P >| \"*) while [ ! -f " + shellsyntax.Quote(claimGate) + " ]; do sleep 0.05; done ;; esac\n"
	}
	// Use a genuine prior Mekugi observer, not just ordinary startup exports.
	// Its private socket is intentionally absent. If the fresh launch lets this
	// observer run, it declines the claim but sets the guard and masks Claude's
	// new observer. Caller-owned exports and audit effects must still execute.
	oldTracker := filepath.Join(filepath.Dir(previous), "previous-exec-track.bash")
	oldSocket, oldDirectory := ExecTrackPaths(filepath.Join(filepath.Dir(previous), "bin"))
	oldSource := execsegment.Tracker(helper, oldSocket, oldDirectory)
	if err := os.WriteFile(oldTracker, []byte(oldSource), 0600); err != nil {
		t.Fatal(err)
	}
	startup += execsegment.Hook(oldTracker)
	if err := os.WriteFile(previous, []byte(startup), 0600); err != nil {
		t.Fatal(err)
	}
	bashEnv, err := service.PrepareCommandTracking(ctx, helper, previous)
	if err != nil {
		t.Fatal(err)
	}
	const first = "SEGMENT_FIRST_CEDAR"
	const gateOutput = "SEGMENT_GATE_MAPLE"
	const last = "SEGMENT_LAST_BIRCH"
	gate := filepath.Join(binding.Workspace, "segments.gate")
	gateScript := "while [ ! -f segments.gate ]; do sleep 0.05; done\nprintf '%s\\n' \"$NATIVE_SEGMENT_STARTUP\" " + gateOutput + "\n"
	if err := os.WriteFile(filepath.Join(binding.Workspace, "gate.sh"), []byte(gateScript), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(gate, nil, 0600)
		_ = os.WriteFile(claimGate, nil, 0600)
	})
	penultimate := "printf 'SEGMENT_SUCCESS_OAK\\n'"
	if failure {
		penultimate = "false"
	}
	command := "printf '" + first + "\\n'; printf 'once\\n' >> executions.txt; bash ./gate.sh && " + penultimate + " && printf '" + last + "\\n'"
	expected, ok := execsegment.Split(command)
	if !ok || len(expected) != 5 {
		t.Fatalf("fixture command is not a five-segment list: %v", expected)
	}
	var mu sync.Mutex
	requests := 0
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
		mu.Lock()
		defer mu.Unlock()
		content := []any{map[string]any{"type": "text", "text": "NATIVE_SEGMENTS_ACCEPTED"}}
		stop := "end_turn"
		if tools, _ := packet["tools"].([]any); len(tools) > 0 {
			requests++
			if requests > 8 {
				t.Error("native segments exceeded scripted request budget")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if requests == 1 {
				content = []any{map[string]any{"type": "tool_use", "id": "native-segments-once", "name": "Bash", "input": map[string]any{"command": command, "description": "Native segment acceptance", "run_in_background": background}}}
				if ambiguous {
					content = append(content, map[string]any{"type": "tool_use", "id": "native-segments-twin", "name": "Bash", "input": map[string]any{"command": command, "description": "Native segment acceptance twin", "run_in_background": true}})
				}
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
	endpoint := service.Endpoint()
	client, err := claude.Start(ctx, "node", bridge, claude.Config{Cwd: binding.Workspace, Executable: executable, Model: "haiku", Companion: &claude.ObservationEndpoint{Socket: endpoint.Socket, Token: endpoint.Token, Plugin: presentation.Plugin, FrontendDirectory: presentation.FrontendDirectory, JournalSchema: presentation.JournalSchema, BashEnv: bashEnv}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	u := newRuntimeUI(ctx, client, "Claude Code", binding.Workspace)
	u.attachRuntimeObservation(service)
	defer u.shell.diff.close()
	defer u.shell.diffScreen.Close()
	entryID := func(id string) *liveActivityNativeItem {
		for _, row := range u.view.entries {
			if row.CallID == id && row.native != nil {
				return row.native
			}
		}
		return nil
	}
	entry := func() *liveActivityNativeItem { return entryID("native-segments-once") }
	isCall := func(id string) bool { return id == "native-segments-once" || ambiguous && id == "native-segments-twin" }
	assertEvidence := func(wantEffects string) {
		t.Helper()
		if os.Getenv(execsegment.Guard) != "1" {
			t.Fatal("native launch changed the parent's nested-shell guard")
		}
		effects, err := os.ReadFile(filepath.Join(binding.Workspace, "executions.txt"))
		if err != nil || string(effects) != wantEffects {
			t.Fatalf("native effect was not exactly once per call: %q %v", effects, err)
		}
		wrappers, err := os.ReadFile(audit)
		if err != nil || !strings.Contains(string(wrappers), "eval ") || !strings.Contains(string(wrappers), "pwd -P >| ") {
			t.Fatalf("native wrapper/startup evidence missing: %q %v", wrappers, err)
		}
		matched := false
		for _, wrapper := range strings.Split(string(wrappers), "\n") {
			literal, _, _, supported := execsegment.ClaudeWrapper(wrapper)
			matched = matched || supported && literal == command
		}
		if !matched {
			t.Fatalf("installed native wrapper did not preserve the literal Bash input: %q", wrappers)
		}
		original, err := os.ReadFile(previous)
		if err != nil || string(original) != startup {
			t.Fatal("caller-owned BASH_ENV was changed")
		}
		originalTracker, err := os.ReadFile(oldTracker)
		if err != nil || string(originalTracker) != oldSource {
			t.Fatal("caller-owned prior observer was changed")
		}
	}
	var held []session.Event
	var live, released, reportBeforeTerminal, terminal, done bool
	var permissionCount, resultCount int
	var events []string
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			wrappers, _ := os.ReadFile(audit)
			t.Logf("native startup execution strings: %s", wrappers)
			service.owner.execTrack.mu.Lock()
			for key, track := range service.owner.execTrack.tracks {
				t.Logf("report %v: hostOnly=%v ended=%v done=%v code=%d segments=%d", key, track.hostOnly, track.ended, track.done, track.code, len(track.segments))
			}
			service.owner.execTrack.mu.Unlock()
			t.Logf("native events: %v", events)
			t.Fatalf("native segments timed out: live=%v released=%v reportBeforeTerminal=%v terminal=%v done=%v row=%+v", live, released, reportBeforeTerminal, terminal, done, entry())
		case event, open := <-client.Events():
			if !open {
				t.Fatal("native bridge disconnected before segment acceptance")
			}
			events = append(events, event.Kind+"/"+event.ID)
			if event.Kind == "error" {
				t.Fatal(event.Text)
			}
			if event.Kind == "tool" && event.Role == "Bash" {
				var input struct {
					Command string `json:"command"`
				}
				if err := json.Unmarshal([]byte(event.Text), &input); err != nil {
					t.Fatal(err)
				}
				if input.Command != "" && (input.Command != command || !isCall(event.ID)) {
					t.Fatalf("native command changed: %+v", event)
				}
			}
			if event.Kind == "tool_result" && isCall(event.ID) {
				resultCount++
				if !background && event.Failed != failure {
					t.Fatalf("native result disagrees with shell failure: %+v", event)
				}
			}
			// Native task updates may omit immutable ToolID. Resolve it from the
			// earlier native task start, just as the shared consumer does.
			var taskCall string
			if event.Task != nil {
				taskCall = event.Task.ToolID
				if taskCall == "" {
					taskCall = u.runtime.tasks[event.Task.ID].ToolID
				}
			}
			isTerminal := event.Kind == "command_output" && isCall(event.ID) && event.Output != nil && event.Output.Done || event.Kind == "task" && event.Task != nil && isCall(taskCall) && runtimeTaskTerminal(event.Task.Status)
			if background && isTerminal && !reportBeforeTerminal && !ambiguous {
				held = append(held, event)
				continue
			}
			if err := u.runtimeEvent(event); err != nil {
				t.Fatal(err)
			}
			if isTerminal {
				terminal = true
			}
			switch event.Kind {
			case "ready":
				if err := client.Send(ctx, "Execute the isolated native segment acceptance command."); err != nil {
					t.Fatal(err)
				}
			case "prompt":
				if event.Prompt == nil || event.Prompt.Tool != "Bash" {
					t.Fatalf("unexpected native permission: %+v", event.Prompt)
				}
				permissionCount++
				if err := client.Respond(ctx, session.Decision{ID: event.Prompt.ID, Allow: true}); err != nil {
					t.Fatal(err)
				}
			case "done":
				done = true
			}
		case <-tick.C:
			u.flushRuntimeCommandSegments()
			native := entry()
			if native == nil {
				continue
			}
			if ambiguous {
				if !released {
					service.owner.execTrack.mu.Lock()
					pending := 0
					for _, call := range service.owner.execTrack.started {
						if isCall(call.key[2]) {
							pending++
						}
					}
					service.owner.execTrack.mu.Unlock()
					if pending == 2 {
						if err := os.WriteFile(gate, nil, 0600); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(claimGate, nil, 0600); err != nil {
							t.Fatal(err)
						}
						released = true
					}
				}
				for _, id := range []string{"native-segments-once", "native-segments-twin"} {
					if row := entryID(id); row != nil && len(row.segments) != 0 {
						t.Fatalf("identical concurrent native command borrowed segment identity: %s", id)
					}
				}
				twin := entryID("native-segments-twin")
				if released && done && twin != nil && !native.running && !twin.running {
					if permissionCount == 0 || resultCount != 2 {
						t.Fatalf("native permissions/results: %d/%d", permissionCount, resultCount)
					}
					for _, row := range []*liveActivityNativeItem{native, twin} {
						if row.output == nil || !row.output.View().Done {
							t.Fatal("native aggregate fallback did not settle")
						}
						output := strings.Join(row.output.View().Lines, "\n")
						for _, marker := range []string{first, gateOutput, "STARTUP_SURVIVED", last} {
							if !strings.Contains(output, marker) {
								t.Fatalf("native aggregate fallback lost %s: %q", marker, output)
							}
						}
					}
					assertEvidence("once\nonce\n")
					t.Log("two concurrent identical native Bash inputs declined segment claims and retained their own native output; each effect executed once")
					return
				}
				continue
			}
			if !released && len(native.segments) >= 3 && native.segments[2].running {
				if native.segments[0].output == nil || !strings.Contains(strings.Join(native.segments[0].output.View().Lines, "\n"), first) {
					continue
				}
				live = native.running
				if err := os.WriteFile(gate, nil, 0600); err != nil {
					t.Fatal(err)
				}
				released = true
			}
			if background && !reportBeforeTerminal && len(held) > 0 {
				service.owner.execTrack.mu.Lock()
				ended := false
				for key, track := range service.owner.execTrack.tracks {
					if key[2] == "native-segments-once" && track.ended && track.done {
						ended = true
					}
				}
				service.owner.execTrack.mu.Unlock()
				if ended {
					if !native.running {
						t.Fatal("shell report settled background command before native terminal evidence")
					}
					if failure && len(native.segments) == 5 {
						t.Fatal("shell report finalized skipped row before native terminal evidence")
					}
					reportBeforeTerminal = true
					for _, event := range held {
						if err := u.runtimeEvent(event); err != nil {
							t.Fatal(err)
						}
					}
					held = nil
					terminal = true
					u.flushRuntimeCommandSegments()
					native = entry()
				}
			}
			if live && done && !native.running && len(native.segments) == 5 && (!background || terminal && reportBeforeTerminal) {
				for i, segment := range native.segments {
					wantExit := 0
					if failure && i == 3 {
						wantExit = 1
					}
					if segment.source != expected[i].Source || segment.running || segment.skipped != (failure && i == 4) || segment.exit != wantExit {
						t.Fatalf("shared UI segment %d: %+v", i, segment)
					}
					if segment.skipped {
						if segment.output != nil {
							t.Fatal("skipped segment has invented output")
						}
						continue
					}
					if segment.output == nil || !segment.output.View().Done {
						t.Fatalf("segment %d output not retained and settled", i)
					}
					output := strings.Join(segment.output.View().Lines, "\n")
					for marker, owner := range map[string]int{first: 0, gateOutput: 2, "STARTUP_SURVIVED": 2, last: 4} {
						if strings.Contains(output, marker) != (i == owner && !(failure && owner == 4)) {
							t.Fatalf("segment %d misattributed %s: %q", i, marker, output)
						}
					}
				}
				if permissionCount == 0 || resultCount != 1 {
					t.Fatalf("native permissions/results: %d/%d", permissionCount, resultCount)
				}
				assertEvidence("once\n")
				t.Logf("native wrapper and prior BASH_ENV survived; five shared UI segments, exactly-once effect, background=%v failure=%v", background, failure)
				return
			}
		}
	}
}
