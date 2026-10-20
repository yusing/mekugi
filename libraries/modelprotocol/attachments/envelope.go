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
const snapshotPrefix = "<mekugi-file-attachments-v2>\n"
const snapshotSuffix = "\n</mekugi-file-attachments-v2>"
const ReceiptPrefix = "attachment\x00"

const ReadGuidance = "use supplied content; reread only for edits or missing/newer content"

const (
	Budget = 192 << 10 // Encoded envelope bytes, including JSON escaping.
	Chunk  = 24 << 10  // Raw UTF-8 bytes per framed model message.
)

func Encode(frames []string) string {
	data, _ := json.Marshal(frames)
	return Prefix + string(data) + Suffix
}

// Snapshot retains submission identity independently of frame contents. Legacy
// envelopes have no identity and must not be correlated by content or position.
type Snapshot struct {
	ID     string   `json:"id"`
	Frames []string `json:"frames"`
}

func (s Snapshot) Encode() string {
	if s.ID == "" {
		return Encode(s.Frames)
	}
	data, _ := json.Marshal(&s)
	return snapshotPrefix + string(data) + snapshotSuffix
}

func Decode(text string) ([]string, bool) {
	snapshot, ok := DecodeSnapshot(text)
	return snapshot.Frames, ok
}

func DecodeSnapshot(text string) (Snapshot, bool) {
	var snapshot Snapshot
	if len(text) > Budget {
		return snapshot, false
	}
	switch {
	case strings.HasPrefix(text, Prefix) && strings.HasSuffix(text, Suffix):
		if json.Unmarshal([]byte(text[len(Prefix):len(text)-len(Suffix)]), &snapshot.Frames) != nil {
			return Snapshot{}, false
		}
	case strings.HasPrefix(text, snapshotPrefix) && strings.HasSuffix(text, snapshotSuffix):
		if json.Unmarshal([]byte(text[len(snapshotPrefix):len(text)-len(snapshotSuffix)]), &snapshot) != nil || snapshot.ID == "" || len(snapshot.ID) > 256 || strings.ContainsAny(snapshot.ID, "\x00\r\n") {
			return Snapshot{}, false
		}
	default:
		return snapshot, false
	}
	if len(snapshot.Frames) == 0 {
		return Snapshot{}, false
	}
	for _, frame := range snapshot.Frames {
		if len(frame) > Chunk+4096 || !strings.HasPrefix(frame, "Attached file ") && !SelectionFrame(frame) && !SkillFrame(frame) {
			return Snapshot{}, false
		}
	}
	return snapshot, true
}

// ReceiptID identifies one file or skill within a submitted envelope. Chunks
// share this identity; different envelopes never share it, even for equal bytes.
func (s Snapshot) ReceiptID(frame string) string {
	if s.ID == "" {
		return ""
	}
	var key string
	if name, _, rest := ParseSkillFrame(frame); name != "" && strings.HasPrefix(rest, " (UTF-8 bytes ") {
		key = strings.TrimSuffix(frame, rest)
	} else if rest, ok := strings.CutPrefix(frame, "Attached file "); ok {
		quoted, err := strconv.QuotedPrefix(rest)
		if err != nil || !strings.HasPrefix(rest[len(quoted):], " (UTF-8 bytes ") {
			return ""
		}
		key = "Attached file " + quoted
	}
	if key == "" {
		return ""
	}
	data, _ := json.Marshal([]string{s.ID, key})
	return ReceiptPrefix + string(data)
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
