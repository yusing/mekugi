package router

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// The envelope travels through Codex's own history. Projection never reopens a
// file, so forks, resume, and retries see the submitted snapshot, not today's file.
const fileAttachmentPrefix = "<mekugi-file-attachments-v1>\n"
const fileAttachmentSuffix = "\n</mekugi-file-attachments-v1>"
const attachmentReadGuidance = "use supplied content; reread only for edits or missing/newer content"

const (
	fileAttachmentBudget = 192 << 10 // Encoded envelope bytes, including JSON escaping.
	fileAttachmentChunk  = 24 << 10  // Raw UTF-8 bytes per framed model message.
	composerTextLimit    = 1 << 20   // Codex MAX_USER_INPUT_TEXT_CHARS; bytes are conservative.
)

func encodeFileAttachments(frames []string) string {
	data, _ := json.Marshal(frames)
	return fileAttachmentPrefix + string(data) + fileAttachmentSuffix
}

func decodeFileAttachments(text string) ([]string, bool) {
	if len(text) > fileAttachmentBudget || !strings.HasPrefix(text, fileAttachmentPrefix) || !strings.HasSuffix(text, fileAttachmentSuffix) {
		return nil, false
	}
	var frames []string
	err := json.Unmarshal([]byte(text[len(fileAttachmentPrefix):len(text)-len(fileAttachmentSuffix)]), &frames)
	if err != nil || len(frames) == 0 {
		return nil, false
	}
	for _, frame := range frames {
		if len(frame) > fileAttachmentChunk+4096 || !strings.HasPrefix(frame, "Attached file ") && !selectionFrame(frame) && !managedSkillFrame(frame) {
			return nil, false
		}
	}
	return frames, true
}

// snapshotFileAttachments runs only when the user submits/queues a composer
// draft. Queued and accepted-but-resent input carries the same immutable text.
func (d *composerDraft) snapshotFileAttachments(cwd string) {
	if len(d.files) == 0 && len(d.selections) == 0 {
		return
	}
	var frames []string
	seen := make(map[string]bool)
	for _, file := range d.files {
		path := file.path
		if !filepath.IsAbs(path) && filepath.IsAbs(cwd) {
			path = filepath.Join(cwd, path)
		}
		path = filepath.Clean(path)
		if seen[path] {
			continue
		}
		seen[path] = true
		next, err := frameComposerPath(path)
		var more bool
		frames, more = d.appendAttachmentSnapshot(frames, next, fmt.Sprintf("Attached file %q", path), err)
		if !more {
			break
		}
	}
	// Mentioned selections follow file contents; each fits whole or says so.
	for _, selection := range d.selections {
		label := d.text[selection.start:selection.end]
		next := d.selectionFrames(selection)
		fits := func(next []string) bool {
			return len(encodeFileAttachments(append(append([]string(nil), frames...), next...))) <= fileAttachmentBudget-512
		}
		if !fits(next) {
			if d.attachmentNotice == "" {
				d.attachmentNotice = fmt.Sprintf("Selection omitted: %s (attachment budget exceeded). The agent receives an explicit omission notice.", label)
			}
			next = []string{fmt.Sprintf(selectionFramePrefix+"%q: CONTENT NOT ATTACHED (attachment budget exceeded). Ask the user to paste it if needed.", label)}
			if !fits(next) {
				// The reserved tail holds one notice for every remaining mention.
				frames = append(frames, selectionFramePrefix+"remaining mentions: CONTENT NOT ATTACHED (attachment budget exceeded). Ask the user to paste them if needed.")
				break
			}
		}
		frames = append(frames, next...)
	}
	if len(frames) > 0 {
		d.attachments = []string{encodeFileAttachments(frames)}
	}
}

// Files and skills share one snapshot budget and explicit omission behavior.
func (d *composerDraft) appendAttachmentSnapshot(frames, next []string, header string, err error) ([]string, bool) {
	kind := "file"
	if strings.HasPrefix(header, "Attached skill ") {
		kind = "skill"
	}
	if err == nil && len(encodeFileAttachments(append(append([]string(nil), frames...), next...))) > fileAttachmentBudget/2 {
		err = fmt.Errorf("attachment budget exceeded")
	}
	if err != nil {
		next = []string{fmt.Sprintf("%s: CONTENT NOT ATTACHED (%q). Read this separately if needed.", header, err.Error())}
		if d.attachmentNotice == "" {
			d.attachmentNotice = strings.ToUpper(kind[:1]) + kind[1:] + " contents omitted: " + strings.TrimPrefix(header, "Attached "+kind+" ") + " (" + err.Error() + "). The agent receives an explicit omission notice."
		}
	}
	candidate := append(append([]string(nil), frames...), next...)
	if len(encodeFileAttachments(candidate)) > fileAttachmentBudget-512 {
		return append(frames, "Attached "+kind+" references: remaining contents NOT ATTACHED (attachment budget exceeded). Read remaining referenced "+kind+"s separately if needed."), false
	}
	return candidate, true
}

