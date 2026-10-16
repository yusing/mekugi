// Source: apps/mekugi/cmd/mekugi/main.go:1:86@[ebde9ad5]
package launch

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"syscall"

	launchoptions "github.com/yusing/mekugi/apps/mekugi/internal/launch/options"

	"github.com/yusing/mekugi/apps/mekugi/internal/frontends"
	"github.com/yusing/mekugi/contracts/command"

	"github.com/yusing/mekugi/libraries/mcptransport"

	"github.com/yusing/mekugi/apps/mekugi/internal/composition"
	"github.com/yusing/mekugi/apps/mekugi/internal/router"
)

// Source: main.go:36:48 process signals and router exit behavior.
// Run dispatches the process invocation and returns its exit status.
func Run() (exitCode int) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	worker, err := frontends.Recognize(os.Args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if worker == nil && len(os.Args) > 1 && os.Args[1] == "mcp-bridge" {
		if len(os.Args) != 3 {
			fmt.Fprintln(os.Stderr, "mekugi: mcp-bridge requires one socket path")
			return 2
		}
		if err := mcptransport.RelayStdio(ctx, os.Args[2], os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "mekugi:", err)
			return 1
		}
		return 0
	}
	operation := ""
	if len(os.Args) > 1 {
		operation = os.Args[1]
	}
	if worker != nil || slices.Contains([]string{"inspect-sessions", "replay-session", "inspect-session", "inspect-storage"}, operation) {
		runtime, err := composition.Frontends(ctx, false, "")
		if err != nil {
			fmt.Fprintln(os.Stderr, "mekugi:", err)
			return 1
		}
		defer func() {
			if err := runtime.Close(context.WithoutCancel(ctx)); err != nil {
				fmt.Fprintln(os.Stderr, "mekugi:", err)
				if exitCode == 0 {
					exitCode = 1
				}
			}
		}()
		if worker != nil {
			return worker.Run(runtime.History, runtime.Recovery, runtime.AgentMetrics, runtime.Commands, command.DedicatedHostProcess, ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
		}
		switch operation {
		case "inspect-sessions":
			return router.RunSessionCorpusInspection(runtime.Inspection, ctx, os.Args[2:], os.Stdout, os.Stderr)
		case "replay-session":
			return router.RunSessionUIReplay(runtime.Activity, runtime.Replay, ctx, os.Args[2:], os.Stdin, os.Stdout, os.Stderr)
		case "inspect-session":
			return router.RunSessionInspection(runtime.Inspection, runtime.Diagnostics, ctx, os.Args[2:], os.Stdout, os.Stderr)
		case "inspect-storage":
			return router.RunStorageInspection(runtime.History, runtime.Segments.Reports, ctx, os.Args[2:], os.Stdout, os.Stderr)
		}
	}
	routerArgs, command, err := launchoptions.SplitCommand(os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		launchoptions.PrintUsage(os.Stdout)
		return 0
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "mekugi:", err)
		return 2
	}
	stop()
	return runWrap(routerArgs, command)
}
