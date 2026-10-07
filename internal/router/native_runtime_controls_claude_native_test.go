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
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/claude"
	"github.com/yusing/mekugi/internal/session"
)

// Installed native Claude owns each query and transcript. The local provider
// returns deterministic text without model inference or filesystem execution.
func TestNativeRuntimeSessionControlsClaudeNative(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_NATIVE_CLAUDE") != "1" {
		t.Skip("requires installed native Claude and built bridge")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Second)
	defer cancel()
	t.Setenv(routerTestWorkerEnvironment, "1")
	t.Setenv("MEKUGI_RUNTIME_DIR", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "native-session-controls-fixture")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	packets := make(chan string, 8)
	systems := make(chan string, 8)
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
		if tools, _ := packet["tools"].([]any); len(tools) > 0 {
			messages, _ := json.Marshal(packet["messages"])
			system, _ := json.Marshal(packet["system"])
			select {
			case packets <- string(messages):
				systems <- string(system)
			default:
				t.Error("native session request budget exceeded")
			}
		}
		nativeGuidanceProviderReply(w, packet, []any{map[string]any{"type": "text", "text": "NATIVE_CONTROLLERS_ACCEPTED"}}, "end_turn")
	}))
	defer provider.Close()
	t.Setenv("ANTHROPIC_BASE_URL", provider.URL)
	binding := ObservationBinding{Runtime: "claude", Workspace: t.TempDir()}
	s, _, closeObservation := observationIsolationService(t, t.TempDir(), binding)
	presentation, err := s.PrepareCompanion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := filepath.Abs("../claude/bridge/dist/bridge.js")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := s.Endpoint()
	client, err := claude.Start(ctx, "node", bridge, claude.Config{Cwd: binding.Workspace, Executable: executable, Model: "haiku", Companion: &claude.ObservationEndpoint{
		Socket: endpoint.Socket, Token: endpoint.Token, Plugin: presentation.Plugin, FrontendDirectory: presentation.FrontendDirectory, JournalSchema: presentation.JournalSchema,
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	u := newRuntimeUI(ctx, client, "Claude Code", binding.Workspace)
	u.attachRuntimeObservation(s)
	defer u.shell.diff.close()
	defer u.shell.diffScreen.Close()
	wait := func(kind string) session.Event {
		t.Helper()
		for {
			select {
			case <-ctx.Done():
				t.Fatalf("native controller timed out waiting for %s", kind)
			case e, ok := <-client.Events():
				if !ok {
					t.Fatal("native controller disconnected")
				}
				if e.Kind == "error" || e.Kind == "notice" && strings.Contains(e.Text, "unavailable") {
					t.Fatalf("native controller: %s", e.Text)
				}
				if err := u.runtimeEvent(e); err != nil {
					t.Fatal(err)
				}
				if e.Kind == kind {
					if e.Failed {
						t.Fatalf("native %s failed: %s", kind, e.Text)
					}
					return e
				}
			}
		}
	}
	prompt := func(text string) string {
		t.Helper()
		runtimeKeys(t, u, text+"\r")
		wait("done")
		select {
		case packet := <-packets:
			<-systems
			return packet
		case <-ctx.Done():
			t.Fatal("native work prompt missing")
			return ""
		}
	}
	wait("ready")
	const markerA, markerB = "ORIGINAL_SESSION_CEDAR_8151", "CLEAR_SESSION_MAPLE_3392"
	if packet := prompt(markerA); !strings.Contains(packet, markerA) {
		t.Fatal("native A prompt missing")
	}
	a := u.thread
	if a == "" {
		t.Fatal("native A identity missing")
	}
	runtimeKeys(t, u, "/title Shared native title 修復\r")
	wait("title")
	if u.title != "Shared native title 修復" {
		t.Fatal("native saved title was not confirmed")
	}
	runtimeKeys(t, u, "/resume\r")
	wait("sessions")
	found := false
	for _, row := range u.resumePicker.rows {
		if row.id == a && row.title == u.title {
			found = true
		}
	}
	if !found {
		t.Fatal("native list did not retain renamed title")
	}
	if frame := runtimeFrame(t, u, 120, 28); !strings.Contains(frame, "Resume a previous session") || !strings.Contains(frame, u.title) {
		t.Fatal("shared picker did not render native metadata")
	}
	if err := u.resumePickerKey("\x1b"); err != nil {
		t.Fatal(err)
	}
	runtimeKeys(t, u, "/clear\r")
	wait("session_ready")
	if u.thread != "" || len(u.view.entries) != 0 || u.runtime.busy {
		t.Fatal("clear did not retire the old presentation")
	}
	if packet := prompt(markerB); !strings.Contains(packet, markerB) || strings.Contains(packet, markerA) {
		t.Fatal("native clear inherited A context")
	}
	b := u.thread
	if b == "" || b == a {
		t.Fatal("clear did not create a distinct native session")
	}
	runtimeKeys(t, u, "/resume "+a+"\r")
	wait("session_ready")
	frame := runtimeFrame(t, u, 120, 28)
	if u.thread != a || u.title != "Shared native title 修復" || !strings.Contains(frame, markerA) || strings.Contains(frame, markerB) {
		t.Fatal("resume did not restore A history and title")
	}
	if packet := prompt("RESUMED_SESSION_BIRCH_6430"); !strings.Contains(packet, markerA) || strings.Contains(packet, markerB) {
		t.Fatal("native resume context crossed sessions")
	}
	runtimeKeys(t, u, "/resume "+b+"\r")
	wait("session_ready")
	frame = runtimeFrame(t, u, 120, 28)
	if u.thread != b || !strings.Contains(frame, markerB) || strings.Contains(frame, markerA) {
		t.Fatal("return to B crossed native history")
	}
	runtimeKeys(t, u, "/resume "+a+"\r")
	wait("session_ready")
	if u.thread != a || s.owner.session != a || !u.runtime.ready {
		t.Fatal("A → B → A could not reuse its own observation scope")
	}
	// Seed an ordinary native transcript with no Mekugi companion. Resuming it
	// from this initially fresh bridge must deliver the complete current guidance.
	plain, err := claude.Start(ctx, "node", bridge, claude.Config{Cwd: binding.Workspace, Executable: executable, Model: "haiku"})
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	plainSession := ""
plainLoop:
	for {
		select {
		case <-ctx.Done():
			t.Fatal("plain native session timed out")
		case event, ok := <-plain.Events():
			if !ok || event.Kind == "error" || event.Failed {
				t.Fatal("plain native session failed")
			}
			switch event.Kind {
			case "ready":
				if err := plain.Send(ctx, "PLAIN_SESSION_CONTEXT_NO_MEKUGI_7823"); err != nil {
					t.Fatal(err)
				}
			case "session":
				plainSession = event.SessionID
			case "done":
				break plainLoop
			}
		}
	}
	if plainSession == "" {
		t.Fatal("plain native identity missing")
	}
	<-packets
	plainSystem := <-systems
	if err := plain.Close(); err != nil {
		t.Fatal(err)
	}
	skill, err := os.ReadFile(filepath.Join(presentation.Plugin, "skills", "mekugi", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, workflow, ok := strings.Cut(string(skill), "\n---\n")
	if !ok {
		t.Fatal("generated workflow body unavailable")
	}
	workflow = strings.TrimSpace(workflow)
	var plainCarrier any
	if err := json.Unmarshal([]byte(plainSystem), &plainCarrier); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(nativeGuidanceRequestText(plainCarrier), workflow) {
		t.Fatal("ordinary session unexpectedly contains current companion guidance")
	}
	runtimeKeys(t, u, "/resume "+plainSession+"\r")
	wait("session_ready")
	resumedPacket := prompt("RESUMED_PLAIN_GUIDANCE_CHECK_9917")
	var resumedCarrier any
	if err := json.Unmarshal([]byte(resumedPacket), &resumedCarrier); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(nativeGuidanceRequestText(resumedCarrier), workflow) {
		t.Fatal("in-process resume omitted complete current workflow from native model input")
	}
	assertNativeFrontendContracts(t, s.registry, resumedCarrier)
	runtimeKeys(t, u, "/resume "+a+"\r")
	wait("session_ready")
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	closeObservation()
	freshBinding := binding
	freshBinding.Session = a
	fresh, _, _ := observationIsolationService(t, s.owner.store.directory, freshBinding)
	freshPresentation, err := fresh.PrepareCompanion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	freshEndpoint := fresh.Endpoint()
	client, err = claude.Start(ctx, "node", bridge, claude.Config{Cwd: binding.Workspace, Executable: executable, Model: "haiku", Resume: a, Companion: &claude.ObservationEndpoint{
		Socket: freshEndpoint.Socket, Token: freshEndpoint.Token, Plugin: freshPresentation.Plugin, FrontendDirectory: freshPresentation.FrontendDirectory, JournalSchema: freshPresentation.JournalSchema,
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	u = newRuntimeUI(ctx, client, "Claude Code", binding.Workspace)
	u.attachRuntimeObservation(fresh)
	defer u.shell.diff.close()
	defer u.shell.diffScreen.Close()
	wait("ready")
	frame = runtimeFrame(t, u, 120, 28)
	if u.thread != a || u.title != "Shared native title 修復" || !strings.Contains(frame, markerA) || strings.Contains(frame, markerB) || fresh.owner.session != a {
		t.Fatal("fresh native resume lost title/history or crossed scopes")
	}
}
