package router

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"time"
	"unicode/utf8"
)

type managedSkillOutput struct{ bytes.Buffer }

func (b *managedSkillOutput) Write(data []byte) (int, error) {
	if b.Len()+len(data) > fileAttachmentBudget/2 {
		return 0, fmt.Errorf("skill exceeds attachment limit")
	}
	return b.Buffer.Write(data)
}
func readManagedSkill(ctx context.Context, cwd string, environment []string, name string) ([]byte, error) {
	executable, err := managedSkillsExecutable(cwd, environment)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, executable, "get", "--codex", name)
	cmd.Dir, cmd.Env, cmd.WaitDelay = cwd, environment, time.Second
	var output managedSkillOutput
	cmd.Stdout = &output
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	if !utf8.Valid(output.Bytes()) {
		return nil, fmt.Errorf("skill output is not UTF-8")
	}
	return output.Bytes(), nil
}
func frameComposerSkillFromPath(name, path, content string) []string {
	header := fmt.Sprintf("Attached skill %q", name)
	if path != "" {
		header += fmt.Sprintf(" from %q", path)
	}
	return frameAttachmentText(content, func(start, end, total int) string {
		return fmt.Sprintf("%s (UTF-8 bytes %d:%d of %d; skill instructions):\n", header, start, end, total)
	})
}
func (u *appServerUI) snapshotDraftSkills(d *composerDraft) {
	d.snapshotSkillAttachments(u.session.cwd, u.skillEnvironment)
}

// Skills use the file attachment store, budgets, framing and delivery. Only
// source resolution and the header differ; no second snapshot lifecycle exists.
func (d *composerDraft) snapshotSkillAttachments(cwd string, environment []string) {
	var frames []string
	seen := make(map[string]bool)
	for _, attachment := range d.attachments {
		retained, ok := decodeFileAttachments(attachment)
		if !ok {
			continue
		}
		frames = append(frames, retained...)
		for _, frame := range retained {
			if name, path, _ := skillAttachmentFrame(frame); name != "" {
				seen[name+"\x00"+path] = true
			}
		}
	}
	for _, skill := range d.skills {
		key := skill.name + "\x00" + skill.path
		if seen[key] {
			continue
		}
		seen[key] = true
		header := fmt.Sprintf("Attached skill %q", skill.name)
		var data []byte
		var err error
		if skill.path != "" {
			header += fmt.Sprintf(" from %q", skill.path)
			if !filepath.IsAbs(skill.path) {
				err = fmt.Errorf("skill metadata requires an absolute path")
			} else {
				data, err = readComposerFile(skill.path)
			}
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			data, err = readManagedSkill(ctx, cwd, environment, skill.name)
			cancel()
		}
		next := frameComposerSkillFromPath(skill.name, skill.path, string(data))
		var more bool
		frames, more = d.appendAttachmentSnapshot(frames, next, header, err)
		if !more {
			break
		}
	}
	if len(frames) > 0 {
		d.attachments = []string{encodeFileAttachments(frames)}
	}
}
