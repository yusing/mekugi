package accounting

import "github.com/yusing/mekugi/contracts/recordkind"

// RecordKinds retains the historical per-thread usage identity.
func RecordKinds() []recordkind.Spec {
	return []recordkind.Spec{{ID: "usage", Match: recordkind.HashedName("usage-", ".json")}}
}
