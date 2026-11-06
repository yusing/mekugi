package presentation

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/modules/activity/feed"
)

func TestCatWriteRequiresObservedOutcome(t *testing.T) {
	for _, source := range []string{"cat > file <<'EOF'\nbody\nEOF", `cat >> "$fixture_dir/main.js"`, `env X=1 cat > file`, `bash -c 'cat > file'`, `timeout 5 cat > file`, `mrun -n 5 cat > file`} {
		for _, tracked := range []bool{false, true} {
			for _, state := range []string{"pending", "running", "completed", "failed", "captured"} {
				t.Run(source+"/"+state+fmt.Sprint(tracked), func(t *testing.T) {
					text := ShellSource(source, "")
					if text != "" {
						t.Fatalf("source created an operation before execution: %q", text)
					}
					command := Command{Source: 1, Script: source, Display: source, Text: text, Running: state == "running", Exited: state == "completed" || state == "failed" || state == "captured"}
					if state == "failed" {
						command.ExitCode, command.Tail = 1, []string{"write failed"}
					}
					if tracked {
						command.Segments = []CommandSegment{{Source: source, Display: source, Text: text, Running: command.Running, Pending: state == "pending", ExitCode: command.ExitCode, Tail: command.Tail, Writer: true}}
					}
					if state == "captured" {
						command.Captured, command.CaptureText = true, "Create `file` · +1 -0 · cat"
					}
					item := ItemContent{Seq: 1, Kind: "tool", Text: text, Command: command, Details: &ItemDetails{CommandText: source}}
					values := item.Values()
					if state == "pending" || state == "running" {
						if len(values) != 0 {
							t.Fatalf("interim cat row: %+v", values)
						}
						return
					}
					if state == "captured" && tracked && len(values) == 2 && values[0].Exited && !values[0].Running {
						values = values[1:] // Settled source action retains capture details.
					}
					if len(values) != 1 || values[0].Verb == "Run" || values[0].Requested || strings.Contains(values[0].EditSource, "requested") {
						t.Fatalf("completed cat row: %+v", values)
					}
					if state == "failed" && (!values[0].Exited || values[0].ExitCode != 1 || len(values[0].Tail) == 0) {
						t.Fatalf("lost failure: %+v", values)
					}
				})
			}
		}
	}
}

func TestTrackedCatRetainedOutputDoesNotCreateInterimRun(t *testing.T) {
	source := CommandSource{Script: "cat > file <<'EOF'\nbody\nEOF", Display: "cat > file <<'EOF'\nbody\nEOF"}
	invocation := NewInvocation(source, time.Unix(100, 0), new(feed.Retention))
	segment := SegmentObservation{Source: source.Script, Analysis: source.Display, Text: ShellSource(source.Script, ""), Began: true}
	values := func() []Value {
		command := invocation.Snapshot()
		command.Source, command.Text = 1, ShellSource(source.Script, "")
		for _, segment := range invocation.ReportSnapshot(false).Segments {
			command.Segments = append(command.Segments, segment.CommandSegment)
		}
		return (ItemContent{Seq: 1, Kind: "tool", Text: command.Text, Command: command, Details: &ItemDetails{CommandText: source.Script}}).Values()
	}
	invocation.ObserveSegments(SegmentReport{Segments: []SegmentObservation{segment}})
	for _, output := range []string{"", "cat: write failed\n"} {
		invocation.Write(output)
		if got := values(); len(got) != 0 {
			t.Fatalf("retained output created an interim cat row: %+v", got)
		}
	}
	segment.Finished, segment.ExitCode = true, 1
	invocation.ObserveSegments(SegmentReport{Ended: true, Done: true, Closed: true, ExitCode: 1, Segments: []SegmentObservation{segment}})
	invocation.Complete(CommandCompletion{Source: source, Exit: new(1), Aggregate: new("cat: write failed\n")})
	if got := values(); len(got) != 1 || got[0].Verb != "Edit" || got[0].EditOutcome != "failed" || got[0].Output != 1 || got[0].ExitCode != 1 {
		t.Fatalf("completed cat lost failure or retained output: %+v", got)
	}
	if got := strings.Join(invocation.Output().View().Lines, "\n"); got != "cat: write failed" {
		t.Fatalf("suppression discarded output: %q", got)
	}
}

