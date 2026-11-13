package rollout

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/contracts/observation"
)

func TestReplayRetainsNativeCoordinationWithoutToolReconstruction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	source := `{"type":"session_meta","payload":{"id":"main","cwd":"/workspace"}}
{"timestamp":"2026-11-11T12:00:00Z","type":"response_item","payload":{"type":"agent_message","id":"native-message","author":"/root/worker","recipient":"/root","content":[{"type":"input_text","text":"Message Type: MESSAGE\nTask name: /root\nSender: /root/worker\nPayload:\nStatus: failed\nverbatim body"}]}}
{"timestamp":"2026-11-11T12:00:01Z","type":"response_item","payload":{"type":"message","id":"native-task","role":"assistant","content":[{"type":"input_text","text":"Message Type: NEW_TASK\nTask name: /root/worker\nSender: /root\nPayload:\nOriginal assignment"}],"internal_chat_message_metadata_passthrough":{"content_item_kinds":["multi_agent.inter_agent_message"]}},"metadata":{"inherited_user_message":true}}
{"timestamp":"2026-11-11T12:00:02Z","type":"response_item","payload":{"type":"function_call_output","call_id":"wait-call","output":"not parsed as coordination"},"metadata":{"coordination":{"deliveries":[{"deliveryId":"terminal","source":{"threadId":"worker","taskName":"/root/worker","workId":"child-work"},"disposition":"failed","recipient":"/root","body":"Exact final\nCaptured changes: frozen","captureResourceRef":"capture:one"}],"wait":{"waitId":"wait","callerThreadId":"main","callerWorkId":"main-work","origin":{"kind":"cell","turnId":"before-reset","itemId":"exec","windowId":"window","cellId":"cell"},"mode":"all","claimId":"claim","selected":[{"threadId":"worker","taskName":"/root/worker","workId":"child-work"}],"result":{"kind":"released","wake":"events"},"messages":[],"outcomes":[{"target":{"threadId":"worker","taskName":"/root/worker","workId":"child-work"},"disposition":"failed"}]}}}}
{"timestamp":"2026-11-11T12:00:03Z","type":"event_msg","payload":{"type":"agent_coordination","threadId":"main","turnId":"request-turn","projection":{"interrupt":{"operationId":"interrupt","target":{"threadId":"worker","taskName":"/root/worker","workId":"next-work"}}}}}
{"timestamp":"2026-11-11T12:00:04Z","type":"event_msg","payload":{"type":"context_reset","threadId":"main","turnId":"reset-turn","requestId":"manual-reset","generation":2,"resetId":"reset","status":"failed","inline":false,"diagnostic":"Exact reset failure"}}
{"type":"response_item","payload":{"type":"message","role":"assistant","content":"unrelated provider shape remains ignored"}}
`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	replay, err := Replay(t.Context(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.Events) != 5 || len(replay.Unsupported) != 0 {
		t.Fatalf("native facts lost or counted as unsupported tools: %+v", replay)
	}
	for _, event := range replay.Events {
		if event.ThreadID != "main" || event.Item.Type != "" || !event.Started.IsZero() {
			t.Fatalf("native observation became a synthetic tool/lifecycle: %+v", event)
		}
	}
	message, task := replay.Events[0], replay.Events[1]
	if message.Kind != observation.CoordinationObserved || message.NativeMessage.ID != "native-message" || !strings.HasSuffix(message.NativeMessage.Content[0].Text, "Status: failed\nverbatim body") || message.TurnID != "" {
		t.Fatalf("turnless native message lost body or acquired a turn: %+v", message)
	}
	if !task.Inherited || task.NativeMessage.Type != "message" || task.NativeMessage.Metadata.Kinds[0] != "multi_agent.inter_agent_message" {
		t.Fatalf("contextual carrier lost provenance: %+v", task)
	}
	claim := replay.Events[2].Coordination
	if claim == nil || claim.Deliveries[0].Body != "Exact final\nCaptured changes: frozen" || *claim.Deliveries[0].CaptureResourceRef != "capture:one" || claim.Wait.CallerWorkID != "main-work" || claim.Wait.Origin.TurnID != "before-reset" || claim.Wait.Outcomes[0].Disposition != "failed" {
		t.Fatalf("frozen response metadata changed: %+v", claim)
	}
	request := replay.Events[3]
	if request.TurnID != "request-turn" || request.Coordination.Interrupt.OperationID != "interrupt" || request.Coordination.Interrupt.Target.WorkID != "next-work" {
		t.Fatalf("requested interruption lost its exact target: %+v", request)
	}
	reset := replay.Events[4]
	if reset.Kind != observation.ContextResetObserved || reset.Reset.RequestID != "manual-reset" || reset.Reset.Generation != 2 || reset.Reset.ResetID != "reset" || reset.Reset.Diagnostic != "Exact reset failure" || reset.TurnID != "reset-turn" {
		t.Fatalf("reset receipt was dropped or conflated with a wait: %+v", reset)
	}
}

