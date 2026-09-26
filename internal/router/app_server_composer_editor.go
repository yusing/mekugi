package router

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"unicode/utf8"
)

var errOpenComposerEditor = errors.New("open composer editor")

// Called only after withRawPane has joined its input reader and restored the
// terminal. The next pane invocation re-enters raw mode and repaints the UI.
func (u *appServerUI) openComposerEditor(stdin, stdout *os.File) {
	file, err := os.CreateTemp("", "mekugi-draft-*.txt")
	if err != nil {
		u.status, u.alert = "Open editor: "+err.Error(), true
		return
	}
	path := file.Name()
	_, writeErr := file.WriteString(u.draft)
	if err = errors.Join(writeErr, file.Close()); err != nil {
		_ = os.Remove(path)
		u.status, u.alert = "Open editor: "+err.Error(), true
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
		u.status, u.alert = fmt.Sprintf("Editor: %v · draft preserved at %s", err, path), true
		return
	}
	_ = os.Remove(path)
	u.status, u.alert = "Draft updated from editor", false
}

func (u *appServerUI) applyEditorDraft(text string) error {
	u.typing = false
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
	u.recordDraft()
	u.draft, u.images, u.cursorBack = text, images, 0
	u.cursorColumn = nil
	u.renumberImages()
	u.pruneDraftImages()
	return nil
}
