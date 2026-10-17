package presentation

import (
	"testing"
	"time"
)

func TestItemContentRetainsCompoundOutcomeAndSourceIdentity(t *testing.T) {
	item := ItemContent{
		Facts: Facts{Seq: 7, Kind: "tool", Agent: "/root", Text: "Run `false`\n\nRun `echo done`"},
		Command: Command{Script: "false; echo done", Display: "false; echo done", Exited: true, ExitCode: 2,
			Duration: time.Second, Approval: "Approved", Tail: []string{"failure"}},
		Details: &ItemDetails{CommandText: "false; echo done", Output: 1},
	}
	values := item.Values()
	if len(values) != 3 {
		t.Fatalf("compound values: %+v", values)
	}
	for _, value := range values {
		if value.Source != 7 || value.Operation == 0 {
			t.Fatalf("missing identity: %+v", value)
		}
		if value.BatchExit {
			if !value.Exited || value.ExitCode != 2 || value.Duration != time.Second || value.Approval != "Approved" || value.Output != 1 {
				t.Fatalf("lost aggregate facts: %+v", value)
			}
		} else if value.Exited || value.Duration != 0 || value.Output != 0 || value.Approval != "" {
			t.Fatalf("aggregate facts attributed to inferred operation: %+v", value)
		}
	}
}

func TestItemContentPreservesAuthoredSourceAndDetachedDetails(t *testing.T) {
	text := "```go\n\tprintln(\"source\")\n```\n\x1b[31mred\x1b[0m"
	values := (ItemContent{Facts: Facts{Seq: 3, Kind: "final", Text: text, Agent: "/root"}}).Values()
	if len(values) != 1 || values[0].Body != "```go\n\tprintln(\"source\")\n```\nred" || values[0].Owner != "/root" {
		t.Fatalf("source changed: %+v", values)
	}
	attached := []Value{{Kind: "op", Verb: "Attached", Code: "retained source"}}
	values = (ItemContent{Facts: Facts{Seq: 9, Kind: "attachments"}, Details: &ItemDetails{Attachments: attached}}).Values()
	if values[0].Source != 9 || attached[0].Source != 0 {
		t.Fatal("publication mutated admitted source")
	}
	questions := []Question{{Text: "Continue?", State: "pending"}}
	values = (ItemContent{Facts: Facts{Kind: "question", Text: "Continue?"}, Details: &ItemDetails{Questions: questions}}).Values()
	values[0].Questions[0].State = "answered"
	if questions[0].State != "pending" {
		t.Fatal("publication shared mutable questions")
	}
}
