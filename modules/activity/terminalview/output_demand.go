package terminalview

import (
	"slices"

	"github.com/yusing/mekugi/modules/activity/feed"
)

type OutputMode uint8

const (
	OutputContext OutputMode = iota
	OutputExpanded
)

// OutputDemand names immutable source output, never a live stream handle.
type OutputDemand struct {
	Source          uint64
	Output, Version int
	Mode            OutputMode
}

type OutputResult struct {
	Demand  OutputDemand
	View    feed.OutputView
	Limited bool
}

type outputIdentity struct {
	source uint64
	output int
}

// OutputDemands drains the requests made while preparing the frame. The adapter
// resolves these at the retained-output owner, then hydrates before publishing.
func (v *View) OutputDemands() []OutputDemand {
	demands := v.outputDemands
	v.outputDemands = nil
	return demands
}

func (v *View) Hydrate(results []OutputResult) {
	if v.outputCache == nil {
		v.outputCache = make(map[OutputDemand]OutputResult)
	}
	for _, result := range results {
		key := result.Demand
		if version, exists := v.outputVersions[outputIdentity{key.Source, key.Output}]; !exists || version != key.Version {
			continue
		}
		v.outputCache[key] = result
		v.InvalidateRuns(key.Source)
	}
}

func (v *View) output(block Block, mode OutputMode) (OutputResult, bool) {
	version, exists := v.outputVersions[outputIdentity{block.Source, block.Output}]
	if !exists {
		return OutputResult{}, false
	}
	key := OutputDemand{Source: block.Source, Output: block.Output, Version: version, Mode: mode}
	if result, exists := v.outputCache[key]; exists {
		return result, true
	}
	if mode == OutputContext {
		expanded := key
		expanded.Mode = OutputExpanded
		if result, exists := v.outputCache[expanded]; exists {
			return result, true
		}
	}
	if !slices.Contains(v.outputDemands, key) {
		v.outputDemands = append(v.outputDemands, key)
	}
	return OutputResult{}, false
}

func (v *View) outputContext(block Block) Block {
	if v.Painter.LayoutOnly || block.Collapsed || len(block.Tail) == 0 || len(block.Changes) > 0 {
		return block
	}
	if result, exists := v.output(block, OutputContext); exists {
		block.OutputSnapshot, block.OutputContextLimited = &result.View, result.Limited
	}
	return block
}
