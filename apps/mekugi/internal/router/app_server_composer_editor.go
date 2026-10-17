package router

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

type composerEditorRequest struct {
	intent sessionpresentation.ComposerIntent
}

func (*composerEditorRequest) Error() string { return "open composer editor" }

// Called only after withRawPane has joined its input reader and restored the
// terminal. The next pane invocation re-enters raw mode and repaints the UI.
func (u *appServerUI) openComposerEditor(intent sessionpresentation.ComposerIntent, stdin, stdout *os.File) {
	// Leaving the alternate screen discards its contents. The next paint must
	// restore every row, including unchanged panes and failed editor launches.
	if u.shell != nil {
		u.shell.Frame.Rows = nil
	}
	if err := u.attached.presentation.AdmitAcquiredInput(intent, u.attached.conversation); err != nil {
		setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), err.Error(), true)
		return
	}
	file, err := os.CreateTemp("", "mekugi-draft-*.txt")
	if err != nil {
		setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "Open editor: "+err.Error(), true)
		return
	}
	path := file.Name()
	_, writeErr := file.WriteString(u.Text)
	if err = errors.Join(writeErr, file.Close()); err != nil {
		_ = os.Remove(path)
		setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "Open editor: "+err.Error(), true)
		return
	}
	err = runEditor(u.ctx, path, stdin, stdout)
	if err == nil {
		var data []byte
		data, err = os.ReadFile(path)
		if err == nil {
			err = u.applyEditorDraft(intent, string(data))
		}
	}
	if err != nil {
		setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), fmt.Sprintf("Editor: %v · draft preserved at %s", err, path), true)
		return
	}
	_ = os.Remove(path)
	setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "Draft updated from editor", false)
}

func runEditor(ctx context.Context, path string, stdin, stdout *os.File) error {
	editor := strings.TrimSpace(os.Getenv("EDITOR"))
	if editor == "" {
		editor = strings.TrimSpace(os.Getenv("VISUAL"))
	}
	if editor == "" {
		editor = "vi"
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// EDITOR is a user-owned shell command; the file path is a positional
	// argument, never interpolated into that command.
	cmd := exec.CommandContext(ctx, "sh", "-c", "exec "+editor+` "$1"`, "mekugi-editor", path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stdout
	return cmd.Run()
}

func (u *appServerUI) applyEditorDraft(intent sessionpresentation.ComposerIntent, text string) error {
	if err := u.attached.presentation.AdmitAcquiredInput(intent, u.attached.conversation); err != nil {
		return err
	}
	defer func() {
		u.picker.refreshPicker(&u.attached, u.shell, u.clock, u.Requests, u.Editor, u.ctx, u.skillEnvironment)
	}()
	u.Run = composer.NoRun
	if q := u.questions.Current(); q != nil && !q.Note {
		q.Selected = len(q.Choices)
	}
	if text == u.Text {
		return nil
	}
	if err := u.Editor.ApplyExternal(text); err != nil {
		return err
	}
	u.shell.Main.CursorColumn = nil
	setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "", false)
	pruneDraftImages(u.ownedImages, u.Editor, u.attached.conversation, &u.questions, &u.approvals, u.shell.Prompts, u.side)
	return nil
}
