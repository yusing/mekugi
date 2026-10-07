//go:build unix

package router

import (
	"bufio"
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
	"github.com/yusing/mekugi/internal/shellsyntax"
)

// Only the provider is scripted. Installed Claude owns native permissions,
// Bash execution and saved tool arguments; the shared UI answers reached writes.
func TestNativeRuntimeVCSGuardClaudeNative(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_NATIVE_CLAUDE") != "1" {
		t.Skip("requires installed native Claude and built bridge; no inference")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 110*time.Second)
	defer cancel()
	t.Setenv(routerTestWorkerEnvironment, "1")
	t.Setenv("MEKUGI_RUNTIME_DIR", t.TempDir())
	config := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	t.Setenv("ANTHROPIC_API_KEY", "native-guard-fixture")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_SHELL", "/bin/bash")
	// A foreign host's identity must never supply the native guard binding.
	t.Setenv("CODEX_THREAD_ID", "foreign-codex-thread")
	service, binding, _ := observationHTTPFixture(t)
	trace := traceNativeObservation(t, service)
	presentation, err := service.PrepareCompanion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fake := t.TempDir()
	effects := filepath.Join(binding.Workspace, "effects.txt")
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"git", "gh"} {
		// Companion capture and native startup perform their own read-only Git
		// queries. Preserve real capture Git; log only this fixture's effects.
		selectCommand := "case $1 in push|status|add|log) ;; *) exec " + shellsyntax.Quote(realGit) + " \"$@\" ;; esac\n"
		if tool == "gh" {
			selectCommand = "[ \"$1 $2\" = 'pr create' ] || exit 1\n"
		}
		script := "#!/bin/sh\n" + selectCommand + "printf '%s\\n' \"${0##*/} $*\" >> " + shellsyntax.Quote(effects) + "\n"
		if err := os.WriteFile(filepath.Join(fake, tool), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	previous := filepath.Join(t.TempDir(), "bash-env")
	startup := "export PATH=" + shellsyntax.Quote(fake) + ":\"$PATH\"\n"
	if err := os.WriteFile(previous, []byte(startup), 0600); err != nil {
		t.Fatal(err)
	}
	helper, err := execTrackHelper()
	if err != nil {
		t.Fatal(err)
	}
	// Native Bash restores its launch PATH inside its eval wrapper after the
	// startup file. Set this after building the helper, which needs real git.
	t.Setenv("PATH", fake+string(os.PathListSeparator)+os.Getenv("PATH"))
	bashEnv, err := service.PrepareCommandTracking(ctx, helper, previous)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.PrepareVCSGuard(ctx, helper); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(binding.Workspace, ".claude"), 0700); err != nil {
		t.Fatal(err)
	}
	settings := `{"permissions":{"defaultMode":"default","ask":["Bash"],"deny":["Bash(git push automatic)"]}}`
	settingsPath := filepath.Join(binding.Workspace, ".claude", "settings.local.json")
	if err := os.WriteFile(settingsPath, []byte(settings), 0600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, command, effects string
		guard, choice          int
		failed, automatic      bool
		unsupported            bool
	}{
		{name: "skipped", command: "false && git push skipped; git status", effects: "git status\n"},
		{name: "allow", command: "git push allowed", effects: "git push allowed\n", guard: 1},
		{name: "deny-list", command: "git add -A; git push denied; git log", effects: "git add -A\ngit log\n", guard: 1, choice: 2},
		{name: "deny-chain", command: "git push chained && git log", guard: 1, choice: 2, failed: true},
		{name: "wrappers", command: "command " + shellsyntax.Quote(filepath.Join(fake, "git")) + " push wrapped; env -i PATH=" + shellsyntax.Quote(fake+":/usr/bin:/bin") + " /bin/bash -c " + shellsyntax.Quote(shellsyntax.Quote(filepath.Join(fake, "gh"))+" pr create"), effects: "git push wrapped\ngh pr create\n", guard: 2},
		{name: "grant", command: "git push exact", effects: "git push exact\n", guard: 1, choice: 1},
		{name: "grant-repeat", command: "git push exact", effects: "git push exact\n", guard: 1},
		{name: "grant-argv-change", command: "git push other", guard: 1, choice: 2, failed: true},
		{name: "grant-cwd-change", command: "cd .claude && git push exact", guard: 1, choice: 2, failed: true},
		{name: "automatic", command: "git push automatic", failed: true, automatic: true},
		{name: "function-vcs", command: "fixture_write() { git push function; : \"$FUNCNAME\"; }; fixture_write", effects: "git push function\n", guard: 1, unsupported: true},
		{name: "function-plain", command: "fixture_plain() { printf 'plain fallback\\n' >> effects.txt; : \"$FUNCNAME\"; }; fixture_plain", effects: "plain fallback\n", unsupported: true},
	}
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
			w.WriteHeader(400)
			return
		}
		content := []any{map[string]any{"type": "text", "text": "GUARD_SETTLED"}}
		stop := "end_turn"
		if tools, _ := packet["tools"].([]any); len(tools) > 0 {
			text := nativeGuidanceRequestText(packet["messages"])
			selected, last := -1, -1
			for i, tc := range cases {
				if at := strings.LastIndex(text, "NATIVE_GUARD_"+tc.name+"!"); at > last {
					selected, last = i, at
				}
			}
			if selected < 0 {
				t.Error("missing scripted guard phase")
				w.WriteHeader(400)
				return
			}
			tc := cases[selected]
			mu.Lock()
			requests[tc.name]++
			count := requests[tc.name]
			mu.Unlock()
			if count == 1 {
				content = []any{map[string]any{"type": "tool_use", "id": "guard-" + tc.name, "name": "Bash", "input": map[string]any{"command": tc.command, "description": "Native guard " + tc.name}}}
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
	client, err := claude.Start(ctx, "node", bridge, claude.Config{Cwd: binding.Workspace, Executable: executable, Model: "haiku", Companion: &claude.ObservationEndpoint{Socket: endpoint.Socket, Token: endpoint.Token, Plugin: presentation.Plugin, FrontendDirectory: presentation.FrontendDirectory, JournalSchema: presentation.JournalSchema, BashEnv: bashEnv, VCSGuard: true, VCSGuardHelper: helper}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	u := newRuntimeUI(ctx, client, "Claude Code", binding.Workspace)
	u.attachRuntimeObservation(service)
	defer u.shell.diff.close()
	defer u.shell.diffScreen.Close()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("native guard startup timed out")
		case e, ok := <-client.Events():
			if !ok {
				t.Fatal("native bridge closed at startup")
			}
			if err := u.runtimeEvent(e); err != nil {
				t.Fatal(err)
			}
			if e.Kind == "ready" {
				goto ready
			}
		}
	}
ready:
	var wantEffects strings.Builder
	for _, tc := range cases {
		id := "guard-" + tc.name
		runtimeKeys(t, u, "NATIVE_GUARD_"+tc.name+"!\r")
		guards, prompts, results := 0, 0, 0
		settled := false
		for !settled {
			select {
			case <-ctx.Done():
				t.Fatalf("%s timed out: %s", tc.name, runtimeFrame(t, u, 100, 28))
			case request := <-service.owner.execTrack.approvals:
				guards++
				u.runtimeGuardApproval(request)
				wantItem := id
				if tc.unsupported {
					wantItem = ""
				}
				if request.thread != u.thread || request.item != wantItem {
					t.Fatalf("guard identity mismatch: thread=%q item=%q want=%q/%q", request.thread, request.item, u.thread, wantItem)
				}
				if tc.name == "grant-repeat" {
					if len(u.approvals.pending) != 0 {
						t.Fatal("exact session grant asked again")
					}
				} else {
					if len(u.approvals.pending) != 1 {
						t.Fatalf("guard pending=%d", len(u.approvals.pending))
					}
					a := u.approvals.pending[0]
					if tc.unsupported && a.item != "" {
						t.Fatal("guard-only fallback guessed a native command row")
					}
					if frame := runtimeFrame(t, u, 100, 28); !strings.Contains(frame, "Allow this remote write?") {
						t.Fatalf("shared guard dock absent: %s", frame)
					}
					if err := u.answerApproval(a, a.choices[tc.choice]); err != nil {
						t.Fatal(err)
					}
				}
			case e, ok := <-client.Events():
				if !ok {
					t.Fatalf("bridge closed during %s", tc.name)
				}
				if err := u.runtimeEvent(e); err != nil {
					t.Fatal(err)
				}
				switch e.Kind {
				case "tool":
					if e.ID == id && e.Text != "{}" {
						var input struct {
							Command string `json:"command"`
						}
						if err := json.Unmarshal([]byte(e.Text), &input); err != nil || input.Command != tc.command {
							t.Fatalf("original native input changed: %+v error=%v", e, err)
						}
					}
				case "prompt":
					prompts++
					if tc.automatic || e.Prompt == nil || e.Prompt.ToolID != id {
						t.Fatalf("wrong original-command permission prompt: %+v", e.Prompt)
					}
					_, descriptionInput, _ := strings.Cut(e.Prompt.Description, "\n")
					var permissionInput struct {
						Command string `json:"command"`
					}
					if err := json.Unmarshal([]byte(descriptionInput), &permissionInput); err != nil || permissionInput.Command != tc.command {
						t.Fatalf("native permission changed original command: %+v error=%v", e.Prompt, err)
					}
					if len(u.approvals.pending) != 0 || u.questions.active == nil {
						t.Fatal("native permission did not retain its independent question dock")
					}
					if frame := runtimeFrame(t, u, 100, 28); !strings.Contains(frame, "Allow once") {
						t.Fatalf("native permission dock absent: %s", frame)
					}
					runtimeKeys(t, u, "1\r")
				case "tool_result":
					if e.ID == id {
						results++
						if e.Failed != tc.failed {
							t.Fatalf("%s result failed=%v want=%v: %s", tc.name, e.Failed, tc.failed, e.Text)
						}
					}
				case "notice":
					if strings.Contains(strings.ToLower(e.Text), "capture unavailable") {
						t.Fatal(e.Text)
					}
				case "error":
					t.Fatal(e.Text)
				case "done":
					settled = true
				}
			}
		}
		if guards != tc.guard || results != 1 || (!tc.automatic && prompts != 1) || (tc.automatic && prompts != 0) {
			t.Fatalf("%s receipts guards=%d/%d prompts=%d results=%d", tc.name, guards, tc.guard, prompts, results)
		}
		if len(u.approvals.pending) != 0 || u.questions.active != nil || service.owner.pendingCount.Load() != 0 {
			t.Fatalf("%s left pending guard/native permission/capture", tc.name)
		}
		wantEffects.WriteString(tc.effects)
		data, err := os.ReadFile(effects)
		if err != nil || string(data) != wantEffects.String() {
			t.Fatalf("%s exactly-once effects=%q want=%q error=%v", tc.name, data, wantEffects.String(), err)
		}
		trace.mu.Lock()
		before, preOK := trace.before[id]
		after, postOK := trace.after[id]
		trace.mu.Unlock()
		if !preOK || !postOK || !sameObservationCall(&before, &after) || before.Command != tc.command || before.Binding.Session != u.thread || before.Binding.Workspace != binding.Workspace || before.ID != id {
			t.Fatalf("%s strict original hook tuple missing: before=%+v after=%+v", tc.name, before, after)
		}
		baseline := nativeObservationHistory(t, service.owner.store, before, "before")
		persisted := nativeObservationHistory(t, service.owner.store, before, "after")
		if baseline.NativeObservation == nil || persisted.NativeObservation == nil || !sameObservationCall(baseline.NativeObservation.Call, &before) || !sameObservationCall(persisted.NativeObservation.Call, &before) {
			t.Fatalf("%s persisted original before/after tuple changed", tc.name)
		}
		t.Logf("%s: %d reached guard requests, %d native prompts, strict original capture tuple and effects exactly once", tc.name, guards, prompts)
	}
	if data, err := os.ReadFile(settingsPath); err != nil || string(data) != settings {
		t.Fatal("native settings changed")
	}
	if data, err := os.ReadFile(previous); err != nil || string(data) != startup {
		t.Fatal("caller startup changed")
	}
	// Read actual native conversation records, not replay-store surrogates.
	saved := make(map[string]string)
	err = filepath.WalkDir(filepath.Join(config, "projects"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		scan := bufio.NewScanner(f)
		scan.Buffer(nil, 8<<20)
		for scan.Scan() {
			var record struct {
				Message struct {
					Content []struct {
						Type  string `json:"type"`
						ID    string `json:"id"`
						Input struct {
							Command string `json:"command"`
						} `json:"input"`
					} `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal(scan.Bytes(), &record) != nil {
				continue
			}
			for _, block := range record.Message.Content {
				if block.Type == "tool_use" && strings.HasPrefix(block.ID, "guard-") {
					saved[block.ID] = block.Input.Command
				}
			}
		}
		return scan.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		if got := saved["guard-"+tc.name]; got != tc.command {
			t.Fatalf("native saved %s command=%q want=%q", tc.name, got, tc.command)
		}
	}
}
