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
}

func (e Entry) Hidden() bool {
	return e.Suppressed || e.RosterOnly || e.Kind == "question_reply" || e.Kind == "tool" && e.Text == ""
}

func (e Entry) Standalone() bool { return !e.RosterOnly && !(e.Kind == "tool" && e.Text == "") }

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
	facts.Outputs = maps.Clone(facts.Outputs)
	return Entry{Facts: facts, Revision: r.Revision, Values: r.Values}
}

// Publish projects the one retained feed into a terminal-independent value.
func (m *Model[S, A]) Publish() Publication {
	publication := Publication{LastSeq: m.LastSeq, LineCounts: maps.Clone(m.LineCounts)}
	if m.NetCounts != nil {
		publication.NetCounts = new(*m.NetCounts)
	}
	publication.Entries = make([]Entry, len(m.Entries))
	for i, record := range m.Entries {
		publication.Entries[i] = record.Publish()
	}
	skills := m.ActiveSkills()
	for _, row := range m.Roster() {
		facts := row.Agent.ActivityAgent()
		agent := Agent{AgentFacts: facts, Status: m.Status(row.Agent), Skills: slices.Clone(skills[facts.Name])}
		if latest := m.Latest(facts.Name); latest >= 0 {
			agent.LatestKind = m.Entries[latest].Source.ActivityFacts().Kind
		}
		agent.Summary = m.SummaryEntries(facts.Name, facts.Name == "/root")
		publication.Roster = append(publication.Roster, AgentRow{Agent: agent, Depth: row.Depth})
	}
	return publication
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
		entries = append(entries, m.Entries[index].Publish())
	}
	entry := m.Entries[selected.Index].Publish()
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
