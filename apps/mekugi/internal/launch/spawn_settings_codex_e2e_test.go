//go:build codex_e2e

package launch

import (
	"bytes"
	"context"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

func TestSpawnInvokingSettingsNativeCodex(t *testing.T) {
	for _, change := range []string{"effort", "model-and-effort", "default-effort", "override"} {
		t.Run(change, func(t *testing.T) {
			testNativeThreadsServerOwnedCodex(t, nativeThreadsScenario{spawnSettings: change})
		})
	}
}

type spawnSettingsFixture struct {
	mu                 sync.Mutex
	change, root, turn string
	step, children     int
	ready, release     chan struct{}
}

func (p *spawnSettingsFixture) serve(t *testing.T, w http.ResponseWriter, r *http.Request, metadata codexresponses.TurnMetadata, body []byte) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.root == "" {
		p.root, p.turn = metadata.ThreadID, metadata.TurnID
	}
	var program, answer string
	wantModel, wantEffort := "gpt-6-sol", "high"
	if p.change == "model-and-effort" {
		wantModel = "gpt-6-astra"
	}
	if metadata.ThreadID != p.root {
		p.children++
		if p.change == "override" {
			wantEffort = "low"
		}
		if metadata.Model != wantModel || metadata.Effort != wantEffort {
			t.Errorf("child invoking settings = %s/%s, want %s/%s", metadata.Model, metadata.Effort, wantModel, wantEffort)
		}
		answer = "INVOKING_CHILD_DONE"
	} else {
		p.step++
		switch p.step {
		case 1:
			// Keep the first immutable inference alive while the UI applies a
			// real turn/settings/update. No retained records are edited.
			close(p.ready)
			select {
			case <-p.release:
			case <-r.Context().Done():
				return
			}
			program = `text("SETTINGS_CAPTURED");`
		case 2:
			if metadata.TurnID != p.turn {
				t.Error("settings update unexpectedly replaced the invoking turn")
			}
			if metadata.Model != wantModel || metadata.Effort != wantEffort {
				t.Errorf("updated invoking settings = %s/%s, want %s/%s", metadata.Model, metadata.Effort, wantModel, wantEffort)
			}
			program = `text(await tools.mcp__threadcontrol__spawn_agent({task_name:"worker",message:"Finish immediately",fork_turns:"none"}));`
			if p.change == "override" {
				program = `text(await tools.mcp__threadcontrol__spawn_agent({task_name:"worker",message:"Finish immediately",fork_turns:"none",model:"gpt-6-sol",reasoning_effort:"low"}));`
			}
		case 3:
			if metadata.TurnID != p.turn || bytes.Contains(body, []byte("settings disagree")) {
				t.Error("fresh spawn required a reset or rejected current invoking settings")
			}
			if !bytes.Contains(body, []byte(`agent:main/worker`)) || !bytes.Contains(body, []byte(`confirmed`)) {
				t.Error("fresh MCP spawn with current settings did not confirm the child")
			}
			program = `text(await tools.mcp__threadcontrol__wait_agent({}));`
		case 4:
			if !bytes.Contains(body, []byte("INVOKING_CHILD_DONE")) {
				t.Error("spawned child did not complete through the native runtime")
			}
			answer = "INVOKING_DONE"
		default:
			t.Errorf("unexpected invoking-settings inference %d", p.step)
			answer = "INVOKING_DONE"
		}
	}
	id := fmt.Sprintf("invoking-%s-%d", metadata.ThreadID, p.step)
	item := map[string]any{"type": "message", "id": id, "role": "assistant", "phase": "final_answer", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": answer}}}
	if program != "" {
		item = map[string]any{"type": "custom_tool_call", "id": id, "call_id": id + "-call", "name": "exec", "namespace": "functions", "input": program, "status": "completed"}
	}
	response := mchangesNestedCodexResponse(p.step, item)
	defer response.Body.Close()
	w.Header().Set("Content-Type", "text/event-stream")
	if _, err := io.Copy(w, response.Body); err != nil {
		t.Error(err)
	}
}

func (p *spawnSettingsFixture) verify(t *testing.T, ctx context.Context, connection *sessionapi.NativeConnection, event sessionapi.NativeEvent) {
	t.Helper()
	select {
	case <-p.ready:
	case <-ctx.Done():
		t.Fatal("first inference did not start:", ctx.Err())
	}
	command := "/effort high"
	if p.change == "model-and-effort" {
		command = "/model gpt-6-astra --reasoning=high"
	}
	released := p.change == "default-effort"
	if released {
		close(p.release)
	} else if _, err := connection.Send(ctx, event.Target(), sessionapi.NativeOperation{Command: &command}); err != nil {
		t.Fatal("live settings update:", err)
	}
	for {
		var err error
		event, err = connection.Next()
		if err != nil {
			t.Fatal("invoking-settings publication:", err)
		}
		body, err := json.Marshal(event.Frame, jsonv1.FormatDurationAsNano(true))
		if err != nil {
			t.Fatal(err)
		}
		if !released && strings.Contains(string(body), "live update published") {
			close(p.release)
			released = true
		}
		if !event.Frame.Facts.Busy && bytes.Contains(body, []byte("INVOKING_DONE")) {
			break
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.step != 4 || p.children != 1 {
		t.Fatalf("invoking-settings fixture repeated or omitted work: parent=%d child=%d", p.step, p.children)
	}
}
