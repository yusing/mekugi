package router

import (
	"context"
	"slices"
	"strings"

	"github.com/yusing/mekugi/internal/router/toolplugin"
	"mvdan.cc/sh/v3/interp"
)

// Availability uses the current command's PATH and cwd, including changes
// inside the script. The snapshotted plugin owns candidates and argv mappings.
func routeShellCommand(ctx context.Context, manifest toolWorkerManifest, runtimeRoot string, arguments []string, handler interp.HandlerContext) []string {
	policy := manifest.CommandRouting
	if policy == nil || len(arguments) == 0 || !slices.Contains(policy.Commands, arguments[0]) || policy.Executable == "" {
		return arguments
	}
	if _, err := interp.LookPathDir(handler.Dir, handler.Env, policy.Executable); err != nil {
		return arguments
	}
	rewritten, err := toolplugin.RewriteCommand(ctx, manifest.NodeExecutable, runtimeRoot,
		handler.Dir, shellEnvironment(handler.Env), arguments)
	if err != nil || len(rewritten) == 0 {
		return arguments
	}
	for _, argument := range rewritten {
		if strings.ContainsRune(argument, 0) {
			return arguments
		}
	}
	return rewritten
}
