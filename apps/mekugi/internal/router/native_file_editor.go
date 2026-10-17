package router

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/yusing/mekugi/libraries/pathdisplay"
	"github.com/yusing/mekugi/libraries/terminalui"
)

// Carries the selected file across raw-terminal restoration, before launching
// the editor. The capture producer resolves paths against their workspace.
type openFileEditor struct{ path string }

func (*openFileEditor) Error() string { return "open file editor" }

func (u *appServerUI) openSelectedFileEditor(path string, stdin, stdout *os.File) {
	u.shell.frame.Rows = nil
	if !filepath.IsAbs(path) {
		u.setNotice("Open editor: file has no absolute workspace path", true)
		return
	}
	info, err := os.Stat(path)
	if err == nil && !info.Mode().IsRegular() {
		err = fmt.Errorf("not a regular file: %s", path)
	}
	if err == nil {
		err = u.runEditor(path, stdin, stdout)
	}
	if err != nil {
		u.setNotice("Open editor: "+err.Error(), true)
		return
	}
	workspace := u.session.cwd
	if editor, ok := u.shell.Auxiliary.(terminalui.PaneEditor); ok {
		workspace = editor.Workspace()
	}
	u.setNotice("Editor closed: "+pathdisplay.ForWorkspace(workspace, path), false)
}
