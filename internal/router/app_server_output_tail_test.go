package router

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	mekugi "github.com/yusing/mekugi"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

func TestAppServerOutputTailKeepsFinalLines(t *testing.T) {
	var lines []string
	for i := range 8 {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	output := strings.Join(lines[:6], "\n") + "\n\n\x1b[2K\n" + strings.Join(lines[6:], "\n") + "\x1b]0;title\x07\n\n"
	tail, omitted := appServerOutputTail(&output)
	if want := []string{"line 3", "line 4", "line 5", "line 6", "line 7"}; !reflect.DeepEqual(tail, want) || omitted != 3 {
		t.Fatalf("tail = %q omitted %d", tail, omitted)
	}
	long := strings.Repeat("é", activityui.OutputTailBytes)
	if tail, _ := appServerOutputTail(&long); len(tail) != 1 || len(tail[0]) > activityui.OutputTailBytes+len("…") || !strings.HasSuffix(tail[0], "…") {
		t.Fatalf("long line tail = %q", tail)
	}
	for _, output := range []*string{nil, new(""), new(" \n\n")} {
		if tail, omitted := appServerOutputTail(output); tail != nil || omitted != 0 {
			t.Fatalf("empty output tail = %q, %d", tail, omitted)
		}
	}
}

func TestAppServerFailedCommandShowsOutputTailInMain(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.view.conversation = true
	for _, method := range []string{"item/started", "item/completed"} {
		item := map[string]any{"id": "cmd", "type": "commandExecution", "command": "go test ./...", "status": "inProgress"}
		if method == "item/completed" {
			item["status"], item["exitCode"], item["aggregatedOutput"] = "failed", 1, "ok\n--- FAIL: TestX\nFAIL\n"
		}
		appServerTestNotify(t, u, method, map[string]any{"threadId": "main", "turnId": "t", "item": item})
	}
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": map[string]any{
		"id": "read", "type": "commandExecution", "command": "cat a.go", "status": "completed", "exitCode": 0,
		"commandActions": []map[string]any{{"type": "read", "path": "a.go", "command": "cat a.go"}}}})
	main := ansi.Strip(strings.Join(u.view.renderFeed(90, 60).lines, "\n"))
	want := "├ Ran  go test ./... · exit 1\n│      ┆ ok\n│      ┆ --- FAIL: TestX\n│      ┆ FAIL\n└ Read a.go"
	if !strings.Contains(main, want) {
		t.Fatalf("Main lacks failure tail on its branch %q:\n%s", want, main)
	}
}

func TestAppServerPendingPatchIsNotEdited(t *testing.T) {
	for _, agent := range []string{"Main", "/root/worker"} {
		v := newLiveActivityView()
		v.childrenOnly = agent != "Main"
		patch := appServerEditText(appServerItem{Status: "inProgress", Changes: []appServerFileChange{{Path: "c.go", Diff: "+new\n-old\n"}}}, "")
		v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: 1, Agent: agent, Kind: "tool", CallID: "patch", Text: patch}}})
		feed := v.renderFeed(100, 80)
		if agent == "Main" {
			feed = v.renderConversation(100)
		}
		got := ansi.Strip(strings.Join(feed.lines, "\n"))
		if strings.Contains(got, "Edited") || !strings.Contains(got, "Edit c.go +1 -1 · pending") {
			t.Fatalf("%s showed an unconfirmed patch as edited:\n%s", agent, got)
		}
	}
}

func TestManagedFilesDoNotBecomeEditsInMain(t *testing.T) {
	files := make([]mekugi.ReviewFile, 4)
	for i := range files {
		files[i] = mekugi.RenderReviewFile("", fmt.Sprintf("gen-%d.txt", i), "", "x\n")
		files[i].Origin = "generator"
	}
	if event := capturedEditActivity(t.TempDir(), mekugiHistory{ReviewFiles: files, ExecOutcome: &execOutcome{Labels: []string{"git"}}}); event != nil {
		t.Fatalf("generated effects created an Activity edit: %+v", event)
	}
}

