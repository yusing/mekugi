//go:build !unix

package query

import "os/exec"

// configureProcessGroup uses the platform's ordinary command cancellation.
func configureProcessGroup(_ *exec.Cmd) {}
