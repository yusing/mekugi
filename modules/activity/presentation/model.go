package presentation

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/contracts/inference"
	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/libraries/diffmodel"
)

// Source identifies an application-supplied observation without interpreting host protocol.
// The original typed source stays on its record; Facts is a transient semantic projection.
type Source interface{ ActivityFacts() Facts }
type Facts struct {
	Images                                                []string `json:"-"` // Bound user or viewed image sources for authorized reads.
	Retained                                              bool     // Bounded projections retired explicitly by their source owner.
	TextRef                                               observation.ItemRef
	Seq                                                   uint64
	Agent, Kind, Text                                     string
	Observed                                              time.Time
	Spans                                                 []TextSpan
	InputGroup                                            string
	Question, Activity                                    uint64
	AssignmentTo, AssignmentText                          string
	Assignment                                            bool
	Running, Live, Restored, Unconfirmed, ContextBoundary bool
	Suppressed, RosterOnly, OutputFailed, OutputComplete  bool
	ItemComplete                                          bool
	Completed                                             time.Time
	Outputs                                               map[int]int
}
type AgentSource interface{ ActivityAgent() AgentFacts }
type AgentFacts struct {
	Name, Role, Thread                           string
	WaitingFor                                   string
	Wait                                         *Value
	Responding, Final, Waiting, Paused           bool
	Started, LastResponse                        time.Time
	WorkElapsed                                  time.Duration
	WorkSince                                    time.Time
	WorkKnown                                    bool
	Turns, Roundtrips                            uint64
	Cost                                         float64
	CostKnown, CostPartial                       bool
	InputTokens, OutputTokens                    uint64
	OutputThroughput                             inference.OutputThroughput
	TokensKnown, UsagePartial, RoundtripsPartial bool
	ContextTokens, ContextWindow                 uint64
	ContextKnown                                 bool
}

// Source: internal/router/live_activity_record.go:9:87@[ebde9ad5].
// Record keeps one original observation, its neutral values and revision together.
// Source is private adapter evidence, never a view publication.
type Record[S Source] struct {
	Source   S
	Values   []Value
	Revision uint64
}

// Model owns one semantic feed. The renderer keeps disclosure, animation, layout and selection.
// Main and child feeds share an attached Session but never duplicate their records.
type Model[S Source, A AgentSource] struct {
	Entries           []Record[S]
	Agents            []A
	Revision, LastSeq uint64
	LineCounts        map[string]diffmodel.Counts
	NetCounts         *diffmodel.Counts
	retiredSkills     map[string]SkillSet
	skillHistory      map[string]bool
	skills            map[string]SkillSet
	skillsRevision    uint64
}
type Session[S Source, A AgentSource] struct {
	Main, Children Model[S, A]
	Commits        *CommitReader
	agents         scope.Key[AgentLifecycle]
	roster         []A
	sequence       uint64
}

var attachedSession scope.Key[any]

// Attached returns the existing presentation session for this attachment. A
// binding uses one source/agent type pair throughout its attachment. Native
// transcript and wait facts remain available without optional output retention.
// Source: modules/activity/feed/service.go:52:63@1bcbdd5a.
func Attached[S Source, A AgentSource](store *scope.Store) *Session[S, A] {
	slot := attachedSession.Value(store)
	if *slot == nil {
		*slot = new(Session[S, A])
	}
	return (*slot).(*Session[S, A])
}

// Lifecycle owns admitted observations in the supplied thread scope. Copies
// published into roster records remain detached from this authoritative value.
func (s *Session[S, A]) Lifecycle(thread *scope.Store) *AgentLifecycle {
	return s.agents.Value(thread)
}

const FeedLimit = 2000

func (m *Model[S, A]) Append(source S, values []Value) {
	Identify(values, source.ActivityFacts().Seq)
	m.Revision++
	m.Entries = append(m.Entries, Record[S]{source, values, m.Revision})
}
func (m *Model[S, A]) Replace(index int, source S, values []Value) {
	Identify(values, source.ActivityFacts().Seq)
	m.Entries[index] = Record[S]{Source: source, Values: values}
	m.Revise(index)
}
func (m *Model[S, A]) Mutate(index int, mutate func(*Record[S])) {
	m.Entries[index].Values = CloneValues(m.Entries[index].Values)
	mutate(&m.Entries[index])
	m.Revise(index)
}
func (m *Model[S, A]) Revise(index int) { m.Revision++; m.Entries[index].Revision = m.Revision }
func (m *Model[S, A]) Remove(from, to int) {
	m.Revision++
	m.Entries = slices.Delete(m.Entries, from, to)
	m.skills = nil
}
func (m *Model[S, A]) Latest(name string) int {
	for i, entry := range slices.Backward(m.Entries) {
		if entry.Source.ActivityFacts().Agent == name {
			return i
		}
	}
	return -1
}

