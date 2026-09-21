package router

import (
	"context"
	"os"
	"os/exec"
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
	return rewriteRoutedCommand(ctx, manifest, runtimeRoot, arguments, handler.Dir, shellEnvironment(handler.Env))
}

// routeFrontendCommand applies the same pinned optional routing policy to an
// mrun child under the frontend process's actual cwd and environment.
func routeFrontendCommand(ctx context.Context, manifest toolWorkerManifest, runtimeRoot string, arguments []string) []string {
	policy := manifest.CommandRouting
	if policy == nil || len(arguments) == 0 || !slices.Contains(policy.Commands, arguments[0]) || policy.Executable == "" {
		return arguments
	}
	if _, err := exec.LookPath(policy.Executable); err != nil {
		return arguments
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		return arguments
	}
	return rewriteRoutedCommand(ctx, manifest, runtimeRoot, arguments, workingDirectory, os.Environ())
}

func rewriteRoutedCommand(
	ctx context.Context,
	manifest toolWorkerManifest,
	runtimeRoot string,
	arguments []string,
	workingDirectory string,
	environment []string,
) []string {
	rewritten, err := toolplugin.RewriteCommand(ctx, manifest.NodeExecutable, runtimeRoot,
		workingDirectory, environment, arguments)
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
