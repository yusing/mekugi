package sessionhistory

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/contracts/recordkind"
	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

type Deps struct {
	// RecordKinds are invocation-owned native receipts supplied by the
	// application. Feature records enter through descriptor contributions.
	RecordKinds []recordkind.Spec
}

var Module = module.Descriptor[Deps, *history.Service, module.NoBindings, module.NoViews]{
	ID:           "sessionhistory",
	Summary:      "Own retained session publication, namespaces, allocation, leases, and retention",
	Lifetime:     module.Invocation,
	HostsRecords: true,
	// Host facts retain their historical codecs and identities. Feature records
	// enter through the same contribution slot as these built-in host records.
	Contributes: module.Contributions[*history.Service]{RecordKinds: []recordkind.Spec{
		history.HookRecord,
		{ID: "call", Match: func(name string) bool { return legacycall.ValidName(name, false) }, Dependencies: func(record recordkind.Record) ([]string, error) {
			return legacycall.Dependencies(record.Name, record.Data)
		}},
		{ID: "snapshot", Match: legacycall.ValidSnapshotName},
		{ID: "commentary", Match: func(name string) bool { return legacycall.ValidName(name, true) }, SeparateBudget: true, Limit: 16 << 20},
	}},
	New: newService,
}
