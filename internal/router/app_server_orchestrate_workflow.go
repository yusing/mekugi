package router

import (
	"fmt"
	"strings"
)

// Tool instructions are explicit input, not request-dependent tool guidance.
const orchestrateCoordinatorInstructions = `Use isolated Git, SVN or unversioned shadow batches. Bind each integration task to /root/task_name. Names use lowercase letters, digits and underscores.
Call tools.mcp__orchestrate__<tool> with JSON arguments:
prepare: task_name; optional evidence:[{name,source}] (absolute source files copied into run state)
spawn_agent: task_name,message; optional model,reasoning_effort,service_tier
list_agents: {}
wait_agent: timeout_ms (default 10000)
send_message,followup_task: target,message
interrupt_agent: target
integrate: target (shadow merge and writeback; conflicts write nothing)
cleanup: target (accepted idle Git or shadow checkout; removes unchanged owned evidence; retains branches, private repositories and nested commits)
Prepare at Git HEAD, SVN's local committed baseline or a complete unversioned source snapshot. Evidence paths are returned and listed in the first child input. Copy required checkout-relative ignored inputs into the returned cwd before spawn. Versioned source edits do not follow. Spawn starts a fresh independent thread; message carries its complete assignment, constraints and checks. Select requested budgets; omitted settings inherit Main's effective settings.
send_message queues without waking; followup_task starts or steers. Targets are task names or /root/task_name. Native collaboration tools cannot reach these threads.
Git batches use native Git integration. Acceptance requires batch commits to be reachable or patch-equivalent in the source, including changed submodule commits. Mark the bound task accepted only after verifying integration; child completion leaves it open. cleanup requires an accepted idle checkout.
Use integrate for shadow results; it preserves unrelated source edits and reports conflicts before writes. Inspect partial or uncertain writeback before retrying. Main marks the bound task accepted separately after verifying integration.`

const orchestrateWorkflow = orchestrateCoordinatorInstructions + "\nIssues:\n"

func (u *appServerUI) expandOrchestrate() bool {
	prefix := strings.Index(u.draft, "/orchestrate") + len("/orchestrate")
	if strings.TrimSpace(u.draft[prefix:]) == "" {
		u.openOrchestratePicker()
		return false
	}
	if u.proxy == nil || u.proxy.orchestration == nil {
		u.setNotice("Orchestration is unavailable in this session", true)
		return false
	}
	if u.thread == "" || u.restoring != nil || u.replacement.pending() {
		u.setNotice("Wait for the session to be ready", false)
		return false
	}
	u.deleteDraftRange(0, prefix)
	u.loadDraft(joinDrafts(composerDraft{text: orchestrateWorkflow}, u.draftSnapshot()))
	return true
}

func orchestrateChildInstructions(cwd string) string {
	return fmt.Sprintf(`Work in %s. Your coordinator is main. tools.mcp__orchestrate__send_message({target,message}) queues sibling input without waking; tools.mcp__orchestrate__followup_task({target,message}) starts or steers a sibling. Either tool with target main reports to the coordinator without starting or steering a user turn. Targets are task names or /root/task_name; native collaboration tools reach only your native descendants. Main's journal is readable with agent:"main"; your journal root is independent. Human decision questions use request_user_input_async.
`, cwd)
}

func orchestrateChildInput(cwd, message string) string {
	return orchestrateChildInstructions(cwd) + "Assignment:\n" + message
}

// relatedJournals has verified this root's durable run membership before rendering.
func orchestrateRecoveryInstructions(j threadJournal) string {
	if j.Orchestration == nil || j.Parent != "" || j.Author != "/root" {
		return ""
	}
	if j.Thread == j.Orchestration.Main {
		return "Orchestration tools\n" + orchestrateCoordinatorInstructions + "\n\n"
	}
	return "Orchestration tools\n" + orchestrateChildInstructions(j.Workspace) + "\n"
}
