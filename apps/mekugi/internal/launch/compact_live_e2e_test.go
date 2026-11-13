//go:build codex_e2e && e2e

package launch

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	segmenttransport "github.com/yusing/mekugi/modules/commandsegments/transport"
	"github.com/yusing/mekugi/modules/conversation/composer"
	"github.com/yusing/mekugi/providers/openai"
)

// Live continuation uses the built-in OpenAI provider. Reset itself is native;
// updated fixture contents reach inference through ordinary selected reads.
func TestCompactReloadLiveOpenAI(t *testing.T) {
	model := strings.TrimSpace(os.Getenv("MEKUGI_E2E_MODEL"))
	if model == "" {
		t.Fatal("MEKUGI_E2E_MODEL must be set")
	}
	registry, err := buildToolRegistryForTest(t, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	workspace := t.TempDir()
	if out, err := exec.Command("git", "init", "--quiet", workspace).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	source := filepath.Join(workspace, "guide.txt")
	skill := filepath.Join(workspace, "SKILL.md")
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(source, "Reusable project guidance. Current file token: FILE_BEFORE.\n")
	write(skill, "---\nname: reload-check\ndescription: reusable continuation guidance\n---\nCurrent skill token: SKILL_BEFORE.\n")
	proxy := newProxyWithSharedTestRegistry(t, registry)
	store, err := openMekugiReplayStore(registry.ReplayDirectory)
	if err != nil {
		t.Fatal(err)
	}
	setTestProxyStore(proxy, store)
	provider := &liveCompactProvider{Forwarder: newTestProviderClient(openai.BaseURL, http.DefaultClient)}
	executor := testRequestExecutor(provider, nil, proxy)
	handler := inferencegateway.ResponsesHandler(t.Context(), time.Minute, executor.ExecuteHTTP)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "use HTTP", http.StatusUpgradeRequired)
			return
		}
		handler(w, r)
	}))
	defer server.Close()
	// Copy authentication privately into the isolated host configuration.
	configRoot := os.Getenv("CODEX_HOME")
	if configRoot == "" {
		user, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		configRoot = filepath.Join(user, ".codex")
	}
	auth, err := os.ReadFile(filepath.Join(configRoot, "auth.json"))
	if err != nil {
		t.Fatal("live Codex authentication required")
	}
	environment := builtInOpenAICodexEnvironment(t)
	for _, entry := range environment {
		if path, ok := strings.CutPrefix(entry, "CODEX_HOME="); ok {
			if err := os.WriteFile(filepath.Join(path, "auth.json"), auth, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	environment, err = frontendShellEnvironment(environment, filepath.Dir(filepath.Join(registry.FrontendDirectory, "mcat")), "", nil, segmenttransport.Endpoints{})
	if err != nil {
		t.Fatal(err)
	}
	environment = append(environment, routerTestWorkerEnvironment+"=1")
	terminal := startAppResumeTerminalWithIntegration(t, func(ctx context.Context) *exec.Cmd {
		args := nativeAppServerArgs(codexArgs(server.URL+"/v1", []string{"-c", fmt.Sprintf("model=%q", model), "-c", "features.plugins=false", "-c", "model_reasoning_effort=\"low\""}, nil))
		cmd := exec.CommandContext(ctx, "mekugi-app-server", args...)
		cmd.Env, cmd.Dir = environment, workspace
		return cmd
	}, "", testAppServerIntegration(proxy))
	terminal.await("Ready")
	terminal.send("Read guide.txt and SKILL.md using exec. SKILL.md contains the active native skill named reload-check. Treat the file and skill as reusable instructions for future turns. This task is then complete; there is no pending implementation work. Reply exactly INITIAL_DONE.\r")
	terminal.await("INITIAL_DONE")
	terminal.await("╭─ Completed")
	terminal.send("/compact\r")
	terminal.await("Context reset")
	write(source, "Reusable project guidance. Current file token: FILE_AFTER_7391.\n")
	write(skill, "---\nname: reload-check\ndescription: reusable continuation guidance\n---\nCurrent skill token: SKILL_AFTER_8264.\n")
	terminal.send("Read guide.txt and SKILL.md with ordinary tools and report their current file and skill tokens.\r")
	terminal.await("FILE_AFTER_7391")
	terminal.await("SKILL_AFTER_8264")
	if !provider.reloaded.Load() {
		t.Fatal("fresh file and skill results did not reach live provider as reloaded context")
	}
	terminal.stopCanceled()
	t.Log("actual /compact: native reset followed by ordinary selected reads reached live continuation")
}

// This uses the production server, native runtime and Features consumer. Unlike
// scripted provider fixtures, every inference and tool choice comes from Luna.
func TestNativeMigrationLiveOpenAI(t *testing.T) {
	model := strings.TrimSpace(os.Getenv("MEKUGI_E2E_MODEL"))
	if model == "" {
		t.Fatal("MEKUGI_E2E_MODEL must be set")
	}
	configRoot := os.Getenv("CODEX_HOME")
	if configRoot == "" {
		user, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		configRoot = filepath.Join(user, ".codex")
	}
	auth, err := os.ReadFile(filepath.Join(configRoot, "auth.json"))
	if err != nil {
		t.Fatal("live Codex authentication required")
	}
	for _, entry := range builtInOpenAICodexEnvironment(t) {
		key, value, _ := strings.Cut(entry, "=")
		switch key {
		case "HOME", "CODEX_HOME", "XDG_CONFIG_HOME", "PATH":
			t.Setenv(key, value)
		}
	}
	for _, key := range []string{"OPENAI_API_KEY", "OPENAI_BASE_URL", "CODEX_API_KEY", "CHATGPT_API_KEY"} {
		t.Setenv(key, "")
	}
	stateDirectory := t.TempDir()
	if export := os.Getenv("MEKUGI_TEST_LIFECYCLE_EXPORT"); export != "" {
		stateDirectory = filepath.Join(export, t.Name())
		if err := os.MkdirAll(stateDirectory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("XDG_STATE_HOME", stateDirectory)
	t.Setenv(routerTestWorkerEnvironment, "1")
	if err := os.WriteFile(filepath.Join(os.Getenv("CODEX_HOME"), "auth.json"), auth, 0600); err != nil {
		t.Fatal(err)
	}
	workspace := gitTestWorkspace(t)
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	guide := filepath.Join(workspace, "guide.txt")
	write(guide, "The reset has completed. Do not reset again or use tools. Reply with DIRECT_RELOAD_OK_7391 and the token in the reloaded reload-check skill.\n")
	write(filepath.Join(workspace, ".agents", "skills", "reload-check", "SKILL.md"), "---\nname: reload-check\ndescription: native reset verification\n---\nThe skill token is NATIVE_SKILL_8264.\n")
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	socketDirectory := t.TempDir()
	if err := os.Chmod(socketDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(socketDirectory, "s")
	var diagnostics bytes.Buffer
	joined := make(chan int, 1)
	go func() { joined <- RunServer(ctx, []string{"--socket", socket}, &diagnostics) }()
	t.Cleanup(func() {
		cancel()
		select {
		case code := <-joined:
			if code != 0 || t.Failed() {
				t.Logf("isolated server exit=%d\n%s", code, &diagnostics)
			}
		case <-time.After(15 * time.Second):
			t.Error("isolated server did not join its native runtimes")
		}
	})
	serverCLIAwait(t, func() bool { _, err := os.Stat(socket); return err == nil })
	transport := sessionapi.ControlTransport(socket)
	defer transport.CloseIdleConnections()
	connection, err := sessionapi.StartNative(ctx, &http.Client{Transport: transport}, "http://mekugi", sessionapi.NativeStart{
		Workspace: workspace, RouterArgs: []string{"--shell-guard=false", "--debug"},
		Args: []string{"--yolo", "-m", model, "-c", "features.plugins=false", "-c", `model_reasoning_effort="low"`},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	var event sessionapi.NativeEvent
	await := func(label string, ready func() bool) {
		t.Helper()
		for !ready() {
			next, err := connection.Next()
			if err != nil {
				t.Fatalf("%s: %v; status=%+v", label, err, event.Frame.View.Status)
			}
			event = next
			if state := event.Frame.View.Status.State; state.FailureKnown {
				t.Fatalf("%s: native turn failed: %s", label, state.Failure)
			}
		}
	}
	var submittedAfter uint64
	contains := func(text string) bool {
		if event.Frame.Main != nil {
			for _, entry := range event.Frame.Main.Entries {
				if entry.Seq > submittedAfter && entry.Agent == "/root" && entry.Kind == "text" && strings.Contains(entry.Text, text) {
					return true
				}
			}
		}
		return false
	}
	completed := func() bool { return event.Frame.View.Status.State.Text == "Completed" && !event.Frame.Facts.Busy }
	resets := func() int {
		count := 0
		if event.Frame.Main != nil {
			for _, entry := range event.Frame.Main.Entries {
				if entry.Kind == "compaction" {
					count++
				}
			}
		}
		return count
	}
	send := func(operation sessionapi.NativeOperation) {
		t.Helper()
		if _, err := connection.Send(ctx, event.Target(), operation); err != nil {
			t.Fatal(err)
		}
	}
	submit := func(prompt string) {
		t.Helper()
		if event.Frame.Main != nil {
			submittedAfter = event.Frame.Main.LastSeq
		}
		send(sessionapi.NativeOperation{Composer: &sessionapi.NativeComposerInput{
			Intent: sessionpresentation.ComposerIntent{Version: event.Frame.View.Version, Thread: event.Frame.View.Main.ID, Operation: sessionpresentation.SubmitComposer},
			Draft:  composer.Draft{Text: prompt},
		}})
	}
	metrics := func() *sessionpresentation.FeatureMetrics {
		t.Helper()
		send(sessionapi.NativeOperation{Command: new("/session")})
		await("opened metrics panel", func() bool { return event.Frame.View.Panel != nil })
		send(sessionapi.NativeOperation{Panel: &sessionpresentation.PanelAction{
			Version: event.Frame.View.Version, Panel: event.Frame.View.Panel.ID,
			Kind: sessionpresentation.PanelMetrics, Tab: sessionpresentation.MetricFeatures,
		}})
		await("native FeatureUse snapshot", func() bool {
			p := event.Frame.View.Panel
			return p != nil && p.Features != nil && !p.Features.Unavailable
		})
		result := *event.Frame.View.Panel.Features
		send(sessionapi.NativeOperation{Panel: &sessionpresentation.PanelAction{Version: event.Frame.View.Version, Panel: event.Frame.View.Panel.ID, Kind: sessionpresentation.PanelClose}})
		await("closed metrics panel", func() bool { return event.Frame.View.Panel == nil })
		return &result
	}
	await("native readiness", func() bool { return event.Frame.View.Main.ID != "" })
	submit(`This is an explicitly requested native reset verification. First use functions.exec and tools.exec_command to run printf NATIVE_JS_OK. Then call the native functions.new_context tool with two reload.sources formed by concatenating "@" + "guide.txt" and "$" + "reload-check". Do not read either source beforehand. After reset follow the reloaded instructions.`)
	await("direct reset and native file/skill reload", func() bool {
		return completed() && contains("DIRECT_RELOAD_OK_7391") && contains("NATIVE_SKILL_8264")
	})
	first := metrics()
	if first.Self.Compactions != 1 || first.Self.SelfCompactions != 1 || first.Self.CompactionPreloads != 1 || first.Self.SelfCompactionsCompleted != 1 || first.Self.SelfCompactionFailures != 0 {
		t.Fatalf("direct reset native counts: %+v", first.Self.FeatureUse)
	}
	t.Logf("Luna direct reset, native file+skill reload and Features query: %+v", first.Self.FeatureUse)
	write(guide, "This is fresh guidance after the later terminal reset. Do not use tools or reset again. Reply with RETAINED_SELECTION_OK_9517 and the token in reload-check.\n")
	before := resets()
	send(sessionapi.NativeOperation{Command: new("/compact")})
	await("terminal reset", func() bool { return resets() > before && !event.Frame.Facts.Busy })
	submit("Continue from the freshly reloaded guidance without reading files or using tools.")
	await("retained source selection", func() bool { return completed() && contains("RETAINED_SELECTION_OK_9517") })
	second := metrics()
	if second.Self.Compactions != 2 || second.Self.SelfCompactions != 1 || second.Self.CompactionPreloads != 1 {
		t.Fatalf("manual reset changed self-reset attribution: %+v", second.Self.FeatureUse)
	}
	t.Log("Luna later terminal reset reacquired retained sources without restoring old conversation")
	write(guide, "These instructions apply only after the requested compound reset completes. Before that reset, follow the user task to spawn the delegate and register its compound wait. After the reset, consume the pending delegate outcome; do not spawn again or reset again. When the delegate finishes, reply COMPOUND_LIVE_OK_6238.\n")
	submit(`The prior no-tools guidance is complete and superseded by this explicit new task. Do not read guide.txt before the compound reset; its instructions apply only after that reset. First use functions.exec to call tools.collaboration__spawn_agent with task_name worker, fork_turns none, inherited native model, reasoning_effort low, preload.sources containing "@" + "guide.txt", message "This assignment supersedes the preloaded root instructions. Run sleep 3 with exec_command, then reply CHILD_LIVE_DONE. Do not spawn or reset.". Then await tools.collaboration__wait_agent with compact:true and reload.sources containing "@" + "guide.txt" in its own functions.exec cell, printing the returned result with text. The original cell stays pending until the result. After the compound reset follow the reloaded guidance.`)
	await("compound reset and child completion", func() bool { return completed() && contains("COMPOUND_LIVE_OK_6238") })
	last := metrics()
	if last.Agents != 1 || last.Self.Spawns != 1 || last.Self.SpawnPreloads != 1 || last.Self.Waits == 0 || last.Self.Compactions != 3 || last.Self.SelfCompactions != 2 || last.Self.SelfCompactionsCompleted != 2 || last.Descendants.Finals != 1 {
		t.Fatalf("compound/native family counts: self=%+v descendants=%+v agents=%d", last.Self.FeatureUse, last.Descendants.FeatureUse, last.Agents)
	}
	t.Logf("Luna compound reset and native family aggregation: self=%+v descendants=%+v", last.Self.FeatureUse, last.Descendants.FeatureUse)
}

type liveCompactProvider struct {
	inferencegateway.Forwarder
	reloaded atomic.Bool
}

func (p *liveCompactProvider) ForwardExecution(ctx, execution context.Context, body []byte, headers http.Header, model string) (*inferencegateway.Response, error) {
	if bytes.Contains(body, []byte("FILE_AFTER_7391")) && bytes.Contains(body, []byte("SKILL_AFTER_8264")) {
		if i := bytes.Index(body, []byte("<mekugi-reload-v1>")); i >= 0 {
			return nil, fmt.Errorf("selection metadata leaked into continuation: %.300s", body[i:])
		}
		p.reloaded.Store(true)
	}
	return p.Forwarder.ForwardExecution(ctx, execution, body, headers, model)
}
