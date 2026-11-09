//go:build codex_e2e

package sessioncontrol

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
	"testing"
	"time"

	"github.com/yusing/mekugi/backends/codex/appserver"
)

func TestHookTrustNativeCodexPersistence(t *testing.T) {
	binary, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"fixture\",\"status\":\"in_progress\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"fixture\",\"status\":\"completed\",\"output\":[]}}\n\n")
	}))
	defer provider.Close()
	configRoot, workspace := t.TempDir(), t.TempDir()
	configPath, script := filepath.Join(configRoot, "config.toml"), filepath.Join(workspace, "startup.sh")
	marker := filepath.Join(workspace, "hook-ran")
	writeScript := func(value string) {
		t.Helper()
		if err := os.WriteFile(script, []byte(fmt.Sprintf("#!/bin/sh\nprintf '%%s' %q > %q\n", value, marker)), 0700); err != nil {
			t.Fatal(err)
		}
	}
	writeScript("first")
	initial := fmt.Sprintf(`# Preserve unrelated settings and comments.
check_for_update_on_startup = false
model = "gpt-6-astra"
model_provider = "fixture"
[model_providers.fixture]
name = "No external inference"
base_url = %q
wire_api = "responses"
requires_openai_auth = false
[features]
hooks = true
plugins = false
[hooks]
SessionStart = [{matcher="startup",hooks=[{type="command",command=%q,timeout=5}]}]
[hooks.state.unrelated]
enabled = false
trusted_hash = "untouched"
`, provider.URL, "bash "+script)
	if err := os.WriteFile(configPath, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}
	start := func() (*appserver.Client, *Requests[*Approval]) {
		t.Helper()
		command := exec.CommandContext(ctx, binary, "app-server")
		command.Dir = workspace
		command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + configRoot, "CODEX_HOME=" + configRoot, "XDG_CONFIG_HOME=" + configRoot, "XDG_STATE_HOME=" + configRoot}
		host, err := appserver.Start(command)
		if err != nil {
			t.Fatal(err)
		}
		return host, NewRequests[*Approval](host)
	}
	reply := func(host *appserver.Client, id string) appserver.Message {
		t.Helper()
		for {
			select {
			case m, ok := <-host.Messages:
				if !ok {
					t.Fatal("host disconnected")
				}
				if string(m.ID) == id {
					if m.Error != nil {
						t.Fatal(m.Error.Message)
					}
					return m
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
	}
	initialize := func(host *appserver.Client) {
		t.Helper()
		id, err := host.Initialize()
		if err != nil {
			t.Fatal(err)
		}
		reply(host, id)
		if _, err := host.Send("initialized", nil, false); err != nil {
			t.Fatal(err)
		}
	}
	var currentThread string
	read := func(host *appserver.Client) []*Approval {
		t.Helper()
		id, err := host.Send("hooks/list", map[string]any{"threadId": currentThread}, true)
		if err != nil {
			t.Fatal(err)
		}
		approvals, err := HookTrustApprovals(reply(host, id).Result, workspace)
		if err != nil {
			t.Fatal(err)
		}
		return approvals
	}
	newThread := func(host *appserver.Client) string {
		t.Helper()
		id, err := host.Send("thread/start", map[string]any{}, true)
		if err != nil {
			t.Fatal(err)
		}
		var started struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
		}
		if err := json.Unmarshal(reply(host, id).Result, &started); err != nil {
			t.Fatal(err)
		}
		return started.Thread.ID
	}
	run := func(host *appserver.Client) {
		t.Helper()
		id, err := host.Send("turn/start", map[string]any{"threadId": currentThread, "input": appserver.Input("fixture")}, true)
		if err != nil {
			t.Fatal(err)
		}
		reply(host, id)
		for {
			select {
			case m := <-host.Messages:
				if m.Method == "turn/completed" {
					return
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
	}
	host, requests := start()
	defer func() {
		if err := host.Shutdown(); err != nil {
			t.Error(err)
		}
	}()
	initialize(host)
	currentThread = newThread(host)
	approvals := read(host)
	if len(approvals) != 1 {
		t.Fatalf("untrusted hooks: %+v", approvals)
	}
	originalHash := approvals[0].Hook.Hash
	// Declining is not a write. The same hook must still need review next time.
	if bytes, err := os.ReadFile(configPath); err != nil || string(bytes) != initial {
		t.Fatalf("discovery changed config: %v", err)
	}
	if again := read(host); len(again) != 1 || again[0].Hook.Hash != originalHash {
		t.Fatal("unapproved hook stopped requiring review")
	}
	if err := requests.TrustHook(approvals[0], approvals[0]); err != nil {
		t.Fatal(err)
	}
	var id string
	for pending := range requests.pending {
		id = pending
	}
	reply(host, id)
	requests.Take(id)
	if pending := read(host); len(pending) != 0 {
		t.Fatal("trust did not reload")
	}
	run(host)
	if bytes, err := os.ReadFile(marker); err != nil || string(bytes) != "first" {
		t.Fatalf("approved startup hook did not run: %q %v", bytes, err)
	}
	bytes, err := os.ReadFile(configPath)
	if err != nil || !strings.Contains(string(bytes), "# Preserve unrelated") || !strings.Contains(string(bytes), `trusted_hash = "untouched"`) || !strings.Contains(string(bytes), "enabled = false") {
		t.Fatalf("unrelated configuration changed: %v", err)
	}
	// A fresh host retains trust. Changing the hook declaration must require a
	// new decision even though its key and previously persisted trust remain.
	second, _ := start()
	defer func() {
		if err := second.Shutdown(); err != nil {
			t.Error(err)
		}
	}()
	initialize(second)
	currentThread = newThread(second)
	if pending := read(second); len(pending) != 0 {
		t.Fatal("trust was not persisted across hosts")
	}
	writeScript("changed")
	updated := strings.Replace(string(bytes), "bash "+script, "bash "+script+" changed", 1)
	if err := os.WriteFile(configPath, []byte(updated), 0600); err != nil {
		t.Fatal(err)
	}
	third, _ := start()
	defer func() {
		if err := third.Shutdown(); err != nil {
			t.Error(err)
		}
	}()
	initialize(third)
	currentThread = newThread(third)
	changed := read(third)
	if len(changed) != 1 || changed[0].Hook.Hash == originalHash || !strings.Contains(strings.Join(changed[0].Details, "\n"), "modified") {
		t.Fatalf("changed declaration did not require renewed approval: %+v", changed)
	}
	run(third)
	if bytes, err := os.ReadFile(marker); err != nil || string(bytes) != "first" {
		t.Fatalf("declined modified hook executed: %q %v", bytes, err)
	}
	fourth, changedRequests := start()
	defer func() {
		if err := fourth.Shutdown(); err != nil {
			t.Error(err)
		}
	}()
	initialize(fourth)
	currentThread = newThread(fourth)
	changed = read(fourth)
	if len(changed) != 1 {
		t.Fatalf("declined hash did not require review on the next startup: %+v", changed)
	}
	if err := changedRequests.TrustHook(changed[0], changed[0]); err != nil {
		t.Fatal(err)
	}
	for id := range changedRequests.pending {
		reply(fourth, id)
		changedRequests.Take(id)
	}
	run(fourth)
	if bytes, err := os.ReadFile(marker); err != nil || string(bytes) != "changed" {
		t.Fatalf("reapproved startup hook did not run: %q %v", bytes, err)
	}
}
