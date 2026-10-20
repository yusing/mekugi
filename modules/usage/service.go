package usage

import (
	"context"

	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/usage/accounting"
)

type Deps struct{ History *history.Service }

func newService(deps Deps, _ module.Slots) (*accounting.Service, error) {
	return accounting.New(deps.History), nil
}
func startService(ctx context.Context, s *accounting.Service) error { return s.Start(ctx) }
func stopService(_ context.Context, s *accounting.Service) error    { s.Close(); return nil }
