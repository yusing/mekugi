package router

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/yusing/mekugi/modules/conversation/composer"
)

var errOpenComposerEditor = errors.New("open composer editor")

// Called only after withRawPane has joined its input reader and restored the
// terminal. The next pane invocation re-enters raw mode and repaints the UI.
func (u *appServerUI) openComposerEditor(stdin, stdout *os.File) {
	// Leaving the alternate screen discards its contents. The next paint must
	// restore every row, including unchanged panes and failed editor launches.
	if u.shell != nil {
		u.shell.paintedRows = nil
	}
	file, err := os.CreateTemp("", "mekugi-draft-*.txt")
	if err != nil {
		u.setNotice("Open editor: "+err.Error(), true)
		return
	}
	path := file.Name()
	_, writeErr := file.WriteString(u.Text)
	if err = errors.Join(writeErr, file.Close()); err != nil {
		_ = os.Remove(path)
		u.setNotice("Open editor: "+err.Error(), true)
		return
	}
	err = u.runEditor(path, stdin, stdout)
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

func (u *appServerUI) runEditor(path string, stdin, stdout *os.File) error {
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
	// EDITOR is a user-owned shell command; the file path is a positional
	// argument, never interpolated into that command.
	cmd := exec.CommandContext(ctx, "sh", "-c", "exec "+editor+` "$1"`, "mekugi-editor", path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stdout
	return cmd.Run()
}

func (u *appServerUI) applyEditorDraft(text string) error {
	defer u.refreshPicker()
	u.Run = composer.NoRun
	if q := u.currentQuestion(); q != nil && !q.Note {
		q.Selected = len(q.Choices)
	}
	if text == u.Text {
		return nil
	}
	if err := u.Editor.ApplyExternal(text); err != nil {
		return err
	}
	u.cursorColumn = nil
	u.setNotice("", false)
	u.pruneDraftImages()
	return nil
}
