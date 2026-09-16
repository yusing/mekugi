package router

import "context"

type routerShellCommand uint8

const (
	routerShellCommandHelp routerShellCommand = iota
	routerShellCommandRead
	routerShellCommandChanges
	routerShellCommandRun
)

func classifyRouterShellCommand(name string) (routerShellCommand, bool) {
	switch name {
	case "hhelp":
		return routerShellCommandHelp, true
	case "hread":
		return routerShellCommandRead, true
	case "hchanges":
		return routerShellCommandChanges, true
	case "hrun":
		return routerShellCommandRun, true
	default:
		return 0, false
	}
}

func isRouterShellCommand(name string) bool {
	_, ok := classifyRouterShellCommand(name)
	return ok
}

func executeRouterShellCommand(
	ctx context.Context,
	manifest toolWorkerManifest,
	runtimeRoot string,
	command []string,
	terminalShell bool,
) (bool, error) {
	kind, ok := classifyRouterShellCommand(command[0])
	if !ok {
		return false, nil
	}
	arguments := command[1:]
	switch kind {
	case routerShellCommandHelp:
		return true, executeHHelp(ctx, arguments)
	case routerShellCommandRead:
		return true, executeHRead(ctx, manifest, runtimeRoot, arguments)
	case routerShellCommandChanges:
		return true, executeHChanges(ctx, manifest, runtimeRoot, arguments)
	case routerShellCommandRun:
		return true, executeHRun(ctx, manifest, runtimeRoot, arguments, terminalShell)
	default:
		panic("unreachable router shell command")
	}
}