func TestAppServerCommandRunsWithLiveTailThenRan(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.view.conversation = true
	notify := func(method string, params map[string]any) {
		t.Helper()
		params["threadId"], params["turnId"] = "main", "t"
		appServerTestNotify(t, u, method, params)
	}
	main := func() string { return ansi.Strip(strings.Join(u.view.renderFeed(90, 60).lines, "\n")) }
	item := map[string]any{"id": "cmd", "type": "commandExecution", "command": "go test ./...", "status": "inProgress"}
	notify("item/started", map[string]any{"item": item})
	if got := main(); !strings.Contains(got, "Running go test ./...") || strings.Contains(got, "Ran") {
		t.Fatalf("started command is not running:\n%s", got)
	}
	for i := range 8 {
		notify("item/commandExecution/outputDelta", map[string]any{"itemId": "cmd", "delta": fmt.Sprintf("ok %d\n", i)})
	}
	notify("item/commandExecution/outputDelta", map[string]any{"itemId": "cmd", "delta": "--- partial"})
	if got := main(); strings.Contains(got, "ok 7") {
		t.Fatalf("output rendered before its frame:\n%s", got)
	}
	u.flushStreamOutput()
	if got := main(); !strings.Contains(got, "┆ ok 1") || strings.Contains(got, "ok 2") {
		t.Fatalf("burst jumped to its tail instead of rolling:\n%s", got)
	}
	rollCommandOutput(u)
	want := "  +4      ┆ ok 4\n          ┆ ok 5\n          ┆ ok 6\n          ┆ ok 7\n          ┆ --- partial"
	if got := main(); !strings.Contains(got, "└ Running go test ./...") || !strings.Contains(got, want) {
		t.Fatalf("live tail missing header or %q:\n%s", want, got)
	}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "child", "turnId": "c", "item": map[string]any{
		"id": "cmd", "type": "commandExecution", "command": "make lint", "status": "inProgress"}})
	appServerTestNotify(t, u, "item/commandExecution/outputDelta", map[string]any{"threadId": "child", "turnId": "c", "itemId": "cmd", "delta": "linting\n"})
	// A rename after the start must not move the running row back.
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child",
		"source": map[string]any{"subAgent": map[string]any{"thread_spawn": map[string]any{"agent_path": "/root/linter"}}}}})
	rollCommandOutput(u)
	agents := ansi.Strip(strings.Join(u.agents.renderFeed(90, 60).lines, "\n"))
	if !strings.Contains(agents, "linter") || !strings.Contains(agents, "Running make lint") || !strings.Contains(agents, "┆ linting") || strings.Contains(agents, "ok 7") {
		t.Fatalf("Agents lacks the child's live tail:\n%s", agents)
	}
	item["status"], item["exitCode"], item["aggregatedOutput"] = "completed", 0, "ok 1\nok 2\nPASS\n"
	notify("item/completed", map[string]any{"item": item})
	notify("item/commandExecution/outputDelta", map[string]any{"itemId": "cmd", "delta": "late\n"})
	rollCommandOutput(u)
	// The host's output stays readable until Main's next event, then collapses.
	open := "      ┆ ok 1\n      ┆ ok 2\n      ┆ PASS"
	if got := main(); !strings.Contains(got, "└ Ran go test ./...") || !strings.Contains(got, open) || strings.Contains(got, "Running") || strings.Contains(got, "late") {
		t.Fatalf("completed command lacks its settled output %q:\n%s", open, got)
	}
	if settleActivity(time.Now().Add(time.Hour), u.view) {
		t.Fatal("output collapsed before a later event")
	}
	nextEvent(t, u, "main")
	if settleActivity(time.Now(), u.view) {
		t.Fatal("output collapsed before events paused")
	}
	if !settleActivity(time.Now().Add(activityui.OutputDebounce), u.view) {
		t.Fatal("output did not collapse after the next event")
	}
	feed := u.view.renderFeed(90, 60)
	got := ansi.Strip(strings.Join(feed.lines, "\n"))
	if collapsed := "└ Ran go test ./...\n      ┆ … +3 lines"; !strings.Contains(got, collapsed) || strings.Contains(got, "ok 1") {
		t.Fatalf("settled output is not collapsed to %q:\n%s", collapsed, got)
	}
	// Either row of the command opens its full retained output in the dialog.
	index := slices.IndexFunc(feed.lines, func(line string) bool { return strings.Contains(line, "… +3 lines") })
	snippet := feed.snippets[index]
	if snippet == (liveActivitySnippet{}) || feed.snippets[index-1] != snippet {
		t.Fatalf("collapsed output has no opening target: %v", feed.snippets)
	}
	terminal := &terminalUI{main: u}
	if !terminal.openOutput(u.view, snippet) {
		t.Fatal("command did not open output dialog")
	}
	terminal.output.layout(76)
	if got := terminal.output.laid.Text; got != "ok 1\nok 2\nPASS" {
		t.Fatalf("dialog output = %q", got)
	}
	terminal.outputKey("\x1b")
	if terminal.output != nil || !strings.Contains(main(), "┆ … +3 lines") {
		t.Fatal("closing output dialog changed the command row")
	}
	if _, tracked := u.session.commands[[3]string{"main", "t", "cmd"}]; tracked || len(u.session.commands) != 1 {
		t.Fatalf("completed command still tracked: %v", u.session.commands)
	}
}

