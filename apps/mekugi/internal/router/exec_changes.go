package router

import (
	"context"
	"time"

	"github.com/yusing/mekugi/libraries/commandanalysis/execscope"
	"github.com/yusing/mekugi/modules/changehistory/changes"
)

// storeChangeResolver reads the calling thread's change namespace.
func storeChangeResolver(ctx context.Context, selected changes.Scope, source changes.Service) execscope.ChangeResolver {
	return func(arguments []string, directory string, deadline time.Time) execscope.ChangeResolution {
		options, err := source.Parse(arguments, directory)
		if err != nil {
			return execscope.ChangeResolution{InvalidArguments: true}
		}
		ctx, cancel := context.WithDeadline(ctx, deadline)
		defer cancel()
		paths, err := source.MutationPaths(ctx, observationScope(ctx, selected), options)
		if err != nil {
			return execscope.ChangeResolution{}
		}
		return execscope.ChangeResolution{Paths: paths}
	}
}