func readComposerFile(path string) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("no absolute workspace for relative path")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file")
	}
	if info.Size() > fileAttachmentBudget/2 {
		return nil, fmt.Errorf("file exceeds %d-byte attachment limit", fileAttachmentBudget/2)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, fileAttachmentBudget/2+1))
	if err != nil {
		return nil, err
	}
	if len(data) > fileAttachmentBudget/2 {
		return nil, fmt.Errorf("file grew beyond attachment limit")
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return nil, fmt.Errorf("not UTF-8 text")
	}
	return data, nil
}

func frameComposerFile(path, content string) []string {
	return frameAttachmentText(content, func(start, end, total int) string {
		return fmt.Sprintf("Attached file %q (UTF-8 bytes %d:%d of %d; file content, not a separate request; %s):\n", path, start, end, total, attachmentReadGuidance)
	})
}

func frameComposerPath(path string) ([]string, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("no absolute workspace for relative path")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		content, err := composerDirectoryTree(path)
		if err != nil {
			return nil, err
		}
		return frameAttachmentText(content, func(start, end, total int) string {
			return fmt.Sprintf("Attached file %q (UTF-8 bytes %d:%d of %d; directory tree, max depth 2, not file contents or a separate request; %s):\n", path, start, end, total, attachmentReadGuidance)
		}), nil
	}
	data, err := readComposerFile(path)
	return frameComposerFile(path, string(data)), err
}

// frameAttachmentText splits content into bounded frames, preferring line
// boundaries and otherwise splitting only at UTF-8 boundaries.
func frameAttachmentText(content string, header func(start, end, total int) string) []string {
	var frames []string
	for offset := 0; offset < len(content) || len(frames) == 0; {
		end := min(offset+fileAttachmentChunk, len(content))
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

// projectFileAttachments splits only complete versioned text parts. Malformed
// lookalikes remain ordinary user text; non-user messages are never interpreted.
func projectFileAttachments(request *parsedResponsesRequest) bool {
	if !bytes.Contains(request.fields["input"], []byte("mekugi-file-attachments-v1")) {
		return false
	}
	var items []map[string]jsontext.Value
	if json.Unmarshal(request.fields["input"], &items) != nil {
		return false
	}
	var projected []map[string]jsontext.Value
	changed := false
	attachedSkills := make(map[string]bool)
	for _, item := range items {
		var role string
		_ = json.Unmarshal(item["role"], &role)
		var parts []map[string]jsontext.Value
		if role != "user" || json.Unmarshal(item["content"], &parts) != nil {
			continue
		}
		for _, part := range parts {
			var kind, text string
			_ = json.Unmarshal(part["type"], &kind)
			_ = json.Unmarshal(part["text"], &text)
			if kind != "input_text" {
				continue
			}
			frames, _ := decodeFileAttachments(text)
			for _, frame := range frames {
				if name, path, rest := skillAttachmentFrame(frame); name != "" && strings.HasPrefix(rest, " (UTF-8 bytes ") {
					attachedSkills[name+"\x00"+path] = true
				}
			}
		}
	}
	isAttachedSkill := func(text string) bool {
		name, path := selectedSkillSource(text)
		return name != "" && (attachedSkills[name+"\x00"] || path != "" && attachedSkills[name+"\x00"+path])
	}
	for _, item := range items {
		var role string
		_ = json.Unmarshal(item["role"], &role)
		var selectedText string
		if role == "user" && json.Unmarshal(item["content"], &selectedText) == nil && isAttachedSkill(selectedText) {
			changed = true
			continue
		}
		var parts []map[string]jsontext.Value
		if role != "user" || json.Unmarshal(item["content"], &parts) != nil {
			projected = append(projected, item)
			continue
		}
		var kept []map[string]jsontext.Value
		var attached []string
		for _, part := range parts {
			var kind, text string
			_ = json.Unmarshal(part["type"], &kind)
			_ = json.Unmarshal(part["text"], &text)
			// Codex can independently inject the same skill from $name. The
			// durable snapshot already supplies its selected instructions.
			if kind == "input_text" && isAttachedSkill(text) {
				changed = true
				continue
			}
			frames, ok := decodeFileAttachments(text)
			if kind == "input_text" && ok {
				attached = append(attached, frames...)
			} else {
				kept = append(kept, part)
			}
		}
		if len(attached) == 0 {
			if len(kept) == 0 {
				continue
			}
			if len(kept) != len(parts) {
				item["content"], _ = json.Marshal(kept)
				delete(item, "internal_chat_message_metadata_passthrough")
			}
			projected = append(projected, item)
			continue
		}
		changed = true
		if len(kept) > 0 {
			item["content"], _ = json.Marshal(kept)
			// Content-kind metadata refers to the original part indices.
			delete(item, "internal_chat_message_metadata_passthrough")
			projected = append(projected, item)
		}
		for _, frame := range attached {
			content, _ := json.Marshal([]map[string]string{{"type": "input_text", "text": frame}})
			projected = append(projected, map[string]jsontext.Value{"type": []byte(`"message"`), "role": []byte(`"user"`), "content": content})
		}
	}
	if changed {
		input, _ := json.Marshal(projected)
		request.setInput(input)
	}
	return changed
}