// Status reports only observed activity, not host task completion.
func (m *Model[S, A]) Status(agent A) string {
	facts := agent.ActivityAgent()
	if facts.Paused {
		return "paused"
	}
	if facts.Waiting {
		return "waiting"
	}
	if facts.Responding {
		return "responding"
	}
	if latest := m.Latest(facts.Name); latest >= 0 && m.Entries[latest].Source.ActivityFacts().Kind == "error" {
		return "error"
	}
	if facts.Final {
		return "answered"
	}
	return ""
}

type RosterRow[A AgentSource] struct {
	Agent A
	Depth int
}

// Source: internal/router/live_activity_view.go:497:531@[ebde9ad5].
// Roster keeps active siblings before idle and completed work, preserving
// observation order within each state and promoting unobserved parents' children.
func (m *Model[S, A]) Roster() []RosterRow[A] {
	known := make(map[string]bool, len(m.Agents))
	for _, agent := range m.Agents {
		known[agent.ActivityAgent().Name] = true
	}
	parent := func(name string) string {
		if prefix, _, ok := strings.CutLast(name, "/"); ok && known[prefix] {
			return prefix
		}
		return ""
	}
	priorities := make(map[string]int, len(m.Agents))
	for _, agent := range m.Agents {
		facts := agent.ActivityAgent()
		priority := 1
		if facts.Responding || facts.Waiting {
			priority = 0
		} else if facts.Final {
			priority = 2
		}
		priorities[facts.Name] = priority
	}
	// Keep a completed ancestor beside its active descendants rather than
	// relegating the entire branch below later siblings.
	for _, agent := range m.Agents {
		name := agent.ActivityAgent().Name
		priority := priorities[name]
		for under := parent(name); under != ""; under = parent(under) {
			priorities[under] = min(priorities[under], priority)
		}
	}
	agents := slices.Clone(m.Agents)
	slices.SortStableFunc(agents, func(a, b A) int {
		return cmp.Compare(priorities[a.ActivityAgent().Name], priorities[b.ActivityAgent().Name])
	})
	var rows []RosterRow[A]
	var visit func(string, int)
	visit = func(under string, depth int) {
		for _, agent := range agents {
			if parent(agent.ActivityAgent().Name) == under {
				rows = append(rows, RosterRow[A]{agent, depth})
				visit(agent.ActivityAgent().Name, depth+1)
			}
		}
	}
	visit("", 0)
	return rows
}

// Identify moves stable entry and operation identity with flat group membership.
func Identify(values []Value, source uint64) {
	for i := range values {
		values[i].Source = source
		if values[i].Operation == 0 {
			values[i].Operation = i + 1
		}
		for j := range values[i].Members {
			if values[i].Members[j].Operation == values[i].Operation {
				original := values[i]
				original.Members = nil
				values[i].Members[j] = original
			}
			values[i].Members[j].Source = source
		}
	}
}

// Accept deduplicates host publications independently of display order.
func (m *Model[S, A]) Accept(seq uint64) bool {
	if seq <= m.LastSeq {
		return false
	}
	m.LastSeq = seq
	return true
}

// Trim protects live invocations and folds retired context evidence before removal.
func (m *Model[S, A]) Trim(limit int) []uint64 {
	var retired []uint64
	for i, extra := 0, len(m.Entries)-limit; extra > 0 && i < len(m.Entries); {
		if facts := m.Entries[i].Source.ActivityFacts(); facts.Running || facts.Retained {
			i++
			continue
		}
		m.retireSkills(m.Entries[i : i+1])
		retired = append(retired, m.Entries[i].Source.ActivityFacts().Seq)
		m.Remove(i, i+1)
		extra--
	}
	return retired
}

// Summary selects the latest meaningful observation, including a root-directed
// message. Earlier reasoning sections stay tied to their own agent across traffic.
type Summary struct {
	Index    int
	Earlier  []int
	Incoming bool
}

func (m *Model[S, A]) Summary(owner string, root bool) Summary {
	for i, record := range slices.Backward(m.Entries) {
		if len(record.Values) == 0 {
			continue
		}
		facts := record.Source.ActivityFacts()
		if facts.Agent == owner {
			summary := Summary{Index: i}
			if facts.Kind == "reasoning" {
				for j := i - 1; j >= 0; j-- {
					previous := m.Entries[j].Source.ActivityFacts()
					if previous.Agent != facts.Agent {
						continue
					}
					if previous.Kind != "reasoning" {
						break
					}
					summary.Earlier = append(summary.Earlier, j)
				}
			}
			return summary
		}
		if root && len(record.Values) == 1 && record.Values[0].Kind == "message" && record.Values[0].To == "/root" {
			return Summary{Index: i, Incoming: true}
		}
	}
	return Summary{Index: -1}
}
