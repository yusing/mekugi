// Source: apps/mekugi/internal/router/composer_skill_attachment.go:63:116@[e9da429a] skill snapshot framing and budget.
package composer

import (
	"fmt"

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
	var frames []string
	seen := make(map[string]bool)
	for _, attachment := range d.Attachments {
		retained, ok := attachments.Decode(attachment)
		if !ok {
			continue
		}
		frames = append(frames, retained...)
		for _, frame := range retained {
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
		data, err := read(skill.Name, skill.Path)
		next := SkillFrames(skill.Name, skill.Path, string(data))
		var more bool
		frames, more = d.appendAttachmentSnapshot(frames, next, header, err)
		if !more {
			break
		}
	}
	if len(frames) > 0 {
		d.Attachments = []string{attachments.Encode(frames)}
	}
}
