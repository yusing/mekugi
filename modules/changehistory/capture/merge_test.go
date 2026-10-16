package capture

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/yusing/mekugi/libraries/commandanalysis"
	"github.com/yusing/mekugi/libraries/commandanalysis/execscope"
)

func TestMergeObservationsDoesNotMutateFirstMember(t *testing.T) {
	commands := []commandanalysis.Command{{Command: "first"}, {Command: "command spare"}}
	labels := []string{"shared", "label spare"}
	programs := []execscope.Program{{Label: "shared"}, {Label: "program spare"}}
	roots := []string{"/workspace/project", "/root spare"}
	files := []File{{Path: "/workspace/first"}, {Path: "/file spare"}}
	omitted := []Omission{{Path: "/workspace/omitted"}, {Path: "/omission spare"}}
	listings := []Listing{{Root: "/workspace/listed"}, {Root: "/listing spare"}}
	excluded := []string{"/workspace/excluded", "/excluded spare"}
	first := &Observation{
		Commands: commands[:1], Class: execscope.Scoped.String(), Reason: "first reason", Labels: labels[:1],
		Files: files[:1], Omitted: omitted[:1], Listings: listings[:1], Roots: roots[:1],
		Programs: programs[:1], Excluded: excluded[:1], WindowStart: time.Unix(30, 0),
	}
	second := &Observation{
		Commands: []commandanalysis.Command{{Command: "second"}}, Class: execscope.Opaque.String(), Reason: "stronger reason",
		Labels: []string{"shared", "second"}, Programs: []execscope.Program{{Label: "shared"}, {Label: "second"}},
		Roots: []string{"/workspace", "/outside"}, Files: []File{{Path: "/workspace/second"}},
		Omitted: []Omission{{Path: "/workspace/omitted-2"}}, Listings: []Listing{{Root: "/workspace/listed-2"}},
		Excluded: []string{"/workspace/excluded-2"}, WindowStart: time.Unix(20, 0), CodeMode: true,
	}
	third := &Observation{
		Commands: []commandanalysis.Command{{Command: "third"}}, Labels: []string{"third"},
		Programs: []execscope.Program{{Label: "third"}}, Roots: []string{"/workspace/child", "/last"},
		Files: []File{{Path: "/workspace/third"}}, WindowStart: time.Unix(10, 0),
	}
	firstBefore := *first
	firstBefore.Commands = append([]commandanalysis.Command(nil), first.Commands...)
	firstBefore.Labels = append([]string(nil), first.Labels...)
	firstBefore.Programs = append([]execscope.Program(nil), first.Programs...)
	firstBefore.Roots = append([]string(nil), first.Roots...)
	firstBefore.Files = append([]File(nil), first.Files...)
	firstBefore.Omitted = append([]Omission(nil), first.Omitted...)
	firstBefore.Listings = append([]Listing(nil), first.Listings...)
	firstBefore.Excluded = append([]string(nil), first.Excluded...)

	merged := Merge([]Observation{*first, *second, *third})
	if !reflect.DeepEqual(*first, firstBefore) {
		t.Fatalf("merge mutated first member: got %+v, want %+v", *first, firstBefore)
	}
	if commands[1].Command != "command spare" || labels[1] != "label spare" || programs[1].Label != "program spare" ||
		roots[1] != "/root spare" || files[1].Path != "/file spare" || omitted[1].Path != "/omission spare" ||
		listings[1].Root != "/listing spare" || excluded[1] != "/excluded spare" {
		t.Fatalf("merge overwrote spare-capacity storage in first member: commands=%+v labels=%+v programs=%+v roots=%+v files=%+v omitted=%+v listings=%+v excluded=%+v",
			commands, labels, programs, roots, files, omitted, listings, excluded)
	}
	if got, want := merged.Commands, []commandanalysis.Command{{Command: "first"}, {Command: "second"}, {Command: "third"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("commands = %+v, want %+v", got, want)
	}
	if got, want := merged.Labels, []string{"shared", "second", "third"}; !reflect.DeepEqual(got, want) {
		t.Errorf("labels = %v, want %v", got, want)
	}
	if got, want := merged.Programs, []execscope.Program{{Label: "shared"}, {Label: "second"}, {Label: "third"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("programs = %+v, want %+v", got, want)
	}
	if got, want := merged.Roots, []string{"/workspace", "/outside", "/last"}; !reflect.DeepEqual(got, want) {
		t.Errorf("roots = %v, want %v", got, want)
	}
	if got, want := merged.Files, []File{{Path: "/workspace/first"}, {Path: "/workspace/second"}, {Path: "/workspace/third"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("files = %+v, want %+v", got, want)
	}
	if got, want := merged.Omitted, []Omission{{Path: "/workspace/omitted"}, {Path: "/workspace/omitted-2"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("omitted = %+v, want %+v", got, want)
	}
	if got, want := merged.Listings, []Listing{{Root: "/workspace/listed"}, {Root: "/workspace/listed-2"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("listings = %+v, want %+v", got, want)
	}
	if got, want := merged.Excluded, []string{"/workspace/excluded", "/workspace/excluded-2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("excluded = %v, want %v", got, want)
	}
	if merged.Class != execscope.Opaque.String() || merged.Reason != "stronger reason" || !merged.CodeMode || !merged.WindowStart.Equal(time.Unix(10, 0)) {
		t.Errorf("scalar merge fields = %+v", merged)
	}
}

func TestMergedExecObservationReconcilesDuplicatePathsAndExcludesSameCellPatch(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "shared.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var budget = MaxContentBytes
	before := ReadFile(path, &budget)
	if err := os.WriteFile(path, []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	members := []Observation{{Files: []File{before}}, {Files: []File{before}}}
	merged := Merge(members)
	result := Reconcile(t.Context(), merged, Comparison{})
	reviews, complete, coverage := result.Files, result.Complete, result.Coverage
	if !complete || coverage != CoverageExact || len(reviews) != 1 || reviews[0].BeforePath != path || reviews[0].AfterPath != path {
		t.Fatalf("duplicate-path reconciliation = %+v complete=%v coverage=%q", reviews, complete, coverage)
	}

	excludedMembers := []Observation{{Files: []File{before}}, {Excluded: []string{path}}}
	excluded := Merge(excludedMembers)
	result = Reconcile(t.Context(), excluded, Comparison{})
	reviews, complete, coverage = result.Files, result.Complete, result.Coverage
	if !complete || coverage != CoverageExact || len(reviews) != 0 {
		t.Fatalf("same-cell patch exclusion = %+v complete=%v coverage=%q", reviews, complete, coverage)
	}
}