func TestAppServerSingleLineOutputStaysVisible(t *testing.T) {
	for _, conversation := range []bool{false, true} {
		u := newAppServerSessionTestUI(t, t.TempDir())
		u.view.conversation = conversation
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": map[string]any{
			"id": "cmd", "type": "commandExecution", "command": "echo done", "status": "completed", "exitCode": 0, "aggregatedOutput": "done\n",
		}})
		nextEvent(t, u, "main")
		settleActivity(time.Now().Add(activityui.OutputDebounce), u.view)
		feed := u.view.renderFeed(90, 60)
		got := ansi.Strip(strings.Join(feed.lines, "\n"))
		if !strings.Contains(got, "┆ done") || strings.Contains(got, "+1 lines") {
			t.Fatalf("single output line hidden: %s", got)
		}
		index := slices.IndexFunc(feed.lines, func(line string) bool { return strings.Contains(ansi.Strip(line), "┆ done") })
		terminal := &terminalUI{main: u}
		if index < 0 || !terminal.openOutput(u.view, feed.snippets[index]) {
			t.Fatal("single output line has no dialog target")
		}
		terminal.output.layout(76)
		if terminal.output.laid.Text != "done" {
			t.Fatalf("dialog output = %q", terminal.output.laid.Text)
		}
	}
}

func TestAppServerMixedCommandOutputFollowsFinalRead(t *testing.T) {
	for _, exit := range []int{0, 1} {
		t.Run(fmt.Sprint(exit), func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			u.view.conversation = true
			item := map[string]any{
				"id": "mixed", "type": "commandExecution",
				"command": "pwd; skills-mgr get mekugi-owners; cat a.go",
				"status":  "inProgress",
			}
			notify := func(method string, params map[string]any) {
				t.Helper()
				params["threadId"], params["turnId"] = "main", "t"
				appServerTestNotify(t, u, method, params)
			}
			check := func(output string) {
				t.Helper()
				got := ansi.Strip(strings.Join(u.view.renderFeed(100, 60).lines, "\n"))
				read, tail := strings.Index(got, "Read"), strings.Index(got, output)
				if read < 0 || tail < read || strings.Count(got, output) != 1 {
					t.Fatalf("output must appear once after final Read:\n%s", got)
				}
			}
			notify("item/started", map[string]any{"item": item})
			finishPacing(u.view)
			notify("item/commandExecution/outputDelta", map[string]any{"itemId": "mixed", "delta": "first\nsecond\n"})
			rollCommandOutput(u)
			check("┆ second")
			item["status"], item["exitCode"], item["aggregatedOutput"] = "completed", exit, "first\nsecond\n"
			notify("item/completed", map[string]any{"item": item})
			if exit != 0 {
				check("┆ second")
				nextEvent(t, u, "main")
				settleActivity(time.Now().Add(activityui.OutputDebounce), u.view)
				check("┆ second")
				return
			}
			// A read's output starts collapsed, without waiting for a later event.
			check("Read  a.go (2 lines)")
			feed := u.view.renderFeed(100, 60)
			index := slices.IndexFunc(feed.lines, func(line string) bool { return strings.Contains(line, "(2 lines)") })
			snippet := feed.snippets[index]
			if snippet == (liveActivitySnippet{}) {
				t.Fatal("final Read output has no toggle")
			}
			terminal := &terminalUI{main: u}
			if !terminal.openOutput(u.view, snippet) {
				t.Fatal("final Read did not open output dialog")
			}
			terminal.output.layout(80)
			if got := terminal.output.laid.Text; got != "first\nsecond" {
				t.Fatalf("read dialog output = %q", got)
			}
		})
	}
}

