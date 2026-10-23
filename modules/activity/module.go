package activity

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/modules/activity/feed"
	"github.com/yusing/mekugi/modules/activity/terminalview"
)

var Module = module.Descriptor[Deps, *feed.Service, module.NoBindings, terminalui.RosterDefinition[*terminalview.View]]{
	ID: "activity", Summary: "Own host-operation feeds, roster derivation and retained output",
	Lifetime: module.AttachedSession, New: newService,
	HostsActivityDetails: true,
	Views:                terminalui.RosterDefinition[*terminalview.View]{Name: "activity", Label: "Agents", New: terminalview.NewView},
}
