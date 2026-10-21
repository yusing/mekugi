package presentation

import (
	"testing"
	"time"
)

func TestItemContentRetainsCompoundOutcomeAndSourceIdentity(t *testing.T) {
	item := ItemContent{
		Seq: 7, Kind: "tool", Agent: "/root", Text: "Run `false`\n\nRun `echo done`",
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
	values := (ItemContent{Seq: 3, Kind: "final", Text: text, Agent: "/root"}).Values()
	if len(values) != 1 || values[0].Body != "```go\n\tprintln(\"source\")\n```\nred" || values[0].Owner != "/root" {
		t.Fatalf("source changed: %+v", values)
	}
	attached := []Value{{Kind: "op", Verb: "Attached", Code: "retained source"}}
	values = (ItemContent{Seq: 9, Kind: "attachments", Details: &ItemDetails{Attachments: attached}}).Values()
	if values[0].Source != 9 || attached[0].Source != 0 {
		t.Fatal("publication mutated admitted source")
	}
	questions := []Question{{Text: "Continue?", State: "pending"}}
	values = (ItemContent{Kind: "question", Text: "Continue?", Details: &ItemDetails{Questions: questions}}).Values()
	values[0].Questions[0].State = "answered"
	if questions[0].State != "pending" {
		t.Fatal("publication shared mutable questions")
	}
}

func TestUserShellRetainsCommandAlongsideCapturedEdits(t *testing.T) {
	command := Command{UserShell: true, Display: "printf changed > file; false", Script: "printf changed > file; false",
		CaptureText: "Edit `file` · +1 −0 · bash", Exited: true, ExitCode: 2, Duration: time.Second,
		Segments: []CommandSegment{{Source: "printf changed > file", Writer: true}, {Source: "false", ExitCode: 2}}}
	values := (ItemContent{Seq: 1, Kind: "tool", Command: command, Details: &ItemDetails{CommandText: command.Script}}).Values()
	values = CommandExit(values, 2, []string{"retained failure"}, 3)
	if len(values) != 2 || !values[0].UserShell || values[0].Code != command.Display || values[0].ExitCode != 2 || values[0].Duration != time.Second ||
		len(values[0].Tail) != 1 || values[0].TailOmitted != 3 || values[1].EditPath() != "file" || values[1].Exited {
		t.Fatalf("capture replaced user source/output or inherited its exit: %+v", values)
	}
	pages := RefreshDetails(DetailPages(values[:1]), map[uint64]DetailSource{1: {Entry: Entry{Values: values}, Command: &command}})
	if len(pages) != 1 || pages[0].Code != command.Display || !pages[0].UserShell {
		t.Fatalf("details replaced the user command with classified segments: %+v", pages)
	}
}
