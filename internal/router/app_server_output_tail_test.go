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

func TestManagedFilesJoinEditedGroupInMain(t *testing.T) {
	workspace := t.TempDir()
	files := make([]mekugi.ReviewFile, 4)
	for i := range files {
		files[i] = mekugi.RenderReviewFile("", fmt.Sprintf("gen-%d.txt", i), "", "x\n")
		files[i].Origin = "generator"
	}
	receipt := editReceiptText(workspace, mekugiHistory{ReviewFiles: files, ExecOutcome: &execOutcome{Labels: []string{"git"}}})
	if want := "+ 4 tool-managed files (`gen-0.txt`, `gen-1.txt`, `gen-2.txt`, …) · git"; receipt != want {
		t.Fatalf("receipt = %q, want %q", receipt, want)
	}
	v := newLiveActivityView()
	v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{
		{Seq: 1, Agent: "Main", Kind: "tool", CallID: "pick", Text: receipt},
		{Seq: 2, Agent: "Main", Kind: "tool", CallID: "skill", Text: "Skill `run use-modern-go/scripts/run-tool.sh list`"},
	}})
	got := ansi.Strip(strings.Join(v.renderConversation(100).lines, "\n"))
	want := "├ Edited 4 tool-managed files (gen-0.txt, gen-1.txt, gen-2.txt, …) via git\n└ Skill  run use-modern-go/scripts/run-tool.sh list"
	if !strings.Contains(got, want) {
		t.Fatalf("Main = %q, want %q", got, want)
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
	u.flushCommandOutput()
	if got := main(); !strings.Contains(got, "┆ ok 1") || strings.Contains(got, "ok 2") {
		t.Fatalf("burst jumped to its tail instead of rolling:\n%s", got)
	}
	rollCommandOutput(u)
	want := "└ Running go test ./...\n          ┆ … 4 earlier lines\n          ┆ ok 4\n          ┆ ok 5\n          ┆ ok 6\n          ┆ ok 7\n          ┆ --- partial"
	if got := main(); !strings.Contains(got, want) {
		t.Fatalf("live tail missing %q:\n%s", want, got)
	}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "child", "turnId": "c", "item": map[string]any{
		"id": "cmd", "type": "commandExecution", "command": "make lint", "status": "inProgress"}})
	appServerTestNotify(t, u, "item/commandExecution/outputDelta", map[string]any{"threadId": "child", "turnId": "c", "itemId": "cmd", "delta": "linting\n"})
	// A rename after the start must not move the running row back.
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child",
		"source": map[string]any{"subAgent": map[string]any{"thread_spawn": map[string]any{"agent_path": "/root/linter"}}}}})
	rollCommandOutput(u)
	agents := ansi.Strip(strings.Join(u.agents.renderFeed(90, 60).lines, "\n"))
	if !strings.Contains(agents, "linter") || !strings.Contains(agents, "Running make lint\n") || !strings.Contains(agents, "┆ linting") || strings.Contains(agents, "ok 7") {
		t.Fatalf("Agents lacks the child's live tail:\n%s", agents)
	}
	item["status"], item["exitCode"], item["aggregatedOutput"] = "completed", 0, "ok 1\nok 2\nPASS\n"
	notify("item/completed", map[string]any{"item": item})
	notify("item/commandExecution/outputDelta", map[string]any{"itemId": "cmd", "delta": "late\n"})
	rollCommandOutput(u)
	// The host's output stays readable until Main's next event, then collapses.
	open := "└ Ran go test ./...\n      ┆ ok 1\n      ┆ ok 2\n      ┆ PASS"
	if got := main(); !strings.Contains(got, open) || strings.Contains(got, "Running") || strings.Contains(got, "late") {
		t.Fatalf("completed command lacks its settled output %q:\n%s", open, got)
	}
	if u.view.settle(time.Now().Add(time.Hour)) {
		t.Fatal("output collapsed before a later event")
	}
	nextEvent(t, u, "main")
	if u.view.settle(time.Now()) {
		t.Fatal("output collapsed before events paused")
	}
	if !u.view.settle(time.Now().Add(activityui.OutputDebounce)) {
		t.Fatal("output did not collapse after the next event")
	}
	feed := u.view.renderFeed(90, 60)
	got := ansi.Strip(strings.Join(feed.lines, "\n"))
	if collapsed := "└ Ran go test ./...\n      ┆ … +3 lines"; !strings.Contains(got, collapsed) || strings.Contains(got, "ok 1") {
		t.Fatalf("settled output is not collapsed to %q:\n%s", collapsed, got)
	}
	// Either row of the command opens its output, and again closes it.
	index := slices.IndexFunc(feed.lines, func(line string) bool { return strings.Contains(line, "… +3 lines") })
	snippet := feed.snippets[index]
	if snippet == (liveActivitySnippet{}) || feed.snippets[index-1] != snippet {
		t.Fatalf("collapsed output has no toggle: %v", feed.snippets)
	}
	u.view.toggleSnippet(snippet)
	if got := main(); !strings.Contains(got, open) {
		t.Fatalf("expanded output = \n%s", got)
	}
	u.view.toggleSnippet(snippet)
	if got := main(); !strings.Contains(got, "┆ … +3 lines") {
		t.Fatalf("output did not collapse again:\n%s", got)
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
		u.view.settle(time.Now().Add(activityui.OutputDebounce))
		feed := u.view.renderFeed(90, 60)
		got := ansi.Strip(strings.Join(feed.lines, "\n"))
		if !strings.Contains(got, "┆ done") || strings.Contains(got, "+1 lines") {
			t.Fatalf("single output line hidden: %s", got)
		}
		for _, snippet := range feed.snippets {
			if snippet != (liveActivitySnippet{}) {
				t.Fatal("single output line has a collapse toggle")
			}
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
			check("┆ second")
			nextEvent(t, u, "main")
			u.view.settle(time.Now().Add(activityui.OutputDebounce))
			if exit != 0 {
				check("┆ second")
				return
			}
			check("┆ … +2 lines")
			feed := u.view.renderFeed(100, 60)
			index := slices.IndexFunc(feed.lines, func(line string) bool { return strings.Contains(line, "… +2 lines") })
			snippet := feed.snippets[index]
			if snippet == (liveActivitySnippet{}) {
				t.Fatal("final Read output has no toggle")
			}
			u.view.toggleSnippet(snippet)
			check("┆ second")
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
		u.flushCommandOutput()
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
	u.flushCommandOutput()
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
		"id": "next-" + thread, "type": "reasoning", "summary": []string{"**Next step**"}}})
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
	if u.view.settle(start.Add(activityui.OutputDebounce / 2)) {
		t.Fatal("output collapsed while events were still arriving")
	}
	run("third")
	third := u.view.events["Main"].at
	if u.view.settle(third.Add(activityui.OutputDebounce / 2)) {
		t.Fatal("a later event did not restart the pause")
	}
	if !u.view.settle(third.Add(activityui.OutputDebounce)) {
		t.Fatal("settled output did not collapse after events paused")
	}
	got := ansi.Strip(strings.Join(u.view.renderFeed(100, 60).lines, "\n"))
	if strings.Contains(got, "first 1") || strings.Contains(got, "second 1") || !strings.Contains(got, "third 1") {
		t.Fatalf("only the output with a later event should collapse:\n%s", got)
	}
}
