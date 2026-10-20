//go:build unix

package toolplugin

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// ConfigureProcessGroup makes command cancellation terminate descendants that
// inherited the command's standard streams instead of leaving Wait blocked.
func ConfigureProcessGroup(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
