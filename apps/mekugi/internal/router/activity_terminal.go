package router

import (
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	presentationcontract "github.com/yusing/mekugi/contracts/presentation"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func (v *activityAdapter) publishActivity() {
	publication := v.Model.Publish()
	for i, entry := range publication.Entries {
		if frame, needed := v.View.CommandFrame(entry.Seq, entry.Revision); needed {
			v.View.UpdateCommandFrame(commandFrame(v.Entries[i], frame))
		}
	}
	for i := range publication.Roster {
		agent := &publication.Roster[i].Agent
		agent.StatusItems = v.display.StatusItems(presentationcontract.Thread{ID: agent.Thread, OutputThroughput: agent.OutputThroughput}, presentationcontract.AgentMetric)
	}
	// Output versions belong to publication, not semantic scans of source facts.
	for i := range publication.Entries {
		native := v.Entries[i].Source.native
		if native == nil || native.output == nil && len(native.segments) == 0 {
			continue
		}
		versions := make(map[int]int)
		if native.output != nil {
			versions[1] = native.output.Version()
		}
		for j, segment := range native.segments {
			if segment.output != nil {
				versions[j+2] = segment.output.Version()
			}
		}
		publication.Entries[i].Outputs = versions
	}
	if v.mainView != nil {
		mainSummary := v.mainView.Model.SummaryEntries("Main", true)
		v.View.MainSummary = v.mainView.View.SummaryBlocks(mainSummary)
		for i := range publication.Roster {
			agent := &publication.Roster[i].Agent
			if agent.Name != "/root" {
				continue
			}
			agent.Summary = mainSummary
			if latest := v.mainView.Model.Latest("Main"); latest >= 0 {
				agent.LatestKind = v.mainView.Entries[latest].Source.Kind
			}
		}
		v.View.MainStatus = v.mainView.StatusLabel
	}
	display := v.display.View()
	v.View.Update(publication, activityui.Display{Caller: display.Displayed.Caller, Attachment: display.Version.Attachment, Revision: display.Version.Revision})
}

// resolveOutputDemands reads admitted source handles without accessing a renderer.
// Application integration delivers the detached results to the requesting view.
func (v *activityAdapter) resolveOutputDemands(demands []activityui.OutputDemand) []activityui.OutputResult {
	results := make([]activityui.OutputResult, 0, len(demands))
	for _, demand := range demands {
		result := activityui.OutputResult{Demand: demand}
		output := v.retainedOutput(activityui.Block{Source: demand.Source, Output: demand.Output})
		if output == nil {
			result.View.Released = true
		} else if demand.Mode == activityui.OutputExpanded {
			result.View = output.View()
		} else {
			result.View, result.Limited = output.Context(activityui.OutputContextBytes)
		}
		results = append(results, result)
	}
	return results
}

func (v *activityAdapter) Render(width, height int, now time.Time) []string {
	v.publishActivity()
	for {
		lines := v.View.Render(width, height, now)
		demands := v.View.OutputDemands()
		if len(demands) == 0 {
			return lines
		}
		v.View.Hydrate(v.resolveOutputDemands(demands))
	}
}

func (v *activityAdapter) RenderFeed(width, rows int) activityui.Feed {
	v.publishActivity()
	for {
		feed := v.View.RenderFeed(width, rows)
		demands := v.View.OutputDemands()
		if len(demands) == 0 {
			return feed
		}
		v.View.Hydrate(v.resolveOutputDemands(demands))
	}
}

func (v *activityAdapter) NativeRoster(width, limit int, now time.Time, focused bool) []string {
	v.publishActivity()
	return v.View.NativeRoster(width, limit, now, focused)
}

func (v *activityAdapter) takeDisplayIntent() *sessionpresentation.DisplayIntent {
	intent := v.View.DisplayRequest
	if intent == nil {
		return nil
	}
	v.View.DisplayRequest = nil
	return &sessionpresentation.DisplayIntent{
		Version: sessionpresentation.Version{Attachment: intent.Attachment, Revision: intent.Revision},
		Thread:  v.displayTarget(intent.Caller),
	}
}

func settleActivity(now time.Time, adapters ...*activityAdapter) bool {
	views := make([]*activityui.View, len(adapters))
	for i, adapter := range adapters {
		adapter.publishActivity()
		views[i] = adapter.View
	}
	return activityui.Settle(now, views...)
}
