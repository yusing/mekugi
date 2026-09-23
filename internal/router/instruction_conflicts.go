package router

import (
	"fmt"
	"strings"
)

// Source: internal/router/model_instruction_conflicts.go:1:141@origin/main rewriteStockToolConflicts.
// One exact-phrase rewrite handles current stock-prompt conflicts in the
// request's instruction carriers, excluding fenced examples. Frontend
// descriptions remain registry-owned.
func rewriteRequestInstructionConflicts(request *parsedResponsesRequest) error {
	catalog := request.responseTools()
	sections := []*responsesToolSection{catalog.top}
	for _, group := range catalog.additional {
		sections = append(sections, group.tools)
	}
	planOnly := false
	for _, section := range sections {
		if section == nil || section.err != nil {
			continue
		}
		for _, tool := range section.tools {
			if tool == nil {
				continue
			}
			candidates := []*responsesToolDefinition{tool}
			if tool.Type == "namespace" && tool.Name == "functions" && tool.nested != nil && tool.nested.err == nil {
				candidates = tool.nested.tools
			}
			for _, candidate := range candidates {
				if candidate != nil && candidate.Type == "function" &&
					(candidate.Name == "request_user_input" || candidate.Name == "functions.request_user_input") &&
					strings.Contains(candidate.Description, "This tool is only available in Plan mode.") {
					planOnly = true
				}
			}
		}
	}
	pairs := []string{
		"Put this explanation in a short, separate paragraph at the end of both commentary and final, after any permission question.",
		"Record this explanation as a short journal item after any permission question. Use report_now for an immediate progress notice.",
		"Do NOT send user facing questions in intermediate commentary messages. Do NOT put a final response in the commentary channel. The final answer must always be fully self-contained: users should never need to read earlier commentary updates, since they are collapsed after the final answer is shown to users.",
		"Use the user-input tools for questions when available. Record the terminal result in the journal; do not emit provider final-answer text.",
		"Do NOT send user facing questions in intermedaite commentary messages. Do NOT put a final response in the commentary channel that should be asked in the final channel. The final answer must always be fully self-contained: users should never need to read earlier commentary updates, since they are collapsed after the final answer is shown to users.",
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
		"Use the projected Journal guidance for progress delivery; batch add/edit/delete mutations on useful ordinary calls. Call functions.journal for list or finish.",
		"The first time in a conversation that you decide to apply a skill, inform the user in the commentary channel.",
		"When applying a skill is a meaningful milestone, record it in the journal.",
		"Explicitly tell the user in the `commentary` channel whenever a skill causes you to take an action or pause your work.",
		"Record skill-related milestones in the journal; use report_now for a blocking issue.",
		"- First, tell the user in the commentary channel **why** you are using the skill.",
		"- Record why you are using the skill when it is a meaningful journal milestone.",
		"answer briefly in commentary,",
		"answer briefly with a report_now journal item,",
	}
	// Restore the pinned planning/wait conflicts without rewriting caller-added
	// qualifications. Newlines restrict these replacements to whole physical lines.
	for _, pair := range [][2]string{
		{"* Keep asking until you can clearly state: goal + success criteria, audience, in/out of scope, constraints, current state, and the key preferences/tradeoffs.", "* Resolve enough intent to clearly state: goal + success criteria, audience, in/out of scope, constraints, current state, and the key preferences/tradeoffs."},
		{"* Once intent is stable, keep asking until the spec is decision complete: approach, interfaces (APIs/schemas/I/O), data flow, edge cases/failure modes, testing + acceptance criteria, rollout/monitoring, and any migrations/compat constraints.", "* Once intent is stable, resolve the spec until it is decision complete: approach, interfaces (APIs/schemas/I/O), data flow, edge cases/failure modes, testing + acceptance criteria, rollout/monitoring, and any migrations/compat constraints."},
		{"You SHOULD ask many questions, but each question must:", "Ask only the questions needed to make the plan decision complete. Each question must:"},
		{"- Avoid performing blocking sleep or wait calls longer than 60 seconds, as they may prevent you from communicating with the user for their duration.", "- Use completion notifications or interruptible waits; do not shorten waits solely to record progress."},
	} {
		pairs = append(pairs, "\n"+pair[0]+"\n", "\n"+pair[1]+"\n")
	}
	// Frame one physical line at a time, so only complete stock-tool lines
	// disappear. In particular, caller-added suffixes and generic plan advice
	// must not be removed with the unavailable update_plan tool.
	for _, line := range []string{
		"When using the planning tool:",
		"- Skip using the planning tool for straightforward tasks (roughly the easiest 25%).",
		"- When you made a plan, update it after having performed one of the sub-tasks that you shared on the plan.",
		"You have access to an `update_plan` tool which tracks steps.",
		"A tool named `update_plan` is available to you. Update the checklist.",
		"Separately, `update_plan` is a checklist/progress/TODOs tool; it does not enter or exit Plan Mode.",
		"When `update_plan` is available, follow this section.",
		"- Use the plan tool to explain the work",
		"- If you create a checklist or task list, update its statuses.",
		"If update_plan is available, use it for complex work.",
	} {
		pairs = append(pairs, "\n"+line+"\n", "\n\n")
	}
	if planOnly {
		pairs = append(pairs,
			"Use the `request_user_input` tool only when it is listed in the available tools for this turn.",
			"Do not call the `request_user_input` tool in Default mode, even if it is listed in the available tools for this turn.",
			"Use the `request_user_input` tool only for optional questions where the answer would materially improve the quality of the work.",
			"For optional questions, make a reasonable assumption and continue unless explicit user input is required.",
			"If `request_user_input` returns no answers, continue with best judgment instead of asking again or treating the turn as blocked.",
			"",
		)
	}
	replacer := strings.NewReplacer(pairs...)
	rewriteText := func(source string) string {
		var output strings.Builder
		var fence byte
		var fenceWidth int
		for line := range strings.SplitAfterSeq(source, "\n") {
			content := strings.TrimRight(line, "\r\n")
			trimmed := strings.TrimLeft(content, " ")
			marker := byte(0)
			width := 0
			if len(content)-len(trimmed) <= 3 && len(trimmed) >= 3 && (trimmed[0] == '`' || trimmed[0] == '~') {
				marker = trimmed[0]
				for width < len(trimmed) && trimmed[width] == marker {
					width++
				}
				if width < 3 {
					marker = 0
				}
			}
			fenced := fence != 0
			if fence == 0 && marker != 0 {
				fence, fenceWidth = marker, width
				fenced = true
			} else if fence != 0 && marker == fence && width >= fenceWidth && strings.TrimSpace(trimmed[width:]) == "" {
				fence = 0
			}
			if fenced {
				output.WriteString(line)
				continue
			}
			framed := replacer.Replace("\n" + content + "\n")
			output.WriteString(strings.TrimSuffix(strings.TrimPrefix(framed, "\n"), "\n"))
			output.WriteString(line[len(content):])
		}
		return output.String()
	}
	if original, ok := decodeJSONString(request.fields["instructions"]); ok {
		if updated := rewriteText(original); updated != original {
			request.fields["instructions"] = mustMarshalJSON(updated)
		}
	}
	if len(request.fields["input"]) == 0 {
		return nil
	}
	input, err := decodeResponsesInput(request.fields["input"])
	if err != nil {
		return fmt.Errorf("decode instruction conflicts: %w", err)
	}
	if !input.array {
		return nil
	}
	changed := false
	for index, raw := range input.items {
		item, ok := decodeResponsesItem(raw)
		if !ok || (item.Type != "" && item.Type != "message") || item.Role != "developer" {
			continue
		}
		content, found, err := transformResponsesTextContent(item.Content, rewriteText, isResponsesInputTextPart)
		if err != nil {
			return fmt.Errorf("rewrite developer instruction conflicts: %w", err)
		}
		if !found || string(content) == string(item.Content) {
			continue
		}
		item.setContent(content)
		input.items[index] = mustMarshalJSON(item)
		changed = true
	}
	if !changed {
		return nil
	}
	encoded, err := input.encode()
	if err != nil {
		return fmt.Errorf("encode instruction conflicts: %w", err)
	}
	request.setInput(encoded)
	return nil
}
