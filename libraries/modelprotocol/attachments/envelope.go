// Package attachments encodes submitted snapshots carried in host history.
// Source: internal/router/composer_file_attachment.go:15:47@b9a7b3fe;
// internal/router/app_server_text_spans.go:40:74@b9a7b3fe;
// internal/router/composer_selection.go:124:129@b9a7b3fe.
package attachments

import (
	json "encoding/json/v2"
	"strconv"
	"strings"
)

// The envelope travels through Codex's own history. Projection never reopens a
// file, so forks, resume, and retries see the submitted snapshot, not today's file.
const Prefix = "<mekugi-file-attachments-v1>\n"
const Suffix = "\n</mekugi-file-attachments-v1>"
const ReadGuidance = "use supplied content; reread only for edits or missing/newer content"

const (
	Budget = 192 << 10 // Encoded envelope bytes, including JSON escaping.
	Chunk  = 24 << 10  // Raw UTF-8 bytes per framed model message.
)

func Encode(frames []string) string {
	data, _ := json.Marshal(frames)
	return Prefix + string(data) + Suffix
}

func Decode(text string) ([]string, bool) {
	if len(text) > Budget || !strings.HasPrefix(text, Prefix) || !strings.HasSuffix(text, Suffix) {
		return nil, false
	}
	var frames []string
	err := json.Unmarshal([]byte(text[len(Prefix):len(text)-len(Suffix)]), &frames)
	if err != nil || len(frames) == 0 {
		return nil, false
	}
	for _, frame := range frames {
		if len(frame) > Chunk+4096 || !strings.HasPrefix(frame, "Attached file ") && !SelectionFrame(frame) && !SkillFrame(frame) {
			return nil, false
		}
	}
	return frames, true
}

func SkillFrame(frame string) bool {
	if frame == "Attached skill references: remaining contents NOT ATTACHED (attachment budget exceeded). Read remaining referenced skills separately if needed." {
		return true
	}
	if name, _, _ := ParseSkillFrame(frame); name != "" {
		return true
	}
	return false
}

func ParseSkillFrame(frame string) (name, path, rest string) {
	rest, ok := strings.CutPrefix(frame, "Attached skill ")
	if !ok {
		return "", "", ""
	}
	quoted, err := strconv.QuotedPrefix(rest)
	if err != nil {
		return "", "", ""
	}
	name, _ = strconv.Unquote(quoted)
	rest = rest[len(quoted):]
	if source, ok := strings.CutPrefix(rest, " from "); ok {
		quoted, err = strconv.QuotedPrefix(source)
		if err != nil {
			return "", "", ""
		}
		path, _ = strconv.Unquote(quoted)
		rest = source[len(quoted):]
	}
	if !strings.HasPrefix(rest, " (UTF-8 bytes ") && !strings.HasPrefix(rest, ": CONTENT NOT ATTACHED (") {
		return "", "", ""
	}
	return name, path, rest
}

// SelectionPrefix starts every selection frame, including omissions.
const SelectionPrefix = "Selected text for "

func SelectionFrame(frame string) bool {
	return strings.HasPrefix(frame, SelectionPrefix)
}
