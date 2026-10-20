//go:build !unix

package router

import "os/exec"

func mrunProcessExitCode(err *exec.ExitError) int { return err.ExitCode() }
