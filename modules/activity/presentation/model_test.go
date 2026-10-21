package presentation

import (
	"slices"
	"testing"

	"github.com/yusing/mekugi/contracts/scope"
)

type modelSource struct {
	Facts
	Body string
}

func (s modelSource) ActivityFacts() Facts { return s.Facts }

type modelAgent struct{ AgentFacts }

func (a modelAgent) ActivityAgent() AgentFacts { return a.AgentFacts }

func TestModelRecordsRetentionAndOrder(t *testing.T) {
	var model Model[modelSource, modelAgent]
	for seq := uint64(1); seq <= 4; seq++ {
		model.Append(modelSource{Seq: seq, Agent: "/root/a", Running: seq == 1, Body: "original"}, []Value{{Kind: "op", Verb: "Run"}})
	}
	model.Mutate(2, func(record *Record[modelSource]) {
		record.Source.Body = "updated"
		record.Values[0].Repeated = "not returned"
	})
	revision := model.Entries[2].Revision
	model.Reorder([]int{2, 0, 1, 3})
	if moved := model.Entries[0]; moved.Source.Seq != 3 || moved.Revision != revision || moved.Values[0].Repeated != "not returned" || moved.Source.Body != "updated" {
		t.Fatalf("reorder detached a source or revision: %+v", moved)
	}
	if retired := model.Trim(2); !slices.Equal(retired, []uint64{3, 2}) {
		t.Fatalf("retired source identities = %v", retired)
	}
	if model.Entries[0].Source.Seq != 1 || model.Entries[0].Values[0].Source != 1 || model.Entries[1].Source.Seq != 4 {
		t.Fatal("retention lost live source or stable identity")
	}
	model.Replace(1, modelSource{Seq: 4}, []Value{{Kind: "text", Body: "replacement"}})
	if model.Entries[1].Revision <= revision || model.Entries[1].Values[0].Source != 4 || model.Entries[1].Values[0].Body != "replacement" {
		t.Fatal("replacement did not revise the same record")
	}
	if !model.Accept(9) || model.Accept(9) || model.Accept(8) {
		t.Fatal("publication sequence followed display order")
	}
}

func TestModelRosterAndSummary(t *testing.T) {
	model := Model[modelSource, modelAgent]{Agents: []modelAgent{
		{Name: "/root/a", Responding: true}, {Name: "/root"}, {Name: "/root/b", Final: true}, {Name: "/missing/child"},
	}}
	rows := model.Roster()
	for i, want := range []struct {
		name  string
		depth int
	}{{"/root", 0}, {"/root/a", 1}, {"/root/b", 1}, {"/missing/child", 0}} {
		if rows[i].Agent.Name != want.name || rows[i].Depth != want.depth {
			t.Fatalf("row %d = %+v", i, rows[i])
		}
	}
	for i, kind := range []string{"reasoning", "text", "reasoning"} {
		owner := "/root/a"
		if i == 1 {
			owner = "/root/b"
		}
		model.Append(modelSource{Seq: uint64(i + 1), Agent: owner, Kind: kind}, []Value{{Kind: kind, Body: "content"}})
	}
	summary := model.Summary("/root/a", false)
	if summary.Index != 2 || !slices.Equal(summary.Earlier, []int{0}) {
		t.Fatalf("reasoning lost its owner: %+v", summary)
	}
	model.Append(modelSource{Seq: 4, Agent: "/root/b", Kind: "error"}, []Value{{Kind: "error", Body: "observed"}})
	if model.Status(model.Agents[0]) != "responding" || model.Status(model.Agents[2]) != "error" || model.Status(model.Agents[1]) != "" {
		t.Fatal("status inferred completion or hid observed error")
	}
}

func TestModelSkillsAndAttachmentLifetime(t *testing.T) {
	var first, second scope.Store
	model := &Attached[modelSource, modelAgent](&first).Children
	model.Append(modelSource{Seq: 1, Agent: "/root/a", Live: true}, []Value{{Kind: "op", Verb: "Attached skill", Label: "review"}})
	model.Trim(0)
	if !slices.Equal(model.ActiveSkills()["/root/a"], SkillSet{"review"}) {
		t.Fatal("feed retention discarded active context")
	}
	model.Append(modelSource{Seq: 2, Agent: "/root/a", ContextBoundary: true}, nil)
	// Restore follows a paginated owner's known-context flag. A live boundary wins
	// once that owner is tracked, even without host-specific source metadata.
	model.MarkSkillHistoryUnknown("/root/a")
	if model.NeedsSkillHistory("/root/a") {
		t.Fatal("live reset did not settle context")
	}
	model.RestoreSkills("/root/a", SkillSet{"old"})
	if len(model.ActiveSkills()["/root/a"]) != 0 {
		t.Fatal("older context replaced a live reset")
	}
	model.MarkSkillHistoryUnknown("/root/b")
	if !model.NeedsSkillHistory("/root/b") {
		t.Fatal("unknown context was treated as known-empty")
	}
	model.RestoreSkills("/root/b", nil)
	if model.NeedsSkillHistory("/root/b") || len(model.ActiveSkills()["/root/b"]) != 0 {
		t.Fatal("known-empty restoration was lost")
	}
	same := Attached[modelSource, modelAgent](&first)
	if &same.Children != model {
		t.Fatal("attachment duplicated its model")
	}
	if other := Attached[modelSource, modelAgent](&second); other == same || len(other.Children.Entries) != 0 {
		t.Fatal("separate attachment reused feed state")
	}
}
