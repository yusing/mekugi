package moduletest

import (
	"fmt"
	"html"
	"strings"
	"unicode/utf8"

	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
)

// FileFrames builds retained Go attachment fixtures, not live source reads.
func FileFrames(path, content string) []string {
	return frameText(content, func(start, end, total int) string {
		return fmt.Sprintf("Attached file %q (UTF-8 bytes %d:%d of %d; file content, not a separate request; %s):\n", path, start, end, total, attachments.ReadGuidance)
	})
}

// SkillFrames builds retained skill fixtures.
func SkillFrames(name, path, content string) []string {
	header := fmt.Sprintf("Attached skill %q", name)
	if path != "" {
		header += fmt.Sprintf(" from %q", path)
	}
	return frameText(content, func(start, end, total int) string {
		return fmt.Sprintf("%s (UTF-8 bytes %d:%d of %d; skill instructions; %s):\n", header, start, end, total, attachments.ReadGuidance)
	})
}

func frameText(content string, header func(start, end, total int) string) []string {
	var frames []string
	for offset := 0; offset < len(content) || len(frames) == 0; {
		end := min(offset+attachments.Chunk, len(content))
		if end < len(content) {
			if newline := strings.LastIndexByte(content[offset:end], '\n'); newline >= 0 {
				end = offset + newline + 1
			} else {
				for !utf8.RuneStart(content[end]) {
					end--
				}
			}
		}
		frames = append(frames, header(offset, end, len(content))+content[offset:end])
		offset = end
		if offset == len(content) {
			break
		}
	}
	return frames
}

// IdentifyAttachments stamps already-retained fixture envelopes.
func IdentifyAttachments(values *[]string, submission string) error {
	for i, text := range *values {
		snapshot, ok := attachments.DecodeSnapshot(text)
		if !ok {
			continue
		}
		snapshot.ID = fmt.Sprintf("%s/%d", submission, i)
		(*values)[i] = snapshot.Encode()
	}
	return nil
}

// WithLegacyAttachments builds the old retained wire form for history consumers.
func WithLegacyAttachments(input []map[string]any, values []string) []map[string]any {
	if len(values) == 0 {
		return input
	}
	var original []map[string]any
	for _, part := range input {
		text, _ := part["text"].(string)
		if !strings.HasPrefix(text, "\n\n---\nUser selections:\n") {
			original = append(original, part)
		}
	}
	input = original
	for _, text := range values {
		input = append(input, map[string]any{"type": "text", "text": text})
	}
	return input
}

// SelectionFrames builds bounded retained selection fixtures.
func SelectionFrames(label, source, body string) []string {
	frames := frameText(body, func(_, _, _ int) string { return "" })
	for i := range frames {
		frames[i] = FormatSelection(label, source, frames[i])
	}
	return frames
}

// Source: libraries/modelprotocol/attachments/selection.go:18:40@[8e65cb769]
// FormatSelection builds retained Go selection markup, not current input.
func FormatSelection(label, source, body string) string {
	header := `<selected-text label="` + html.EscapeString(label) + `"`
	if source != "" {
		header += ` source="` + html.EscapeString(source) + `"`
	}
	header = strings.NewReplacer("\n", "&#10;", "\r", "&#13;", "\t", "&#9;").Replace(header)
	header += ">\nQuoted from the user's screen, not a separate request:\n"
	if label == "[Selected diff]" || strings.HasPrefix(label, "[Selected diff ") {
		longest, run := 0, 0
		for _, r := range body {
			if r == '`' {
				run++
				longest = max(longest, run)
			} else {
				run = 0
			}
		}
		fence := strings.Repeat("`", max(3, longest+1))
		body = fence + "diff\n" + body + "\n" + fence
	}
	return header + body + "\n</selected-text>"
}
