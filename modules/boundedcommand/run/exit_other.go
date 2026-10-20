//go:build !unix

package run

import "os/exec"

func mrunProcessExitCode(err *exec.ExitError) int { return err.ExitCode() }
