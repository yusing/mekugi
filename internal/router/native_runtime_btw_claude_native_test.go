//go:build unix

package router

import (
	"context"
	"encoding/json/v2"
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
	"github.com/yusing/mekugi/internal/session"
)

// Installed Claude executes the native forks against a local scripted provider.
// Every stream gate is released after inspecting the shared terminal renderer.
func TestNativeRuntimeBTWClaudePTY(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_NATIVE_CLAUDE") != "1" {
		t.Skip("requires installed native Claude and built bridge")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 110*time.Second)
	defer cancel()
	t.Setenv(routerTestWorkerEnvironment, "1")
	t.Setenv("MEKUGI_RUNTIME_DIR", t.TempDir())
	nativeConfig := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", nativeConfig)
	t.Setenv("ANTHROPIC_API_KEY", "native-btw-fixture")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	binding := ObservationBinding{Runtime: "claude", Workspace: t.TempDir()}
	service, _, closeObservation := observationIsolationService(t, t.TempDir(), binding)
	defer closeObservation()
	presentation, err := service.PrepareCompanion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately permissive project policy must not grant side queries tools.
	if err := os.MkdirAll(filepath.Join(binding.Workspace, ".claude"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binding.Workspace, ".claude", "settings.json"), []byte(`{"permissions":{"allow":["Bash(*)","Write(*)"]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binding.Workspace, "notes.txt"), []byte("BTW_FILE_CEDAR\n"), 0600); err != nil {
		t.Fatal(err)
	}
	image := runtimeInputImage(t, binding.Workspace, "diagram.png")
	const root = "BTW_ROOT_CEDAR"
	const later = "BTW_ROOT_MAPLE"
	const side = "BTW_SIDE_QUESTION_BIRCH"
	const follow = "BTW_SIDE_FOLLOW_WILLOW"
	const fresh = "BTW_FRESH_ASH"
	const evil = "BTW_EVIL_TOOL_ATTEMPT"
	mainGate, sideGate, followGate := make(chan struct{}), make(chan struct{}), make(chan struct{})
	followCanceled := make(chan struct{})
	release := func(gate chan struct{}) {
		select {
		case <-gate:
		default:
			close(gate)
		}
	}
	var mu sync.Mutex
	packets := map[string][]map[string]any{}
	identities := map[string]string{}
	var rootID string
	requests, attempts := 0, 0
	mainCanceled := false
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
		messages, _ := packet["messages"].([]any)
		last := ""
		for _, message := range messages {
			m, _ := message.(map[string]any)
			if m["role"] == "user" {
				data, _ := json.Marshal(m["content"])
				last = string(data)
			}
		}
		key := "auxiliary"
		for _, marker := range []string{root, later, side, follow, fresh, evil} {
			if strings.Contains(last, marker) {
				key = marker
			}
		}
		if strings.Contains(last, "btw-malicious") {
			key = "denied"
		}
		// Native title/summary requests can quote the Main marker, but do not
		// advertise the configured work tools. Keep them out of turn assertions.
		if tools, _ := packet["tools"].([]any); (key == root || key == later) && len(tools) == 0 {
			key = "auxiliary"
		}
		mu.Lock()
		requests++
		packets[key] = append(packets[key], packet)
		budget := requests <= 18
		mu.Unlock()
		if !budget {
			t.Error("native BTW exceeded bounded provider request budget")
			w.WriteHeader(400)
			return
		}
		text := "BTW_AUXILIARY"
		var gate <-chan struct{}
		switch key {
		case root:
			text = "BTW_ROOT_ACCEPTED"
		case later:
			text, gate = "BTW_MAIN_INCREMENTAL", mainGate
		case side:
			text, gate = "BTW_SIDE_INCREMENTAL", sideGate
		case evil:
			mu.Lock()
			attempts++
			mu.Unlock()
			nativeGuidanceProviderReply(w, packet, []any{map[string]any{"type": "tool_use", "id": "btw-malicious", "name": "Bash", "input": map[string]any{"command": "printf forbidden > btw-forbidden.txt"}}}, "tool_use")
			return
		case "denied":
			text = "BTW_EVIL_DENIED"
		case follow:
			text, gate = "BTW_FOLLOW_INCREMENTAL", followGate
		case fresh:
			text = "BTW_FRESH_ACCEPTED"
		}
		if gate == nil {
			nativeGuidanceProviderReply(w, packet, []any{map[string]any{"type": "text", "text": text}}, "end_turn")
			return
		}
		// Reuse the exact native SSE fixture, splitting before stream settlement.
		recorded := httptest.NewRecorder()
		nativeGuidanceProviderReply(recorded, packet, []any{map[string]any{"type": "text", "text": text}}, "end_turn")
		wire := recorded.Body.String()
		cut := strings.Index(wire, "event: content_block_stop")
		if cut < 0 {
			t.Error("native fixture lacks stream completion boundary")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, wire[:cut])
		w.(http.Flusher).Flush()
		select {
		case <-gate:
			_, _ = fmt.Fprint(w, wire[cut:])
		case <-r.Context().Done():
			if key == later {
				mu.Lock()
				mainCanceled = true
				mu.Unlock()
			}
			if key == follow {
				close(followCanceled)
			}
		case <-ctx.Done():
		}
	}))
	defer func() {
		release(mainGate)
		release(sideGate)
		release(followGate)
		provider.Close()
	}()
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
	client, err := claude.Start(ctx, "node", bridge, claude.Config{Cwd: binding.Workspace, Executable: executable, Model: "haiku", Companion: &claude.ObservationEndpoint{Socket: endpoint.Socket, Token: endpoint.Token, Plugin: presentation.Plugin, FrontendDirectory: presentation.FrontendDirectory, JournalSchema: presentation.JournalSchema}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	observed := &nativeClaudePTYClient{Client: client, events: make(chan session.Event)}
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		defer close(observed.events)
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-client.Events():
				if !ok {
					return
				}
				mu.Lock()
				if event.Kind == "session" {
					if event.SideID == "" {
						rootID = event.SessionID
					} else {
						identities[event.SideID] = event.SessionID
					}
				}
				mu.Unlock()
				select {
				case observed.events <- event:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	terminal, stop := startNativeRuntimePTY(t, ctx, observed, binding.Workspace, service)
	t.Cleanup(func() {
		release(mainGate)
		release(sideGate)
		release(followGate)
		cancel()
		stop()
		<-joined
	})
	await := func(label string, match func(string) bool) {
		t.Helper()
		if !terminal.observe(25*time.Second, match) {
			mu.Lock()
			counts := map[string]int{}
			for key, values := range packets {
				counts[key] = len(values)
			}
			nativeSides := make(map[string]string, len(identities))
			for key, value := range identities {
				nativeSides[key] = value
			}
			mu.Unlock()
			t.Fatalf("missing %s (provider counts=%v, native side identities=%v):\n%s", label, counts, nativeSides, terminal.screen.String())
		}
	}
	has := func(text string) func(string) bool {
		return func(frame string) bool { return strings.Contains(frame, text) }
	}
	await("ready", has("Ready"))
	terminal.paste(root)
	await("root answer", func(s string) bool {
		return strings.Contains(s, "BTW_ROOT_ACCEPTED") && strings.Contains(s, "Ready")
	})
	terminal.keys("/btw " + side + " @not")
	await("shared file picker", has("notes.txt"))
	terminal.keys("\t")
	await("bound side file attachment", has("@notes.txt"))
	terminal.keys("\x1b[200~" + image + "\x1b[201~\r")
	await("incremental side dock before completion", has("BTW_SIDE_INCREMENTAL"))
	release(sideGate)
	await("completed side dock", func(s string) bool {
		return strings.Contains(s, "BTW_SIDE_INCREMENTAL") && strings.Contains(s, "completed")
	})
	// The ordinary composer remains Main, while /btw explicitly continues the fork.
	terminal.paste(later)
	await("incremental Main while dock remains open", has("BTW_MAIN_INCREMENTAL"))
	terminal.paste("/btw " + follow)
	await("side follow-up while Main remains active", has("BTW_FOLLOW_INCREMENTAL"))
	terminal.paste("/btw BTW_BUSY_DRAFT")
	await("busy side retains composer draft", func(s string) bool {
		return strings.Contains(s, "BTW_BUSY_DRAFT") && strings.Contains(s, "Side answer still running")
	})
	terminal.keys("\x1b")
	await("Esc removes only dock and retains draft", func(s string) bool {
		return !strings.Contains(s, "BTW_FOLLOW_INCREMENTAL") && strings.Contains(s, "BTW_BUSY_DRAFT") && strings.Contains(s, "BTW_MAIN_INCREMENTAL")
	})
	select {
	case <-followCanceled:
	case <-time.After(5 * time.Second):
		t.Fatal("Esc did not cancel native side provider stream")
	}
	// Clear the retained side draft deliberately before asking a new side question.
	terminal.keys("\x1b[H\x0b")
	release(mainGate)
	await("Main finishes after side cancellation", func(s string) bool {
		return strings.Contains(s, "BTW_MAIN_INCREMENTAL") && strings.Contains(s, "Ready")
	})
	terminal.paste("/btw " + fresh)
	await("fresh side answer", has("BTW_FRESH_ACCEPTED"))
	terminal.keys("\x1b[200~BTW_PARKED_DRAFT\x1b[201~")
	await("replacement dock preserves Main draft", func(s string) bool {
		return strings.Contains(s, "BTW_FRESH_ACCEPTED") && strings.Contains(s, "BTW_PARKED_DRAFT") && !strings.Contains(s, "BTW_FOLLOW_INCREMENTAL")
	})
	terminal.keys("\x1b")
	await("fresh dock closes without losing Main draft", func(s string) bool {
		return !strings.Contains(s, "BTW_FRESH_ACCEPTED") && strings.Contains(s, "BTW_PARKED_DRAFT")
	})
	terminal.keys("\x1b[H\x0b")
	terminal.paste("/btw " + evil)
	await("bounded malicious response is denied or rejected by native runtime", func(s string) bool {
		return strings.Contains(s, "BTW_EVIL_DENIED") || strings.Contains(s, "Side query failed:") || strings.Contains(s, "Side turn ended:")
	})

	mu.Lock()
	defer mu.Unlock()
	packetText := func(key string) string {
		t.Helper()
		if len(packets[key]) != 1 {
			t.Fatalf("request %s count=%d", key, len(packets[key]))
		}
		data, _ := json.Marshal(packets[key][0]["messages"])
		return string(data)
	}
	mainInput, sideInput, followInput, freshInput := packetText(later), packetText(side), packetText(follow), packetText(fresh)
	_ = packetText(evil)
	rootTools, _ := packets[root][0]["tools"].([]any)
	configuredMCP := false
	for _, tool := range rootTools {
		value, _ := tool.(map[string]any)
		name, _ := value["name"].(string)
		configuredMCP = configuredMCP || strings.HasPrefix(name, "mcp__")
	}
	if !configuredMCP {
		t.Fatal("fixture did not configure native Main MCP tools")
	}
	if !strings.Contains(sideInput, root) || !strings.Contains(sideInput, "BTW_ROOT_ACCEPTED") || !strings.Contains(sideInput, "@notes.txt") || !strings.Contains(sideInput, "BTW_FILE_CEDAR") || !strings.Contains(sideInput, `"type":"image"`) {
		t.Fatalf("native side attachment/context delivery: root=%v rootAnswer=%v mention=%v fileContent=%v image=%v", strings.Contains(sideInput, root), strings.Contains(sideInput, "BTW_ROOT_ACCEPTED"), strings.Contains(sideInput, "@notes.txt"), strings.Contains(sideInput, "BTW_FILE_CEDAR"), strings.Contains(sideInput, `"type":"image"`))
	}
	for _, marker := range []string{side, "BTW_SIDE_INCREMENTAL", follow, "BTW_FOLLOW_INCREMENTAL"} {
		if strings.Contains(mainInput, marker) || strings.Contains(freshInput, marker) {
			t.Fatalf("side-only context leaked to Main/fresh fork: %s", marker)
		}
	}
	if strings.Contains(followInput, later) || !strings.Contains(followInput, side) || !strings.Contains(followInput, "BTW_SIDE_INCREMENTAL") {
		t.Fatal("side follow-up lost isolated fork context")
	}
	if !strings.Contains(freshInput, later) || !strings.Contains(freshInput, "BTW_MAIN_INCREMENTAL") {
		t.Fatal("fresh side did not snapshot updated Main")
	}
	for _, key := range []string{side, follow, fresh, evil} {
		if tools, _ := packets[key][0]["tools"].([]any); len(tools) != 0 {
			t.Fatalf("side advertised native/MCP tools: %s", key)
		}
		if packets[key][0]["model"] != packets[root][0]["model"] {
			t.Fatal("side did not copy Main model")
		}
	}
	if attempts != 1 {
		t.Fatalf("malicious attempt count=%d", attempts)
	}
	if mainCanceled {
		t.Fatal("closing side interrupted the native Main stream")
	}
	if _, err := os.Stat(filepath.Join(binding.Workspace, "btw-forbidden.txt")); !os.IsNotExist(err) {
		t.Fatalf("side tool effect: %v", err)
	}
	if rootID == "" || len(identities) != 3 {
		t.Fatalf("native identities root=%q sides=%v", rootID, identities)
	}
	seen := map[string]bool{rootID: true}
	for _, id := range identities {
		if id == "" || seen[id] {
			t.Fatalf("side reused root native identity: %v", identities)
		}
		seen[id] = true
	}
	// Native persistence is Main-only, even though side forks have real identities.
	rootSaved := false
	err = filepath.WalkDir(filepath.Join(nativeConfig, "projects"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		name := entry.Name()
		rootSaved = rootSaved || name == rootID+".jsonl"
		for _, id := range identities {
			if name == id+".jsonl" {
				t.Errorf("side native session was persisted: %s", id)
			}
		}
		return nil
	})
	if err != nil || !rootSaved {
		t.Fatalf("Main native persistence unavailable: saved=%v error=%v", rootSaved, err)
	}
}
