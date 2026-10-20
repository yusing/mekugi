package commandsegments

import (
	"context"

	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/commandsegments/segments"
)

func newService(deps Deps, _ module.Slots) (*segments.Service, error) {
	return segments.New(deps.History, deps.Live)
}

func startService(ctx context.Context, service *segments.Service) error { return service.Start(ctx) }
func stopService(ctx context.Context, service *segments.Service) error  { return service.Stop(ctx) }
