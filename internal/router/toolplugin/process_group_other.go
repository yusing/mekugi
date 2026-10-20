//go:build !unix

package toolplugin

import "os/exec"

// ConfigureProcessGroup uses the platform's ordinary command cancellation.
func ConfigureProcessGroup(_ *exec.Cmd) {}
