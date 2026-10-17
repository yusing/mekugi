package router

import (
	"context"
	"slices"

	"github.com/yusing/mekugi/libraries/commandanalysis"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
)

// Source: apps/mekugi/internal/router/mekugi_response_transform.go:340:385@470b8a37.
func executionCaptureEnvironment(ctx context.Context, directory string, store *mekugiReplayStore, patches []changecapture.PatchObservation) changecapture.Options {
	env := changecapture.Options{Directory: directory}
	if store != nil {
		env.Changes = storeChangeResolver(ctx, store)
		env.Workspaces = store.snapshots
	}
	for _, patch := range patches {
		for _, file := range patch.Files {
			for _, path := range []string{file.BeforePath, file.AfterPath} {
				if path != "" && !slices.Contains(env.Excluded, path) {
					env.Excluded = append(env.Excluded, path)
				}
			}
		}
	}
	return env
}

func admitExecutionWindow(windows *execWindowRegistry, review *savedReview, window *execWindow, observation *changecapture.Observation, patches []changecapture.PatchObservation, options changecapture.Options, displayThread, caller string) {
	window.paths = options.Excluded
	var commands []commandanalysis.Command
	if observation != nil {
		observation.Group = window.group
		window.roots = slices.Clone(observation.Roots)
		window.paths = append(window.paths, observation.ScopePaths()...)
		commands = observation.Commands
	}
	if len(patches) != 0 {
		window.roots = changecapture.AddRoot(window.roots, options.Directory)
	}
	windows.open(window, commands...)
	if live := review.Preview(); observation != nil && live.Enabled() {
		var snapshots *changecapture.Workspaces
		if options.Workspaces != nil {
			snapshots = options.Workspaces
		}
		windows.preview(window.ref, *observation, live, options.Directory, displayThread, caller, snapshots)
	}
}