func TestAppServerAdjacentReadOutputsStayWithInvocation(t *testing.T) {
	for _, conversation := range []bool{false, true} {
		t.Run(fmt.Sprint(conversation), func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			u.view.conversation = conversation
			for i, output := range []string{"", "first-output\n", "second-output\n"} {
				appServerTestNotify(t, u, "item/completed", map[string]any{
					"threadId": "main", "turnId": "t", "item": map[string]any{
						"id": fmt.Sprint(i), "type": "commandExecution",
						"command": fmt.Sprintf("cat file%d.go", i),
						"status":  "completed", "exitCode": 0, "aggregatedOutput": output,
					},
				})
			}
			got := ansi.Strip(strings.Join(u.view.renderFeed(100, 60).lines, "\n"))
			previous := -1
			for _, token := range []string{"file0.go", "file1.go", "┆ first-output", "file2.go", "┆ second-output"} {
				index := strings.Index(got, token)
				if index <= previous {
					t.Fatalf("missing or misplaced %q:\n%s", token, got)
				}
				previous = index
			}
		})
	}
}

// rollCommandOutput plays frames until every output burst has rolled through
// and held completions have followed.
func rollCommandOutput(u *appServerUI) {
	for range activityui.OutputPendingLines {
		u.flushStreamOutput()
	}
}

func TestAppServerCompletionWaitsForOutputBurst(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.view.conversation = true
	notify := func(method string, params map[string]any) {
		t.Helper()
		params["threadId"], params["turnId"] = "main", "t"
		appServerTestNotify(t, u, method, params)
	}
	main := func() string { return ansi.Strip(strings.Join(u.view.renderFeed(90, 60).lines, "\n")) }
	item := map[string]any{"id": "cmd", "type": "commandExecution", "command": "mrun -n 50 go test ./...", "status": "inProgress"}
	notify("item/started", map[string]any{"item": item})
	var output strings.Builder
	for i := range 20 {
		fmt.Fprintf(&output, "ok %d\n", i)
	}
	// A buffering command prints everything as it exits.
	notify("item/commandExecution/outputDelta", map[string]any{"itemId": "cmd", "delta": output.String()})
	item["status"], item["exitCode"], item["aggregatedOutput"] = "completed", 1, output.String()
	notify("item/completed", map[string]any{"item": item})
	u.flushStreamOutput()
	if got := main(); !strings.Contains(got, "Running") || strings.Contains(got, "ok 19") {
		t.Fatalf("completion replaced the output before it rolled:\n%s", got)
	}
	rollCommandOutput(u)
	if got := main(); !strings.Contains(got, "Ran") || !strings.Contains(got, "exit 1") || !strings.Contains(got, "┆ ok 19") {
		t.Fatalf("held completion did not follow its rolled output:\n%s", got)
	}
	if len(u.session.commands) != 0 {
		t.Fatalf("completed command still tracked: %v", u.session.commands)
	}
}

// nextEvent adds finished thinking in thread, a standalone event that settles
// the agent's earlier output.
func nextEvent(t *testing.T, u *appServerUI, thread string) {
	t.Helper()
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": "t", "item": map[string]any{
		"id": fmt.Sprintf("next-%s-%d", thread, u.session.seq+1), "type": "reasoning", "summary": []string{"**Next step**"}}})
	rollCommandOutput(u)
}

