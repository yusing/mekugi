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
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/claude"
	"github.com/yusing/mekugi/internal/session"
	"github.com/yusing/mekugi/internal/shellsyntax"
)

// Native Claude owns query handoffs, permissions, execution and saved history.
// Only the provider is scripted; exact grants belong to this UI lifetime.
func TestNativeRuntimeVCSGuardHandoffClaudeNative(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_NATIVE_CLAUDE") != "1" {
		t.Skip("requires installed native Claude and built bridge; no inference")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 160*time.Second)
	defer cancel()
	t.Setenv(routerTestWorkerEnvironment, "1")
	t.Setenv("MEKUGI_RUNTIME_DIR", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "native-guard-handoff-fixture")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_SHELL", "/bin/bash")
	root := t.TempDir()
	a, b := filepath.Join(root, "workspace A with spaces"), filepath.Join(root, "workspace B with spaces")
	const settings = `{"permissions":{"defaultMode":"default","ask":["Bash"]}}`
	for _, cwd := range []string{a, b} {
		if err := os.MkdirAll(filepath.Join(cwd, ".claude"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cwd, ".claude", "settings.local.json"), []byte(settings), 0600); err != nil {
			t.Fatal(err)
		}
	}
	helper, err := execTrackHelper()
	if err != nil {
		t.Fatal(err)
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	effects := filepath.Join(root, "guard effects.txt")
	tools, otherTools := filepath.Join(root, "tools with spaces"), filepath.Join(root, "other tools with spaces")
	for _, directory := range []string{tools, otherTools} {
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
		script := "#!/bin/sh\n[ \"$1\" = push ] || exec " + shellsyntax.Quote(realGit) + " \"$@\"\nprintf '%s|%s|%s|%s\\n' \"$PWD\" \"$0\" \"$1\" \"$2\" >> " + shellsyntax.Quote(effects) + "\n"
		if err := os.WriteFile(filepath.Join(directory, "git"), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	previous := filepath.Join(root, "caller startup")
	startup := "export PATH=" + shellsyntax.Quote(tools) + ":\"$PATH\"\n"
	if err := os.WriteFile(previous, []byte(startup), 0600); err != nil {
		t.Fatal(err)
	}
	const exact = `ref='branch with spaces'; git push "$ref"`
	type phase struct {
		name, command string
	}
	phases := []phase{
		{"a-grant", exact}, {"b-cwd", exact},
		{"b-executable", shellsyntax.Quote(filepath.Join(otherTools, "git")) + " push 'branch with spaces'"},
		{"b-grant", exact}, {"a-return", exact}, {"a-clear", exact}, {"fresh-b", exact},
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
		content := []any{map[string]any{"type": "text", "text": "GUARD_HANDOFF_SETTLED"}}
		stop := "end_turn"
		if tools, _ := packet["tools"].([]any); len(tools) > 0 {
			text := nativeGuidanceRequestText(packet["messages"])
			selected, last := -1, -1
			for i, p := range phases {
				if at := strings.LastIndex(text, "GUARD_HANDOFF_"+p.name+"!"); at > last {
					selected, last = i, at
				}
			}
			if selected >= 0 {
				p := phases[selected]
				mu.Lock()
				requests[p.name]++
				count := requests[p.name]
				mu.Unlock()
				if count == 1 {
					content = []any{map[string]any{"type": "tool_use", "id": "handoff-" + p.name, "name": "Bash", "input": map[string]any{"command": p.command, "description": "Guard handoff " + p.name}}}
					stop = "tool_use"
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
	start := func(cwd, resume string, endpoint *claude.ObservationEndpoint) *claude.Client {
		t.Helper()
		client, err := claude.Start(ctx, "node", bridge, claude.Config{Cwd: cwd, Resume: resume, Executable: executable, Model: "haiku", Companion: endpoint})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { client.Close() })
		return client
	}
	// An ordinary B transcript supplies native metadata, not a synthetic binding.
	seed := start(b, "", nil)
	idB := ""
seedLoop:
	for {
		select {
		case <-ctx.Done():
			t.Fatal("native B seed timed out")
		case e, ok := <-seed.Events():
			if !ok || e.Kind == "error" || e.Failed {
				t.Fatalf("native B seed failed: %+v", e)
			}
			switch e.Kind {
			case "ready":
				if err := seed.Send(ctx, "NATIVE_B_HISTORY_HANDOFF_7183"); err != nil {
					t.Fatal(err)
				}
			case "session":
				idB = e.SessionID
			case "done":
				break seedLoop
			}
		}
	}
	if idB == "" {
		t.Fatal("native B seed identity missing")
	}
	seed.Close()
	store := t.TempDir()
	binding := ObservationBinding{Runtime: "claude", Workspace: a}
	service, _, closeObservation := observationIsolationService(t, store, binding)
	trace := traceNativeObservation(t, service)
	setup := func(service *ObservationService, resume string) (*claude.Client, *appServerUI) {
		t.Helper()
		presentation, err := service.PrepareCompanion(ctx)
		if err != nil {
			t.Fatal(err)
		}
		bashEnv, err := service.PrepareCommandTracking(ctx, helper, previous)
		if err != nil {
			t.Fatal(err)
		}
		if err := service.PrepareVCSGuard(ctx, helper); err != nil {
			t.Fatal(err)
		}
		endpoint := service.Endpoint()
		client := start(a, resume, &claude.ObservationEndpoint{Socket: endpoint.Socket, Token: endpoint.Token, Plugin: presentation.Plugin, FrontendDirectory: presentation.FrontendDirectory, JournalSchema: presentation.JournalSchema, BashEnv: bashEnv, VCSGuard: true, VCSGuardHelper: helper})
		u := newRuntimeUI(ctx, client, "Claude Code", a)
		u.attachRuntimeObservation(service)
		t.Cleanup(func() { u.shell.diff.close(); u.shell.diffScreen.Close() })
		return client, u
	}
	client, u := setup(service, "")
	wait := func(kind string) session.Event {
		t.Helper()
		for {
			select {
			case <-ctx.Done():
				t.Fatalf("native handoff timed out waiting for %s", kind)
			case e, ok := <-client.Events():
				if !ok || e.Kind == "error" || e.Failed && !e.Historical {
					t.Fatalf("native handoff failed: %+v", e)
				}
				if err := u.runtimeEvent(e); err != nil {
					t.Fatal(err)
				}
				if e.Kind == kind {
					return e
				}
			}
		}
	}
	wait("ready")
	var wantEffects strings.Builder
	run := func(index int, cwd string, choice int, automatic bool) {
		t.Helper()
		p := phases[index]
		id := "handoff-" + p.name
		runtimeKeys(t, u, "GUARD_HANDOFF_"+p.name+"!\r")
		guards, prompts, results := 0, 0, 0
		settled := false
		for !settled {
			select {
			case <-ctx.Done():
				t.Fatalf("%s timed out: %s", p.name, runtimeFrame(t, u, 110, 28))
			case request := <-service.owner.execTrack.approvals:
				guards++
				u.runtimeGuardApproval(request)
				wantArg, wantExecutable := "branch with spaces", filepath.Join(tools, "git")
				if p.name == "b-executable" {
					wantExecutable = filepath.Join(otherTools, "git")
				}
				if request.thread != u.thread || request.item != id || request.cwd != cwd || request.executable != wantExecutable || !slices.Equal(request.argv, []string{"git", "push", wantArg}) {
					t.Fatalf("%s selected native identity/expanded tuple mismatch: %+v", p.name, request)
				}
				if automatic {
					if len(u.approvals.pending) != 0 {
						t.Fatalf("%s lost exact grant in the same UI", p.name)
					}
				} else {
					if len(u.approvals.pending) != 1 {
						t.Fatalf("%s inherited unrelated or expired grant", p.name)
					}
					approval := u.approvals.pending[0]
					if err := u.answerApproval(approval, approval.choices[choice]); err != nil {
						t.Fatal(err)
					}
				}
			case e, ok := <-client.Events():
				if !ok || e.Kind == "error" {
					t.Fatalf("%s native bridge failed: %+v", p.name, e)
				}
				if err := u.runtimeEvent(e); err != nil {
					t.Fatal(err)
				}
				switch e.Kind {
				case "prompt":
					prompts++
					if e.Prompt == nil || e.Prompt.ToolID != id || u.questions.active == nil || len(u.approvals.pending) != 0 {
						t.Fatalf("%s native permission did not remain independent: %+v", p.name, e)
					}
					if frame := runtimeFrame(t, u, 110, 28); !strings.Contains(frame, "Allow once") {
						t.Fatalf("%s native permission dock absent: %s", p.name, frame)
					}
					runtimeKeys(t, u, "1\r")
				case "tool_result":
					if e.ID == id {
						results++
						if e.Failed != (choice == 2) {
							t.Fatalf("%s native result failed=%v: %s", p.name, e.Failed, e.Text)
						}
					}
				case "done":
					settled = true
				}
			}
		}
		if guards != 1 || prompts != 1 || results != 1 || len(u.approvals.pending) != 0 || u.questions.active != nil || service.owner.pendingCount.Load() != 0 {
			t.Fatalf("%s unsettled/incorrect native receipts: guards=%d prompts=%d results=%d", p.name, guards, prompts, results)
		}
		trace.mu.Lock()
		before, preOK := trace.before[id]
		after, postOK := trace.after[id]
		trace.mu.Unlock()
		if !preOK || !postOK || !sameObservationCall(&before, &after) || before.Command != p.command || before.Binding.Session != u.thread || before.Binding.Workspace != cwd || before.ID != id {
			t.Fatalf("%s original native hook tuple crossed handoff: before=%+v after=%+v", p.name, before, after)
		}
		persisted := nativeObservationHistory(t, service.owner.store, before, "after")
		if persisted.NativeObservation == nil || !sameObservationCall(persisted.NativeObservation.Call, &before) {
			t.Fatalf("%s saved original native tuple changed", p.name)
		}
		if choice != 2 {
			wantEffects.WriteString(cwd + "|" + filepath.Join(tools, "git") + "|push|branch with spaces\n")
		}
		data, err := os.ReadFile(effects)
		if err != nil || string(data) != wantEffects.String() {
			t.Fatalf("%s effects crossed workspace, replayed or ran denied command: %q want=%q error=%v", p.name, data, wantEffects.String(), err)
		}
		t.Logf("%s: exact selected tuple, independent native permission/result, once-only effects", p.name)
	}
	run(0, a, 1, false)
	idA := u.thread
	switchTo := func(id, cwd, present, absent string) {
		t.Helper()
		runtimeKeys(t, u, "/resume "+id+"\r")
		wait("session_ready")
		frame := runtimeFrame(t, u, 120, 35)
		if u.thread != id || u.session.cwd != cwd || service.owner.workspace != cwd || service.owner.session != id || !strings.Contains(frame, present) || strings.Contains(frame, absent) {
			t.Fatalf("native selected history/workspace mismatch: %s", frame)
		}
	}
	switchTo(idB, b, "NATIVE_B_HISTORY_HANDOFF_7183", "GUARD_HANDOFF_a-grant!")
	run(1, b, 2, false)
	run(3, b, 1, false)
	run(2, b, 2, false)
	switchTo(idA, a, "GUARD_HANDOFF_a-grant!", "NATIVE_B_HISTORY_HANDOFF_7183")
	run(4, a, 0, true)
	runtimeKeys(t, u, "/clear\r")
	wait("session_ready")
	if u.thread != "" || len(u.view.entries) != 0 {
		t.Fatal("native clear retained departing history")
	}
	run(5, a, 0, true)
	if u.thread == "" || u.thread == idA || u.thread == idB {
		t.Fatal("native clear did not create a distinct query session")
	}
	client.Close()
	u.shell.diff.close()
	u.shell.diffScreen.Close()
	closeObservation()
	service, _, _ = observationIsolationService(t, store, binding)
	trace = traceNativeObservation(t, service)
	client, u = setup(service, idB)
	wait("ready")
	if u.thread != idB || u.session.cwd != b || service.owner.workspace != b || !strings.Contains(runtimeFrame(t, u, 120, 70), "NATIVE_B_HISTORY_HANDOFF_7183") {
		t.Fatal("fresh A launch did not restore native B metadata/history/workspace")
	}
	run(6, b, 2, false)
	for _, cwd := range []string{a, b} {
		if data, err := os.ReadFile(filepath.Join(cwd, ".claude", "settings.local.json")); err != nil || string(data) != settings {
			t.Fatal("native handoff changed caller permission settings")
		}
	}
	if data, err := os.ReadFile(previous); err != nil || string(data) != startup {
		t.Fatal("native handoff changed caller startup")
	}
}
