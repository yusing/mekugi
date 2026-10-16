package accounting

import (
	"encoding/hex"
	"path/filepath"
	"strings"

	"github.com/yusing/mekugi/contracts/recordkind"
)

// RecordKinds retains the historical per-thread usage identity.
func RecordKinds() []recordkind.Spec {
	return []recordkind.Spec{{ID: "usage", Match: func(name string) bool {
		if filepath.Base(name) != name {
			return false
		}
		hash, ok := strings.CutPrefix(name, "usage-")
		if !ok {
			return false
		}
		hash, ok = strings.CutSuffix(hash, ".json")
		if !ok || len(hash) != 64 || strings.ToLower(hash) != hash {
			return false
		}
		_, err := hex.DecodeString(hash)
		return err == nil
	}}}
}