func TestAppServerOutputCollapsesTogetherAfterEventsPause(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.view.conversation = true
	run := func(id string) {
		t.Helper()
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": map[string]any{
			"id": id, "type": "commandExecution", "command": "echo " + id, "status": "completed", "exitCode": 0, "aggregatedOutput": id + " 1\n" + id + " 2\n"}})
	}
	run("first")
	run("second")
	start := time.Now()
	// Rapid commands keep deferring the collapse rather than folding one by one.
	if settleActivity(start.Add(activityui.OutputDebounce/2), u.view) {
		t.Fatal("output collapsed while events were still arriving")
	}
	run("third")
	third := u.view.events["Main"].at
	if settleActivity(third.Add(activityui.OutputDebounce/2), u.view) {
		t.Fatal("a later event did not restart the pause")
	}
	if !settleActivity(third.Add(activityui.OutputDebounce), u.view) {
		t.Fatal("settled output did not collapse after events paused")
	}
	got := ansi.Strip(strings.Join(u.view.renderFeed(100, 60).lines, "\n"))
	if strings.Contains(got, "first 1") || strings.Contains(got, "second 1") || !strings.Contains(got, "third 1") {
		t.Fatalf("only the output with a later event should collapse:\n%s", got)
	}
}

func TestAppServerMainClipsLongCommandSourceUntilOpened(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.view.conversation = true
	command := "python3 - <<'PY'\n" + strings.Repeat("print('row')\n", 30) + "PY"
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": map[string]any{
		"id": "cmd", "type": "commandExecution", "command": command, "status": "failed", "exitCode": 1, "aggregatedOutput": "boom\n"}})
	render := func() liveActivityFeed { return u.view.renderFeed(90, 60) }
	feed := render()
	got := ansi.Strip(strings.Join(feed.lines, "\n"))
	if strings.Count(got, "print('row')") != conversationSourceRows-1 || !strings.Contains(got, "│ … +") ||
		!strings.Contains(got, "exit 1") || !strings.Contains(got, "┆ boom") {
		t.Fatalf("long command source is not bounded with its exit and output:\n%s", got)
	}
	index := slices.IndexFunc(feed.lines, func(line string) bool { return strings.Contains(ansi.Strip(line), "│ … +") })
	snippet := feed.snippets[index]
	if snippet == (liveActivitySnippet{}) {
		t.Fatalf("clipped source has no toggle: %v", feed.snippets)
	}
	terminal := &terminalUI{main: u}
	if !terminal.openOutput(u.view, snippet) {
		t.Fatal("clipped source did not open output dialog")
	}
	terminal.output.layout(76)
	if got := terminal.output.laid; strings.Count(ansi.Strip(fmt.Sprint(got.Lines)), "print('row')") != 30 || got.Text != "boom" {
		t.Fatalf("dialog lost source or output: %+v", got)
	}
	terminal.outputKey("q")
	if got := ansi.Strip(strings.Join(render().lines, "\n")); !strings.Contains(got, "│ … +") {
		t.Fatalf("source row changed after dialog closed:\n%s", got)
	}
}

func TestAppServerReadOutputStartsCollapsed(t *testing.T) {
	for _, tc := range []struct {
		command   string
		collapsed bool
	}{
		{"cat a.go", true},
		{"skills-mgr get herdr", true},
		{"skills-mgr run herdr/x.sh list", false},
		{"go vet ./...", false},
	} {
		u := newAppServerSessionTestUI(t, t.TempDir())
		u.view.conversation = true
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": map[string]any{
			"id": "cmd", "type": "commandExecution", "command": tc.command, "status": "completed", "exitCode": 0, "aggregatedOutput": "one\ntwo\n"}})
		got := ansi.Strip(strings.Join(u.view.renderFeed(90, 60).lines, "\n"))
		if collapsed := strings.Contains(got, " (2 lines)") && !strings.Contains(got, "┆ two"); collapsed != tc.collapsed {
			t.Fatalf("%s collapsed = %v, want %v:\n%s", tc.command, collapsed, tc.collapsed, got)
		}
	}
}

