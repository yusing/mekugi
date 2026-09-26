//go:build journal_e2e

package router

import (
	"context"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Installed Codex creates and finishes a real native child. A fresh app-server
// must reconstruct its roster and Activity from history without resuming work.
func TestAppServerRestoreChildContentNativeCodex(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	environment := routerFaultCodexEnvironment(t)
	workspace := t.TempDir()
	proxy := newManagedMekugiProxy(t)
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proxy.replayStore = store
	provider := &journalCodexProvider{store: store, turns: make(map[string]int)}
	server := httptest.NewServer(responsesHandler(t.Context(), time.Minute, provider, nil, proxy, nil))
	defer server.Close()
	newCommand := func(ctx context.Context) *exec.Cmd {
		cmd := exec.CommandContext(ctx, codex, "app-server",
			"-c", `model_providers.child_restore={name="child_restore",base_url=`+strconv.Quote(server.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false}`,
			"-c", `model_provider="child_restore"`, "-c", `model="gpt-6-astra"`,
			"-c", `features.multi_agent_v2={enabled=true,tool_namespace="collaboration"}`,
			"-c", "tools.update_plan.enabled=false", "-c", "include_collaboration_mode_instructions=false")
		cmd.Env = environment
		cmd.Dir = workspace
		return cmd
	}

	first := startAppResumeTerminalWithProxy(t, newCommand, "", proxy)
	first.await("Ready")
	first.send("Create a child and report its completed findings.\r")
	first.await("Native root completion after child review.")
	first.await("completed")
	first.quit()

	provider.mu.Lock()
	before := make(map[string]int, len(provider.turns))
	root := ""
	for thread, count := range provider.turns {
		before[thread] = count
		if count > before[root] {
			root = thread
		}
	}
	childRequests := provider.childRequests
	childResult := provider.childResultSeen
	provider.mu.Unlock()
	if root == "" || len(before) != 2 || childRequests != 2 || !childResult {
		t.Fatalf("native root and completed child missing: root=%q turns=%v childRequests=%d childResult=%v", root, before, childRequests, childResult)
	}

	liveDiffScopeCapture(t, store, workspace, root, "saved-root-edit", filepath.Join(workspace, "restored.go"), "before", "after")
	restoredProxy := newManagedMekugiProxy(t)
	restoredProxy.replayStore, err = openMekugiReplayStore(store.directory)
	if err != nil {
		t.Fatal(err)
	}
	auto, stopAuto := newAutoLiveDiff(t.Context(), store.directory)
	defer stopAuto()
	restoredProxy.autoLiveDiff = auto
	second := startAppResumeTerminalWithProxy(t, newCommand, root, restoredProxy)
	second.await("Ready") // Restoration is complete before accepting new input.
	second.send("\x024")  // Focus the restored Agents roster.
	second.awaitMatch("restored child roster", func(screen string) bool {
		rows := strings.Split(screen, "\n")
		return strings.Contains(strings.Join(rows[len(rows)/2:], "\n"), "journal_child")
	})
	second.send("\x023") // Open restored Activity rather than a child process.
	second.await("Native child second finding")
	second.send("\x022")
	second.await("restored.go")
	second.send("\x021")
	second.await("⏎ send")
	second.quit()

	provider.mu.Lock()
	after := make(map[string]int, len(provider.turns))
	for thread, count := range provider.turns {
		after[thread] = count
	}
	afterChildRequests := provider.childRequests
	provider.mu.Unlock()
	if !reflect.DeepEqual(after, before) || afterChildRequests != childRequests {
		t.Fatalf("observational restore sent model requests or resumed child: before=%v after=%v child=%d->%d", before, after, childRequests, afterChildRequests)
	}
}
