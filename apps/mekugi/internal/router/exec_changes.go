package router

import (
	"context"
	"time"

	"github.com/yusing/mekugi/libraries/commandanalysis/execscope"
)

// storeChangeResolver reads the calling thread's change namespace.
func storeChangeResolver(ctx context.Context, store *mekugiReplayStore) execscope.ChangeResolver {
	return func(arguments []string, directory string, deadline time.Time) execscope.ChangeResolution {
		options, err := store.changes.Parse(arguments, directory)
		if err != nil {
			return execscope.ChangeResolution{InvalidArguments: true}
		}
		ctx, cancel := context.WithDeadline(ctx, deadline)
		defer cancel()
		paths, err := store.changes.MutationPaths(ctx, store.scoped(ctx).changeScope(), options)
		if err != nil {
			return execscope.ChangeResolution{}
		}
		return execscope.ChangeResolution{Paths: paths}
	}
}
