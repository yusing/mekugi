package router

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	mekugi "github.com/yusing/mekugi"
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
	long := strings.Repeat("é", appServerTailBytes)
	if tail, _ := appServerOutputTail(&long); len(tail) != 1 || len(tail[0]) > appServerTailBytes+len("…") || !strings.HasSuffix(tail[0], "…") {
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
