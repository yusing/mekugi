package sessionpresentation

import (
	"maps"
	"slices"

	presentationcontract "github.com/yusing/mekugi/contracts/presentation"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

type activityPublication struct {
	view     *activitypresentation.Publication
	model    *activitypresentation.Model[ActivityEntry, ActivityAgent]
	revision uint64
	main     *activitypresentation.Publication
}

// Source: apps/mekugi/internal/router/activity_terminal.go:13:58@45fbe279
// Publication enriches the existing feed with detached status, output revisions
// and Main summaries. The returned publication is immutable and reused until its inputs change.
// No renderer state participates in source admission.
func (v *ActivitySource) Publication(session *Model, main *ActivitySource) *activitypresentation.Publication {
	var mainPublication *activitypresentation.Publication
	if main != nil {
		mainPublication = main.Publication(session, nil)
	}
	if v.publicationCurrent(session, mainPublication) {
		return v.publication.view
	}
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
			agent.Skills = slices.Clone(main.ActiveSkills()["Main"])
			if latest := main.Model.Latest("Main"); latest >= 0 {
				agent.LatestKind = main.Entries[latest].Source.Kind
			}
		}
	}
	v.publication = &activityPublication{view: &publication, model: v.Model, revision: v.Revision, main: mainPublication}
	return &publication
}

// Check revisions and small dynamic inputs before projecting any feed records.
// Output handles can change without a semantic source revision.
func (v *ActivitySource) publicationCurrent(session *Model, main *activitypresentation.Publication) bool {
	previous := v.publication
	if previous == nil || previous.model != v.Model || previous.revision != v.Revision || previous.main != main {
		return false
	}
	published := previous.view
	if published.LastSeq != v.LastSeq || !maps.Equal(published.LineCounts, v.LineCounts) ||
		(published.NetCounts == nil) != (v.NetCounts == nil) || v.NetCounts != nil && *published.NetCounts != *v.NetCounts {
		return false
	}
	roster, skills := v.Roster(), v.ActiveSkills()
	if len(roster) != len(published.Roster) {
		return false
	}
	for i, row := range roster {
		facts, prior := row.Agent.ActivityAgent(), published.Roster[i]
		// Main owns the root row's skills; its publication identity was checked above.
		sameSkills := main != nil && facts.Name == "/root" || slices.Equal(skills[facts.Name], prior.Agent.Skills)
		if row.Depth != prior.Depth || facts != prior.Agent.AgentFacts || !sameSkills ||
			!slices.Equal(session.StatusItems(presentationcontract.Thread{ID: facts.Thread, OutputThroughput: facts.OutputThroughput}, presentationcontract.AgentMetric), prior.Agent.StatusItems) {
			return false
		}
	}
	for i, record := range v.Entries {
		native := record.Source.Native
		if native == nil {
			continue
		}
		versions, count := published.Entries[i].Outputs, 0
		if native.Output != nil {
			count++
			if version, exists := versions[1]; !exists || version != native.Output.Version() {
				return false
			}
		}
		for j, segment := range native.Segments {
			if segment.Output != nil {
				count++
				if version, exists := versions[j+2]; !exists || version != segment.Output.Version() {
					return false
				}
			}
		}
		if count != len(versions) {
			return false
		}
	}
	return true
}
