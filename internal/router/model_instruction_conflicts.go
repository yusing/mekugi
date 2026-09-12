package router

import "strings"

// Pinned conflicting fragments from the GPT-6 and GPT-5.6 model templates and
// the active Codex prompt. Keep unrelated policy and tool-independent safety
// guidance intact. Apply outside our marked section too, so inherited prompts
// do not retain conflicts from an earlier rewrite.
var stockToolConflictReplacer = strings.NewReplacer(
	"Put this explanation in a short, separate paragraph at the end of both commentary and final, after any permission question.",
	"Record this explanation as a short journal item after any permission question. Use report_now for an immediate progress notice.",
	"Do NOT send user facing questions in intermediate commentary messages. Do NOT put a final response in the commentary channel. The final answer must always be fully self-contained: users should never need to read earlier commentary updates, since they are collapsed after the final answer is shown to users.",
	"Use the user-input tools for questions when available. Record the terminal result in the journal; do not emit provider final-answer text.",
	"Do NOT put a final response (e.g. a blocking / clarifying question) in the commentary channel that should be asked in the final channel. Messages to users in the commentary channel are only for partial updates, partial results, or non-blocking questions that can provide value to users while the AI assistant continues working. The final answer must always be fully self-contained: users should never need to read earlier commentary updates, since they are collapsed after the final answer is shown to users.",
	"Record final results and blocking questions in the journal. Set report_now for immediate notices, or use the user-input tools for questions. Do not emit provider final-answer text.",
	"- You share updates in the `commentary` channel.",
	"- Record milestones using journal mutations; set report_now for immediate progress.",
	"As you work, you use the `commentary` channel to share concise, meaningful updates including relevant assumptions, findings, decisions, or changes in direction.",
	"Record meaningful findings, decisions, and changes of approach as journal items, batching mutations on supported calls.",
	"As you work, you send messages to the `commentary` channel.",
	"As you work, batch journal mutations on supported tool calls.",
	"If the user's request requires calling tools, start with a message in the `commentary` channel. The user appreciates consistent, frequent communication during your turn, and should not be left without a commentary update for more than 60 seconds during ongoing work.",
	"Use the central Journal rules for progress delivery; call functions.journal when no ordinary call can carry the mutation.",
	"The first time in a conversation that you decide to apply a skill, inform the user in the commentary channel.",
	"When applying a skill is a meaningful milestone, record it in the journal.",
	"Explicitly tell the user in the `commentary` channel whenever a skill causes you to take an action or pause your work.",
	"Record skill-related milestones in the journal; use report_now for a blocking issue.",
	"- First, tell the user in the commentary channel **why** you are using the skill.",
	"- Record why you are using the skill when it is a meaningful journal milestone.",
	"answer briefly in commentary,\nthen resume the active task",
	"answer briefly with a report_now journal item,\nthen resume the active task",
	"answer briefly in commentary, then resume the active task",
	"answer briefly with a report_now journal item, then resume the active task",
	"- Batch independent searches and reads in one functions.exec using await Promise.allSettled([...]); inspect every result. Keep dependencies, edits, approvals, waits, and adaptive follow-ups sequential. Avoid unnecessary output.",
	"- Batch already-known searches and reads in one functions.shell script; inspect every result. Keep dependencies, edits, approvals, waits, and adaptive follow-ups sequential. Avoid unnecessary output.",
	"- Batch independent searches, reads, and other tool calls in one functions.exec using await Promise.allSettled([...]); keep each batch bounded to decision-relevant output by selecting needed ranges or fields first, and inspect every returned result. If output truncates, retrieve only the missing evidence rather than repeating an unchanged whole scan. Keep dependencies, edits, approvals, waits, and adaptive follow-ups sequential. Avoid unnecessary output.",
	"- Batch already-known searches and reads in one functions.shell script; bound output to needed ranges or fields and inspect every result. If output truncates, retrieve only missing evidence. Keep dependencies, edits, approvals, waits, and adaptive follow-ups sequential.",
	"- To reduce round trips, batch independent searches, reads, and other tool calls in one functions.exec using await Promise.allSettled([...]); keep each batch bounded to decision-relevant output by selecting needed ranges or fields first, and inspect every returned result. If output truncates, retrieve only the missing evidence rather than repeating an unchanged whole scan. Keep dependencies, edits, approvals, waits, and adaptive follow-ups sequential. Avoid unnecessary output.",
	"- Batch already-known searches and reads in one functions.shell script; bound output to needed ranges or fields and inspect every result. If output truncates, retrieve only missing evidence. Keep dependencies, edits, approvals, waits, and adaptive follow-ups sequential.",
	"- When calling `functions.exec`, parallelize independent tool calls by awaiting Promises. Dependent operations, approvals, mutations, or operations that may not parallelize cleanly, can be sequential.",
	"- Parallelize independent calls only when their tool contracts allow it. Run hpatch alone; sequence dependent operations, approvals, and mutations.",
	"- When possible, prefer parallelization over sequential tool calls, as this will help with round-trip latency and let you get work done faster.",
	"- Parallelize independent calls only when their tool contracts allow it. Run hpatch alone; sequence dependent operations, approvals, and mutations.",
	"- Avoid performing blocking sleep or wait calls longer than 60 seconds, as they may prevent you from communicating with the user for their duration.",
	"- Use completion notifications or interruptible waits; do not shorten waits solely to record progress.",
	"* Keep asking until you can clearly state: goal + success criteria, audience, in/out of scope, constraints, current state, and the key preferences/tradeoffs.",
	"* Resolve enough intent to clearly state: goal + success criteria, audience, in/out of scope, constraints, current state, and the key preferences/tradeoffs.",
	"* Once intent is stable, keep asking until the spec is decision complete: approach, interfaces (APIs/schemas/I/O), data flow, edge cases/failure modes, testing + acceptance criteria, rollout/monitoring, and any migrations/compat constraints.",
	"* Once intent is stable, resolve the spec until it is decision complete: approach, interfaces (APIs/schemas/I/O), data flow, edge cases/failure modes, testing + acceptance criteria, rollout/monitoring, and any migrations/compat constraints.",
	"You SHOULD ask many questions, but each question must:",
	"Ask only the questions needed to make the plan decision complete. Each question must:",
)

