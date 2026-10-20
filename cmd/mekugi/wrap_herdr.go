package main

import (
	"os"
	"syscall"
)

// Herdr sees the wrapper's foreground job, not the app-server child.
// Its documented wrapper hint is read from the process's initial environment;
// Setenv alone is not visible in Linux /proc/PID/environ. Re-exec before any
// router or Codex startup, preserving PID, process group, arguments and stdio.
// No Herdr executable, socket API, or pane-management behavior is involved.
func exposeHerdrCodex() error {
	if os.Getenv("HERDR_ENV") != "1" || os.Getenv("HERDR_AGENT") == "codex" {
		return nil
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.Setenv("HERDR_AGENT", "codex"); err != nil {
		return err
	}
	return syscall.Exec(executable, os.Args, os.Environ())
}
