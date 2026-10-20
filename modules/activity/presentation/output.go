package presentation

import "github.com/yusing/mekugi/modules/activity/feed"

// OutputDemand names immutable source output, never a live stream handle.
// Bytes bounds context including retained line slots; zero requests the complete
// retained snapshot. Source, Output and Version identify the attached owner.
// Source: modules/activity/terminalview/output_demand.go:16:27@7e374dcb.
type OutputDemand struct {
	Source                 uint64
	Output, Version, Bytes int
}

type OutputResult struct {
	Demand  OutputDemand
	View    feed.OutputView
	Limited bool
}
