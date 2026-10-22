package launch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"

	"github.com/yusing/mekugi/libraries/pathdisplay"
	"github.com/yusing/mekugi/libraries/terminalui"
)

// Carries the selected file across raw-terminal restoration, before launching
// the editor. The capture producer resolves paths against their workspace.
type openFileEditor struct{ path string }

func (*openFileEditor) Error() string { return "open file editor" }

func openSelectedFileEditor(attached *attachedPresentation, terminal *terminalsession.Attachment, clock func() time.Time, parent context.Context, path string, stdin, stdout *os.File) {
	terminal.Frame.Rows = nil
	if !filepath.IsAbs(path) {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Open editor: file has no absolute workspace path", true)
		return
	}
	info, err := os.Stat(path)
	if err == nil && !info.Mode().IsRegular() {
		err = fmt.Errorf("not a regular file: %s", path)
	}
	if err == nil {
		err = runEditor(parent, path, stdin, stdout)
	}
	if err != nil {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Open editor: "+err.Error(), true)
		return
	}
	workspace := attached.cwd
	if editor, ok := terminal.Auxiliary.(terminalui.PaneEditor); ok {
		workspace = editor.Workspace()
	}
	setNotice(&attached.feedback, terminal, presentationTime(clock), "Editor closed: "+pathdisplay.ForWorkspace(workspace, path), false)
}
