package terminalview

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/terminalui"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	"github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestUISnapshotPreloadAttachments(t *testing.T) {
	item := presentation.ItemContent{Kind: "tool", Command: presentation.Command{Exited: true}, Details: &presentation.ItemDetails{Reload: []presentation.ReloadSource{
		{Path: "context.txt", Preload: true, ExitCode: new(0)},
		{Skill: "guide", Preload: true, ExitCode: new(0)},
		{Path: "missing.txt", Preload: true, ExitCode: new(1)},
	}}}
	p := Painter{Theme: diffrender.DarkTheme}
	var rows []string
	for _, value := range item.Values() {
		rows = append(rows, p.Block(Block{Value: value}, 100)...)
	}
	uisnapshot.AssertTerminal(t, "testdata/snapshots/preload-attachments-ansi.txt", append(rows, "plain after attachments"), 100)
}

func TestUISnapshotSkillRosterIdentity(t *testing.T) {
	p := Painter{Theme: diffrender.DarkTheme}
	values := presentation.Operations(presentation.ShellSource("skills-mgr get commit/references/fix.md", ""))
	blocks := make([]Block, len(values))
	var rows []string
	for i, value := range values {
		blocks[i] = Block{Value: value}
		rows = append(rows, p.Block(blocks[i], 100)...)
	}
	summary := p.Summary(blocks, 100)
	if !strings.Contains(ansi.Strip(summary), "commit/references/fix.md") {
		t.Fatalf("skill identity truncated: %s", summary)
	}
	rows = append(rows, summary, "plain after skill rows")
	uisnapshot.AssertTerminal(t, "testdata/snapshots/skill-roster-identity-ansi.txt", rows, 100)
}

// An inline program heads its row in its own language, then names its
// interpreter, on one row or after the program's last row.
func TestUISnapshotInterpreterRun(t *testing.T) {
	p := Painter{Theme: diffrender.DarkTheme}
	var rows []string
	for _, tc := range []struct {
		source string
		block  Block
	}{
		{source: `python3 -I -c 'print("ok")'`, block: Block{Exited: true, Duration: 1200 * time.Millisecond}},
		{source: "node --input-type=module - <<'JS'\nconst items = [1, 2, 3]\nfor (const item of items) {\n  const doubled = item * 2\n  console.log(doubled)\n}\nconsole.log(\"done\")\nJS\n",
			block: Block{Running: true, SourceRows: 4}},
		{source: "python3 - \"$dir\" <<'PY'\nimport sys\nraise SystemExit(sys.argv[1])\nPY\n", block: Block{Exited: true, ExitCode: 1}},
	} {
		for _, value := range presentation.Operations(presentation.ShellSource(tc.source, "")) {
			block := tc.block
			block.Kind, block.Verb, block.Label, block.Code, block.Lang, block.Fenced = value.Kind, value.Verb, value.Label, value.Code, value.Lang, value.Fenced
			rows = append(rows, p.Block(block, 60)...)
		}
	}
	uisnapshot.AssertTerminal(t, "testdata/snapshots/interpreter-run-ansi.txt", append(rows, "plain after interpreter rows"), 60)
}