func TestAppServerShortPaneShowsCompactTail(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.view.conversation = true
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": map[string]any{
		"id": "cmd", "type": "commandExecution", "command": "go test ./...", "status": "completed", "exitCode": 0, "aggregatedOutput": "1\n2\n3\n4\n5\n6\n"}})
	render := func(height int) string {
		return ansi.Strip(strings.Join(u.view.render(90, height, time.Now()), "\n"))
	}
	if got, want := render(liveActivityCompactHeight), "└ Ran go test ./...\n  +1  ┆ 2\n      ┆ 3\n      ┆ 4\n      ┆ 5\n      ┆ 6"; !strings.Contains(got, want) {
		t.Fatalf("tall pane lacks the whole tail %q:\n%s", want, got)
	}
	if got, want := render(liveActivityCompactHeight-1), "└ Ran go test ./...\n  +3  ┆ 4\n      ┆ 5\n      ┆ 6"; !strings.Contains(got, want) {
		t.Fatalf("short pane lacks the compact tail %q:\n%s", want, got)
	}
}

func TestAppServerInstantOperationOutputDoesNotRoll(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.view.conversation = true
	notify := func(method string, params map[string]any) {
		t.Helper()
		params["threadId"], params["turnId"] = "main", "t"
		appServerTestNotify(t, u, method, params)
	}
	main := func() string { return ansi.Strip(strings.Join(u.view.renderFeed(90, 60).lines, "\n")) }
	var output strings.Builder
	for i := range 20 {
		fmt.Fprintf(&output, "line %d\n", i)
	}
	item := map[string]any{"id": "read", "type": "commandExecution", "command": "rg -n line internal", "status": "inProgress"}
	notify("item/started", map[string]any{"item": item})
	notify("item/commandExecution/outputDelta", map[string]any{"itemId": "read", "delta": output.String()})
	u.flushStreamOutput()
	if got := main(); !strings.Contains(got, "┆ line 19") || strings.Contains(got, "line 14") {
		t.Fatalf("a search's output rolled instead of showing its tail at once:\n%s", got)
	}
	// Its completion is not held behind a burst either.
	notify("item/commandExecution/outputDelta", map[string]any{"itemId": "read", "delta": "line 20\n"})
	item["status"], item["exitCode"], item["aggregatedOutput"] = "completed", 0, output.String()+"line 20\n"
	notify("item/completed", map[string]any{"item": item})
	if got := main(); strings.Contains(got, "Running") || !strings.Contains(got, "┆ line 20") || len(u.session.commands) != 0 {
		t.Fatalf("a search's completion waited for its output to roll:\n%s", got)
	}

	// Other commands still roll a burst through.
	run := map[string]any{"id": "run", "type": "commandExecution", "command": "go test ./...", "status": "inProgress"}
	notify("item/started", map[string]any{"item": run})
	notify("item/commandExecution/outputDelta", map[string]any{"itemId": "run", "delta": output.String()})
	u.flushStreamOutput()
	if got := main(); strings.Contains(got[strings.Index(got, "Running"):], "line 19") {
		t.Fatalf("a command's burst jumped to its tail:\n%s", got)
	}
}

// Consecutive reads whose content collapsed share one row that counts each
// target's lines; the dialog pages through each invocation's output.
func TestAppServerCollapsedReadsMerge(t *testing.T) {
	for _, conversation := range []bool{false, true} {
		t.Run(fmt.Sprint(conversation), func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			u.view.conversation = conversation
			for i, command := range []string{"sed -n 1,3p a.go", "sed -n 1,2p b.go", "sed -n 5,6p a.go"} {
				appServerTestNotify(t, u, "item/completed", map[string]any{
					"threadId": "main", "turnId": "t", "item": map[string]any{
						"id": fmt.Sprint(i), "type": "commandExecution", "command": command,
						"status": "completed", "exitCode": 0, "aggregatedOutput": fmt.Sprintf("read-%d\nmore-%d\n", i, i),
					},
				})
			}
			feed := u.view.renderFeed(100, 60)
			got := ansi.Strip(strings.Join(feed.lines, "\n"))
			if strings.Count(got, "Read") != 1 || !strings.Contains(got, "Read a.go L1–3, L5–6 (4 lines) · b.go L1–2 (2 lines)") || strings.Contains(got, "read-0") {
				t.Fatalf("collapsed reads did not merge:\n%s", got)
			}
			index := slices.IndexFunc(feed.lines, func(line string) bool { return strings.Contains(line, "(4 lines)") })
			snippet := feed.snippets[index]
			if snippet == (liveActivitySnippet{}) {
				t.Fatal("merged read row has no toggle")
			}
			terminal := &terminalUI{main: u}
			if !terminal.openOutput(u.view, snippet) || len(terminal.output.pages) != 3 {
				t.Fatal("merged reads did not open three dialog pages")
			}
			for i := range 3 {
				terminal.output.layout(80)
				if page := terminal.output.laid.Text; !strings.Contains(page, fmt.Sprintf("read-%d\nmore-%d", i, i)) {
					t.Fatalf("read page %d = %q", i, page)
				}
				if i < 2 {
					terminal.outputKey("\x1b[C")
				}
			}
			terminal.outputKey("q")
			if again := ansi.Strip(strings.Join(u.view.renderFeed(100, 60).lines, "\n")); again != got {
				t.Fatalf("closed reads =\n%s\nwant\n%s", again, got)
			}
		})
	}
}

