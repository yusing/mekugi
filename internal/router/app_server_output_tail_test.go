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
	u.flushCommandOutput()
	agents := ansi.Strip(strings.Join(u.agents.renderFeed(90, 60).lines, "\n"))
	if !strings.Contains(agents, "linter") || !strings.Contains(agents, "Running make lint\n") || !strings.Contains(agents, "┆ linting") || strings.Contains(agents, "ok 7") {
		t.Fatalf("Agents lacks the child's live tail:\n%s", agents)
	}
	item["status"], item["exitCode"], item["aggregatedOutput"] = "completed", 0, "ok 1\nok 2\nPASS\n"
	notify("item/completed", map[string]any{"item": item})
	notify("item/commandExecution/outputDelta", map[string]any{"itemId": "cmd", "delta": "late\n"})
	u.flushCommandOutput()
	// The host's output stays readable for the linger, then collapses.
	open := "└ Ran go test ./...\n      ┆ ok 1\n      ┆ ok 2\n      ┆ PASS"
	if got := main(); !strings.Contains(got, open) || strings.Contains(got, "Running") || strings.Contains(got, "late") {
		t.Fatalf("completed command lacks its settled output %q:\n%s", open, got)
	}
	if u.view.settle(time.Now().Add(activityui.OutputLinger - time.Second)) {
		t.Fatal("output collapsed before its linger")
	}
	if !u.view.settle(time.Now().Add(activityui.OutputLinger)) {
		t.Fatal("output did not collapse after its linger")
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
		u.view.settle(time.Now().Add(activityui.OutputLinger))
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
