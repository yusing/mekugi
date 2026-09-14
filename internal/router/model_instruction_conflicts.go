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
	"Use the central Journal rules for progress delivery; batch mutations on the current ordinary tool call or shell command, and call functions.journal only for list, or for finishing when no current call can carry the operation.",
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

// rewriteStockPlanInstructions removes only pinned checklist fragments. Codex's
// section-removal helper is restricted to Codex-owned text; this boundary also
// receives caller instructions, so nearby paragraphs and continuations are not ours.
// Keep empty line boundaries so removing a fragment cannot expose a new match on
// a later refresh.
func rewriteStockPlanInstructions(input string) string {
	lines := strings.SplitAfter(input, "\n")
	var rendered strings.Builder
	rendered.Grow(len(input))
	planBlock := [...]string{
		"When using the planning tool:",
		"- Skip using the planning tool for straightforward tasks (roughly the easiest 25%).",
		"- Do not make single-step plans.",
		"- When you made a plan, update it after having performed one of the sub-tasks that you shared on the plan.",
	}
	var fence byte
	var fenceWidth int
	for index := 0; index < len(lines); {
		line := strings.TrimRight(lines[index], "\r\n")
		marker := strings.TrimLeft(line, " ")
		if len(line)-len(marker) <= 3 && len(marker) >= 3 && (marker[0] == '`' || marker[0] == '~') {
			width := 1
			for width < len(marker) && marker[width] == marker[0] {
				width++
			}
			if width >= 3 {
				if fence == 0 {
					fence, fenceWidth = marker[0], width
				} else if marker[0] == fence && width >= fenceWidth && strings.TrimSpace(marker[width:]) == "" {
					fence = 0
				}
				rendered.WriteString(lines[index])
				index++
				continue
			}
		}
		if fence != 0 {
			rendered.WriteString(lines[index])
			index++
			continue
		}
		count := 0
		if index+len(planBlock) <= len(lines) {
			matches := true
			for offset, expected := range planBlock {
				if strings.TrimRight(lines[index+offset], "\r\n") != expected {
					matches = false
					break
				}
			}
			if matches {
				count = len(planBlock)
			}
		}
		if count == 0 {
			switch strings.TrimRight(lines[index], "\r\n") {
			case "You have access to an `update_plan` tool which tracks steps.",
				"A tool named `update_plan` is available to you. Update the checklist.",
				"Separately, `update_plan` is a checklist/progress/TODOs tool; it does not enter or exit Plan Mode.",
				"When `update_plan` is available, follow this section.",
				"- Use the plan tool to explain the work",
				"- If you create a checklist or task list, update its statuses.",
				"If update_plan is available, use it for complex work.":
				count = 1
			}
		}
		if count == 0 {
			rendered.WriteString(lines[index])
			index++
			continue
		}
		for _, line := range lines[index : index+count] {
			content := strings.TrimRight(line, "\r\n")
			rendered.WriteString(line[len(content):])
		}
		index += count
	}
	return rendered.String()
}