func TestUISnapshotCommonPathTargets(t *testing.T) {
	for _, tc := range []struct{ name, source, cwd, want string }{
		{"external_search", "rg needle /home/yusing/projects/codex/codex-rs/core/src /home/yusing/projects/codex/codex-rs/tui/src/bottom_pane", "/workspace", "/home/yusing/projects/codex/codex-rs/{core/src,tui/src/bottom_pane}"},
		{"workspace_reads", "mcat /workspace/src/one.go 1:2 /workspace/src/two.go 3:4", "/workspace", "src/{one.go L1–2,two.go L3–4}"},
		{"inspect", "inspect_file src/one.go src/two.go", "/workspace", "src/{one.go,two.go}"},
		{"literal_query", "rg -e src/a -e src/b src/one.go src/two.go", "/workspace", "src/a | src/b in src/{one.go,two.go}"},
		{"list", "ls /workspace/src/one.go /workspace/src/two.go", "/workspace", "src/{one.go,two.go}"},
		{"list_files", "rg --files src/one.go src/two.go", "/workspace", "src/{one.go,two.go}"},
		{"quoted_list", "ls 'src/one file.go' 'src/two file.go'", "/workspace", "src/{one file.go,two file.go}"},
		{"empty_list", "ls ''", "/workspace", "List   ''"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var blocks []Block
			for _, content := range activitymodel.ParseOperations(presentation.ShellSource(tc.source, tc.cwd)) {
				blocks = append(blocks, Block{Content: content})
			}
			blocks = MergeTargets(GroupOperations(blocks))
			p := Painter{Theme: diffrender.DarkTheme}
			var rows []string
			for _, block := range blocks {
				original := dialogTarget(&p, block)
				rows = append(rows, p.Block(block, 100)...)
				if got := dialogTarget(&p, block); got != original || strings.Contains(got, "{") {
					t.Fatalf("dialog targets changed: %q", got)
				}
				for _, row := range p.Block(block, 30) {
					if ansi.StringWidth(row) > 30 {
						t.Fatalf("narrow row exceeds width: %q", row)
					}
				}
			}
			if got := ansi.Strip(strings.Join(rows, "\n")); !strings.Contains(got, tc.want) {
				t.Fatalf("rendered targets = %q, want %q", got, tc.want)
			}
			uisnapshot.AssertTerminal(t, "testdata/snapshots/common_paths_"+tc.name+".txt", append(rows, "plain"), 100)
		})
	}
}

func TestCompactSearchTargetsPreservesOperands(t *testing.T) {
	p := Painter{}
	for _, tc := range []struct{ label, want string }{
		{"`a in b` in `src/two.go` `src/one.go` `src/two.go`", "a in b in src/{two.go,one.go,two.go}"},
		{"`needle` in `src/a,b` `src/c`", "needle in src/a,b src/c"},
		{"`needle` in `src/a` `/outside/b`", "needle in src/a /outside/b"},
	} {
		if got := ansi.Strip(p.compactSearchTargets(tc.label)); got != tc.want {
			t.Errorf("compactSearchTargets(%q) = %q, want %q", tc.label, got, tc.want)
		}
	}
}

func TestUISnapshotTemporaryCreate(t *testing.T) {
	code := strings.Repeat("temporary content\n", 8) + "last line"
	block := Block{Content: activitymodel.ParseOperation("Create `/tmp/large.txt`\n```text\n" + code + "\n```")}
	p := Painter{Theme: diffrender.DarkTheme}
	rows := p.Block(block, 60)
	if strings.Contains(ansi.Strip(strings.Join(rows, "\n")), "temporary content") {
		t.Fatal("large temporary source stayed open")
	}
	uisnapshot.AssertTerminal(t, "testdata/snapshots/temporary_create.txt", rows, 60)
	block.Expanded = true
	if !strings.Contains(ansi.Strip(strings.Join(p.Block(block, 60), "\n")), "last line") {
		t.Fatal("expanded source missing")
	}
	if got := p.DialogPage(block, 60).Text; got != code {
		t.Fatalf("original dialog source changed: %q", got)
	}
	block.Expanded, block.Label = false, "`/workspace/large.txt`"
	if !strings.Contains(ansi.Strip(strings.Join(p.Block(block, 60), "\n")), "last line") {
		t.Fatal("ordinary Create collapsed")
	}
}

