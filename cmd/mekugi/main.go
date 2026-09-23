package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/yusing/mekugi/internal/router"
)

func main() {
	os.Exit(run())
}

// Source: main.go:36:48 process signals and router exit behavior.
func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if handled, exitCode := router.RunOwnedToolPluginWorker(
		ctx,
		os.Args[0],
		os.Args[1:],
		os.Stdin,
		os.Stdout,
		os.Stderr,
	); handled {
		return exitCode
	}
	if len(os.Args) > 1 && os.Args[1] == "live-diff" {
		return router.RunLiveDiff(ctx, os.Args[2:], os.Stdin, os.Stdout, os.Stderr)
	}
	if len(os.Args) > 1 && os.Args[1] == "inspect-sessions" {
		return router.RunSessionCorpusInspection(ctx, os.Args[2:], os.Stdout, os.Stderr)
	}
	if len(os.Args) > 1 && os.Args[1] == "inspect-session" {
		return router.RunSessionInspection(ctx, os.Args[2:], os.Stdout, os.Stderr)
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