func TestOutputCollapseSharesFrameAcrossLateCompletions(t *testing.T) {
	for _, agents := range [][]string{{"Main", "Main"}, {"Main", "/root/worker"}} {
		t.Run(strings.Join(agents, ","), func(t *testing.T) {
			v := newLiveActivityView()
			start := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
			late := start.Add(activityui.OutputDebounce / 2)
			v.events = map[string]liveActivityEvent{}
			for i, agent := range agents {
				settled := start
				if i == 1 {
					settled = late
				}
				v.appendEntry(activityPaneEntry{Seq: uint64(i + 1), Agent: agent, native: &liveActivityNativeItem{settled: settled}}, []activityui.Block{{Kind: "op", Verb: "Run", Tail: []string{"one", "two"}}})
				v.events[agent] = liveActivityEvent{seq: 3, at: start}
			}
			if settleActivity(start.Add(activityui.OutputDebounce), v) {
				t.Fatal("earlier output collapsed in a separate frame")
			}
			if v.entries[0].blocks[0].Collapsed || v.entries[1].blocks[0].Collapsed {
				t.Fatal("partially collapsed batch")
			}
			if !settleActivity(late.Add(activityui.OutputDebounce), v) {
				t.Fatal("batch did not collapse")
			}
			if !v.entries[0].blocks[0].Collapsed || !v.entries[1].blocks[0].Collapsed {
				t.Fatal("outputs did not collapse together")
			}
		})
	}
}

func TestOutputCollapseSharesFrameAcrossPanes(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.view.conversation = true
	start := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	late := start.Add(activityui.OutputDebounce / 2)
	for i, agent := range []string{"/root", "/root/worker"} {
		at := start
		if i == 1 {
			at = late
		}
		u.applyActivity([]activityPaneEntry{
			{Seq: uint64(i*2 + 1), Agent: agent, Kind: "tool", Text: "Run `echo result`", Observed: at, outputTail: []string{"first output line", "last output line"}, native: &liveActivityNativeItem{thread: agent, item: "cmd", settled: at}},
			{Seq: uint64(i*2 + 2), Agent: agent, Kind: "reasoning", Text: "**Next step**", Observed: at, native: &liveActivityNativeItem{thread: agent, item: "next"}},
		}, nil)
	}
	render := func(view *liveActivityView) string {
		return ansi.Strip(strings.Join(view.renderFeed(90, 60).lines, "\n"))
	}
	for _, view := range []*liveActivityView{u.view, u.agents} {
		if !strings.Contains(render(view), "first output line") {
			t.Fatalf("fixture lacks visible output: %s", render(view))
		}
	}
	if settleActivity(start.Add(activityui.OutputDebounce), u.view, u.agents) {
		t.Fatal("panes collapsed in different frames")
	}
	for _, view := range []*liveActivityView{u.view, u.agents} {
		if !strings.Contains(render(view), "first output line") {
			t.Fatal("one pane collapsed early")
		}
	}
	if !settleActivity(late.Add(activityui.OutputDebounce), u.view, u.agents) {
		t.Fatal("panes did not settle")
	}
	for _, view := range []*liveActivityView{u.view, u.agents} {
		if got := render(view); strings.Contains(got, "first output line") || !strings.Contains(got, "… +2 lines") {
			t.Fatalf("pane did not collapse:\n%s", got)
		}
	}
}