var planOnlyDefaultModeConflictReplacer = strings.NewReplacer(
	"Use the `request_user_input` tool only when it is listed in the available tools for this turn.",
	"Do not call the `request_user_input` tool in Default mode, even if it is listed in the available tools for this turn.",
	"Use the `request_user_input` tool only for optional questions where the answer would materially improve the quality of the work.",
	"For optional questions, make a reasonable assumption and continue unless explicit user input is required.",
	"If `request_user_input` returns no answers, continue with best judgment instead of asking again or treating the turn as blocked.",
	"",
)

func rewriteStockToolConflicts(input string) string {
	return rewriteStockPlanInstructions(stockToolConflictReplacer.Replace(input))
}

// rewriteStockPlanInstructions removes checklist guidance independently of Codex's
// launch-time filtering, which does not cover custom catalogs or direct routing.
// Ordinary planning sections and our edit-planning workflow are not checklist APIs.
// Source: codex-rs/core/src/context/update_plan_instructions.rs:4:62
// without_update_plan_instructions in the read-only Codex source.
func rewriteStockPlanInstructions(input string) string {
	lines := strings.SplitAfter(input, "\n")
	var rendered strings.Builder
	for index := 0; index < len(lines); {
		line := strings.TrimRight(lines[index], "\r\n")
		switch line {
		case "## Planning", "## Tasks", "# Tasks", "## `update_plan`", "## Plan tool", "## Plan Mode vs update_plan tool":
			end := index + 1
			for end < len(lines) && !strings.HasPrefix(lines[end], "# ") && !strings.HasPrefix(lines[end], "## ") &&
				strings.TrimSpace(lines[end]) != mekugiInstructionsStartMarker && strings.TrimSpace(lines[end]) != mekugiInstructionsEndMarker {
				end++
			}
			section := strings.Join(lines[index:end], "")
			checklist := strings.Contains(section, "A tool named `update_plan` is available to you.") ||
				strings.Contains(section, "When using the planning tool:\n- Skip using the planning tool for straightforward tasks (roughly the easiest 25%).\n- Do not make single-step plans.\n- When you made a plan, update it after having performed one of the sub-tasks that you shared on the plan.") ||
				strings.Contains(section, "Separately, `update_plan` is a checklist/progress/TODOs tool; it does not enter or exit Plan Mode.") ||
				strings.Contains(section, "You have access to an `update_plan` tool") ||
				strings.Contains(section, "When `update_plan` is available, follow this section")
			if checklist {
				index = end
				continue
			}
		}
		if line == "Progress visibility:" && index+1 < len(lines) && strings.HasPrefix(lines[index+1], "If update_plan is available") {
			index += 2
			if index < len(lines) && strings.TrimSpace(lines[index]) == "" {
				index++
			}
			continue
		}
		if strings.HasPrefix(line, "- Use the plan tool ") || strings.HasPrefix(line, "- If you create a checklist or task list,") {
			index++
			for index < len(lines) && (strings.HasPrefix(lines[index], " ") || strings.HasPrefix(lines[index], "\t")) {
				index++
			}
			continue
		}
		rendered.WriteString(lines[index])
		index++
	}
	return rendered.String()
}
