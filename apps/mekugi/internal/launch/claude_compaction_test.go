package launch

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestClaudeCompoundCompactionKeepsWaitBehindNativeSettlement(t *testing.T) {
	for _, scenario := range []string{"pending", "failed", "message", "timeout"} {
		t.Run(scenario, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newClaudeHookFixture(t)
				b := f.bridge
				ctx := t.Context()
				check := func(err error) {
					t.Helper()
					if err != nil {
						t.Fatal(err)
					}
				}
				caller, err := b.identity("spawn-reviewer")
				check(err)
				spawn, err := b.execution.shared.ReserveSpawn(ctx, caller, control.SpawnRequest{Name: "reviewer", Message: "review", Settings: b.settings})
				check(err)
				for _, phase := range []string{"create", "assignment_input", "admit", "initial"} {
					_, err := b.execution.shared.ClaimEffect(ctx, "child", spawn.ID)
					check(err)
					check(b.execution.shared.EffectAcknowledged(ctx, "child", spawn.ID, control.Receipt{Phase: phase, Thread: "reviewer", Session: "reviewer-session", Turn: "reviewer-turn"}))
				}
				args := jsontext.Value(`{"compact":true}`)
				if scenario == "timeout" {
					args = jsontext.Value(`{"compact":true,"timeout_ms":10}`)
				}
				f.hook(t, "PreToolUse", "mcp__threadcontrol__wait_agent", "wait", args, nil)
				request, _ := json.Marshal(map[string]any{"name": "wait_agent", "toolUseID": "wait", "arguments": args})
				result, err := b.request(ctx, "tool", request)
				check(err)
				if !strings.Contains(string(result), "scheduled") {
					t.Fatalf("checkpoint was not scheduled: %s", result)
				}
				f.hook(t, "PostToolUse", "mcp__threadcontrol__wait_agent", "wait", args, result)
				var tool struct {
					Content jsontext.Value `json:"content"`
				}
				check(json.Unmarshal(result, &tool))
				batch, _ := json.Marshal(map[string]any{"session_id": b.info.Session, "hook_event_name": "PostToolBatch", "tool_calls": []any{map[string]any{"tool_name": "mcp__threadcontrol__wait_agent", "tool_use_id": "wait", "tool_response": tool.Content}}})
				stopped, err := b.request(ctx, "hook", batch)
				check(err)
				if !strings.Contains(string(stopped), `"continue":false`) {
					t.Fatalf("batch did not stop: %s", stopped)
				}
				emit := func(event map[string]any) {
					t.Helper()
					event["session_id"] = b.info.Session
					raw, _ := json.Marshal(event)
					check(b.event(ctx, raw))
				}
				f.mu.Lock()
				initial := f.inputs[0].UUID
				f.mu.Unlock()
				emit(map[string]any{"type": "result", "subtype": "success", "uuid": "stopped", "user_message_uuid": initial, "terminal_reason": "hook_stopped"})
				synctest.Wait()
				f.mu.Lock()
				inputs := len(f.inputs)
				compact := f.inputs[len(f.inputs)-1]
				f.mu.Unlock()
				if inputs != 2 || !strings.Contains(string(compact.Message), `"/compact"`) {
					t.Fatalf("expected exact native compaction after stop: %+v", compact)
				}
				if scenario == "message" {
					sender := control.Identity{Thread: "reviewer", Session: "reviewer-session", Turn: "reviewer-turn", Call: "message", Item: "message"}
					_, err := b.execution.shared.PrepareAction(ctx, sender, "/root/child", "message", "review progress")
					check(err)
					check(b.execution.driver.DispatchPending(ctx, "child"))
				}
				if scenario == "timeout" {
					time.Sleep(20 * time.Millisecond)
					b.execution.driver.Tick(time.Now())
				}
				synctest.Wait()
				member, _ := b.execution.shared.Member("child")
				if member.HostRunning || member.Status != control.Waiting || member.Wait.State != "registered" {
					t.Fatalf("auxiliary work lost logical wait: %+v", member)
				}
				f.mu.Lock()
				inputs = len(f.inputs)
				f.mu.Unlock()
				if inputs != 2 {
					t.Fatal("native compact barrier admitted inference")
				}
				outcome := "success"
				if scenario == "failed" {
					outcome = "failed"
				}
				emit(map[string]any{"type": "system", "subtype": "status", "compact_result": outcome, "compact_error": "fixture failure"})
				if outcome == "success" {
					emit(map[string]any{"type": "system", "subtype": "compact_boundary", "uuid": "boundary", "compact_metadata": map[string]string{"trigger": "manual"}})
				}
				emit(map[string]any{"type": "result", "subtype": "success", "uuid": "compacted", "user_message_uuid": compact.UUID, "terminal_reason": "completed"})
				synctest.Wait()
				if scenario == "pending" || scenario == "failed" {
					f.mu.Lock()
					inputs = len(f.inputs)
					f.mu.Unlock()
					if inputs != 2 {
						t.Fatal("compaction settlement ran inference without a wait outcome")
					}
					b.execution.driver.Completed("reviewer", "reviewer-turn", control.Completed, control.HostFailure{}, nil)
					synctest.Wait()
				}
				f.mu.Lock()
				inputs = len(f.inputs)
				wake := f.inputs[len(f.inputs)-1]
				f.mu.Unlock()
				if inputs != 3 || !strings.Contains(string(wake.Message), "Thread Control wait_agent") || !strings.Contains(string(wake.Message), "compaction") {
					t.Fatalf("wait did not continue once with retained outcome: inputs=%d message=%s", inputs, wake.Message)
				}
				if scenario == "failed" && !strings.Contains(string(wake.Message), "fixture failure") {
					t.Fatal("failed compact outcome was lost")
				}
				if scenario == "timeout" && !strings.Contains(string(wake.Message), "timeout") {
					t.Fatal("deadline lost during compaction")
				}
				b.execution.driver.Tick(time.Now())
				synctest.Wait()
				f.mu.Lock()
				defer f.mu.Unlock()
				if len(f.inputs) != 3 {
					t.Fatal("duplicate wake")
				}
			})
		})
	}
}
