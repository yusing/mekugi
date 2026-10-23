package activity

import (
	"strings"

	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/contracts/recordkind"
	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/modules/activity/feed"
	"github.com/yusing/mekugi/modules/activity/terminalview"
)

var Module = module.Descriptor[Deps, *feed.Service, module.NoBindings, terminalui.RosterDefinition[*terminalview.View]]{
	ID: "activity", Summary: "Own host-operation feeds, roster derivation and retained output",
	Lifetime: module.AttachedSession, New: newService,
	HostsActivityDetails: true,
	Views:                terminalui.RosterDefinition[*terminalview.View]{Name: "activity", Label: "Agents", New: terminalview.NewView},
	// Historical display snapshots retain ordinary expiry without live native waits.
	// Source: modules/activity/waittargets/record.go@[bd4147b31^] Record.
	Contributes: module.Contributions[*feed.Service]{RecordKinds: []recordkind.Spec{{ID: "wait", Match: func(name string) bool {
		hash, ok := strings.CutPrefix(name, "wait-")
		if !ok {
			return false
		}
		hash, ok = strings.CutSuffix(hash, ".json")
		return ok && len(hash) == 64 && strings.Trim(hash, "0123456789abcdef") == ""
	}}}},
}
