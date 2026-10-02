package router

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/claude"
	"github.com/yusing/mekugi/internal/session"
)

// Opt-in native acceptance sends one real prompt, with Claude owning the tool and
// permission lifecycle. It never routes inference or changes installed settings.
func TestNativeObservationClaudeSDKLive(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_CLAUDE_LIVE") != "1" {
		t.Skip("requires installed, authenticated Claude and explicitly enabled live inference")
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
	bridge, err := filepath.Abs("../../bin/claude-bridge/dist/bridge.js")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bridge); err != nil {
		t.Fatal("run make build-claude first")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	service, binding, _ := observationHTTPFixture(t)
	endpoint := service.Endpoint()
	client, err := claude.Start(ctx, node, bridge, claude.Config{Cwd: binding.Workspace, Executable: executable, Companion: &claude.ObservationEndpoint{Socket: endpoint.Socket, Token: endpoint.Token}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	sent, finished := false, false
	writes := make(map[string]bool)
	results, permissions := 0, 0
	nativeID := ""
	for !finished {
		select {
		case <-ctx.Done():
			t.Fatal("native Claude acceptance timed out")
		case event, ok := <-client.Events():
			if !ok {
				t.Fatal("native runtime disconnected")
			}
			switch event.Kind {
			case "ready":
				if !sent {
					sent = true
					if err := client.Send(ctx, fmt.Sprintf("Use your native Write tool exactly once to create %s with the exact content companion-native-evidence followed by a newline. Do not use Bash, other tools or subagents. Then answer done.", filepath.Join(binding.Workspace, "native.txt"))); err != nil {
						t.Fatal(err)
					}
				}
			case "session":
				binding.Session = event.SessionID
			case "tool":
				if event.Role == "Write" {
					writes[event.ID] = true
					nativeID = event.ID
				}
			case "tool_result":
				if event.ID == nativeID {
					results++
				}
			case "prompt":
				if event.Prompt == nil {
					t.Fatal("missing native permission prompt")
				}
				allow := event.Prompt.Tool == "Write"
				if allow {
					permissions++
				}
				if err := client.Respond(ctx, session.Decision{ID: event.Prompt.ID, Allow: allow}); err != nil {
					t.Fatal(err)
				}
			case "notice":
				if strings.Contains(event.Text, "Companion capture unavailable") {
					t.Fatal(event.Text)
				}
			case "error":
				t.Fatal(event.Text)
			case "done":
				if event.Failed {
					t.Fatal("native turn failed")
				}
				finished = true
			}
		}
	}
	data, err := os.ReadFile(filepath.Join(binding.Workspace, "native.txt"))
	if err != nil || string(data) != "companion-native-evidence\n" {
		t.Fatalf("native write mismatch: %v", err)
	}
	if len(writes) != 1 || results != 1 {
		t.Fatalf("native execution count: Write starts=%d terminal results=%d", len(writes), results)
	}
	call := ObservationCall{Binding: binding, ID: nativeID}
	before, found, err := service.owner.store.lookup(ctx, binding.Workspace, observationKey(call)+"/before")
	if err != nil || !found {
		t.Fatalf("native PreToolUse baseline missing: %v", err)
	}
	after, found, err := service.owner.store.lookup(ctx, binding.Workspace, observationKey(call)+"/after")
	if err != nil || !found {
		t.Fatalf("native PostToolUse outcome missing: %v", err)
	}
	if after.ChangeID == "" || len(after.ReviewFiles) != 1 || before.ExecObservation == nil || before.ExecObservation.Files[0].Kind != execFileAbsent {
		t.Fatal("native capture did not preserve pre/post scope")
	}
	if before.NativeObservation.Call.ID != nativeID || after.NativeObservation.Call.ID != nativeID {
		t.Fatal("hook/SDK tool ID mismatch")
	}
	t.Logf("native exactly-once Write, hook/SDK ID equality, durable saved diff and %d native permission requests verified", permissions)
}
