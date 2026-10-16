package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"syscall"

	"github.com/yusing/mekugi/libraries/mcptransport"

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
	worker, err := router.RecognizeToolWorker(os.Args[0])
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
		frontends, err := composition.Frontends(ctx, false)
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
		if worker != nil {
			return worker.Run(frontends.History, frontends.Recovery, frontends.AgentMetrics, frontends.Commands, ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
		}
		switch operation {
		case "inspect-sessions":
			return router.RunSessionCorpusInspection(frontends.Inspection, ctx, os.Args[2:], os.Stdout, os.Stderr)
		case "replay-session":
			return router.RunSessionUIReplay(frontends.Activity, frontends.Replay, ctx, os.Args[2:], os.Stdin, os.Stdout, os.Stderr)
		case "inspect-session":
			return router.RunSessionInspection(frontends.Inspection, frontends.Diagnostics, ctx, os.Args[2:], os.Stdout, os.Stderr)
		case "inspect-storage":
			return router.RunStorageInspection(frontends.History, frontends.Segments.Reports, ctx, os.Args[2:], os.Stdout, os.Stderr)
		}
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
	return runWrap(routerArgs, command)
}
