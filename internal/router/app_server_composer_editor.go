package router

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

var errOpenComposerEditor = errors.New("open composer editor")

// Called only after withRawPane has joined its input reader and restored the
// terminal. The next pane invocation re-enters raw mode and repaints the UI.
func (u *appServerUI) openComposerEditor(stdin, stdout *os.File) {
	file, err := os.CreateTemp("", "mekugi-draft-*.txt")
	if err != nil {
		u.setNotice("Open editor: "+err.Error(), true)
		return
	}
	path := file.Name()
	_, writeErr := file.WriteString(u.draft)
	if err = errors.Join(writeErr, file.Close()); err != nil {
		_ = os.Remove(path)
		u.setNotice("Open editor: "+err.Error(), true)
		return
	}
	editor := strings.TrimSpace(os.Getenv("EDITOR"))
	if editor == "" {
		editor = strings.TrimSpace(os.Getenv("VISUAL"))
	}
	if editor == "" {
		editor = "vi"
	}
	ctx := u.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	// EDITOR is a user-owned shell command; the draft path is a positional
	// argument, never interpolated into that command.
	cmd := exec.CommandContext(ctx, "sh", "-c", "exec "+editor+` "$1"`, "mekugi-editor", path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stdout
	err = cmd.Run()
	if err == nil {
		var data []byte
		data, err = os.ReadFile(path)
		if err == nil {
			err = u.applyEditorDraft(string(data))
		}
	}
	if err != nil {
		u.setNotice(fmt.Sprintf("Editor: %v · draft preserved at %s", err, path), true)
		return
	}
	_ = os.Remove(path)
	u.setNotice("Draft updated from editor", false)
}

func (u *appServerUI) applyEditorDraft(text string) error {
	defer u.refreshPicker()
	u.run = runNone
	if q := u.currentQuestion(); q != nil && !q.note {
		q.selected = len(q.choices)
	}
	if text == u.draft {
		return nil
	}
	if !utf8.ValidString(text) {
		return errors.New("edited draft is not valid UTF-8")
	}
	var images []composerImage
	for _, image := range u.images {
		label := u.draft[image.start:image.end]
		if strings.Count(text, label) > 1 {
			return fmt.Errorf("attachment %s appears more than once", label)
		}
		if start := strings.Index(text, label); start >= 0 {
			image.start, image.end = start, start+len(label)
			images = append(images, image)
		}
	}
	var selections []composerSelection
	for _, selection := range u.selections {
		label := u.draft[selection.start:selection.end]
		if strings.Count(text, label) > 1 {
			return fmt.Errorf("selection %s appears more than once", label)
		}
		if start := strings.Index(text, label); start >= 0 {
			selection.start, selection.end = start, start+len(label)
			selections = append(selections, selection)
		}
	}
	slices.SortFunc(selections, func(a, b composerSelection) int { return a.start - b.start })
	var skills []composerSkill
	for _, skill := range u.skills {
		bindings := restoredSkillBindings(text, skill.name, skill.path)
		if len(bindings) == 1 {
			skills = append(skills, bindings[0])
		}
	}
	var files []composerFile
	next := make(map[string]int)
	for _, file := range u.files {
		label := u.draft[file.start:file.end]
		oldBefore, _ := utf8.DecodeLastRuneInString(u.draft[:file.start])
		oldAfter, _ := utf8.DecodeRuneInString(u.draft[file.end:])
		for offset := next[label]; offset < len(text); {
			index := strings.Index(text[offset:], label)
			if index < 0 {
				break
			}
			start := offset + index
			end := start + len(label)
			offset = end
			before, _ := utf8.DecodeLastRuneInString(text[:start])
			after, _ := utf8.DecodeRuneInString(text[end:])
			// Existing adjacent text belongs outside the atomic token. Keep it
			// across unrelated editor changes, but do not bind a newly extended
			// filename merely because its prefix matches the old label.
			if start > 0 && !unicode.IsSpace(before) && (file.start == 0 || before != oldBefore) ||
				end < len(text) && !unicode.IsSpace(after) && (file.end == len(u.draft) || after != oldAfter) {
				continue
			}
			file.start, file.end = start, end
			next[label] = end
			files = append(files, file)
			break
		}
	}
	slices.SortFunc(files, func(a, b composerFile) int { return a.start - b.start })
	u.recordDraft()
	u.files = files
	u.selections = selections
	u.skills = skills
	u.draft, u.images, u.cursorBack = text, images, 0
	u.cursorColumn = nil
	u.renumberImages()
	u.pruneDraftImages()
	return nil
}
