package executionobservation

import (
	"github.com/yusing/mekugi/modules/changehistory/capture"
	"slices"
)

// Keep request-local state out of immutable replay comparisons as well as JSON.
func durableHistory(h mekugiHistory) mekugiHistory {
	h.confirmed, h.sequence = false, 0
	h.nativeCell = nil
	if len(h.NativePatches) == 0 {
		h.NativePatches = nil // Empty omitted slices read back as nil.
	}
	if h.ResolvedBaseline != nil {
		baseline := *h.ResolvedBaseline
		baseline.Files = slices.Clone(baseline.Files)
		for i := range baseline.Files {
			baseline.Files[i].WatchStamp = ""
		}
		baseline.Inventory = durableInventory(baseline.Inventory)
		h.ResolvedBaseline = &baseline
	}
	h.ExecObservation = durableObservation(h.ExecObservation)
	return h
}

// Durable drops provisional metadata and normalizes omitted collections for
// immutable-record comparison. The live observation keeps its own state.
func durableObservation(o *capture.Observation) *capture.Observation {
	if o == nil {
		return nil
	}
	observation := *o
	observation.WindowStart = observation.WindowStart.UTC()
	observation.Files = slices.Clone(observation.Files)
	for i := range observation.Files {
		observation.Files[i].WatchStamp = ""
		observation.Files[i].WatchMoveOnly = false
	}
	observation.Listings = slices.Clone(observation.Listings)
	for i := range observation.Listings {
		if len(observation.Listings[i].Entries) == 0 {
			observation.Listings[i].Entries = nil
		}
	}
	observation.Inventory = durableInventory(observation.Inventory)
	return &observation
}

// durable drops request-local preview stamps and spells empty collections as
// they read back.
func durableInventory(i *capture.Inventory) *capture.Inventory {
	if i == nil {
		return nil
	}
	inventory := *i
	inventory.Files = slices.Clone(inventory.Files)
	for index := range inventory.Files {
		inventory.Files[index].WatchStamp = ""
	}
	if len(inventory.Files) == 0 {
		inventory.Files = nil
	}
	if len(inventory.Entries) == 0 {
		inventory.Entries = nil
	}
	if len(inventory.Blobs) == 0 {
		inventory.Blobs = nil
	}
	return &inventory
}