func TestReplayNativeWaitItemPreservesFrozenMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	source := `{"type":"session_meta","payload":{"id":"main","cwd":"/workspace"}}
{"timestamp":"2026-11-11T12:00:00Z","type":"event_msg","payload":{"type":"item_completed","thread_id":"main","turn_id":"turn","started_at_ms":1794398400000,"completed_at_ms":1794398401000,"item":{"type":"CollabAgentToolCall","id":"wait-call","tool":"wait","status":"completed","receiver_thread_ids":["child"],"timeout_ms":1000}}}
`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	replay, err := Replay(t.Context(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.Events) != 1 || len(replay.Unsupported) != 0 {
		t.Fatalf("native wait item lost: %+v", replay)
	}
	event := replay.Events[0]
	item := event.Item
	if event.ThreadID != "main" || event.TurnID != "turn" || item.ID != "wait-call" || item.Type != "collabAgentToolCall" || item.Tool != "wait" || len(item.ReceiverThreadIDs) != 1 || item.ReceiverThreadIDs[0] != "child" || item.TimeoutMS == nil || *item.TimeoutMS != 1000 {
		t.Fatalf("native wait metadata changed: %+v", event)
	}
}

func TestReplayNativeFactsRequireSourceIdentityAndValidCarrier(t *testing.T) {
	for name, source := range map[string]string{
		"before metadata": `{"timestamp":"2026-11-11T12:00:00Z","type":"event_msg","payload":{"type":"agent_coordination","threadId":"main","projection":{}}}`,
		"without timestamp": `{"type":"session_meta","payload":{"id":"main"}}
{"type":"response_item","payload":{"type":"function_call_output"},"metadata":{"coordination":{"deliveries":[]}}}`,
		"foreign owner": `{"type":"session_meta","payload":{"id":"main"}}
{"timestamp":"2026-11-11T12:00:00Z","type":"event_msg","payload":{"type":"agent_coordination","threadId":"other","projection":{}}}`,
		"missing reset owner": `{"type":"session_meta","payload":{"id":"main"}}
{"timestamp":"2026-11-11T12:00:00Z","type":"event_msg","payload":{"type":"context_reset","status":"failed","generation":1}}`,
		"malformed native message": `{"type":"session_meta","payload":{"id":"main"}}
{"timestamp":"2026-11-11T12:00:00Z","type":"response_item","payload":{"type":"agent_message","id":"message","content":"not native content blocks"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "rollout.jsonl")
			if err := os.WriteFile(path, []byte(source+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if result, err := Replay(t.Context(), path, nil); err == nil || result.Thread != "" || !strings.Contains(err.Error(), "rollout.jsonl:") {
				t.Fatalf("invalid native evidence became a complete replay: result=%+v err=%v", result, err)
			}
		})
	}
}

func TestReplayNativeFactsPreserveInheritedOwnership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	source := `{"type":"session_meta","payload":{"id":"main"}}
{"type":"session_meta","payload":{"id":"ancestor"}}
{"timestamp":"2026-11-11T12:00:00Z","type":"event_msg","payload":{"type":"agent_coordination","threadId":"ancestor","projection":{"interrupt":{"operationId":"copied","target":{"threadId":"worker","taskName":"/root/worker","workId":"old"}}}}}
{"timestamp":"2026-11-11T12:00:01Z","type":"response_item","payload":{"type":"agent_message","id":"copied-message","author":"/root","recipient":"/root/worker","content":[]}}
{"timestamp":"2026-11-11T12:00:02Z","type":"event_msg","payload":{"type":"context_reset","threadId":"ancestor","turnId":"original-turn","generation":3,"status":"canceled"},"metadata":{"inherited_user_message":true}}
{"timestamp":"2026-11-11T12:00:03Z","type":"event_msg","payload":{"type":"thread_settings_applied","thread_id":"main"}}
{"timestamp":"2026-11-11T12:00:04Z","type":"event_msg","payload":{"type":"agent_coordination","threadId":"sibling","projection":{}}}
{"timestamp":"2026-11-11T12:00:05Z","type":"response_item","payload":{"type":"agent_message","id":"own-message","author":"/root/worker","recipient":"/root","content":[]}}
`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	replay, err := Replay(t.Context(), path, map[string]bool{"sibling": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.Events) != 2 || !replay.Events[0].Inherited || replay.Events[0].ThreadID != "main" || replay.Events[0].Reset.Thread != "ancestor" || replay.Events[0].Reset.Turn != "original-turn" || replay.Events[1].NativeMessage.ID != "own-message" {
		t.Fatalf("copied evidence became new work or inherited identity changed: %+v", replay.Events)
	}
}
