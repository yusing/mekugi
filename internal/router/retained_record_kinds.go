package router

import (
	"encoding/hex"
	"path/filepath"
	"strings"

	"github.com/yusing/mekugi/libraries/legacycall"

	"github.com/yusing/mekugi/contracts/recordkind"
)

// RetainedRecordKinds supplies the existing owners while their feature modules
// remain in this package. Composition replaces each contribution as its owner
// moves, without changing the retained record's identity or encoding.
func RetainedRecordKinds() []recordkind.Spec {
	kinds := []recordkind.Spec{
		{ID: "call", Match: func(name string) bool { return hashedRecordName(name, "call-") }, Dependencies: func(record recordkind.Record) ([]string, error) {
			return legacycall.Dependencies(record.Name, record.Data)
		}},
		{ID: "snapshot", Match: legacycall.ValidSnapshotName},
	}
	for _, prefix := range []string{"commentary-", "wait-", "hooks-"} {
		kind := recordkind.Spec{ID: strings.TrimSuffix(prefix, "-"), Match: func(name string) bool {
			return hashedRecordName(name, prefix)
		}}
		if kind.ID == "commentary" {
			kind.SeparateBudget, kind.Limit = true, 16<<20
		}
		kinds = append(kinds, kind)
	}
	return kinds
}

// Existing hashed record identity, shared by the current feature codecs.
func hashedRecordName(name, prefix string) bool {
	if filepath.Base(name) != name {
		return false
	}
	hash, ok := strings.CutPrefix(name, prefix)
	if !ok {
		return false
	}
	hash, ok = strings.CutSuffix(hash, ".json")
	if !ok || len(hash) != 64 || strings.ToLower(hash) != hash {
		return false
	}
	_, err := hex.DecodeString(hash)
	return err == nil
}
