package launch

import (
	"context"
	"errors"

	"github.com/yusing/mekugi/contracts/observation"
)

// Inspect borrows passive native facts, including retained unloaded actors.
// It never creates an endpoint or reconciles work in Go.
func (e *threadExecution) Inspect(ctx context.Context, thread string) (observation.SessionDetails, error) {
	if e.native == nil {
		return observation.SessionDetails{}, errors.New("native observation connection is unavailable")
	}
	return e.native.root.Client.Requests.ObserveAgent(ctx, thread)
}
