package router

import (
	"fmt"
	"strings"
)

const (
	instructionOmitStart = "<!-- mekugi:omit -->"
	instructionOmitEnd   = "<!-- /mekugi:omit -->"
)

// stripInstructionOmissions removes complete, exact marker pairs, including
// nested blocks. Unmatched markers and all bytes outside a block stay intact.
func stripInstructionOmissions(text string) string {
	var result strings.Builder
	cursor, start, depth := 0, 0, 0
	for pos := 0; pos < len(text); {
		open := strings.Index(text[pos:], instructionOmitStart)
		close := strings.Index(text[pos:], instructionOmitEnd)
		if open < 0 && close < 0 {
			break
		}
		if open >= 0 && (close < 0 || open < close) {
			pos += open
			if depth == 0 {
				start = pos
			}
			depth++
			pos += len(instructionOmitStart)
		} else {
			pos += close + len(instructionOmitEnd)
			if depth == 0 {
				continue
			}
			depth--
			if depth == 0 {
				result.WriteString(text[cursor:start])
				cursor = pos
			}
		}
	}
	if cursor == 0 {
		return text
	}
	result.WriteString(text[cursor:])
	return result.String()
}

// User-role instruction context is carried in Codex's INSTRUCTIONS wrapper.
// Ordinary user messages, tool results, and assistant history are not instructions.
func stripUserInstructionOmissions(text string) string {
	start := strings.Index(text, "<INSTRUCTIONS>")
	if start < 0 {
		return text
	}
	prefix := strings.TrimSpace(text[:start])
	if prefix != "" && prefix != "# AGENTS.md instructions" && !strings.HasPrefix(prefix, "# AGENTS.md instructions for ") {
		return text
	}
	start += len("<INSTRUCTIONS>")
	end := strings.Index(text[start:], "</INSTRUCTIONS>")
	if end < 0 {
		return text
	}
	end += start
	return text[:start] + stripInstructionOmissions(text[start:end]) + text[end:]
}

func stripRequestInstructionOmissions(request *parsedResponsesRequest) error {
	if text, ok := decodeJSONString(request.fields["instructions"]); ok {
		if stripped := stripInstructionOmissions(text); stripped != text {
			request.fields["instructions"] = mustMarshalJSON(stripped)
		}
	}
	return rewriteRequestMessages(request, "instruction omissions", func(item responsesItem) ([]byte, bool, error) {
		transform := stripInstructionOmissions
		switch item.Role {
		case "system", "developer":
		case "user":
			transform = stripUserInstructionOmissions
		default:
			return nil, false, nil
		}
		textChanged := false
		content, _, err := transformResponsesTextContent(item.Content, func(text string) string {
			stripped := transform(text)
			textChanged = textChanged || stripped != text
			return stripped
		}, isResponsesInputTextPart)
		if err != nil {
			return nil, false, fmt.Errorf("strip instruction omissions: %w", err)
		}
		return content, textChanged, nil
	})
}
