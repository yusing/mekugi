package activity

import (
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestUISnapshotActivityBlocks(t *testing.T) {
	for _, tc := range []struct {
		name  string
		width int
		block Block
	}{
		{
			name: "timeout_narrow", width: 18,
			block: ParseOperation("Read `file.go 1:20` (timeout 90s)"),
		},
		{
			name: "timeout_run", width: 44,
			block: ParseOperation("Run (timeout 12.5s)\n```bash\nunknown arg\n```"),
		},
		{
			name: "failed_command", width: 44,
			block: Block{Kind: "op", Verb: "Run", Code: "go test ./internal/ui/activity -run TestDialog", ExitCode: 1, Duration: 125 * time.Millisecond,
				Tail: []string{"--- FAIL: TestDialog (0.00s)", "    dialog_test.go:42: unexpected output", "FAIL"}, TailOmitted: 8},
		},
		{
			name: "collapsed_read", width: 44,
			block: Block{Kind: "reads", Verb: "Read", Reads: []Read{{Path: "internal/ui/activity/dialog.go", Ranges: []string{"42:80"}}},
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
			name: "collapsed_batch", width: 72,
			block: func() Block {
				batch, _ := CollapseBatch(GroupOperations([]Block{
					{Kind: "reads", Verb: "Search", Reads: []Read{{Path: "zzz"}}},
					{Kind: "reads", Verb: "Search", Reads: []Read{{Path: "bbb"}}},
					{Kind: "op", Verb: "Run", Code: "foo", Lang: "bash", Fenced: true},
					{Kind: "reads", Verb: "Read", Reads: []Read{{Path: "bar"}}},
					{Kind: "op", Verb: "Edit", EditSource: "apply_patch"},
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
			p := Painter{Theme: livediff.DarkTheme}
			uisnapshot.Assert(t, "testdata/snapshots/activity_"+tc.name+".txt", strings.Join(p.Block(tc.block, tc.width), "\n")+"\n")
		})
	}
}

func TestUISnapshotActivityOutputDialog(t *testing.T) {
	previous := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = previous })
	var retained Retention
	longOutput := retained.New()
	longOutput.Finish(new(strings.Repeat("row ", (32<<10)/4)), nil)
	longOutput.RetainReference("cedar1")
	releasedTail := retained.New()
	releasedTail.Snapshot("readable native tail\n", true)
	releasedTail.Finish(nil, nil)
	releasedTail.Release()
	releasedTail.Reconcile("late complete aggregate")
	releasedTail.RetainReference("cedar2")
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
			name: "retained_released_tail", width: 44, height: 12,
			block: Block{Kind: "op", Verb: "Run", Code: "bash output.sh", Output: releasedTail, Tail: []string{"readable native tail"}},
		},
		{
			name: "retained_line_bounds", width: 44, height: 12,
			block: Block{Kind: "op", Verb: "Run", Code: "printf long-output", Output: longOutput},
		},
		{
			name: "notification_timing", width: 80, height: 15,
			block: Block{Kind: "op", Verb: "Run", Label: "combined output", Code: "pwd; rg --files", NotificationTiming: true,
				Started: time.Date(2026, 10, 2, 3, 4, 7, 94000000, time.UTC),
				Ended:   time.Date(2026, 10, 2, 3, 4, 7, 196000000, time.UTC), Duration: 268 * time.Millisecond,
				Tail: []string{"/workspace", "setup.sh"}},
		},
		{
			name: "segment_timing", width: 80, height: 15,
			block: Block{Kind: "op", Verb: "Run", Code: "pwd", Segment: true,
				Started: time.Date(2026, 10, 2, 3, 4, 7, 94000000, time.UTC),
				Ended:   time.Date(2026, 10, 2, 3, 4, 7, 196000000, time.UTC), Duration: 102 * time.Millisecond,
				Tail: []string{"/workspace"}},
		},
		{
			name: "failed_output", width: 64, height: 14, position: "2 / 3",
			block: Block{Kind: "op", Verb: "Run", Code: "printf 'one\\ntwo\\n'\nexit 2", ExitCode: 2, Duration: 125 * time.Millisecond,
				Tail: []string{"one", "two"}, TailOmitted: 9},
		},
		{
			name: "paused_stream", width: 64, height: 9, paused: true, top: 2,
			block: Block{Kind: "op", Verb: "Run", Code: "go test ./internal/ui/...", Running: true,
				Tail: []string{"package 1: passed", "package 2: passed", "package 3: passed", "package 4: passed", "package 5: running", "waiting for remaining packages"}},
		},
		{
			name: "released_output", width: 44, height: 14,
			block: Block{Kind: "op", Verb: "Run", Code: "make test-ui-snapshots", Output: &Output{done: true, released: true},
				Tail: []string{"ok  internal/ui/activity", "ok  internal/ui/journal"}, TailOmitted: 20},
		},
		{
			name: "read_range", width: 44, height: 11,
			block: Block{Kind: "reads", Verb: "Read", Reads: []Read{{Path: "sample.go", Ranges: []string{"42:43"}}},
				Tail: []string{"func veryLongFunctionName() string {", "    return \"a wrapped source line\""}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := Painter{Theme: livediff.DarkTheme}
			page := p.DialogPage(tc.block, tc.width-4)
			var body []string
			for i := range page.Lines {
				body = append(body, page.Rows(i, tc.width-4)...)
			}
			frame := DialogFrame{Page: page, Position: tc.position, Paused: tc.paused, Top: tc.top, Total: len(body), Rows: body[tc.top:], Footer: "esc close · ↑↓ scroll · c copy"}
			uisnapshot.Assert(t, "testdata/snapshots/activity_dialog_"+tc.name+".txt", strings.Join(p.Dialog(frame, tc.width, tc.height), "\n")+"\n")
		})
	}
}
