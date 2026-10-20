package changehistory

import (
	"context"

	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/changehistory/capture"
)

func newService(deps Deps, _ module.Slots) (*capture.Service, error) {
	return capture.New(deps.History, deps.Directory)
}

func startService(ctx context.Context, service *capture.Service) error { return service.Start(ctx) }
func stopService(ctx context.Context, service *capture.Service) error  { return service.Stop(ctx) }
