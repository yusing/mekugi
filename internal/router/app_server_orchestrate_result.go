package router

import "fmt"

// Codex queues these items for an active turn or records them for an idle thread.
// It owns mailbox wake-up, history retention and the next model continuation.
func (u *appServerUI) deliverOrchestratedAnswer(child *orchestrateChild, event orchestrateEvent, answer string) {
	text := fmt.Sprintf("Message Type: FINAL_ANSWER\nTask name: /root\nSender: /root/%s\nPayload:\n%s", child.batch.TaskName, answer)
	err := u.orchestrateRPCRequest(orchestrateRPC{method: "thread/inject_items", child: child, completion: &event}, map[string]any{
		"threadId": child.command.main,
		"items": []any{map[string]any{
			"type": "message", "role": "assistant",
			"content": []any{map[string]any{"type": "output_text", "text": text}},
		}},
	})
	if err != nil {
		u.orchestratedAnswerFailure(child, event, err)
	}
}

func (u *appServerUI) orchestratedAnswerFailure(child *orchestrateChild, event orchestrateEvent, err error) {
	u.setNotice("Orchestration result delivery: "+err.Error(), true)
	event.Kind, event.Status, event.Message = "failure", "delivery_failed", err.Error()
	u.publishOrchestratedEvent(child, event)
}
