package presentation

import (
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/yusing/mekugi/modules/activity/feed"
)

func TestPublicationPreservesFactsAndDetachesValues(t *testing.T) {
	now := time.Unix(100, 0)
	facts := Facts{
		Seq: 42, Agent: "/root/a", Kind: "tool", Text: "exact\ntext",
		Observed: now, Completed: now.Add(time.Second), Spans: []TextSpan{{Start: 0, End: 5, Kind: FileToken}},
		InputGroup: "input-1", Question: 12, Activity: 13, AssignmentTo: "/root/b", AssignmentText: "task",
		Running: true, Live: true, Restored: true, Unconfirmed: true, ContextBoundary: true,
		Suppressed: true, RosterOnly: true, OutputFailed: true, Outputs: map[int]int{1: 7, 2: 8},
	}
	var model Model[modelSource, modelAgent]
	model.Accept(42)
	model.Append(modelSource{Facts: facts}, []Value{{
		Content: feed.Content{Kind: "reads", Reads: []feed.Read{{Path: "a.go", Ranges: []string{"1:3"}}}},
		Output:  2, Members: []Value{{Operation: 9, Output: 1, Content: feed.Content{
			Kind: "reads", Reads: []feed.Read{{Path: "b.go", Ranges: []string{"4:6"}}},
		}}},
	}})
	published := model.Publish()
	if len(published.Entries) != 1 || published.LastSeq != 42 {
		t.Fatalf("publication identity = %+v", published)
	}
	entry := published.Entries[0]
	if !reflect.DeepEqual(entry.Facts, facts) || entry.Revision != model.Entries[0].Revision {
		t.Fatalf("facts or record revision changed: %+v", entry)
	}
	model.Mutate(0, func(record *Record[modelSource]) {
		record.Source.Spans[0].End = 4
		record.Values[0].Output = 99
		record.Values[0].Reads[0].Ranges[0] = "changed"
		record.Values[0].Members[0].Output = 98
		record.Values[0].Members[0].Reads[0].Ranges[0] = "changed member"
	})
	model.Replace(0, modelSource{Facts: Facts{Seq: 43}}, []Value{{Content: feed.Content{Kind: "text", Body: "replacement"}}})
	value := entry.Values[0]
	if value.Source != 42 || value.Operation != 1 || value.Output != 2 || value.Reads[0].Ranges[0] != "1:3" ||
		value.Members[0].Source != 42 || value.Members[0].Operation != 9 || value.Members[0].Output != 1 ||
		value.Members[0].Reads[0].Ranges[0] != "4:6" || entry.Spans[0].End != 5 {
		t.Fatalf("model mutation rewrote publication: %+v", entry)
	}
}

func TestPublicationRoster(t *testing.T) {
	model := Model[modelSource, modelAgent]{Agents: []modelAgent{
		{AgentFacts: AgentFacts{Name: "/root/a", Role: "worker", Responding: true}},
		{AgentFacts: AgentFacts{Name: "/root"}},
		{AgentFacts: AgentFacts{Name: "/root/b", Final: true}},
		{AgentFacts: AgentFacts{Name: "/missing/child", Final: true}},
	}}
	model.Append(modelSource{Facts: Facts{Seq: 1, Agent: "/root/a", Kind: "tool"}}, []Value{{Content: feed.Content{Kind: "op", Verb: "Attached skill", Label: "review"}}})
	model.Append(modelSource{Facts: Facts{Seq: 2, Agent: "/root/a", Kind: "reasoning"}}, []Value{{Content: feed.Content{Kind: "text", Body: "latest"}}})
	model.Append(modelSource{Facts: Facts{Seq: 3, Agent: "/root/b", Kind: "error"}}, []Value{{Content: feed.Content{Kind: "error", Body: "failed"}}})
	rows := model.Publish().Roster
	wantNames := []string{"/root", "/root/a", "/root/b", "/missing/child"}
	wantDepths := []int{0, 1, 1, 0}
	wantStatuses := []string{"", "responding", "error", "answered"}
	if len(rows) != len(wantNames) {
		t.Fatalf("roster length = %d", len(rows))
	}
	for i, row := range rows {
		if row.Agent.Name != wantNames[i] || row.Depth != wantDepths[i] || row.Agent.Status != wantStatuses[i] {
			t.Fatalf("row %d = %+v", i, row)
		}
	}
	agent := rows[1].Agent
	if agent.Role != "worker" || agent.LatestKind != "reasoning" || !slices.Equal(agent.Skills, SkillSet{"review"}) ||
		len(agent.Summary) != 1 || agent.Summary[0].Seq != 2 || agent.Summary[0].Values[0].Body != "latest" {
		t.Fatalf("roster lost neutral content: %+v", agent)
	}
	model.ActiveSkills()["/root/a"][0] = "changed"
	if agent.Skills[0] != "review" {
		t.Fatal("publication shares mutable skill cache")
	}
}

func TestPublicationMainSummaryOrderAndIncomingOwner(t *testing.T) {
	var model Model[modelSource, modelAgent]
	for i, owner := range []string{"/root", "/root/a", "/root"} {
		model.Append(modelSource{Facts: Facts{Seq: uint64(i + 1), Agent: owner, Kind: "reasoning"}}, []Value{{Content: feed.Content{Kind: "text"}, Owner: owner}})
	}
	summary := model.SummaryEntries("/root", true)
	if len(summary) != 2 || summary[0].Seq != 1 || summary[1].Seq != 3 {
		t.Fatalf("Main reasoning order = %+v", summary)
	}
	model.Append(modelSource{Facts: Facts{Seq: 4, Agent: "/root/a", Kind: "message"}}, []Value{{Content: feed.Content{Kind: "message", Body: "incoming"}, Owner: "/root/a", To: "/root"}})
	incoming := model.SummaryEntries("/root", true)
	if len(incoming) != 1 || incoming[0].Seq != 4 || incoming[0].Agent != "/root/a" || incoming[0].Values[0].Owner != "/root" ||
		incoming[0].Values[0].Body != "incoming" || model.Entries[3].Values[0].Owner != "/root/a" {
		t.Fatalf("incoming summary lost sender or mutated source: %+v", incoming)
	}
}
