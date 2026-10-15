package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/yusing/mekugi/apps/mekugi/internal/composition"
	"github.com/yusing/mekugi/internal/router"
)

func main() {
	os.Exit(run())
}

// Source: main.go:36:48 process signals and router exit behavior.
func run() (exitCode int) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(os.Args) > 1 && os.Args[1] == "mcp-bridge" {
		if len(os.Args) != 3 {
			fmt.Fprintln(os.Stderr, "mekugi: mcp-bridge requires one socket path")
			return 2
		}
		if err := router.RunMCPBridge(ctx, os.Args[2], os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "mekugi:", err)
			return 1
		}
		return 0
	}
	frontends, err := composition.Frontends(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mekugi:", err)
		return 1
	}
	defer func() {
		if err := frontends.Close(context.WithoutCancel(ctx)); err != nil {
			fmt.Fprintln(os.Stderr, "mekugi:", err)
			if exitCode == 0 {
				exitCode = 1
			}
		}
	}()
	if handled, exitCode := router.RunOwnedToolPluginWorker(
		frontends.History,
		ctx,
		os.Args[0],
		os.Args[1:],
		os.Stdin,
		os.Stdout,
		os.Stderr,
	); handled {
		return exitCode
	}

	if len(os.Args) > 1 && os.Args[1] == "inspect-sessions" {
		return router.RunSessionCorpusInspection(frontends.History, ctx, os.Args[2:], os.Stdout, os.Stderr)
	}
	if len(os.Args) > 1 && os.Args[1] == "replay-session" {
		return router.RunSessionUIReplay(frontends.History, ctx, os.Args[2:], os.Stdin, os.Stdout, os.Stderr)
	}
	if len(os.Args) > 1 && os.Args[1] == "inspect-session" {
		return router.RunSessionInspection(frontends.History, ctx, os.Args[2:], os.Stdout, os.Stderr)
	}
	if len(os.Args) > 1 && os.Args[1] == "inspect-storage" {
		return router.RunStorageInspection(frontends.History, ctx, os.Args[2:], os.Stdout, os.Stderr)
	}
	routerArgs, command, err := router.SplitCommand(os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		router.PrintUsage(os.Stdout)
		return 0
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "mekugi:", err)
		return 2
	}
	stop()
	return runWrap(frontends, routerArgs, command)
}
