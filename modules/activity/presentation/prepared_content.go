package presentation

// PreparedContent is actual latest Mekugi-prepared text, not a projection recipe
// or proof of provider consumption. Calls are observed nested host identities.
type PreparedContent struct {
	ID, Text string
	Calls    []string
	Cell     bool
	// Altered requires different readable content and matching original evidence.
	Altered bool
}
