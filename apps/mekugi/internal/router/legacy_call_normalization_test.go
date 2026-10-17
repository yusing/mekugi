package router

import "slices"

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
		baseline.Inventory = baseline.Inventory.Durable()
		h.ResolvedBaseline = &baseline
	}
	h.ExecObservation = h.ExecObservation.Durable()
	return h
}
