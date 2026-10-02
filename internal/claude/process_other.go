//go:build !unix

package claude

import "os/exec"

func configureProcess(cmd *exec.Cmd) {}
