package router

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/claude"
	"github.com/yusing/mekugi/internal/session"
)

// This opt-in acceptance uses the installed native runtime and its normal
// permissions. Root turn completion does not substitute for background settlement.
func TestNativeRuntimeBashCaptureClaudeLive(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_NATIVE_CLAUDE") != "1" {
		t.Skip("requires explicitly enabled installed, authenticated Claude inference")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	executable, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := filepath.Abs("../claude/bridge/dist/bridge.js")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bridge); err != nil {
		t.Fatal("run make test-claude first")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 110*time.Second)
	defer cancel()
	service, binding, _ := observationHTTPFixture(t)
	trace := traceNativeObservation(t, service)
	presentation, err := service.PrepareCompanion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := service.Endpoint()
	client, err := claude.Start(ctx, node, bridge, claude.Config{
		Cwd: binding.Workspace, Executable: executable, Model: "haiku",
		Companion: &claude.ObservationEndpoint{Socket: endpoint.Socket, Token: endpoint.Token, Plugin: presentation.Plugin, FrontendDirectory: presentation.FrontendDirectory, JournalSchema: presentation.JournalSchema},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	commands := []string{
		`printf 'FOREGROUND_STREAM_FIRST\n'; sleep 4; printf 'foreground-once\n' > foreground.txt; printf 'FOREGROUND_STREAM_LAST\n'`,
		`printf 'partial-once\n' > partial.txt; exit 7`,
		`printf 'BACKGROUND_STREAM_FIRST\n'; sleep 8; printf 'background-once\n' > background.txt; printf 'BACKGROUND_STREAM_LAST\n'`,
	}
	prompt := fmt.Sprintf("This is an authorized isolated native Bash acceptance. Use native Bash exactly three times, in order, with these exact commands: (1) %s, foreground; (2) %s, foreground, expected failure, continue without repair or retry; (3) %s, with run_in_background=true. Do not change any command. Use no other tools, including TaskOutput, TaskStop, Write, Read or Agent. After the background launch result, answer only STARTED. Wait for no additional input.", commands[0], commands[1], commands[2])
	var calls [3]string
	results := make(map[string]bool)
	sent, rootDone, backgroundDone := false, false, false
	taskID := ""
	permissions := 0
	foregroundStreaming, backgroundStreaming := false, false
	foregroundTasks := make(map[string]bool)
	for !rootDone || !backgroundDone {
		select {
		case <-ctx.Done():
			t.Fatalf("native Bash acceptance timed out: results=%d rootDone=%t backgroundDone=%t", len(results), rootDone, backgroundDone)
		case event, ok := <-client.Events():
			if !ok {
				t.Fatal("native runtime disconnected before root and background settlement")
			}
			if strings.Contains(event.Text, "Companion capture unavailable") {
				trace.logDrift(t)
				t.Fatal(event.Text)
			}
			switch event.Kind {
			case "ready":
				if !sent {
					sent = true
					if err := client.Send(ctx, prompt); err != nil {
						t.Fatal(err)
					}
				}
			case "session":
				binding.Session = event.SessionID
			case "tool":
				if event.Role != "Bash" || event.Caller != "" || event.ID == "" {
					t.Fatalf("unexpected native tool: role=%s caller=%s", event.Role, event.Caller)
				}
				var input struct {
					Command    string `json:"command"`
					Background bool   `json:"run_in_background"`
				}
				if err := json.Unmarshal([]byte(event.Text), &input); err != nil {
					t.Fatal(err)
				}
				if input.Command == "" {
					continue
				} // Empty native streaming start.
				index := 0
				for index < len(calls) && calls[index] != "" {
					index++
				}
				if index == len(calls) || input.Command != commands[index] || input.Background != (index == 2) {
					t.Fatalf("native Bash changed command order, count or background intent: %+v", input)
				}
				calls[index] = event.ID
			case "tool_result":
				index := -1
				for i, id := range calls {
					if id != "" && id == event.ID {
						index = i
					}
				}
				if index < 0 || results[event.ID] || event.Failed != (index == 1) {
					t.Fatalf("unexpected, duplicate or incorrectly settled native result: id=%s failed=%t", event.ID, event.Failed)
				}
				results[event.ID] = true
			case "command_output":
				if event.Output == nil || event.Output.TaskID == "" {
					t.Fatal("native output lost task correlation")
				}
				if event.ID == calls[0] && strings.Contains(event.Text, "FOREGROUND_STREAM_FIRST") && !strings.Contains(event.Text, "FOREGROUND_STREAM_LAST") && !results[event.ID] {
					foregroundStreaming = true
				}
				if event.ID == calls[2] && strings.Contains(event.Text, "BACKGROUND_STREAM_FIRST") && !strings.Contains(event.Text, "BACKGROUND_STREAM_LAST") && !backgroundDone {
					backgroundStreaming = true
				}
			case "prompt":
				if event.Prompt == nil {
					t.Fatal("native permission prompt missing")
				}
				allow := event.Prompt.Tool == "Bash"
				if err := client.Respond(ctx, session.Decision{ID: event.Prompt.ID, Allow: allow}); err != nil {
					t.Fatal(err)
				}
				if !allow {
					t.Fatalf("unexpected permission request: %s", event.Prompt.Tool)
				}
				permissions++
			case "task":
				if event.Task == nil || event.Task.Ambient {
					continue
				}
				if calls[0] != "" && event.Task.ToolID == calls[0] {
					foregroundTasks[event.Task.ID] = true
				}
				if foregroundTasks[event.Task.ID] {
					continue
				}
				if event.Role == "task_started" {
					if taskID != "" || calls[2] == "" || event.Task.ToolID != calls[2] || event.Task.Kind != "local_bash" {
						t.Fatal("background native task start was missing, duplicated or uncorrelated")
					}
					taskID = event.Task.ID
				}
				if event.Task.ID != taskID || taskID == "" {
					t.Fatal("unexpected native task identity")
				}
				if event.Task.Status == "completed" {
					backgroundDone = true
				} else if event.Task.Status == "failed" || event.Task.Status == "stopped" || event.Task.Status == "killed" {
					t.Fatalf("background write did not complete: %s", event.Task.Status)
				}
			case "error":
				t.Fatal(event.Text)
			case "done":
				if event.Failed {
					t.Fatal("expected Bash failure incorrectly failed the whole native turn")
				}
				rootDone = true
			}
		}
	}
	if len(results) != 3 || binding.Session == "" || binding.Session == "session" {
		t.Fatal("missing three native results or actual native session identity")
	}
	if !foregroundStreaming || !backgroundStreaming {
		t.Fatalf("output did not arrive before native command completion: foreground=%t background=%t", foregroundStreaming, backgroundStreaming)
	}
	files, err := service.owner.store.liveDiffSnapshotFiles(ctx, liveDiffScope{Workspaces: map[string]map[string]bool{binding.Workspace: {observationThread(binding): true}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Fatalf("saved capture files=%d, want exactly three actual effects", len(files))
	}
	for i, name := range []string{"foreground", "partial", "background"} {
		path := filepath.Join(binding.Workspace, name+".txt")
		content := name + "-once\n"
		data, err := os.ReadFile(path)
		if err != nil || string(data) != content {
			t.Fatalf("native %s effect missing or duplicated: %v", name, err)
		}
		call := ObservationCall{Binding: binding, ID: calls[i]}
		after := nativeObservationHistory(t, service.owner.store, call, "after")
		status := "completed"
		if i == 1 {
			status = "failed"
		}
		if after.ChangeID == "" || after.ExecOutcome == nil || after.ExecOutcome.Status != status {
			t.Fatalf("native %s effect has no saved terminal capture: outcome=%+v", name, after.ExecOutcome)
		}
		if i == 2 && (after.NativeObservation == nil || after.NativeObservation.Terminal == nil || after.NativeObservation.Terminal.Task != taskID) {
			t.Fatal("background saved completion lost native task correlation")
		}
		found := false
		for _, file := range files {
			if file.Path != path {
				continue
			}
			found = true
			expected := mekugi.RenderReviewFile("", path, "", content)
			if len(file.Chunks) != 1 || file.Chunks[0].Change != after.ChangeID || file.Chunks[0].Review.Diff != expected.Diff {
				t.Fatalf("native %s saved diff is missing, duplicated or differs from actual effect", name)
			}
		}
		if !found {
			t.Fatalf("native %s missing from saved Diff consumer", name)
		}
	}
	t.Logf("three native Bash results, partial failure, terminal background task, three saved effects and %d permission requests verified", permissions)
}
