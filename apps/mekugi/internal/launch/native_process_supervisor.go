package launch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/yusing/mekugi/modules/diagnostics/diagnostic"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

// A separate process must own the pipe and Wait: the launching renderer may
// disappear long before the server. No runtime or client resources live here.
func runNativeServerSupervisor(ctx context.Context, args []string) int {
	var status *os.File
	if os.Getenv(nativeServerSupervisorEnvironment) == "3" {
		syscall.CloseOnExec(3)
		status = os.NewFile(3, "server-diagnostics-status")
		defer status.Close()
	}
	_ = os.Unsetenv(nativeServerSupervisorEnvironment)
	executable, err := os.Executable()
	if err != nil {
		return 1
	}
	var log *diagnostic.ProcessLog
	if root, err := history.StateDirectory(); err == nil {
		log, _ = diagnostic.OpenProcessLog(context.WithoutCancel(ctx), filepath.Join(root, "server"))
	}
	if log != nil {
		defer log.Close()
	}
	command := exec.Command(executable, append([]string{"server"}, args...)...)
	command.Env = os.Environ()
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	command.WaitDelay = time.Second // Descendants retaining stderr cannot hide exit.
	if log != nil {
		command.Stderr = log
	}
	event := diagnostic.ProcessEvent{Event: "start", Time: time.Now().UTC(), Executable: executable, Build: mekugiVersion(), GoVersion: runtime.Version()}
	for i, arg := range args {
		if arg == "--socket" && i+1 < len(args) {
			event.Socket = args[i+1]
		}
	}
	if build, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range build.Settings {
			switch setting.Key {
			case "vcs.revision":
				event.Revision = setting.Value
			case "vcs.modified":
				event.Modified = setting.Value == "true"
			}
		}
	}
	if err := command.Start(); err != nil {
		event.Event, event.Error = "start_failed", err.Error()
		if log != nil {
			_ = log.Record(event)
		}
		return 1
	}
	event.PID = command.Process.Pid
	available := byte(0)
	if log != nil && log.Record(event) == nil {
		available = 1
	}
	if status != nil {
		_, _ = status.Write([]byte{available})
		_ = status.Close()
	}
	stop := context.AfterFunc(ctx, func() { _ = command.Process.Signal(syscall.SIGTERM) })
	defer stop()
	err = command.Wait()
	exitCode := command.ProcessState.ExitCode()
	if state, ok := command.ProcessState.Sys().(syscall.WaitStatus); ok && state.Signaled() {
		event.Signal = state.Signal().String()
		exitCode = 128 + int(state.Signal())
	}
	event.Event, event.Time, event.ExitCode = "exit", time.Now().UTC(), &exitCode
	if err != nil {
		event.Error = err.Error()
	}
	if log != nil {
		_ = log.Record(event)
	}
	return exitCode
}
