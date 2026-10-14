package router

import (
	"context"
	"fmt"
	"slices"

	"github.com/yusing/mekugi/internal/orchestrate"
)

func (u *appServerUI) deliverDiagnosisOutcome(child *orchestrateChild) {
	var queued []*orchestrate.WorkflowOutcome
	for outcome := child.batch.Workflow.Outcome; outcome != nil; outcome = outcome.Previous {
		if outcome.State == orchestrate.DispatchQueued {
			queued = append(queued, outcome)
		}
	}
	if len(queued) == 0 {
		return
	}
	slices.Reverse(queued)
	c := child.command
	var dispatches []*orchestrate.WorkflowOutcome
	u.workflowStore(child, func(ctx context.Context, s *orchestrate.Store) error {
		for _, outcome := range queued {
			dispatch, err := s.BeginWorkflowOutcome(ctx, c.workspace, c.main, child.batch.TaskName, outcome.Message, outcome.TurnID)
			if err != nil {
				return err
			}
			if dispatch {
				dispatches = append(dispatches, outcome)
			}
		}
		return nil
	}, func(err error) {
		if err != nil {
			u.setNotice("Diagnosis outcome: "+err.Error(), true)
		}
		for _, outcome := range dispatches {
			event := orchestrateEvent{Task: child.batch.TaskName, Thread: child.batch.Launch.ThreadID, Turn: outcome.TurnID, Kind: "done", Status: "completed"}
			u.deliverOrchestratedAnswer(child, event, outcome.Message)
		}
	})
}

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
		if child.batch.Workflow != nil && child.batch.Workflow.Assignment.Kind == orchestrate.WorkflowDiagnosis {
			u.workflowOutcomeResponse(child, event, err)
		} else {
			u.orchestratedAnswerFailure(child, event, err)
		}
	}
}

func (u *appServerUI) orchestratedAnswerFailure(child *orchestrateChild, event orchestrateEvent, err error) {
	u.setNotice("Orchestration result delivery: "+err.Error(), true)
	event.Kind, event.Status, event.Message = "failure", "delivery_failed", err.Error()
	u.publishOrchestratedEvent(child, event)
}
