//go:build unix

package claude

import (
	"os/exec"
	"syscall"
)

// The bridge and its SDK-owned runtime share a private process group. Graceful
// EOF is tried first; cancellation cannot leave the runtime behind the UI.
func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
