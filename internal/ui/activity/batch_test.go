package activity

import "testing"

func TestCollapseBatchKeepsUnsettledOrSingleWork(t *testing.T) {
	read := Block{Kind: "reads", Verb: "Read", Reads: []Read{{Path: "a.go"}}}
	run := Block{Kind: "op", Verb: "Run", Code: "go test"}
	for name, blocks := range map[string][]Block{
		"single":         {read, {Kind: "filter"}},
		"running":        {read, func() Block { b := run; b.Running = true; return b }()},
		"skipped":        {read, func() Block { b := run; b.Skipped = true; return b }()},
		"approval":       {read, func() Block { b := run; b.Approval = "Approved"; return b }()},
		"requested edit": {read, {Kind: "op", Verb: "Edit", EditSource: "cat (requested)"}},
		"question":       {read, {Kind: "op", Verb: "Asked", Questions: []Question{{Text: "Continue?"}}}},
		"message":        {read, {Kind: "text", Body: "note"}},
		"attach":         {read, {Kind: "op", Verb: "Attach failed", Path: "b.go", Label: "too large"}},
	} {
		if _, ok := CollapseBatch(blocks); ok {
			t.Errorf("%s: collapsed", name)
		}
	}
	merged := read
	merged.Members = []Block{read, read}
	batch, ok := CollapseBatch([]Block{merged, run, {Kind: "op", Verb: "Edit", EditSource: "apply_patch"}, {Kind: "op", Verb: "Run", Label: "shell batch", BatchExit: true}})
	if !ok || len(batch.Members) != 5 || batch.Members[3].EditSource != "apply_patch" {
		t.Fatalf("batch = %+v, %v; want merged reads and the batch result as dialog pages", batch, ok)
	}
}
