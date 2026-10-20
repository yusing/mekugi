package activity

import "testing"

func TestMergeReadGroupsRetainsIndependentFirstMembers(t *testing.T) {
	read := func(source uint64) Block {
		return Block{Source: source, Kind: "reads", Verb: "Read", Reads: []Read{{Path: "file.go", Ranges: []string{"1:2"}}}}
	}
	input := []Block{read(1), read(2), {Kind: "op", Verb: "Run", Source: 3}, read(4), read(5)}
	groups := MergeLiveActivityReads(input)
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
