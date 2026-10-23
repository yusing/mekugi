//go:build codex_e2e

// Source: apps/mekugi/internal/launch/web_shell_codex_e2e_test.go:22:115@[14a6d1f3] shell acceptance.
package launch

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
)

// Use the server's actual owner loop with an explicitly supplied API. The local
// provider fixture replaces account routing, while installed Codex owns execution.
func TestAPIShellServerOwnedNativeCodex(t *testing.T) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal("installed Codex is required:", err)
	}
	p := new(shellPreviewProvider)
	f := newMChangesSliceFixture(t, "api-server-shell-native")
	proxy := newProxyWithSharedTestRegistry(t, f.registry)
	auto := newLivePreview(t.Context())
	defer auto.Close()
	f.store.liveDiff = testReview(auto).Publish
	proxy.review = testReview(auto)
	setTestProxyStore(proxy, f.store)
	provider := httptest.NewServer(testResponsesHandler(t.Context(), time.Minute, p, nil, proxy))
	defer provider.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	workspace := savedDiffWorkspace(t)
	commandArgs := []string{"app-server",
		"-c", `model_providers.preview={name="preview",base_url=` + strconv.Quote(provider.URL+"/v1") + `,wire_api="responses",requires_openai_auth=false}`,
		"-c", `model_provider="preview"`, "-c", `model="gpt-6-astra"`,
		"-c", "features.plugins=false", "-c", "include_collaboration_mode_instructions=false"}
	environment := slices.DeleteFunc(serverEnvironment(routerFaultCodexEnvironment(t)), func(entry string) bool {
		key, _, _ := strings.Cut(entry, "=")
		return slices.Contains([]string{"OPENAI_API_KEY", "CODEX_API_KEY", "CHATGPT_API_KEY", "XAI_API_KEY", "OPENAI_BASE_URL", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY"}, key)
	})
	startup := filepath.Join(t.TempDir(), "frontends.bash")
	writeTestFile(t, startup, "export PATH="+quoteShellWord(f.registry.FrontendDirectory)+":\"$PATH\"\n")
	environment = append(environment, routerTestWorkerEnvironment+"=1", "PATH="+f.registry.FrontendDirectory+string(os.PathListSeparator)+execTrackPath(), "BASH_ENV="+startup, "NO_PROXY=*")
	streams, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer streams.Close()
	conversation := new(conversationmodel.Service)
	observations := conversationbinding.Codex.Observation(conversation)
	reviewSource := new(changecapture.Service)
	integration := testAppServerIntegration(proxy)
	var cmd *exec.Cmd
	done := make(chan error, 1)
	launcher := &nativeReattachLauncher{run: func(ctx context.Context, start sessionapi.Start, api *sessionapi.Server) error {
		cmd = exec.CommandContext(ctx, codex, commandArgs...)
		cmd.Dir, cmd.Env = start.Workspace, environment
		wait, err := startNativeSession(testTerminalViews(reviewSource), reviewSource, testPresentation(), conversation, new(activitymodel.Service), testHistory(), testPreferences(), testReports(), ctx, cmd, streams, integration.controlObservations, integration.usage, integration.audience, integration.review, integration.execWindows, integration.nativeTrace, integration.retained, integration.changeSource, integration.snapshots, integration.recovered, integration.publishChanges, integration.captureNotice, integration.guard, integration.guards, integration.execTrack, integration.skillsManager, nil, "", nil, nil, nil, "", nil, &observations, false, nil, api, false, nil)
		if err == nil {
			err = wait()
		}
		done <- err
		return err
	}}
	hub := sessionapi.NewHub(ctx, launcher)
	defer hub.Close()
	joined := false
	defer func() {
		cancel()
		if !joined {
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Error("server runtime did not join after cancellation")
			}
		}
	}()
	server := httptest.NewServer(sessionapi.Versioned(hub.Handler()))
	defer server.Close()
	client := &http.Client{Timeout: 30 * time.Second}
	rpc, err := sessionapi.OpenRPC(ctx, client, server.URL+sessionapi.RPCPath, "")
	if err != nil {
		t.Fatal(err)
	}
	defer rpc.Close()
	var started struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(serverRPCCall(t, rpc, "session/start", "", sessionapi.Start{Workspace: workspace, Model: "fixture"}), &started); err != nil || started.ID == "" {
		t.Fatalf("runtime start: %+v, %v", started, err)
	}
	events := serverRPCSubscribe(t, rpc, started.ID)
	await := func(match func([]byte) bool) {
		t.Helper()
		for {
			event, err := events.Next(ctx)
			if err != nil {
				t.Fatalf("publication ended before expected state: %v", err)
			}
			if match(event.Data) {
				return
			}
		}
	}
	await(func(data []byte) bool {
		var state struct {
			Session sessionapi.Session `json:"session"`
			Input   *sessionapi.Input  `json:"input"`
		}
		return json.Unmarshal(data, &state) == nil && state.Session.Thread != "" && state.Session.Workspace == workspace && state.Input != nil && state.Input.Reason == ""
	})
	target := filepath.Join(workspace, "api-shell-edit.txt")
	serverRPCCall(t, rpc, "session/shell", started.ID, map[string]string{"command": "printf 'SHELL_MODE_%s' OK; printf 'shell-body\n' >> " + quoteShellWord(target)})
	output, completed := false, false
	await(func(data []byte) bool {
		output = output || strings.Contains(string(data), "SHELL_MODE_OK")
		completed = completed || strings.Contains(string(data), `"exitCode":0`)
		return output && completed
	})
	content, err := os.ReadFile(target)
	if err != nil || string(content) != "shell-body\n" {
		t.Fatalf("shell file effect, exactly once: %q, %v", content, err)
	}
	// Keep an actual shell and its child alive when the runtime is stopped.
	// A completed command alone cannot establish descendant cleanup.
	pidFile := filepath.Join(workspace, "active-shell-pids")
	serverRPCCall(t, rpc, "session/shell", started.ID, map[string]string{"command": "sleep 60 & child=$!; printf '%s %s' \"$$\" \"$child\" > " + quoteShellWord(pidFile) + "; wait \"$child\""})
	var pids []string
	serverCLIAwait(t, func() bool {
		body, _ := os.ReadFile(pidFile)
		pids = strings.Fields(string(body))
		return len(pids) == 2
	})
	var descendants []*os.Process
	for _, text := range pids {
		pid, err := strconv.Atoi(text)
		if err != nil || pid <= 0 {
			t.Fatalf("invalid fixture process identity: %q", text)
		}
		process, err := os.FindProcess(pid)
		if err != nil || process.Signal(syscall.Signal(0)) != nil {
			t.Fatalf("fixture descendant was not running before shutdown: %v", err)
		}
		descendants = append(descendants, process)
		t.Cleanup(func() {
			if t.Failed() {
				state, _ := os.ReadFile(fmt.Sprintf("/proc/%d/status", process.Pid))
				for line := range strings.SplitSeq(string(state), "\n") {
					if strings.HasPrefix(line, "Name:") || strings.HasPrefix(line, "State:") || strings.HasPrefix(line, "PPid:") {
						t.Logf("descendant %d: %s", process.Pid, line)
					}
				}
			}
			_ = process.Kill()
			_ = process.Release()
		})
	}
	cancel()
	select {
	case <-done:
		joined = true
	case <-time.After(10 * time.Second):
		t.Fatal("server stop did not join installed Codex")
	}
	if cmd.ProcessState == nil {
		t.Fatal("server runtime returned before waiting for installed Codex")
	}
	serverCLIAwait(t, func() bool {
		for _, process := range descendants {
			err := process.Signal(syscall.Signal(0))
			if !errors.Is(err, os.ErrProcessDone) && !errors.Is(err, syscall.ESRCH) {
				return false
			}
		}
		return true
	})
	if p.calls.Load() != 0 {
		t.Fatal("idle API shell invoked a model")
	}
}
