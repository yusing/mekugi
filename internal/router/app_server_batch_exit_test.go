package router

import (
	"strings"
	"testing"

	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

func TestAppServerBatchFailureDoesNotFailEachOperation(t *testing.T) {
	const script = "git show --stat --oneline 3333fb1c; skills-mgr get mekugi-owners; git cherry-pick 3333fb1c"
	item := appServerItem{ID: "cmd", Type: "commandExecution", Command: "/usr/bin/bash -lc " + quoteShellWord(script),
		Status: "failed", ExitCode: new(1), AggregatedOutput: new("git output\nskill output\nconflict\n")}
	for _, mode := range []string{"live", "resume", "child history"} {
		t.Run(mode, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			view := u.view
			switch mode {
			case "live":
				started := item
				started.Status, started.ExitCode, started.AggregatedOutput = "inProgress", nil, nil
				appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "t", "item": started})
				appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": item})
			case "resume":
				u.restoreHistory([]appServerHistoryTurn{{ID: "t", Status: "completed", Items: []appServerItem{item}}})
			case "child history":
				appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
				u.restoreActivityThread(appServerThreadInfo{ID: "child", Turns: []appServerHistoryTurn{{ID: "t", Status: "completed", Items: []appServerItem{item}}}})
				view = u.agents
			}
			finishPacing(u.view, u.agents)
			got := mainFeed(&appServerUI{view: view}, 140)
			if strings.Count(got, "exit 1") != 1 || !strings.Contains(got, "Ran   shell batch · exit 1") || !strings.Contains(got, "┆ conflict") {
				t.Fatalf("missing unique batch result:\n%s", got)
			}
			for _, record := range view.entries {
				blocks := record.blocks
				for _, block := range blocks {
					if !block.BatchExit && (block.ExitCode != 0 || len(block.Tail) != 0) {
						t.Fatalf("batch result attributed to an operation: %+v", block)
					}
				}
			}
		})
	}
}

func TestCommandExitBlocksKeepsBatchIdentity(t *testing.T) {
	blocks := []activityui.Block{{Kind: "op", Verb: "Run", Code: "true"}, {Kind: "op", Verb: "Skill", Label: "example"}, {Kind: "filter"}}
	for range 2 {
		blocks = commandExitBlocks(blocks, 1, []string{"failure"}, 3)
	}
	if len(blocks) != 4 || !blocks[3].BatchExit || blocks[3].ExitCode != 1 || blocks[3].TailOmitted != 3 || blocks[0].ExitCode != 0 || blocks[1].ExitCode != 0 {
		t.Fatalf("batch status duplicated or attributed to operations: %+v", blocks)
	}
	tracked := []activityui.Block{{Kind: "op", Verb: "Run", Segment: true}, {Kind: "op", Verb: "Run", Segment: true, ExitCode: 2}}
	tracked = commandExitBlocks(tracked, 2, []string{"combined"}, 0)
	if len(tracked) != 2 || tracked[0].ExitCode != 0 || tracked[1].ExitCode != 2 || len(tracked[1].Tail) != 0 {
		t.Fatalf("aggregate overwrote tracked outcomes: %+v", tracked)
	}
}
