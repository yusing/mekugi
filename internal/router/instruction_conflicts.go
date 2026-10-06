package router

import (
	"fmt"
	"regexp"
	"strings"
)

// Match Codex's uninstalled-plugin advertisement, not caller-authored plugin
// policy. Names and IDs come from the host's current recommendation list.
var stockRecommendedPlugins = regexp.MustCompile(`(?m)^<recommended_plugins>\r?\nHere is a list of plugins that are available but not installed\.\r?\n\r?\n(?:- [^\r\n<>]+ \([^\r\n<>]+\)\r?\n)+</recommended_plugins>(\r?)$`)

// Exact stock-fragment rewrites handle current stock-prompt conflicts in the
// request's instruction carriers, excluding fenced examples. Frontend
// descriptions remain registry-owned.
func rewriteRequestInstructionConflicts(request *parsedResponsesRequest) error {
	pairs := []string{
		"Put this explanation in a short, separate paragraph at the end of both commentary and final, after any permission question.",
		"",
		"Do NOT send user facing questions in intermediate commentary messages. Do NOT put a final response in the commentary channel. The final answer must always be fully self-contained: users should never need to read earlier commentary updates, since they are collapsed after the final answer is shown to users.",
		"",
		"Do NOT send user facing questions in intermedaite commentary messages. Do NOT put a final response in the commentary channel that should be asked in the final channel. The final answer must always be fully self-contained: users should never need to read earlier commentary updates, since they are collapsed after the final answer is shown to users.",
		"",
		"Do NOT put a final response (e.g. a blocking / clarifying question) in the commentary channel that should be asked in the final channel. Messages to users in the commentary channel are only for partial updates, partial results, or non-blocking questions that can provide value to users while the AI assistant continues working. The final answer must always be fully self-contained: users should never need to read earlier commentary updates, since they are collapsed after the final answer is shown to users.",
		"",
		"- You share updates in the `commentary` channel.",
		"",
		"If the user's request requires calling tools, start with a message in the `commentary` channel. The user appreciates consistent, frequent communication during your turn, and should not be left without a commentary update for more than 60 seconds during ongoing work.",
		"",
		"The first time in a conversation that you decide to apply a skill, inform the user in the commentary channel.",
		"",
		"Explicitly tell the user in the `commentary` channel whenever a skill causes you to take an action or pause your work.",
		"",
		"- First, tell the user in the commentary channel **why** you are using the skill.",
		"",
	}
	for i := range pairs {
		pairs[i] = "\n" + pairs[i] + "\n"
	}
	// These stock paragraphs mix useful policy with a commentary directive.
	// Match the complete paragraph so caller-added qualifications remain intact.
	for _, paragraph := range []struct{ source, remove string }{
		{
			"The user gets very frustrated when you stop and ask for confirmation or permission, so make sure to explicitly explain why you need the confirmation (for example, a SKILL.md, AGENTS.md, memory, or approval auto-review block) and where it came from. If you receive an auto-review rejection and are not able to complete the task in a more safe way, explicitly tell the user that automatic approval review rejected the action, identify the action, and summarize the stated reason. Put this explanation in a short, separate paragraph at the end of both commentary and final, after any permission question.",
			" Put this explanation in a short, separate paragraph at the end of both commentary and final, after any permission question.",
		},
		{
			"The user may send a new message while you are still working. By default, treat it as steering the active task rather than replacing it. Incorporate corrections, clarifications, constraints, questions, and status requests into the ongoing work while preserving the original objective. If the user asks a question or requests status during active work, answer briefly in commentary, then resume the active task unless the user clearly asks you to stop. Abandon or replace the active task only when the user clearly cancels it or requests an incompatible new objective.",
			" in commentary",
		},
	} {
		pairs = append(pairs, "\n"+paragraph.source+"\n", "\n"+strings.ReplaceAll(paragraph.source, paragraph.remove, "")+"\n")
	}
	// Rewrite the pinned wait conflict without changing caller-added
	// qualifications. Newlines restrict these replacements to whole physical lines.
	for _, pair := range [][2]string{
		{"- Avoid performing blocking sleep or wait calls longer than 60 seconds, as they may prevent you from communicating with the user for their duration.", embeddedInstruction("interruptible_wait")},
	} {
		pairs = append(pairs, "\n"+pair[0]+"\n", "\n"+pair[1]+"\n")
	}
	// Remove complete stock progress lines, preserving caller qualifications.
	for _, line := range []string{
		"<multi_agent_mode>Any earlier instruction enabling proactive multi-agent delegation no longer applies. Do not spawn sub-agents unless the user or applicable AGENTS.md/skill instructions explicitly ask for sub-agents, delegation, or parallel agent work.</multi_agent_mode>",
		"- You yield back to the user and end your turn by sending a final message to the `final` channel.",
		"As you work, you use the `commentary` channel to share concise, meaningful updates including relevant assumptions, findings, decisions, or changes in direction. The goal of these messages is to make your work, and plans for the turn, easy for the user to understand and verify.",
		"As you work, you send messages to the `commentary` channel. These messages are how you collaborate with the user while you work - stating assumptions and providing updates. These messages should be concise and quickly scannable. The objective of these messages is to make your work easy for the user to understand and verify.",
		"- Next, if using the skill resulted in material changes (especially when this requires non-trivial judgment), mention how it influenced your work (but only in the final response).",
		"- You share updates in `commentary` channel.",
		"You have two channels for staying in conversation with the user:",
		"## Intermediate commentary",
		"As you work, you send messages to the `commentary` channel.",
	} {
		pairs = append(pairs, "\n"+line+"\n", "\n\n")
	}
	replacer := strings.NewReplacer(pairs...)
	rewriteText := func(source string) string {
		var output strings.Builder
		var plain strings.Builder
		flushPlain := func() {
			output.WriteString(stockRecommendedPlugins.ReplaceAllString(plain.String(), "$1"))
			plain.Reset()
		}
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
				flushPlain()
				output.WriteString(line)
				continue
			}
			framed := replacer.Replace("\n" + content + "\n")
			plain.WriteString(strings.TrimSuffix(strings.TrimPrefix(framed, "\n"), "\n"))
			plain.WriteString(line[len(content):])
		}
		flushPlain()
		return output.String()
	}
	if original, ok := decodeJSONString(request.fields["instructions"]); ok {
		if updated := rewriteText(original); updated != original {
			request.fields["instructions"] = mustMarshalJSON(updated)
		}
	}
	return rewriteRequestMessages(request, "instruction conflicts", func(item responsesItem) ([]byte, bool, error) {
		if item.Role != "developer" {
			return nil, false, nil
		}
		content, found, err := transformResponsesTextContent(item.Content, rewriteText, isResponsesInputTextPart)
		if err != nil {
			return nil, false, fmt.Errorf("rewrite developer instruction conflicts: %w", err)
		}
		return content, found && string(content) != string(item.Content), nil
	})
}
