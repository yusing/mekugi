package presentation

import (
	"reflect"
	"testing"

	"github.com/yusing/mekugi/modules/activity/feed"
)

func TestCanonicalMembershipAcrossGroups(t *testing.T) {
	first := Value{Kind: "reads", Verb: "Read", Source: 11, Operation: 1, Output: 1, Reads: []feed.Read{{Path: "a.go", Ranges: []string{"1:3"}}}}
	second := Value{Kind: "reads", Verb: "Read", Source: 12, Operation: 1, Output: 2, Reads: []feed.Read{{Path: "a.go", Ranges: []string{"7:9"}}}}
	merged := MergeReads([]Value{first, second}, []int{3, 3})
	stage := Value{Kind: "op", Verb: "Stage", Label: "`a.go` · git", Source: 13, Operation: 1}
	commit := Value{Kind: "op", Verb: "Commit", Label: "`update` · git", Source: 13, Operation: 2, Output: 1}
	folded := FoldStages([]Value{stage, commit})
	batch := Batch(append([]Value{merged}, folded...))
	if !batch.Members[2].Subsumed || batch.Members[3].Subsumed {
		t.Fatal("folded work changed summary membership")
	}
	want := [][3]int{{11, 1, 1}, {12, 1, 2}, {13, 1, 0}, {13, 2, 1}}
	if len(batch.Members) != len(want) {
		t.Fatalf("members = %#v", batch.Members)
	}
	for i, member := range batch.Members {
		if got := [3]int{int(member.Source), member.Operation, member.Output}; got != want[i] || len(member.Members) != 0 {
			t.Fatalf("member %d = %#v", i, member)
		}
	}
	if len(merged.Reads) != 1 || merged.Reads[0].Lines != 6 || len(merged.Reads[0].Ranges) != 2 || !reflect.DeepEqual(merged.Reads[0].RangeLines, []int{3, 3}) {
		t.Fatalf("read group = %#v", merged.Reads)
	}
	cloned := CloneValues([]Value{merged})
	cloned[0].Reads[0].RangeLines[0] = 99
	if merged.Reads[0].RangeLines[0] != 3 {
		t.Fatal("publication borrowed range counts")
	}
	batch.Members[0].Reads[0].Ranges[0] = "changed"
	if first.Reads[0].Ranges[0] != "1:3" || merged.Members[0].Reads[0].Ranges[0] != "1:3" {
		t.Fatal("group mutated its input")
	}
	reasoning := Reasoning([]Value{{Kind: "summary", Source: 14, Section: 0, Body: "first"}, {Kind: "summary", Source: 14, Section: 1, Body: "second"}})
	if reasoning.Members[0].Section != 0 || reasoning.Members[1].Section != 1 || reasoning.Body != "first\n\nsecond" {
		t.Fatal("reasoning lost section identity")
	}
}

func TestValueHasNoRendererOrLiveState(t *testing.T) {
	seen := make(map[reflect.Type]bool)
	var visit func(reflect.Type)
	visit = func(typ reflect.Type) {
		if seen[typ] {
			return
		}
		seen[typ] = true
		switch typ.Kind() {
		case reflect.Func, reflect.Chan, reflect.UnsafePointer:
			t.Errorf("live field type %v", typ)
		case reflect.Pointer, reflect.Slice, reflect.Array:
			visit(typ.Elem())
		case reflect.Struct:
			if typ == reflect.TypeFor[feed.Output]() || typ == reflect.TypeFor[feed.OutputView]() {
				t.Errorf("retained output in publication: %v", typ)
			}
			// Instants are values; their private location metadata is not owner state.
			if typ.PkgPath() == "time" {
				return
			}
			for field := range typ.Fields() {
				visit(field.Type)
			}
		}
	}
	visit(reflect.TypeFor[Value]())
}

func TestOperationGroupsPreserveOutcomesAndAnnotations(t *testing.T) {
	values := []Value{
		{Kind: "op", Verb: "Search", Source: 1, Exited: true, ExitCode: 2, Code: "rg needle missing", Tail: []string{"missing path"}},
		{Kind: "filter", Source: 1, Label: "output not returned to the model"},
		{Kind: "op", Verb: "Search", Source: 2, Exited: true, Code: "rg needle src", Tail: []string{"src/a.go:needle"}},
		{Kind: "op", Verb: "Search", Source: 3}, // Unknown exit remains distinct.
		{Kind: "op", Verb: "Search", Source: 4, Running: true},
		{Kind: "op", Verb: "Search", Source: 5, Skipped: true},
		{Kind: "op", Verb: "Search", Source: 6, Workdir: "/other"},
	}
	got := GroupOperations(values)
	if got[0].GroupHeader != "Search" || !got[0].GroupStart || got[1].GroupHeader != "Search" || got[1].GroupStart || got[2].GroupStart {
		t.Fatalf("shared group lost its annotation: %#v", got[:3])
	}
	for i := range got {
		if i >= 3 && got[i].GroupHeader != "" {
			t.Errorf("distinct state grouped at %d", i)
		}
		got[i].GroupHeader, got[i].GroupStart = "", false
		if !reflect.DeepEqual(got[i], values[i]) {
			t.Errorf("operation evidence changed at %d", i)
		}
	}
}
