package router

import "strings"

// Tool descriptions and parameter help belong to MCP metadata.
const orchestrateCoordinatorInstructions = `Use tools.mcp__orchestrate__spawn_agent, similar to mekugi_collaboration.spawn_agent.`

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

const orchestrateChildInstructions = `Your coordinator is main. Main's journal is readable with agent:"main"; your journal root is independent.
`

// relatedJournals has verified this root's durable run membership before rendering.
func orchestrateRecoveryInstructions(j threadJournal) string {
	if j.Orchestration == nil || j.Parent != "" || j.Author != "/root" {
		return ""
	}
	if j.Thread == j.Orchestration.Main {
		return "Orchestration tools\n" + orchestrateCoordinatorInstructions + "\n\n"
	}
	return "Orchestration tools\n" + orchestrateChildInstructions + "\n"
}
