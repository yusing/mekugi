package launch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
)

const nativeServerSupervisorEnvironment = "MEKUGI_SERVER_SUPERVISOR"

// ensureNativeServer starts one detached server only when the peer-checked
// control endpoint is absent. Waiting belongs to the client; execution does not.
func ensureNativeServer(ctx context.Context, socket string) error {
	return sessionapi.EnsureControl(ctx, socket, func() error {
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		command := exec.Command(executable, "server", "--socket", socket)
		command.Env = append(serverEnvironment(os.Environ()), nativeServerSupervisorEnvironment+"=1")
		command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		status, ready, err := os.Pipe()
		if err == nil {
			defer status.Close()
			defer ready.Close()
			command.ExtraFiles = []*os.File{ready}
			command.Env = append(command.Env, nativeServerSupervisorEnvironment+"=3")
		}
		// Nil streams attach to the null device. The server never inherits the
		// client's terminal, descriptors, foreground process group or cancellation.
		if err := command.Start(); err != nil {
			return err
		}
		go func() { _ = command.Wait() }()
		if status == nil {
			fmt.Fprintln(os.Stderr, "mekugi: detached server diagnostic status unavailable")
			return nil
		}
		_ = ready.Close()
		stop := context.AfterFunc(ctx, func() { _ = status.Close() })
		defer stop()
		var result [1]byte
		n, _ := status.Read(result[:])
		if err := ctx.Err(); err != nil {
			return err
		}
		if n != 1 || result[0] != 1 {
			fmt.Fprintln(os.Stderr, "mekugi: detached server diagnostics unavailable")
		}
		return nil
	})
}
