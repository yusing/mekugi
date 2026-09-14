package router

import (
	"encoding/json"
	"maps"
	"strings"
)

// Observe complete calls, not argument deltas. This is a user-only description
// of a request, never a second executable call or a claim of tool success.
func (t *mekugiResponseTransform) collectSubagentToolCall(item map[string]json.RawMessage) {
	if !t.subagentTurn {
		return
	}
	kind := jsonString(item, "type")
	if !strings.HasSuffix(kind, "_call") {
		return
	}
	if status := jsonString(item, "status"); status != "" && status != "completed" && status != "failed" {
		return
	}
	id := jsonString(item, "id")
	if id == "" || len(id) > maxCommentaryPublicationBytes-len("tool-call\x00") {
		return
	}
	name := jsonString(item, "name")
	if name == "" {
		name = strings.TrimSuffix(kind, "_call")
	}
	name = qualifiedToolName(jsonString(item, "namespace"), name)
	if len(name) > maxCommentaryPublicationBytes {
		return
	}
	var history *mekugiHistory
	callID := jsonString(item, "call_id")
	if retained, exists := t.local[callID]; exists {
		history = &retained
	} else if retained, exists := t.visible[callID]; exists {
		history = &retained
	}
	// Recovery changes execution, not the provider's retained call identity.
	// Use the translated owner for presentation only after translation succeeded.
	if history != nil && history.ToolName == "shell" && history.PluginID == builtinToolsPluginID &&
		!history.ReplayCarrier && jsonString(history.UpstreamItem, "name") == t.codeModeToolName {
		item = maps.Clone(item)
		item["name"] = mustMarshalJSON("shell")
		delete(item, "namespace")
		name = "shell"
	}
	displays := subagentToolActivityTexts(item, name, t.shellActivityDisplay)
	for _, text := range displays {
		t.proxy.activity.collect(t.threadID, "tool-call\x00"+id, "tool", text)
	}
}

func toolActivityCode(input string) string {
	if !strings.ContainsAny(input, "\r\n") {
		return commentaryCode(input)
	}
	fence := "```"
	for strings.Contains(input, fence) {
		fence += "`"
	}
	return fence + "\n" + input + "\n" + fence
}

// Collapse adjacent action headings, keeping each detail (and fence) intact.
// Blank lines within source code are not operation boundaries.
func toolActivityGroup(author, text string) string {
	var blocks []string
	var lines []string
	fence := ""
	for line := range strings.SplitSeq(text, "\n") {
		if line == "" && fence == "" {
			if len(lines) > 0 {
				blocks = append(blocks, strings.Join(lines, "\n"))
				lines = nil
			}
			continue
		}
		lines = append(lines, line)
		if fence != "" {
			if line == fence {
				fence = ""
			}
		} else if delimiter, ok := toolActivityFenceDelimiter(line); ok {
			fence = delimiter
		}
	}
	if len(lines) > 0 {
		blocks = append(blocks, strings.Join(lines, "\n"))
	}
	var actions []string
	previous := ""
	for _, block := range blocks {
		heading, detail, _ := strings.Cut(block, "\n")
		separator := "\n"
		// These classified operations place their operand on the heading line.
		for _, label := range []string{"Read", "Skill Read", "Skill Reference Read", "List", "Search", "Inspect", "Write", "Edit", "Delete", "Move", "Run"} {
			if operand, ok := strings.CutPrefix(heading, label+" "); ok && strings.HasPrefix(operand, "`") {
				heading = label
				if detail != "" {
					operand += "\n" + detail
				}
				detail = operand
				separator = " "
				break
			}
		}
		if heading == previous && len(actions) > 0 {
			if detail != "" {
				if strings.Contains(actions[len(actions)-1], "\n") {
					separator = "\n"
				}
				actions[len(actions)-1] += separator + detail
			}
		} else {
			actions = append(actions, block)
			previous = heading
		}
	}
	if len(actions) <= 1 {
		return author + strings.Join(actions, "")
	}
	return "In " + strings.TrimSuffix(strings.TrimPrefix(author, "["), "] ") + "\n\n" + toolActivityNested(strings.Join(actions, "\n\n"))
}

// Indent continuation lines so fenced source stays inside its list item.
// Blank lines outside a fence separate classified operations from one call.
func toolActivityNested(text string) string {
	var out strings.Builder
	fence := ""
	newItem := true
	for line := range strings.SplitSeq(text, "\n") {
		if line == "" && fence == "" {
			out.WriteString("\n")
			newItem = true
			continue
		}
		if out.Len() > 0 {
			out.WriteString("\n")
		}
		if newItem {
			out.WriteString("- ")
			newItem = false
		} else {
			out.WriteString("  ")
		}
		out.WriteString(line)
		if fence != "" {
			if line == fence {
				fence = ""
			}
		} else if delimiter, ok := toolActivityFenceDelimiter(line); ok {
			fence = delimiter
		}
	}
	return out.String()
}

func toolActivityFenceDelimiter(line string) (string, bool) {
	ticks := 0
	for ticks < len(line) && line[ticks] == '`' {
		ticks++
	}
	if ticks < 3 || strings.ContainsRune(line[ticks:], '`') {
		return "", false
	}
	return line[:ticks], true
}
