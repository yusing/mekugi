package terminalview

import (
	"slices"

	"github.com/yusing/mekugi/modules/activity/presentation"
)

type outputIdentity struct {
	source uint64
	output int
}

// OutputDemands drains the requests made while preparing the frame. The adapter
// resolves these at the retained-output owner, then hydrates before publishing.
func (v *View) OutputDemands() []presentation.OutputDemand {
	demands := v.outputDemands
	v.outputDemands = nil
	return demands
}

func (v *View) Hydrate(results []presentation.OutputResult) {
	if v.outputCache == nil {
		v.outputCache = make(map[presentation.OutputDemand]presentation.OutputResult)
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

func (v *View) output(block Block, limit int) (presentation.OutputResult, bool) {
	version, exists := v.outputVersions[outputIdentity{block.Source, block.Output}]
	if !exists {
		return presentation.OutputResult{}, false
	}
	key := presentation.OutputDemand{Source: block.Source, Output: block.Output, Version: version, Bytes: limit}
	if result, exists := v.outputCache[key]; exists {
		return result, true
	}
	if limit > 0 {
		expanded := key
		expanded.Bytes = 0
		if result, exists := v.outputCache[expanded]; exists {
			return result, true
		}
	}
	if !slices.Contains(v.outputDemands, key) {
		v.outputDemands = append(v.outputDemands, key)
	}
	return presentation.OutputResult{}, false
}

func (v *View) outputContext(block Block) Block {
	if v.Painter.LayoutOnly || block.Collapsed || len(block.Tail) == 0 || len(block.Changes) > 0 {
		return block
	}
	if result, exists := v.output(block, OutputContextBytes); exists {
		block.OutputSnapshot, block.OutputContextLimited = &result.View, result.Limited
	}
	return block
}
