package presentation

import (
	"testing"
	"time"
)

func TestCompactReloadPreservesIndependentOutcomes(t *testing.T) {
	for _, preload := range []bool{false, true} {
		for _, completed := range []bool{false, true} {
			sources := []ReloadSource{{Path: "guide.txt", Preload: preload}, {Skill: "loaded", Preload: preload}, {Skill: "denied", Preload: preload}}
			if completed {
				sources[0].ExitCode, sources[1].ExitCode, sources[2].ExitCode = new(0), new(0), new(1)
			}
			item := ItemContent{Seq: 12, Kind: "tool", Command: Command{Script: "mreload selection", Running: !completed, Exited: completed, Approval: "Approved", Tail: []string{"Reload snapshot: mread amber"}}, Details: &ItemDetails{Reload: sources, CommandText: "mreload selection", Output: 1}}
			values := item.Values()
			if len(values) != 3 {
				t.Fatalf("source rows: %+v", values)
			}
			for i, value := range values[:3] {
				if value.Verb == "Run" || len(value.Tail) > 0 || value.Output != 0 {
					t.Fatalf("internal reload command leaked into source row: %+v", value)
				}
				code := 0
				if completed && i == 2 {
					code = 1
				}
				if value.AutoReload == preload || value.Source != 12 || value.Operation != i+1 || value.Exited != completed || value.ExitCode != code || value.Running == completed {
					t.Fatalf("source %d outcome: %+v", i, value)
				}
			}
			loads := SkillLoads(Record[modelSource]{Source: modelSource{Kind: "tool"}, Values: values})
			if completed && (len(loads) != 1 || loads[0] != "loaded") || !completed && len(loads) != 0 {
				t.Fatalf("preload=%t completed=%t skill loads=%v", preload, completed, loads)
			}
		}
	}
	// Successful acquisition-process exit cannot establish a source result.
	value := (ItemContent{Kind: "tool", Command: Command{Exited: true}, Details: &ItemDetails{Reload: []ReloadSource{{Skill: "unknown"}}}}).Values()[0]
	if value.Exited {
		t.Fatal("aggregate exit invented source success")
	}
}

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