func TestUISnapshotActivityBlocks(t *testing.T) {
	for _, tc := range []struct {
		name  string
		width int
		block Block
	}{
		{
			name: "timeout_narrow", width: 18,
			block: Block{Content: activitymodel.ParseOperation("Read `file.go 1:20` (timeout 90s)")},
		},
		{
			name: "timeout_run", width: 44,
			block: Block{Content: activitymodel.ParseOperation("Run (timeout 12.5s)\n```bash\nunknown arg\n```")},
		},
		{
			name: "timeout_day", width: 44,
			block: Block{Content: activitymodel.ParseOperation("Run (timeout 1d)\n```bash\nbackup\n```")},
		},
		{
			name: "failed_command", width: 44,
			block: Block{Kind: "op", Verb: "Run", Code: "go test ./internal/ui/activity -run TestDialog", ExitCode: 1, Duration: 125 * time.Millisecond,
				Tail: []string{"--- FAIL: TestDialog (0.00s)", "    dialog_test.go:42: unexpected output", "FAIL"}, TailOmitted: 8},
		},
		{
			name: "collapsed_read", width: 44,
			block: Block{Kind: "reads", Verb: "Read", Reads: []activitymodel.Read{{Path: "internal/ui/activity/dialog.go", Ranges: []string{"42:80"}}},
				Tail: []string{"first", "second", "third"}, TailOmitted: 36, Collapsed: true},
		},
		{
			name: "mixed_questions", width: 44,
			block: Block{Kind: "op", Verb: "Asked", Label: "2 questions · waiting", Questions: []Question{
				{Text: "Who receives the complete launch update?", Answer: "Send only to beta users on Friday.", State: "answered"},
				{Text: "When should the public rollout begin?", Options: "Monday (Recommended) · Friday", State: "waiting"},
			}},
		},
		{
			name: "wait_targets", width: 32,
			block: Block{Kind: "progress", Body: "Finished waiting", WaitTargets: []WaitTarget{
				{Name: "/root/tests", Status: "completed"}, {Name: "/root/review", Status: "Still running"},
			}},
		},
		{
			name: "wait_completed", width: 80,
			block: Block{Kind: "progress", Body: "Finished waiting for", WaitTargets: []WaitTarget{{Name: "/root/review", Status: "completed"}}, WaitReason: "completed"},
		},
		{
			name: "shell_error_options", width: 80,
			block: Block{Kind: "op", Verb: "Run", Lang: "bash", Fenced: true, SourceRows: 3,
				Code: "set -e\nset -u\nset -o pipefail\nplane comment ITEM --body - <<'EOF'\nA retained comment.\nEOF\nplane update ITEM"},
		},
		{
			name: "collapsed_batch", width: 72,
			block: func() Block {
				batch, _ := CollapseBatch(GroupOperations([]Block{
					{Kind: "reads", Verb: "Search", Reads: []activitymodel.Read{{Path: "zzz"}}},
					{Kind: "reads", Verb: "Search", Reads: []activitymodel.Read{{Path: "bbb"}}},
					{Kind: "op", Verb: "Run", Code: "foo", Lang: "bash", Fenced: true},
					{Kind: "reads", Verb: "Read", Reads: []activitymodel.Read{{Path: "bar"}}},
				}))
				return batch
			}(),
		},
		{
			name: "change_report", width: 80,
			block: Block{Kind: "op", Verb: "Run", Code: "mchanges --summary", Changes: []ChangeRow{
				{Verb: "Edited", Label: "internal/ui/activity/dialog_test.go", Added: 24, Removed: 3},
				{Verb: "Created", Label: "internal/ui/activity/snapshot_test.go", Added: 80},
				{Verb: "Moved", From: "old_test.go", Label: "renamed_test.go", Note: "renamed"},
				{Verb: "Conflict", Label: "conflicted_test.go", Note: "unresolved"},
			}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.block.Timeouts) > 0 {
				tc.block.Duration = 500 * time.Millisecond
			}
			p := Painter{Theme: diffrender.DarkTheme}
			uisnapshot.Assert(t, "testdata/snapshots/activity_"+tc.name+".txt", strings.Join(p.Block(tc.block, tc.width), "\n")+"\n")
		})
	}
}

