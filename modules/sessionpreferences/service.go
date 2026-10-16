package sessionpreferences

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

type Deps struct{ History *history.Service }

func newService(deps Deps, _ module.Slots) (*preferences.Service, error) {
	return preferences.New(deps.History)
}
