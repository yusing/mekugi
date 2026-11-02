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
	return &observation
}
