package router

import (
	"testing"

	"github.com/yusing/mekugi/internal/session"
)

func TestNativeRuntimeSavedAgentCallerContinuity(t *testing.T) {
	u, _ := runtimeTestUI(t)
	runtimeEvidenceEvent(t, u, session.Event{Kind: "task", Historical: true, Callers: []string{"original-parent"}, Task: &session.Task{ID: "review", Kind: "local_agent", Status: "saved"}})
	runtimeEvidenceEvent(t, u, session.Event{Kind: "message", ID: "history/answer", Caller: "original-parent", Text: "Saved child answer", Historical: true})
	runtimeEvidenceTask(t, u, "task_started", session.Task{ID: "review", ToolID: "resumed-launch", Kind: "local_agent", Status: "running"})
	runtimeEvidenceEvent(t, u, session.Event{Kind: "message", ID: "continued-answer", Caller: "original-parent", Text: "Continued child answer"})
	runtimeEvidenceEvent(t, u, session.Event{Kind: "tool", ID: "child-tool", Caller: "resumed-launch", Role: "Bash", Text: `{"command":"true"}`})
	if len(u.runtime.taskOrder) != 1 || u.runtime.tasks["review"].ToolID != "resumed-launch" {
		t.Fatal("native continuation duplicated the saved child or lost its current launch")
	}
	for _, entry := range u.agents.entries {
		if entry.CallID == "history/answer" || entry.CallID == "continued-answer" || entry.CallID == "child-tool" {
			if entry.Agent != runtimeTaskLane("review") {
				t.Fatalf("saved or current caller left its child lane: %+v", entry.activityPaneEntry)
			}
		}
	}
	if runtimeTaskTerminal("running") || !runtimeTaskTerminal("saved") {
		t.Fatal("saved history revived child work")
	}
}