func TestOrderEditDetailsMoveAndDirectory(t *testing.T) {
	for _, tc := range []struct {
		name, row string
		page      DetailPage
	}{
		{"captured move", "Move `src/{old=>new}` +1 -1", DetailPage{Path: "src/new", DisplayPath: "src/{old=>new}"}},
		{"host move", "Move `src/{old=>new}` +1 -1", DetailPage{Path: "src/{old=>new}", SyntaxPath: "src/new"}},
		{"directory", "Delete `dir/` • 2 files", DetailPage{Path: "dir/a"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := []Value{{Content: feed.ParseOperation(tc.row)}, {Content: feed.ParseOperation("Edit `b` +1 -1")}}
			pages := []DetailPage{{Path: "b"}, tc.page}
			got := OrderEditDetails(pages, values)
			if got[0].Path != tc.page.Path {
				t.Fatalf("order: %+v", got)
			}
		})
	}
}

func TestCompactReloadPreservesIndependentOutcomes(t *testing.T) {
	for _, preload := range []bool{false, true} {
		for _, completed := range []bool{false, true} {
			sources := []ReloadSource{{Path: "guide.txt", Preload: preload}, {Skill: "loaded", Preload: preload}, {Skill: "denied", Preload: preload}, {Changes: "amber1..3", Preload: preload}}
			if completed {
				sources[0].ExitCode, sources[1].ExitCode, sources[2].ExitCode, sources[3].ExitCode = new(0), new(0), new(1), new(0)
			}
			item := ItemContent{Seq: 12, Kind: "tool", Command: Command{Script: "mreload selection", Running: !completed, Exited: completed, Approval: "Approved", Tail: []string{"Reload snapshot: mread amber"}}, Details: &ItemDetails{Reload: sources, CommandText: "mreload selection", Output: 1}}
			values := item.Values()
			if len(values) != 4 {
				t.Fatalf("source rows: %+v", values)
			}
			entry := Entry{Facts: item.Facts, Values: values}
			if entry.Hidden() || !entry.Standalone() {
				t.Fatal("internal transport suppression hid acquired source attachments")
			}
			if values[3].Verb != "Attached changes" || values[3].Label != "amber1..3" {
				t.Fatalf("change identity lost: %+v", values[3])
			}
			for i, value := range values {
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
		CaptureText: "Edit `file` · +1 −0 · bash", Exited: true, ExitCode: 2, Duration: time.Second, Approval: "Approved",
		Segments: []CommandSegment{{Source: "printf changed > file", Writer: true}, {Source: "false", ExitCode: 2}}}
	values := (ItemContent{Seq: 1, Kind: "tool", Command: command, Details: &ItemDetails{CommandText: command.Script}}).Values()
	values = CommandExit(values, command.Display, 2, []string{"retained failure"}, 3, false)
	if len(values) != 2 || !values[0].UserShell || values[0].Code != command.Display || values[0].ExitCode != 2 || values[0].Duration != time.Second ||
		len(values[0].Tail) != 1 || values[0].TailOmitted != 3 || values[0].Approval != "Approved" || values[1].EditPath() != "file" || values[1].Exited {
		t.Fatalf("capture replaced user source/output or inherited its exit: %+v", values)
	}
	pages := RefreshDetails(DetailPages(values[:1]), map[uint64]DetailSource{1: {Entry: Entry{Values: values}, Command: &command}})
	if len(pages) != 1 || pages[0].Code != command.Display || !pages[0].UserShell {
		t.Fatalf("details replaced the user command with classified segments: %+v", pages)
	}
}

func TestCapturedApprovalKeepsSingleAction(t *testing.T) {
	const source = "sed -i s/old/new/ file"
	for _, outcome := range []string{"Pending Approval", "Approved", "Denied\nCommand: " + source} {
		command := Command{Script: source, Display: source, Captured: true, CaptureText: "Edit `file` · +1 −1 · sed", Approval: outcome,
			Segments: []CommandSegment{{Source: source, Text: ShellSource(source, ""), Writer: true, Duration: time.Second}}}
		values := (ItemContent{Seq: 7, Kind: "tool", Command: command, Details: &ItemDetails{CommandText: source}}).Values()
		if len(values) != 2 || values[0].Code != source || values[0].Approval != outcome || values[0].BatchExit || !values[1].Effect || values[1].Approval != "" || values[1].Source != 7 {
			t.Fatalf("capture created another approval action: %+v", values)
		}
	}
}

func TestAgentTrafficInterpretation(t *testing.T) {
	const caller, child, peer = "/root/worker", "/root/worker/reviewer", "/root/other"
	for _, test := range []struct {
		name, kind, from, to, outcome, owner, wantKind, wantDetail, wantPeer string
		incoming, external                                                   bool
	}{
		{"start", "start", "Main", child, "", caller, "start", "started", child, false, false},
		{"assignment", "assignment", "Main", child, "", caller, "message", "assignment", child, false, false},
		{"outgoing", "reply", "Main", peer, "", caller, "message", "", peer, false, false},
		{"incoming", "reply", child, "Main", "", caller, "message", "", child, true, false},
		{"root incoming", "reply", "/root", "Main", "", caller, "message", "", "/root", true, false},
		{"external", "reply", child, peer, "", caller, "message", "", "", true, true},
		{"completed", "final", child, "Main", "completed", caller, "final", "finished", child, true, false},
		{"interrupted", "final", child, "Main", "interrupted", caller, "interrupted", "interrupted", child, true, false},
		{"failed", "final", child, "Main", "failed", caller, "error", "failed", child, true, false},
		{"own answer", "final", child, "Main", "completed", child, "", "", "", false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			item := ItemContent{Facts: Facts{Kind: test.kind, Agent: test.from, Outcome: test.outcome}}
			directed := &DirectedContent{From: test.from, To: test.to, Text: "Authored message"}
			label := "review-correctness"
			item.Message, item.Assignment, item.StartLabel = directed, directed, &label
			item.Facts.Assignment = test.kind == "assignment" || test.kind == "start"
			values := item.Values()
			entry := Entry{Facts: item.Facts, Values: values}
			traffic := entry.Traffic(values[0], caller, test.owner)
			if test.wantKind == "" {
				if traffic == nil || !traffic.OwnAnswer || traffic.Kind != "final" {
					t.Fatalf("own answer lost its completion semantics: %+v", traffic)
				}
				return
			}
			if traffic == nil {
				t.Fatal("missing traffic")
			}
			if traffic.Kind != test.wantKind || traffic.Detail != test.wantDetail || traffic.Peer != test.wantPeer || traffic.Incoming != test.incoming || traffic.External != test.external {
				t.Fatalf("traffic = %+v", traffic)
			}
			if traffic.From == "Main" || traffic.To == "Main" {
				t.Fatalf("unresolved runtime address: %+v", traffic)
			}
		})
	}
}

func TestCompletionExchangeContext(t *testing.T) {
	const peer = "/root/worker"
	assignment := Entry{Facts: Facts{Seq: 1, Kind: "assignment", AssignmentTo: peer}}
	answer := Entry{Facts: Facts{Seq: 2, Agent: peer, Kind: "final"}, Values: []Value{{Kind: "final", From: peer, To: "Main"}}}
	traffic := answer.Traffic(answer.Values[0], "/root", "/root")
	if !traffic.SameRoute(traffic) || traffic.SameRoute(&Traffic{From: "/root", To: peer}) {
		t.Fatal("route identity lost direction")
	}
	delivered := answer
	delivered.Delivered = true
	entries := []Entry{assignment, delivered}
	if ReplyAssignment(entries, 1, "/root", "/root") != nil {
		t.Fatal("received completion guessed an assignment link")
	}
	entries[1].Question = 1
	if got := ReplyAssignment(entries, 1, "/root", "/root"); got == nil || got.Seq != 1 {
		t.Fatal("received completion lost its exact known link")
	}
	for _, kind := range []string{"tool", "reasoning", "progress", "reply", "assignment"} {
		t.Run(kind, func(t *testing.T) {
			middle := Entry{Facts: Facts{Seq: 3, Kind: kind}, Values: []Value{{Kind: kind}}}
			if kind == "reply" {
				middle.Values = []Value{{Kind: "message", From: peer, To: "Main"}}
			}
			if kind == "assignment" {
				middle.AssignmentTo = peer
			}
			entries := []Entry{assignment, answer, middle, answer}
			context := ReplyAssignment(entries, 3, "/root", "/root")
			switch kind {
			case "reply":
				if context == nil || context.Seq != 1 {
					t.Fatal("intervening message lost original context")
				}
			case "assignment":
				if context == nil || context.Seq != 3 {
					t.Fatal("new assignment inherited old context")
				}
			default:
				if context != nil {
					t.Fatalf("%s repeated assignment", kind)
				}
			}
		})
	}
}
