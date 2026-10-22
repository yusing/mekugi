package launch

import (
	"context"
	"os"
	"os/exec"
	"syscall"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
)

// ensureNativeServer starts one detached server only when the peer-checked
// control endpoint is absent. Waiting belongs to the client; execution does not.
func ensureNativeServer(ctx context.Context, socket string) error {
	return sessionapi.EnsureControl(ctx, socket, func() error {
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		command := exec.Command(executable, "server", "--socket", socket)
		command.Env = serverEnvironment(os.Environ())
		command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		// Nil streams attach to the null device. The server never inherits the
		// client's terminal, descriptors, foreground process group or cancellation.
		if err := command.Start(); err != nil {
			return err
		}
		go func() { _ = command.Wait() }()
		return nil
	})
}
