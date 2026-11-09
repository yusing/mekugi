package presentation

import (
	"maps"
	"slices"

	"github.com/yusing/mekugi/contracts/presentation"
	"github.com/yusing/mekugi/libraries/diffmodel"
)

// Entry is a detached observation publication. Values are immutable until the
// owning record is replaced; mutations copy their slices before changing them.
// It carries no original host source or retained output handles.
type Entry struct {
	Facts
	Revision uint64
	Values   []Value
	Command  *ItemContent // Detached command input for terminal-only reveal reprojection.
}

func (e Entry) ActivityFacts() Facts { return e.Facts }

func (e Entry) Hidden() bool {
	return e.Suppressed || e.Kind == "question_reply" || e.Kind == "tool" && e.Text == "" && len(e.Values) == 0
}

func (e Entry) Standalone() bool {
	return !(e.Kind == "tool" && e.Text == "" && len(e.Values) == 0)
}

func (e Entry) StatusAnimating() bool {
	if !e.Live || len(e.Values) == 0 {
		return false
	}
	last := e.Values[len(e.Values)-1]
	return last.Live && (last.Kind == "summary" || last.Kind == "progress")
}

type Agent struct {
	AgentFacts
	StatusItems        []presentation.Item
	Status, LatestKind string
	Summary            []Entry
	Skills             SkillSet
}

type AgentRow struct {
	Agent Agent
	Depth int
}

type Publication struct {
	Entries     []Entry
	Roster      []AgentRow
	MainSummary []Entry // Main's borrowed feed summary, including when no roster row is present.
	LastSeq     uint64
	LineCounts  map[string]diffmodel.Counts
	NetCounts   *diffmodel.Counts
}

func (r Record[S]) Publish() Entry {
	facts := r.Source.ActivityFacts()
	facts.Spans = slices.Clone(facts.Spans)
	facts.Images = slices.Clone(facts.Images)
	facts.Outputs = maps.Clone(facts.Outputs)
	return Entry{Facts: facts, Revision: r.Revision, Values: AttachmentEvents(r.Values)}
}

// PublishedEntries reuses detached entries by their owning record revisions.
// The indexes are rebuilt in one pass only when semantic membership changes.
func (m *Model[S, A]) PublishedEntries() []Entry {
	if m.publicationEntries != nil && m.publicationRevision == m.Revision {
		return m.publicationEntries
	}
	previous := make(map[uint64]Entry, len(m.publicationEntries))
	for _, entry := range m.publicationEntries {
		previous[entry.Revision] = entry
	}
	entries := make([]Entry, len(m.Entries))
	m.latest, m.summaries = make(map[string]int), make(map[string]Summary)
	m.reasoningPrevious = make(map[int]int)
	for i, record := range m.Entries {
		entry, exists := previous[record.Revision]
		if !exists || record.Revision == 0 {
			entry = record.Publish()
		}
		entries[i] = entry
		if previous, exists := m.latest[entry.Agent]; exists && entry.Kind == "reasoning" && entries[previous].Kind == "reasoning" {
			m.reasoningPrevious[i] = previous
		}
		m.latest[entry.Agent] = i
		if len(entry.Values) == 0 {
			continue
		}
		summary := Summary{Index: i}
		m.summaries[entry.Agent] = summary
		if entry.Assignment {
			m.summaries[entry.AssignmentTo] = summary
		}
		if len(entry.Values) == 1 && entry.Values[0].Kind == "message" && entry.Values[0].To == "/root" {
			m.summaries[""] = Summary{Index: i, Incoming: true}
		}
	}
	m.publicationEntries, m.publicationRevision = entries, m.Revision
	return entries
}

// Publish projects the one retained feed into a terminal-independent value.
func (m *Model[S, A]) Publish() Publication {
	publication := Publication{LastSeq: m.LastSeq, LineCounts: maps.Clone(m.LineCounts)}
	if m.NetCounts != nil {
		publication.NetCounts = new(*m.NetCounts)
	}
	publication.Entries = m.PublishedEntries()
	skills := m.ActiveSkills()
	for _, row := range m.Roster() {
		facts := row.Agent.ActivityAgent()
		agent := Agent{AgentFacts: facts, Skills: slices.Clone(skills[facts.Name])}
		if latest, exists := m.latest[facts.Name]; exists {
			agent.LatestKind = publication.Entries[latest].Kind
		}
		agent.Status = facts.ObservedStatus(agent.LatestKind)
		agent.Summary = m.SummaryEntries(facts.Name, facts.Name == "/root")
		if facts.Responding && !facts.Waiting && !facts.Paused {
			if summary := facts.GenerationSummary(); summary != nil {
				agent.Summary = summary
			}
		}
		publication.Roster = append(publication.Roster, AgentRow{Agent: agent, Depth: row.Depth})
	}
	return publication
}

// GenerationSummary is shared by feed-backed and native SDK roster producers.
func (facts AgentFacts) GenerationSummary() []Entry {
	if facts.Generating == "" {
		return nil
	}
	// There is no feed event, but this summary belongs to the current work.
	return []Entry{{Agent: facts.Name, Kind: "progress", Observed: facts.WorkSince, Live: true, Values: []Value{{Kind: "progress", Body: facts.Generating, Live: true}}}}
}

// SummaryEntries also lets the application supply Main's existing feed summary
// to the child roster without a renderer-to-renderer pointer or copied transcript.
func (m *Model[S, A]) SummaryEntries(owner string, root bool) []Entry {
	selected := m.Summary(owner, root)
	if selected.Index < 0 {
		return nil
	}
	var entries []Entry
	for _, index := range slices.Backward(selected.Earlier) {
		entries = append(entries, m.publicationEntries[index])
	}
	entry := m.publicationEntries[selected.Index]
	// An exchange can follow progress in the transcript; the roster keeps its status.
	if entry.Kind == "progress" && len(entry.Values) > 1 {
		entry.Values = entry.Values[:1]
	}
	if selected.Incoming {
		entry.Values = CloneValues(entry.Values)
		entry.Values[0].Owner = "/root"
	}
	return append(entries, entry)
}

// CloneValues detaches mutable slices and pointer-valued facts, including flat
// original membership, so replacing an observation cannot rewrite a publication.
func CloneValues(values []Value) []Value {
	values = slices.Clone(values)
	for i := range values {
		v := &values[i]
		v.Timeouts = slices.Clone(v.Timeouts)
		v.Reads = slices.Clone(v.Reads)
		for j := range v.Reads {
			v.Reads[j].Ranges = slices.Clone(v.Reads[j].Ranges)
			v.Reads[j].RangeLines = slices.Clone(v.Reads[j].RangeLines)
		}
		v.Questions = slices.Clone(v.Questions)
		v.WaitTargets = slices.Clone(v.WaitTargets)
		v.Tail = slices.Clone(v.Tail)
		v.Changes = slices.Clone(v.Changes)
		v.Members = CloneValues(v.Members)
		v.RequestedOperations = CloneValues(v.RequestedOperations)
		if v.Results != nil {
			result := *v.Results
			v.Results = &result
		}
		if v.Hook != nil {
			hook := *v.Hook
			v.Hook = &hook
		}
	}
	return values
}
