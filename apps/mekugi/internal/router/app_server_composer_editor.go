package router

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

type composerEditorRequest struct {
	intent sessionpresentation.ComposerIntent
}

func (*composerEditorRequest) Error() string { return "open composer editor" }

// Called only after withRawPane has joined its input reader and restored the
// terminal. The next pane invocation re-enters raw mode and repaints the UI.
func openComposerEditor(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, side **sessioncontrol.Side, owned *map[string]bool, environment []string, parent context.Context, intent sessionpresentation.ComposerIntent, stdin, stdout *os.File) {
	// Leaving the alternate screen discards its contents. The next paint must
	// restore every row, including unchanged panes and failed editor launches.
	if terminal != nil {
		terminal.Frame.Rows = nil
	}
	if err := attached.presentation.AdmitAcquiredInput(intent, attached.conversation); err != nil {
		setNotice(&attached.feedback, terminal, presentationTime(clock), err.Error(), true)
		return
	}
	file, err := os.CreateTemp("", "mekugi-draft-*.txt")
	if err != nil {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Open editor: "+err.Error(), true)
		return
	}
	path := file.Name()
	_, writeErr := file.WriteString((*editor).Text)
	if err = errors.Join(writeErr, file.Close()); err != nil {
		_ = os.Remove(path)
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Open editor: "+err.Error(), true)
		return
	}
	err = runEditor(parent, path, stdin, stdout)
	if err == nil {
		var data []byte
		data, err = os.ReadFile(path)
		if err == nil {
			err = applyEditorDraft(attached, terminal, control, picker, editor, questions, approvals, clock, side, owned, environment, parent, intent, string(data))
		}
	}
	if err != nil {
		setNotice(&attached.feedback, terminal, presentationTime(clock), fmt.Sprintf("Editor: %v · draft preserved at %s", err, path), true)
		return
	}
	_ = os.Remove(path)
	setNotice(&attached.feedback, terminal, presentationTime(clock), "Draft updated from editor", false)
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

func applyEditorDraft(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, side **sessioncontrol.Side, owned *map[string]bool, environment []string, parent context.Context, intent sessionpresentation.ComposerIntent, text string) error {
	if err := attached.presentation.AdmitAcquiredInput(intent, attached.conversation); err != nil {
		return err
	}
	defer func() {
		picker.refreshPicker(attached, terminal, clock, control.Requests, (*editor), parent, environment)
	}()
	(*editor).Run = composer.NoRun
	if q := questions.Current(); q != nil && !q.Note {
		q.Selected = len(q.Choices)
	}
	if text == (*editor).Text {
		return nil
	}
	if err := (*editor).ApplyExternal(text); err != nil {
		return err
	}
	terminal.Main.CursorColumn = nil
	setNotice(&attached.feedback, terminal, presentationTime(clock), "", false)
	pruneDraftImages((*owned), (*editor), attached.conversation, questions, approvals, terminal.Prompts, (*side))
	return nil
}
