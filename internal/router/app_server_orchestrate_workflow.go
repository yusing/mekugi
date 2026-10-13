package router

import "strings"

// Tool descriptions and parameter help belong to MCP metadata.
const orchestrateCoordinatorInstructions = `Use tools.mcp__orchestrate__spawn_agent for isolated implementation batches. Children implement and validate requested changes; Mekugi drives reviews and corrections; Main coordinates and verifies integration. For change requests, this workflow includes local commits and linear Git integration, subject to explicit user restrictions. Diagnosis-only assignments remain read-only.`

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
	if u.orchestrationOwner() != u {
		u.setNotice("Start orchestration from Main", true)
		return false
	}
	if u.proxy.replayStore != nil {
		j, _, err := readThreadJournal(u.proxy.replayStore, u.session.cwd, u.thread)
		if err != nil {
			u.setNotice("Orchestration: "+err.Error(), true)
			return false
		}
		if j.IdentityConflicted || j.Parent != "" || j.Orchestration != nil && j.Orchestration.Main != u.thread {
			u.setNotice("Start orchestration from Main", true)
			return false
		}
	}
	u.deleteDraftRange(0, prefix)
	u.loadDraft(joinDrafts(composerDraft{text: orchestrateWorkflow}, u.draftSnapshot()))
	return true
}

const orchestrateChildInstructions = `Your coordinator is main. Main's journal is readable with agent:"main"; your journal root is independent.
Implement and validate your assigned changes in this checkout. For change requests, local commits are part of delivery; keep Git history linear. Mekugi drives reviews and corrections; Main verifies and integrates your result. Honor explicit user restrictions and read-only assignments.
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
