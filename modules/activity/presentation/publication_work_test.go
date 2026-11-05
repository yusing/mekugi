package presentation

import (
	"fmt"
	"testing"
)

type countedSource struct {
	Facts
	reads *int
}

func (s countedSource) ActivityFacts() Facts { *s.reads++; return s.Facts }

func TestPublicationWorkTracksChangedRecords(t *testing.T) {
	reads := 0
	var model Model[countedSource, modelAgent]
	for i := range 50 {
		model.Agents = append(model.Agents, modelAgent{Name: fmt.Sprintf("/root/worker%d", i)})
	}
	for i := range FeedLimit {
		model.Append(countedSource{Facts: Facts{Seq: uint64(i + 1), Agent: model.Agents[i%50].Name, Kind: "reasoning"}, reads: &reads}, []Value{{Kind: "summary", Label: "Checking"}})
	}
	reads = 0
	first := model.Publish()
	if reads != FeedLimit {
		t.Fatalf("initial publication read %d facts, want %d", reads, FeedLimit)
	}
	reads = 0
	model.Publish()
	if reads != 0 {
		t.Fatalf("idle publication read %d retained facts", reads)
	}
	model.Mutate(FeedLimit-1, func(record *Record[countedSource]) { record.Values[0].Label = "Updated" })
	last := model.Publish()
	if reads != 1 || &first.Entries[0].Values[0] != &last.Entries[0].Values[0] || first.Entries[FeedLimit-1].Values[0].Label != "Checking" {
		t.Fatalf("single update read %d facts or replaced unchanged content", reads)
	}
	if summary := model.Summary(model.Agents[49].Name, false); summary.Index != FeedLimit-1 || len(summary.Earlier) != FeedLimit/50-1 {
		t.Fatalf("indexed reasoning lost earlier sections: %+v", summary)
	}
	t.Logf("ActivityFacts calls for %d entries / 50 agents: initial=%d idle=0 one update=%d", FeedLimit, FeedLimit, reads)
}
