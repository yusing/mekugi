package terminalview

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	"github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestMergeReadGroupsRetainsIndependentFirstMembers(t *testing.T) {
	read := func(source uint64) Block {
		return Block{Source: source, Kind: "reads", Verb: "Read", Reads: []activitymodel.Read{{Path: "file.go", Ranges: []string{"1:2"}}}}
	}
	input := []Block{read(1), read(2), {Kind: "op", Verb: "Run", Source: 3}, read(4), read(5)}
	groups := MergeTargets(input)
	if len(groups) != 3 {
		t.Fatalf("groups=%d, want 3", len(groups))
	}
	for _, group := range []struct {
		index int
		first uint64
	}{{0, 1}, {2, 4}} {
		members := groups[group.index].Members
		if len(members) != 2 || members[0].Source != group.first || members[1].Source != group.first+1 {
			t.Fatal("group borrowed another group's first invocation")
		}
		groups[group.index].Reads[0].Ranges[0] = "changed"
		members[0].Reads[0].Path = "changed"
	}
	for _, i := range []int{0, 1, 3, 4} {
		if input[i].Reads[0].Path != "file.go" || input[i].Reads[0].Ranges[0] != "1:2" {
			t.Fatal("group mutated shared parsed evidence")
		}
	}
}

func TestUISnapshotMergedReadRanges(t *testing.T) {
	var blocks []Block
	for i, command := range []string{"timeout 10s sed -n '1,9p' file.go", "timeout 90s sed -n '20,40p' file.go"} {
		values := presentation.Operations(presentation.ShellSource(command, "/workspace"))
		facts := presentation.Command{Exited: true, Started: time.Unix(100, 0), Duration: time.Duration(i+1) * time.Second}
		facts.ApplyFacts(values)
		value := values[0]
		value.Source, value.Output, value.Workdir = uint64(i+1), i+1, "/workspace"
		value.Tail, value.TailOmitted = []string{"last line"}, i+1
		blocks = append(blocks, Block{Value: value, Collapsed: true})
	}
	merged := MergeTargets(blocks)
	if len(merged) != 1 || merged[0].Duration != 3*time.Second {
		t.Fatalf("timed reads did not compact: %+v", merged)
	}
	p := Painter{}
	rows := p.Block(merged[0], 80)
	uisnapshot.Assert(t, "testdata/snapshots/merged_read_ranges.txt", strings.Join(rows, "\n")+"\n")
	for i, original := range presentation.Originals(merged[0].Value) {
		if original.Source != uint64(i+1) || original.Output != i+1 || original.Duration != time.Duration(i+1)*time.Second || original.Reads[0].Ranges[0] != blocks[i].Reads[0].Ranges[0] {
			t.Fatalf("original read identity changed: %+v", original)
		}
		if page := p.DialogPage(Block{Value: original}, 80); !strings.Contains(page.Text, "last line") {
			t.Fatal("original dialog output missing")
		}
	}
	for _, boundary := range []string{"workdir", "failure", "streaming", "unknown"} {
		t.Run(boundary, func(t *testing.T) {
			second := blocks[1]
			switch boundary {
			case "workdir":
				second.Workdir = "/other"
			case "failure":
				second.ExitCode = 1
			case "streaming":
				second.Running = true
			case "unknown":
				second.Exited = false
			}
			if got := MergeTargets([]Block{blocks[0], second}); len(got) != 2 {
				t.Fatalf("merged across %s boundary", boundary)
			}
		})
	}
}

func TestMergedReadRangesKeepUnattributedCounts(t *testing.T) {
	values := presentation.Operations(presentation.ShellSource("sed -n '1,9p;20,40p' file.go", "/workspace"))
	first := Block{Value: values[0], Collapsed: true}
	first.Tail = []string{"one", "two", "three"}
	second := Block{Content: activitymodel.ParseOperation("Read `file.go 50:60`"), Tail: []string{"four"}, Collapsed: true}
	merged := MergeTargets([]Block{first, second})
	p := Painter{}
	rows := strings.Join(p.Block(merged[0], 80), "\n")
	if !strings.Contains(ansi.Strip(rows), "L1–9, L20–40, L50–60 (4 lines)") {
		t.Fatalf("unattributed output gained range counts: %s", rows)
	}
}
