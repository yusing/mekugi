package sessionpresentation

import (
	"testing"

	presentationcontract "github.com/yusing/mekugi/contracts/presentation"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/modules/activity/feed"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

func TestActivityPublicationReusesUnchangedInputs(t *testing.T) {
	source := &ActivitySource{Model: new(activitypresentation.Model[ActivityEntry, ActivityAgent])}
	main := &ActivitySource{Model: new(activitypresentation.Model[ActivityEntry, ActivityAgent])}
	status := "initial"
	session := &Model{status: []presentationcontract.Status{{ID: "metric", Placements: []presentationcontract.Placement{presentationcontract.AgentMetric}, Text: func(presentationcontract.Thread) string { return status }}}}
	var retention feed.Retention
	output, segment := retention.New(), retention.New()
	source.Apply(ActivityEvent{Entries: []ActivityEntry{
		{Seq: 1, Agent: "/root/worker", Kind: "tool", Text: "command", Native: &NativeItem{Output: output, Segments: []ActivitySegment{{Output: segment}}}},
		{Seq: 2, Agent: "/root/worker", Kind: "text", Text: "summary"},
	}, Agents: []ActivityAgent{{Name: "/root/worker", Thread: "worker"}}})
	main.Apply(ActivityEvent{Entries: []ActivityEntry{{Seq: 1, Agent: "Main", Kind: "text", Text: "main"}}})
	previous := source.Publication(session, main)
	for _, step := range []struct {
		name   string
		change func()
	}{
		{"output", func() { output.Write("new output") }},
		{"segment", func() { segment.Write("segment output") }},
		{"completion", func() { output.Finish(nil, new(1)) }},
		{"release", func() { segment.Release() }},
		{"roster", func() { source.Agents[0].Role = "reviewer" }},
		{"metrics", func() { source.Agents[0].OutputTokens++ }},
		{"contribution", func() { status = "updated" }},
		{"skills", func() {
			source.MarkSkillHistoryUnknown("/root/worker")
			source.RestoreSkills("/root/worker", activitypresentation.SkillSet{"skill"})
		}},
		{"review", func() {
			source.LineCounts = map[string]diffmodel.Counts{"/root/worker": {Added: 1}}
			source.NetCounts = new(diffmodel.Counts{Added: 1})
		}},
		{"review replacement", func() {
			source.LineCounts["/root/worker"] = diffmodel.Counts{Removed: 2}
			*source.NetCounts = diffmodel.Counts{Removed: 2}
		}},
		{"main summary", func() {
			main.Mutate(0, func(record *ActivityRecord) {
				record.Source.Text = "changed main"
				record.Values = ActivityContent(record.Source).Values()
			})
		}},
		{"reorder", func() { source.Reorder([]int{1, 0}) }},
		{"remove", func() { source.Remove(0, 1) }},
		{"replacement", func() { source.Model = new(activitypresentation.Model[ActivityEntry, ActivityAgent]) }},
	} {
		t.Run(step.name, func(t *testing.T) {
			if got := source.Publication(session, main); got != previous {
				t.Fatal("idle publication replaced immutable snapshot")
			}
			step.change()
			next := source.Publication(session, main)
			if next == previous {
				t.Fatal("changed input retained stale publication")
			}
			previous = next
		})
	}
}

func TestActivityIdlePublicationDoesNotProjectFeed(t *testing.T) {
	source := &ActivitySource{Model: new(activitypresentation.Model[ActivityEntry, ActivityAgent])}
	session := new(Model)
	for i := range activitypresentation.FeedLimit {
		source.Apply(ActivityEvent{Entries: []ActivityEntry{{Seq: uint64(i + 1), Agent: "Main", Kind: "text", Text: "retained"}}})
	}
	source.Publication(session, nil)
	if allocations := testing.AllocsPerRun(10, func() { source.Publication(session, nil) }); allocations != 0 {
		t.Fatalf("idle publication projected retained feed: %g allocations", allocations)
	}
}
