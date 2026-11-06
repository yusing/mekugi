// Source: apps/mekugi/internal/router/composer_skill_attachment.go:63:116@[e9da429a] skill snapshot framing and budget.
package composer

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
)

func SkillFrames(name, path, content string) []string {
	header := fmt.Sprintf("Attached skill %q", name)
	if path != "" {
		header += fmt.Sprintf(" from %q", path)
	}
	return frameText(content, func(start, end, total int) string {
		return fmt.Sprintf("%s (UTF-8 bytes %d:%d of %d; skill instructions; %s):\n", header, start, end, total, attachments.ReadGuidance)
	})
}

// Skills use the file attachment store, budgets, framing and delivery. Only
// source resolution and the header differ; no second snapshot lifecycle exists.
func (d *Draft) SnapshotSkills(read func(name, path string) ([]byte, error)) {
	d.SnapshotSkillSources("", func(name, path string) ([]byte, bool, error) {
		data, err := read(name, path)
		return data, false, err
	})
}

// SnapshotSkillSources acquires nested files only for a live workspace. Retained
// skill snapshots use SnapshotSkills to frame already-acquired bytes without I/O.
func (d *Draft) SnapshotSkillSources(workspace string, read func(name, path string) ([]byte, bool, error)) {
	var frames []string
	seen := make(map[string]bool)
	files := make(map[string]attachmentFile)
	for _, attachment := range d.Attachments {
		retained, ok := attachments.Decode(attachment)
		if !ok {
			continue
		}
		frames = append(frames, retained...)
		for _, frame := range retained {
			if rest, ok := strings.CutPrefix(frame, "Attached file "); ok {
				if quoted, err := strconv.QuotedPrefix(rest); err == nil {
					path, _ := strconv.Unquote(quoted)
					if filepath.IsAbs(path) {
						prior := files[path]
						files[path] = attachmentFile{prior.body + attachmentBody([]string{frame}), attachments.MaxDepth + 1}
					}
				}
			}
			if name, path, _ := attachments.ParseSkillFrame(frame); name != "" {
				seen[name+"\x00"+path] = true
			}
		}
	}
	for _, skill := range d.Skills {
		key := skill.Name + "\x00" + skill.Path
		if seen[key] {
			continue
		}
		seen[key] = true
		header := fmt.Sprintf("Attached skill %q", skill.Name)
		if skill.Path != "" {
			header += fmt.Sprintf(" from %q", skill.Path)
		}
		data, managed, err := read(skill.Name, skill.Path)
		next := SkillFrames(skill.Name, skill.Path, string(data))
		before := len(frames)
		var more bool
		frames, more = d.appendAttachmentSnapshot(frames, next, header, err)
		if !more {
			break
		}
		if err == nil && workspace != "" {
			base := workspace
			if skill.Path != "" && !managed {
				base = filepath.Dir(skill.Path)
			}
			frames, more = d.appendNestedAttachments(frames, attachmentBody(frames[before:]), base, 1, files)
			if !more {
				break
			}
		}
	}
	if len(frames) > 0 {
		d.Attachments = []string{attachments.Encode(frames)}
	}
}
