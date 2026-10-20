package scope_test

import (
	"testing"

	"github.com/yusing/mekugi/contracts/scope"
)

func TestStoreTypedKeysAndIsolation(t *testing.T) {
	var first, second scope.Store
	var count, otherCount scope.Key[int]
	var label scope.Key[string]
	value := count.Value(&first)
	if *value != 0 || *label.Value(&first) != "" {
		t.Fatal("new entries must contain their typed zero values")
	}
	*value = 7
	*label.Value(&first) = "attached"
	if count.Value(&first) != value || *count.Value(&first) != 7 {
		t.Fatal("repeated access lost the entry")
	}
	if *otherCount.Value(&first) != 0 || *count.Value(&second) != 0 {
		t.Fatal("state crossed a key or store boundary")
	}
	*otherCount.Value(&first) = 11
	*count.Value(&second) = 13
	if *value != 7 || *label.Value(&first) != "attached" {
		t.Fatal("independent entries changed the original state")
	}
}

func TestStoreReplacementStartsFresh(t *testing.T) {
	var key scope.Key[int]
	var store scope.Store
	previous := key.Value(&store)
	*previous = 7
	store = scope.Store{}
	current := key.Value(&store)
	if current == previous || *current != 0 {
		t.Fatal("replacement store reused the retired entry")
	}
	*current = 11
	if *previous != 7 {
		t.Fatal("replacement changed the retired entry")
	}
}
