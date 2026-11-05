package sessionpresentation

import (
	"maps"
	"slices"

	presentationcontract "github.com/yusing/mekugi/contracts/presentation"
	"github.com/yusing/mekugi/modules/activity/feed"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

type activityPublication struct {
	unowned  []activityOutput
	view     *activitypresentation.Publication
	model    *activitypresentation.Model[ActivityEntry, ActivityAgent]
	revision uint64
	main     *activitypresentation.Publication
	agents   []activitypresentation.AgentFacts
	roster   map[string]int
	outputs  []activityOutput
	owners   map[*feed.Retention]uint64
}

type activityOutput struct {
	output     *feed.Output
	index, key int
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
	// Output owners invalidate streaming state without scanning retained records while idle.
	var outputs []activityOutput
	if prior := v.publication; prior != nil && prior.model == v.Model && prior.revision == v.Revision {
		publication.Entries = prior.view.Entries
		outputs = prior.outputs
	} else {
		for i, record := range v.Entries {
			native := record.Source.Native
			if native == nil {
				continue
			}
			observe := func(output *feed.Output, key int) {
				if output != nil {
					outputs = append(outputs, activityOutput{output, i, key})
				}
			}
			observe(native.Output, 1)
			for j, segment := range native.Segments {
				observe(segment.Output, j+2)
			}
		}
	}
	owners := make(map[*feed.Retention]uint64)
	var unowned []activityOutput
	copied, changedEntry := false, -1
	for _, ref := range outputs {
		if owner := ref.output.Retention(); owner != nil {
			owners[owner] = owner.Revision()
		} else {
			unowned = append(unowned, ref)
		}
		version := ref.output.Version()
		if prior, exists := publication.Entries[ref.index].Outputs[ref.key]; exists && prior == version {
			continue
		}
		if !copied {
			publication.Entries = slices.Clone(publication.Entries)
			copied = true
		}
		entry := &publication.Entries[ref.index]
		// References stay in entry/key order, so each changed map is detached once.
		if changedEntry != ref.index {
			entry.Outputs = maps.Clone(entry.Outputs)
			if entry.Outputs == nil {
				entry.Outputs = make(map[int]int)
			}
			changedEntry = ref.index
		}
		entry.Outputs[ref.key] = version
		if ref.key == 1 {
			entry.OutputFailed = ref.output.Failed()
		}
	}
	publication.MainSummary = v.Model.SummaryEntries("Main", true)
	if main != nil {
		mainSummary := mainPublication.MainSummary
		publication.MainSummary = mainSummary
		for i := range publication.Roster {
			agent := &publication.Roster[i].Agent
			if agent.Name != "/root" {
				continue
			}
			agent.Summary = mainSummary
			agent.Skills = slices.Clone(main.ActiveSkills()["Main"])
			if latest := main.Model.Latest("Main"); latest >= 0 {
				agent.LatestKind = mainPublication.Entries[latest].Kind
			}
			agent.Status = agent.AgentFacts.ObservedStatus(agent.LatestKind)
		}
	}
	cached := &activityPublication{view: &publication, model: v.Model, revision: v.Revision, main: mainPublication, outputs: outputs, owners: owners, unowned: unowned, roster: make(map[string]int)}
	for _, agent := range v.Agents {
		cached.agents = append(cached.agents, agent.ActivityAgent())
	}
	for index, row := range publication.Roster {
		cached.roster[row.Agent.Name] = index
	}
	v.publication = cached
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
	skills := v.ActiveSkills()
	if len(v.Agents) != len(previous.agents) {
		return false
	}
	for i, agent := range v.Agents {
		facts := agent.ActivityAgent()
		index, exists := previous.roster[facts.Name]
		if !exists || facts != previous.agents[i] {
			return false
		}
		prior := published.Roster[index].Agent
		sameSkills := main != nil && facts.Name == "/root" || slices.Equal(skills[facts.Name], prior.Skills)
		if !sameSkills ||
			!slices.Equal(session.StatusItems(presentationcontract.Thread{ID: facts.Thread, OutputThroughput: facts.OutputThroughput}, presentationcontract.AgentMetric), prior.StatusItems) {
			return false
		}
	}
	for owner, revision := range previous.owners {
		if owner.Revision() != revision {
			return false
		}
	}
	for _, ref := range previous.unowned {
		if ref.output.Retention() == nil && ref.output.Version() != published.Entries[ref.index].Outputs[ref.key] {
			return false
		}
	}
	return true
}