func TestUISnapshotActivityOutputDialog(t *testing.T) {
	previous := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = previous })
	for _, tc := range []struct {
		name     string
		width    int
		height   int
		block    Block
		position string
		paused   bool
		top      int
	}{
		{
			name: "notification_timing", width: 80, height: 15,
			block: Block{Kind: "op", Verb: "Run", Label: "combined output", Code: "pwd; rg --files", NotificationTiming: true, Exited: true,
				Started: time.Date(2026, 10, 2, 3, 4, 7, 94000000, time.UTC),
				Ended:   time.Date(2026, 10, 2, 3, 4, 7, 196000000, time.UTC), Duration: 268 * time.Millisecond,
				Tail: []string{"/workspace", "setup.sh"}},
		},
		{
			name: "segment_timing", width: 80, height: 15,
			block: Block{Kind: "op", Verb: "Run", Code: "pwd", Segment: true, Exited: true,
				Started: time.Date(2026, 10, 2, 3, 4, 7, 94000000, time.UTC),
				Ended:   time.Date(2026, 10, 2, 3, 4, 7, 196000000, time.UTC), Duration: 102 * time.Millisecond,
				Tail: []string{"/workspace"}},
		},
		{
			name: "failed_output", width: 64, height: 14, position: "2 / 3",
			block: Block{Kind: "op", Verb: "Run", Code: "printf 'one\\ntwo\\n'\nexit 2", Exited: true, ExitCode: 2, Duration: 125 * time.Millisecond,
				Tail: []string{"one", "two"}, TailOmitted: 9},
		},
		{
			name: "paused_stream", width: 64, height: 9, paused: true, top: 2,
			block: Block{Kind: "op", Verb: "Run", Code: "go test ./internal/ui/...", Running: true,
				Tail: []string{"package 1: passed", "package 2: passed", "package 3: passed", "package 4: passed", "package 5: running", "waiting for remaining packages"}},
		},
		{
			name: "released_output", width: 44, height: 14,
			block: Block{Kind: "op", Verb: "Run", Code: "make test-ui-snapshots", OutputSnapshot: new(releasedOutput().View()),
				Tail: []string{"ok  internal/ui/activity", "ok  internal/ui/terminal"}, TailOmitted: 20},
		},
		{
			name: "read_range", width: 44, height: 11,
			block: Block{Kind: "reads", Verb: "Read", Reads: []activitymodel.Read{{Path: "sample.go", Ranges: []string{"42:43"}}},
				Tail: []string{"func veryLongFunctionName() string {", "    return \"a wrapped source line\""}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := Painter{Theme: diffrender.DarkTheme}
			page := p.DialogPage(tc.block, tc.width-4)
			var body []string
			for i := range page.Lines {
				body = append(body, page.Rows(i, tc.width-4)...)
			}
			frame := terminalui.DialogFrame{Page: page, Position: tc.position, Paused: tc.paused, Top: tc.top, Total: len(body), Rows: body[tc.top:], Footer: "esc close · ↑↓ scroll · c copy"}
			uisnapshot.Assert(t, "testdata/snapshots/activity_dialog_"+tc.name+".txt", strings.Join(p.Dialog(frame, tc.width, tc.height), "\n")+"\n")
		})
	}
}

func TestUISnapshotTemporaryCapturedChanges(t *testing.T) {
	values := []presentation.Value{{Kind: "op", Verb: "Edit", Label: "`project.go` +2 -1", EditSource: "git worktree add", EditHeader: true}}
	for i := range 200 {
		values = append(values, presentation.Value{Kind: "op", Verb: "Create", Label: fmt.Sprintf("`/tmp/worktree/file%03d.go` +3 -0", i), EditSource: "git worktree add"})
	}
	values = append(values, presentation.Value{Kind: "op", Verb: "Edit", Label: "`after.go` +1 -1", EditSource: "git worktree add"})
	v := NewView()
	v.Conversation = true
	v.Painter.Theme = diffrender.DarkTheme
	v.Clock = func() time.Time { return time.Unix(1000, 0) }
	v.Update(&presentation.Publication{Entries: []presentation.Entry{{Seq: 1, Agent: "Main", Kind: "tool", Text: "captured changes", Values: values}}}, Display{})
	for _, expansion := range []uint8{0, 1, 2} {
		v.Expansion = expansion
		feed := v.RenderConversation(100)
		text := ansi.Strip(strings.Join(feed.Lines, "\n"))
		if expansion != 2 {
			if !strings.Contains(text, "… +190 files") || strings.Contains(text, "file010.go") || !strings.Contains(text, "after.go") || !strings.Contains(text, "project.go") {
				t.Fatalf("collapsed file rows: %s", text)
			}
			row := slices.IndexFunc(feed.Lines, func(line string) bool { return strings.Contains(line, "… +190 files") })
			if feed.Snippets[row] != (Snippet{Run: 1, Block: EditNavigationSnippet}) {
				t.Fatalf("omitted files lost invocation navigation: %+v", feed.Snippets[row])
			}
			if expansion == 0 {
				uisnapshot.AssertTerminal(t, "testdata/snapshots/temporary_captured_changes.txt", append(feed.Lines, "plain after changes"), 100)
			}
		} else {
			for i := range 200 {
				path := fmt.Sprintf("/tmp/worktree/file%03d.go", i)
				if !slices.Contains(feed.Snippets, Snippet{Run: 1, Block: EditNavigationSnippet, Path: path}) {
					t.Fatalf("expanded navigation lost %s", path)
				}
			}
		}
	}
}

