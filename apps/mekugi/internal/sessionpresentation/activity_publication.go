package sessionpresentation

import (
	presentationcontract "github.com/yusing/mekugi/contracts/presentation"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

// Source: apps/mekugi/internal/router/activity_terminal.go:13:58@45fbe279
// Publication enriches the existing feed with detached status, output revisions
// and Main summaries. No renderer state participates in source admission.
func (v *ActivitySource) Publication(session *Model, main *ActivitySource) activitypresentation.Publication {
	publication := v.Model.Publish()
	for i := range publication.Roster {
		agent := &publication.Roster[i].Agent
		agent.StatusItems = session.StatusItems(presentationcontract.Thread{ID: agent.Thread, OutputThroughput: agent.OutputThroughput}, presentationcontract.AgentMetric)
	}
	// Output versions belong to publication, not semantic scans of source facts.
	for i := range publication.Entries {
		native := v.Entries[i].Source.Native
		if native == nil || native.Output == nil && len(native.Segments) == 0 {
			continue
		}
		versions := make(map[int]int)
		if native.Output != nil {
			versions[1] = native.Output.Version()
		}
		for j, segment := range native.Segments {
			if segment.Output != nil {
				versions[j+2] = segment.Output.Version()
			}
		}
		publication.Entries[i].Outputs = versions
	}
	if main != nil {
		mainSummary := main.Model.SummaryEntries("Main", true)
		publication.MainSummary = mainSummary
		for i := range publication.Roster {
			agent := &publication.Roster[i].Agent
			if agent.Name != "/root" {
				continue
			}
			agent.Summary = mainSummary
			if latest := main.Model.Latest("Main"); latest >= 0 {
				agent.LatestKind = main.Entries[latest].Source.Kind
			}
		}
	}
	return publication
}
