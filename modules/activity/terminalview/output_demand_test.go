package terminalview

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi/modules/activity/feed"
	"github.com/yusing/mekugi/modules/activity/presentation"
)

func TestOutputContextDemandBoundsAndExpansion(t *testing.T) {
	output := new(feed.Retention).New()
	output.Finish(new(strings.Repeat("retained context\n", 20000)+"var answer = 42"), new(0))
	v := NewView()
	v.Update(&presentation.Publication{Entries: []presentation.Entry{{Seq: 7, Outputs: map[int]int{1: output.Version()}}}}, Display{})
	block := Block{Kind: "op", Source: 7, Output: 1, Tail: []string{"var answer = 42"}, TailOmitted: 20000}
	for _, excluded := range []string{"layout", "collapsed", "empty", "changes"} {
		candidate := block
		v.Painter.LayoutOnly = excluded == "layout"
		switch excluded {
		case "collapsed":
			candidate.Collapsed = true
		case "empty":
			candidate.Tail = nil
		case "changes":
			candidate.Changes = []presentation.ChangeRow{{Label: "a.go"}}
		}
		if got := v.outputContext(candidate); got.OutputSnapshot != nil || len(v.OutputDemands()) != 0 {
			t.Fatalf("%s requested retained syntax context", excluded)
		}
	}
	v.Painter.LayoutOnly = false
	v.outputContext(block)
	v.outputContext(block)
	want := presentation.OutputDemand{Source: 7, Output: 1, Version: output.Version(), Bytes: OutputContextBytes}
	if got := v.OutputDemands(); !slices.Equal(got, []presentation.OutputDemand{want}) || len(v.OutputDemands()) != 0 {
		t.Fatalf("context demand was not deduplicated and drained: %+v", got)
	}
	context, limited := output.Context(OutputContextBytes)
	v.Hydrate([]presentation.OutputResult{{Demand: want, View: context, Limited: limited}})
	got := v.outputContext(block)
	if got.OutputSnapshot == nil || !got.OutputContextLimited || len(got.OutputSnapshot.Lines) == 0 ||
		len(got.OutputSnapshot.Lines) >= 20000 || len(strings.Join(got.OutputSnapshot.Lines, "\n")) > OutputContextBytes ||
		!reflect.ValueOf(output).Elem().FieldByName("snapshot").IsNil() {
		t.Fatal("feed context was unbounded or populated the full retained snapshot")
	}
	if !slices.Equal(got.Tail, block.Tail) || got.TailOmitted != block.TailOmitted || len(v.OutputDemands()) != 0 {
		t.Fatal("hydration changed the observed tail or requested cached context again")
	}
	v.Expansion = 2
	v.discloseBlock(block)
	want.Bytes = 0
	if got := v.OutputDemands(); !slices.Equal(got, []presentation.OutputDemand{want}) {
		t.Fatalf("bounded context satisfied a full expansion: %+v", got)
	}
	full := output.View()
	v.Hydrate([]presentation.OutputResult{{Demand: want, View: full}})
	expanded := v.discloseBlock(block)
	if !expanded.Expanded || expanded.Collapsed || expanded.TailOmitted != 0 || !slices.Equal(expanded.Tail, full.Lines) || len(expanded.Tail) != 20001 {
		t.Fatal("expansion did not consume the full scoped result")
	}
}

func TestOutputDemandCacheIdentityAndVersion(t *testing.T) {
	v := NewView()
	publication := presentation.Publication{Entries: []presentation.Entry{
		{Seq: 7, Outputs: map[int]int{1: 1, 2: 3}},
		{Seq: 8, Outputs: map[int]int{1: 1}},
	}}
	v.Update(new(publication), Display{})
	keys := []presentation.OutputDemand{{Source: 7, Output: 1, Version: 1, Bytes: OutputContextBytes}, {Source: 7, Output: 2, Version: 3, Bytes: OutputContextBytes}, {Source: 8, Output: 1, Version: 1, Bytes: OutputContextBytes}}
	for i, key := range keys {
		v.Hydrate([]presentation.OutputResult{{Demand: key, View: feed.OutputView{Lines: []string{string(rune('a' + i))}}}})
	}
	for i, key := range keys {
		result, ok := v.output(Block{Source: key.Source, Output: key.Output}, OutputContextBytes)
		if !ok || result.View.Lines[0] != string(rune('a'+i)) {
			t.Fatalf("cache conflated source/output identity: %+v", key)
		}
	}
	publication.Entries = slices.Clone(publication.Entries)
	publication.Entries[0].Outputs = map[int]int{1: 2, 2: 3}
	v.Update(new(publication), Display{})
	v.Hydrate([]presentation.OutputResult{{Demand: keys[0], View: feed.OutputView{Lines: []string{"stale"}}}})
	block := Block{Source: 7, Output: 1}
	if _, ok := v.output(block, OutputContextBytes); ok {
		t.Fatal("old version survived update or stale hydration")
	}
	newKey := presentation.OutputDemand{Source: 7, Output: 1, Version: 2, Bytes: OutputContextBytes}
	if got := v.OutputDemands(); !slices.Equal(got, []presentation.OutputDemand{newKey}) {
		t.Fatalf("new version demand = %+v", got)
	}
	newKey.Bytes = 0
	v.Hydrate([]presentation.OutputResult{{Demand: newKey, View: feed.OutputView{Lines: []string{"new full"}}}})
	if result, ok := v.output(block, OutputContextBytes); !ok || result.View.Lines[0] != "new full" || len(v.OutputDemands()) != 0 {
		t.Fatal("full cached output did not satisfy context")
	}
	for _, key := range keys[1:] {
		if _, ok := v.output(Block{Source: key.Source, Output: key.Output}, OutputContextBytes); !ok {
			t.Fatalf("version update discarded unrelated output: %+v", key)
		}
	}
	v.Update(&presentation.Publication{}, Display{})
	if _, ok := v.output(block, 0); ok || len(v.outputCache) != 0 || len(v.OutputDemands()) != 0 {
		t.Fatal("removed source retained cache or requested unauthorized output")
	}
}