func TestUISnapshotProjectCreateDeleteBatches(t *testing.T) {
	for _, verb := range []string{"Create", "Delete"} {
		counts, writer := "+3 -0", "cp"
		if verb == "Delete" {
			counts, writer = "+0 -3", "rm"
		}
		for _, count := range []int{10, 11, 200} {
			t.Run(fmt.Sprintf("%s/%d", verb, count), func(t *testing.T) {
				var records []string
				for i := range count {
					records = append(records, fmt.Sprintf("%s `src/file%03d.go` %s · %s", verb, i, counts, writer))
				}
				facts := presentation.Facts{Seq: 1, Agent: "Main", Kind: "tool", Text: strings.Join(records, "\n\n")}
				item := presentation.ItemContent{Facts: facts}
				v := NewView()
				v.Conversation = true
				v.Painter.Theme = diffrender.DarkTheme
				v.Clock = func() time.Time { return time.Unix(1000, 0) }
				v.Update(&presentation.Publication{Entries: []presentation.Entry{
					{Facts: facts, Values: item.Values()},
					{Seq: 2, Agent: "Main", Kind: "text", Text: "Later text stays reachable"},
				}}, Display{})
				for _, expansion := range []uint8{0, 2} {
					v.Expansion = expansion
					feed := v.RenderConversation(100)
					text := ansi.Strip(strings.Join(feed.Lines, "\n"))
					if !strings.Contains(text, "Later text stays reachable") {
						t.Fatal("later text lost")
					}
					if expansion == 0 && count > 10 {
						if !strings.Contains(text, fmt.Sprintf("… +%d file", count-10)) || strings.Contains(text, "file010.go") {
							t.Fatalf("unbounded batch: %s", text)
						}
						if !slices.Contains(feed.Snippets, Snippet{Run: 1, Block: EditNavigationSnippet}) {
							t.Fatal("elision lost invocation navigation")
						}
					} else {
						for i := range count {
							if !slices.Contains(feed.Snippets, Snippet{Run: 1, Block: EditNavigationSnippet, Path: fmt.Sprintf("src/file%03d.go", i)}) {
								t.Fatalf("file %d lost", i)
							}
						}
					}
					if expansion == 0 && count == 11 {
						uisnapshot.AssertTerminal(t, "testdata/snapshots/project_"+strings.ToLower(verb)+"_batch.txt", append(feed.Lines, "plain after changes"), 100)
					}
				}
			})
		}
	}
}

