//go:build unix

package run

import (
	"os/exec"
	"syscall"
)

func mrunProcessExitCode(err *exec.ExitError) int {
	if status, ok := err.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal())
	}
	return err.ExitCode()
}
