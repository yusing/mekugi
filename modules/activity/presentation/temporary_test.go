package presentation

import (
	"fmt"
	"testing"
)

func TestFoldedEditsKeepDetailsVisible(t *testing.T) {
	t.Parallel()
	for _, change := range []struct {
		name string
		set  func(*Value)
	}{
		{"running", func(v *Value) { v.Running = true }},
		{"pending", func(v *Value) { v.Pending = true }},
		{"failed", func(v *Value) { v.ExitCode = 1 }},
		{"outcome", func(v *Value) { v.EditOutcome = "failed" }},
		{"skipped", func(v *Value) { v.Skipped = true }},
		{"approval", func(v *Value) { v.Approval = "requested" }},
		{"output", func(v *Value) { v.Output = 1 }},
		{"source", func(v *Value) { v.Code = "original command" }},
		{"directory", func(v *Value) { v.Verb, v.Label = "Delete", "`tree` • 200 files" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			values := make([]Value, 23)
			for i := range values {
				values[i] = Value{Kind: "op", Verb: "Create", Label: fmt.Sprintf("`tree/file%d` +1 -0", i), Source: 1}
			}
			change.set(&values[11])
			values = GroupOperations(values)
			count, shown := FoldedEditRun(values)
			if count != 11 || shown != 10 {
				t.Fatalf("prefix = %d/%d", shown, count)
			}
			if count, _ := FoldedEditRun(values[11:]); count != 0 {
				t.Fatal("detail-bearing row folded")
			}
			count, shown = FoldedEditRun(values[12:])
			if count != 11 || shown != 10 {
				t.Fatalf("suffix = %d/%d", shown, count)
			}
		})
	}
}