func TestTemporaryCapturedChangesKeepGroupBoundaries(t *testing.T) {
	var blocks []Block
	for source := uint64(1); source <= 2; source++ {
		for i := range 11 {
			blocks = append(blocks, Block{Kind: "op", Source: source, Verb: "Create", Label: fmt.Sprintf("`/tmp/tree/file%d` +1 -0", i), EditSource: "cp"})
		}
	}
	for _, boundary := range []string{"source", "directory", "outcome", "writer"} {
		t.Run(boundary, func(t *testing.T) {
			input := slices.Clone(blocks)
			for i := 11; i < len(input); i++ {
				switch boundary {
				case "directory":
					input[i].Source, input[i].Workdir = 1, "/other"
				case "outcome":
					input[i].Source, input[i].EditOutcome = 1, "failed"
				case "writer":
					input[i].Source, input[i].EditSource = 1, "python3"
				}
			}
			shown := collapseCapturedEdits(GroupOperations(input))
			for _, b := range shown {
				if b.HiddenEdits > 1 {
					t.Fatalf("elision crossed %s boundary: %+v", boundary, b)
				}
				if b.EditOutcome == "failed" && b.HiddenEdits != 0 {
					t.Fatal("failed outcome hidden")
				}
			}
		})
	}
}

func TestTemporaryCapturedChangesRetainBatchAndOutput(t *testing.T) {
	var records []string
	for i := range 200 {
		records = append(records, fmt.Sprintf("Create `/tmp/worktree/file%03d.go` +3 -0 · git worktree add", i))
	}
	for _, state := range []string{"output", "expanded-output"} {
		t.Run(state, func(t *testing.T) {
			facts := presentation.Facts{Seq: 1, Agent: "Main", Kind: "tool", Text: strings.Join(records, "\n\n")}
			item := presentation.ItemContent{Facts: facts}
			item.Command.Tail = []string{"retained command output"}
			item.Details = &presentation.ItemDetails{Output: 1}
			v := NewView()
			v.Conversation = true
			v.Clock = func() time.Time { return time.Unix(1000, 0) }
			v.Update(&presentation.Publication{Entries: []presentation.Entry{{Facts: facts, Values: item.Values()}}}, Display{})
			if state == "expanded-output" {
				v.Expansion = 1
			}
			feed := v.RenderConversation(100)
			text := ansi.Strip(strings.Join(feed.Lines, "\n"))
			if !strings.Contains(text, "retained command output") || !strings.Contains(text, "… +189 files") || !strings.Contains(text, "file199.go") {
				t.Fatalf("output-bearing final capture lost: %s", text)
			}
		})
	}
}

func TestUISnapshotOperationResultCounts(t *testing.T) {
	p := Painter{Theme: diffrender.DarkTheme}
	var rows []string
	for _, tc := range []struct{ source, output string }{
		{"rg needle src", "a.go:1:needle\nb.go:2:needle"},
		{"ls src", "a.go\nb.go"},
		{"inspect_file a.go", "1-2 import\n4-6 function main"},
		{"ls empty", ""},
		{"ls *", "a:\none\n\nb:\ntwo"},
		{"rg -A 1 needle src", "a.go:1:needle\na.go-2-context"},
		{"inspect_file a.go", "1-2 import\n[output truncated]"},
	} {
		entry := presentation.ItemContent{Kind: "tool", Text: presentation.ShellSource(tc.source, ""), Details: &presentation.ItemDetails{SearchResults: presentation.OperationResultCount(tc.source, &tc.output, 0)}, Command: presentation.Command{Tail: strings.Split(tc.output, "\n"), Exited: true}}
		value := entry.Values()[0]
		block := Block{Value: value, Collapsed: true}
		painted := p.Block(block, 60)
		if strings.Contains(ansi.Strip(strings.Join(painted, "\n")), "lines") {
			t.Fatal("count retained hidden-line hint")
		}
		rows = append(rows, painted...)
		// Disclosure changes only presentation; retained rows still open.
		if tc.output != "" && len(p.DialogPage(block, 60).Lines) == 0 {
			t.Fatal("collapsed operation lost its detail page")
		}
		block.Collapsed = false
		if tc.output != "" && !strings.Contains(ansi.Strip(strings.Join(p.Block(block, 60), "\n")), strings.Split(tc.output, "\n")[0]) {
			t.Fatal("expanded output missing")
		}
	}
	uisnapshot.AssertTerminal(t, "testdata/snapshots/operation_result_counts.txt", rows, 60)
}
