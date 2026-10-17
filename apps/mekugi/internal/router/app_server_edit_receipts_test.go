package router

import (
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/backends/codex/nativetrace"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/presentation"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/commandargs"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"

	activitymodel "github.com/yusing/mekugi/modules/activity/feed"

	jsonv1 "encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffmodel"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// These are retained host observations, not guesses from the shell text. The
// command item can arrive on either side of the change publication.
func TestUISnapshotAppServerCapturedEditReceipts(t *testing.T) {
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	type command struct {
		thread, item, carrier, path, program string
		codeMode                             bool
	}
	commands := []command{
		{"main", "cat-call", "cat-call\x00exec", "direct.go", "cat", false},
		{"child", "python-call", "cell-call", "nested.go", "python3", true},
	}
	for _, cmd := range commands {
		id, err := store.changes.Reserve(t.Context(), store.scoped(t.Context()).changeScope(), workspace, cmd.thread, cmd.carrier)
		if err != nil {
			t.Fatal(err)
		}
		history := mekugiHistory{
			ToolName: nativeExecCommandToolName, Root: workspace, ExecutingThread: cmd.thread,
			CorrelationID: cmd.carrier, ChangeID: id,
			ReviewFiles: []diffmodel.ReviewFile{diffmodel.RenderReviewFile(cmd.path, cmd.path, "old\n", "new\n")},
			ExecOutcome: &changecapture.Outcome{Status: changecapture.StatusCompleted, Coverage: changecapture.CoverageExact,
				CodeMode: cmd.codeMode, Labels: []string{cmd.program}},
		}
		if cmd.codeMode {
			history.HostResults = []binding.ToolResult{
				{CallID: cmd.item, Tool: nativeExecCommandToolName, Status: "completed"},
				{CallID: "sibling-call", Tool: nativeExecCommandToolName, Status: "completed"},
			}
		} else {
			history.ExecOutcome.Status = changecapture.StatusFailed
			history.ExecOutcome.Exit = new(2)
		}
		if err := store.put(t.Context(), workspace, map[string]mekugiHistory{cmd.carrier: history}); err != nil {
			t.Fatal(err)
		}
	}
	// A fresh replay store proves that the UI needs only durable evidence.
	store = testReplayStore(mekugiReplayStore{directory: store.directory})
	scope := previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"main": true, "child": true}}}
	snapshot, err := store.liveDiffSnapshot(t.Context(), scope)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Len() != len(commands) {
		t.Fatalf("retained attempts = %d, want %d", snapshot.Len(), len(commands))
	}
	u := newAppServerSessionTestUI(t, workspace)
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
	// Main receives its command first, then the retained change. The child
	// receives the retained change first, then its native command item.
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "m", "item": map[string]any{
		"id": "cat-call", "type": "commandExecution", "command": "cat > direct.go <<'EOF'\nnew\nEOF", "exitCode": 2}})
	assertCapturedCommand(t, u.view, "main", "cat-call", "Edit", 2)
	u.shell.saved.data = snapshot
	publishFixtureReview(t, u)
	u.applyCapturedEdits()
	assertCapturedCommand(t, u.view, "main", "cat-call", "Edit", 2)
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "c", "item": map[string]any{
		"id": "cat-call", "type": "commandExecution", "command": "cat unrelated.go", "exitCode": 0}})
	assertCapturedCommand(t, u.agents, "child", "cat-call", "Read", 0)
	sibling := appServerItem{ID: "sibling-call", Type: "commandExecution", Command: "/bin/bash -lc " + commandargs.QuoteArgument("rg needle src; python3 - <<'PY'\nopen('nested.go', 'w').write('PRIVATE_EDIT_SOURCE')\nPY"), ExitCode: new(0), DurationMS: new(int64(64))}
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "c", "item": sibling})
	assertCapturedCommand(t, u.agents, "child", "sibling-call", "Search", 0)
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "c", "item": map[string]any{
		"id": "python-call", "type": "commandExecution", "command": "python3 - <<'PY'\nopen('nested.go','w').write('new')\nPY", "exitCode": 0}})
	assertCapturedCommand(t, u.agents, "child", "python-call", "Edit", 0)
	assertCapturedCommand(t, u.agents, "child", "sibling-call", "Search", 0)
	assertCapturedCommand(t, u.view, "main", "cat-call", "Edit", 2)
	feed := ansi.Strip(strings.Join(u.agents.RenderFeed(120, 80).Lines, "\n"))
	if strings.Contains(feed, "PRIVATE_EDIT_SOURCE") || strings.Contains(feed, "included in grouped edit capture") || !strings.Contains(feed, "Search") {
		t.Fatalf("grouped capture leaked sibling source or lost its status row: %q", feed)
	}
	for _, tc := range []struct {
		view                        *activityAdapter
		thread, item, path, program string
	}{
		{u.view, "main", "cat-call", "direct.go", "cat"},
		{u.agents, "child", "python-call", "nested.go", "python3"},
	} {
		for i, entry := range tc.view.Entries {
			if entry.Source.native == nil || entry.Source.native.thread != tc.thread || entry.Source.native.item != tc.item {
				continue
			}
			if !strings.Contains(entry.Source.Text, "Edit `"+tc.path+"` +1 -1 · "+tc.program) ||
				len(tc.view.blocks(i)) == 0 || tc.view.blocks(i)[0].Verb != "Edit" {
				t.Fatalf("receipt for %s: %+v, blocks %+v", tc.item, entry, tc.view.blocks(i))
			}
			for _, theme := range []diffrender.Theme{diffrender.DarkTheme, diffrender.LightTheme} {
				painted := strings.Join((&activityui.Painter{Theme: theme}).Block(tc.view.blocks(i)[0], 120), "\n")
				// The file row names its outcome and source.
				heading := "Edited " + tc.path + " +1 -1 via " + tc.program
				if tc.program == "apply_patch" {
					heading = "Edited " + tc.path + " +1 -1"
				}
				if !strings.HasPrefix(ansi.Strip(painted), heading) || strings.Contains(ansi.Strip(painted), " · "+tc.program) {
					t.Fatalf("source was not a shared edit header: %q", painted)
				}
			}
		}
	}
	// History restored into a new UI still reconciles against retained receipts.
	restored := newAppServerSessionTestUI(t, workspace)
	restored.shell.saved.data = snapshot
	publishFixtureReview(t, restored)
	restored.restoreHistory([]appServerHistoryTurn{{ID: "prior", Status: "completed", Items: []appServerItem{{
		ID: "cat-call", Type: "commandExecution", Command: "cat > direct.go", ExitCode: new(2),
	}}}})
	assertCapturedCommand(t, restored.view, "main", "cat-call", "Edit", 2)
	restored.session.path("child")
	restored.restoreActivityThread(appServerThreadInfo{ID: "child", Cwd: workspace, Turns: []appServerHistoryTurn{{ID: "c", Status: "completed", Items: []appServerItem{sibling}}}})
	assertCapturedCommand(t, restored.agents, "child", sibling.ID, "Search", 0)
	for _, view := range []*activityAdapter{u.agents, restored.agents} {
		for _, entry := range view.Entries {
			if entry.Source.native == nil || entry.Source.native.item != sibling.ID {
				continue
			}
			var rows []string
			for _, block := range view.Disclosure.Blocks(entry.Values) {
				if block.Verb == "Edit" {
					t.Fatalf("captured sibling retained edit intent: %+v", block)
				}
				rows = append(rows, view.Painter.Block(block, 120)...)
			}
			assertNativeUISnapshot(t, "captured-edit-sibling-batch", rows)
		}
	}
}

func assertCapturedCommand(t *testing.T, view *activityAdapter, thread, item, verb string, exit int) {
	t.Helper()
	for i, entry := range view.Entries {
		if entry.Source.native == nil || entry.Source.native.thread != thread || entry.Source.native.item != item || entry.Source.Kind != "tool" {
			continue
		}
		if len(view.blocks(i)) == 0 || view.blocks(i)[0].Verb != verb || view.blocks(i)[0].ExitCode != exit {
			t.Fatalf("%s: text %q, blocks %+v, want %s exit %d", item, entry.Source.Text, view.blocks(i), verb, exit)
		}
		return
	}
	t.Fatalf("missing native command %s", item)
}

func TestAppServerCapturedEditsReuseReceipt(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	item := appServerItem{ID: "cmd", Type: "commandExecution", Command: "cat > a.go <<'EOF'\nnew\nEOF"}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	var data []*presentation.Receipt
	receipt := &presentation.Receipt{Thread: "main", Calls: []string{"cmd"}, Text: "Edit `a.go` +1 -1"}
	data = []*presentation.Receipt{receipt}
	u.shell.saved.data = reviewReceiptSnapshot(t, data)
	u.applyCapturedEdits()
	assertCapturedCommand(t, u.view, "main", "cmd", "Edit", 0)
	if allocations := testing.AllocsPerRun(5, u.applyCapturedEdits); allocations > 1 {
		t.Fatalf("unchanged receipt allocated %.0f times; want at most 1", allocations)
	}
	// A terminal host refresh must still reapply counts and keep its failure.
	item.ExitCode = new(2)
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	assertCapturedCommand(t, u.view, "main", "cmd", "Edit", 2)
	if !strings.Contains(u.view.Entries[0].Source.Text, "+1 -1") {
		t.Fatal("host refresh lost captured counts")
	}
	// A new receipt must replace the cached projection.
	updated := *receipt
	updated.Text = "Edit `a.go` +2 -1"
	data[0] = &updated
	u.shell.saved.data = reviewReceiptSnapshot(t, data)
	u.applyCapturedEdits()
	assertCapturedCommand(t, u.view, "main", "cmd", "Edit", 2)
	if !strings.Contains(u.view.Entries[0].Source.Text, "+2 -1") {
		t.Fatal("new receipt did not replace captured counts")
	}
}

func TestAppServerCapturedEditsPreserveTrackedExits(t *testing.T) {
	v := newActivityAdapter()
	v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{
		Seq: 1, Agent: "Main", Kind: "tool", CallID: "cmd", Text: "Run `git show`; Skill `mekugi-owners`; Run `git cherry-pick`",
		native: &liveActivityNativeItem{thread: "main", item: "cmd", phase: "item/completed", segments: []commandSegment{
			{text: "Run `git show`", tail: []string{"commit summary"}},
			{text: "Skill `mekugi-owners`", tail: []string{"owners"}},
			{text: "Run `git cherry-pick`", exit: 1, tail: []string{"conflict"}},
		}},
	}}})
	data := []*presentation.Receipt{{
		Thread: "main", Calls: []string{"cmd"}, Text: "Edit `a.go` +1 -1",
	}}
	for range 2 {
		v.applyCapturedEdits(data)
		blocks := v.blocks(0)
		if len(blocks) != 4 || blocks[0].Verb != "Edit" || blocks[0].ExitCode != 0 || blocks[1].ExitCode != 0 || blocks[2].ExitCode != 0 || blocks[3].ExitCode != 1 {
			t.Fatalf("receipt overwrote per-command statuses: %+v", blocks)
		}
	}
	got := mainFeed(&appServerUI{session: appServerSession{activity: new(activitymodel.Service)}, view: v}, 100)
	if strings.Count(got, "exit 1") != 1 || !strings.Contains(got, "git cherry-pick · exit 1") {
		t.Fatalf("receipt changed rendered failure ownership:\n%s", got)
	}
}

func TestAppServerGroupedReceiptOmitsBookkeeping(t *testing.T) {
	v := newActivityAdapter()
	calls := []string{"anchor", "edit", "tests", "failed-edit"}
	for i, source := range []string{
		"cat > a.go <<'EOF'\nfirst\nEOF",
		"cat >> b.go <<'EOF'\nPRIVATE_GROUPED_BODY\nEOF",
		"go test ./internal/router",
		"python3 -c 'open(\"c.go\", \"w\").write(\"PRIVATE_FAILED_BODY\")'",
	} {
		v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{
			Seq: uint64(i + 1), Agent: "Main", Kind: "tool", CallID: calls[i], Text: toolActivityShell(source),
			native: &liveActivityNativeItem{thread: "main", item: calls[i], phase: "item/completed"},
		}}})
	}
	v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{
		{Seq: 5, Agent: "Main", Kind: "exit", CallID: "tests", Text: "2"},
		{Seq: 6, Agent: "Main", Kind: "exit", CallID: "failed-edit", Text: "1"},
	}})
	data := []*presentation.Receipt{{
		Thread: "main", Calls: calls, Text: "Edit `a.go` +1 -0\n\nEdit `b.go` +1 -0",
	}}
	for range 2 {
		v.applyCapturedEdits(data)
		if v.Entries[1].Source.Text != "" || len(v.blocks(1)) != 0 || v.testVisible(v.Entries[1].Source) {
			t.Fatalf("successful sibling retained a placeholder: %+v", v.Entries[1])
		}
		assertCapturedCommand(t, v, "main", "tests", "Run", 2)
		assertCapturedCommand(t, v, "main", "failed-edit", "Edit", 1)
	}
	feed := ansi.Strip(strings.Join(v.RenderFeed(120, 80).Lines, "\n"))
	if strings.Contains(feed, "included in grouped") || strings.Contains(feed, "PRIVATE_") ||
		!strings.Contains(feed, "go test") || !strings.Contains(feed, "exit 2") || !strings.Contains(feed, "exit 1") {
		t.Fatalf("grouped receipt hid work/failures or exposed bookkeeping: %q", feed)
	}
}

// A cell that edits through an interpreter and
// reads in the same cell still yields host identities for its confirmed edit,
// live and after history is restored into a new UI.
func TestCodeModeCellEditReceipt(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	trace := newNativeTraceFixture(t)
	proxy.nativeTrace = nativetrace.New(trace.root)
	transform, _, _, workspace := newMekugiTestTransformWithProxy(t, proxy)
	transform.sessionShell = "bash"
	target := filepath.Join(workspace, "file.txt")
	if err := os.WriteFile(target, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	const edit = "python3 - <<'PY'\nopen('file.txt','w').write('new\\n')\nPY\ncat file.txt"
	const read = "git diff --stat"
	source := `text(await tools.exec_command({cmd: ` + string(responses.MustMarshalJSON(edit)) + `}));` +
		`text(await tools.exec_command({cmd: ` + string(responses.MustMarshalJSON(read)) + `}));`
	call := map[string]any{"type": "custom_tool_call", "id": "cell-item", "call_id": "cell", "name": "exec", "input": source, "status": "completed"}
	if _, err := transform.TransformJSON(mustTestJSON(t, map[string]any{"id": "response", "status": "completed", "output": []any{call}})); err != nil {
		t.Fatal(err)
	}
	if transform.local["cell"].CarrierPayload != source {
		t.Fatal("stock exec source changed")
	}
	trace.start("thread-1", "runtime", "cell", source)
	for _, tool := range []struct{ id, cmd string }{{"edit-exec", edit}, {"read-exec", read}} {
		trace.tool("thread-1", "runtime", tool.id, "exec_command", string(responses.MustMarshalJSON(map[string]any{"cmd": tool.cmd})))
		trace.result("thread-1", tool.id, "completed", map[string]any{"exit_code": 0})
	}
	trace.end("thread-1", "runtime")
	if err := os.WriteFile(target, []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	visible := maps.Clone(call)
	visible["input"] = source
	request := codexresponses.Request{Fields: map[string]jsonv1.RawMessage{"input": mustTestJSON(t, []any{visible, map[string]any{
		"type": "custom_tool_call_output", "call_id": "cell", "output": "Script completed\nWall time 0.1 seconds\nOutput:\n",
	}})}}
	if _, err := proxy.reconcileVisibleInput(transform.ctx, &request, workspace, transform.historySessionID); err != nil {
		t.Fatal(err)
	}
	record, found, err := proxy.replayStore.lookup(t.Context(), workspace, legacycall.ExecCallID("cell", true))
	if err != nil || !found || record.ChangeID == "" || record.ExecOutcome.Status != changecapture.StatusCompleted {
		t.Fatalf("exec record: found=%v err=%v record=%+v", found, err, record)
	}
	var calls []string
	for _, result := range record.HostResults {
		calls = append(calls, result.CallID)
	}
	if !slices.Equal(calls, []string{"edit-exec", "read-exec"}) {
		t.Fatalf("host results = %+v", record.HostResults)
	}

	store := testReplayStore(mekugiReplayStore{directory: proxy.replayStore.directory})
	snapshot, err := store.liveDiffSnapshot(t.Context(), previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"thread-1": true}}})
	if err != nil {
		t.Fatal(err)
	}
	item := appServerItem{ID: "edit-exec", Type: "commandExecution", Command: edit, ExitCode: new(0)}
	live := newAppServerSessionTestUI(t, workspace)
	live.Thread = "thread-1"
	live.session.start(live.Thread, workspace)
	live.shell.saved.data = snapshot
	publishFixtureReview(t, live)
	appServerTestNotify(t, live, "item/completed", map[string]any{"threadId": "thread-1", "turnId": "turn", "item": item})
	live.applyCapturedEdits()
	assertCapturedCommand(t, live.view, "thread-1", "edit-exec", "Edit", 0)
	restored := newAppServerSessionTestUI(t, workspace)
	restored.Thread = "thread-1"
	restored.session.start(restored.Thread, workspace)
	restored.shell.saved.data = snapshot
	publishFixtureReview(t, restored)
	restored.restoreHistory([]appServerHistoryTurn{{ID: "turn", Status: "completed", Items: []appServerItem{item}}})
	assertCapturedCommand(t, restored.view, "thread-1", "edit-exec", "Edit", 0)
}

func TestAppServerCapturedRemovalKeepsRunNeighbors(t *testing.T) {
	for _, source := range []string{"rm -- FILE; make check", "make check; rm -- FILE"} {
		for _, thread := range []string{"main", "child"} {
			t.Run(thread+source, func(t *testing.T) {
				u := newAppServerSessionTestUI(t, t.TempDir())
				if thread == "child" {
					appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": thread, "agentNickname": "worker"}})
				}
				view := u.view
				if thread == "child" {
					view = u.agents
				}
				item := appServerItem{ID: "cmd", Type: "commandExecution", Command: "/bin/bash -lc " + commandargs.QuoteArgument(source), ExitCode: new(0), AggregatedOutput: new(" M tracked.go\n")}
				appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": "t", "item": item})
				data := []*presentation.Receipt{{Thread: thread, Calls: []string{"cmd"}, Text: "Delete `FILE` +0 -1 · rm"}}
				check := func(view *activityAdapter) {
					for range 2 {
						view.applyCapturedEdits(data)
						blocks := view.blocks(0)
						if len(blocks) != 2 {
							t.Fatalf("lost neighbor: %+v", blocks)
						}
						run, edit := 1, 0
						if strings.HasPrefix(source, "make") {
							run, edit = 0, 1
						}
						if blocks[run].Verb != "Run" || !strings.Contains(blocks[run].Label+blocks[run].Code, "make check") || blocks[edit].Verb != "Delete" {
							t.Fatalf("incorrect operations: %+v", blocks)
						}
						if !strings.Contains(strings.Join(blocks[1].Tail, "\n"), "tracked.go") {
							t.Fatalf("lost aggregate output: %+v", blocks)
						}
					}
				}
				check(view)
				if thread == "main" {
					restored := newAppServerSessionTestUI(t, t.TempDir())
					restored.restoreHistory([]appServerHistoryTurn{{ID: "t", Status: "completed", Items: []appServerItem{item}}})
					check(restored.view)
				}
			})
		}
	}
}

func TestAppServerCapturedEditKeepsCommandPaths(t *testing.T) {
	u := newAppServerSessionTestUI(t, "/workspace")
	item := appServerItem{ID: "cmd", Type: "commandExecution", Cwd: "/command-dir", Command: "mcat /command-dir/read.go; rg '/command-dir/query' /command-dir/src; printf new > /command-dir/edit.go", Status: "completed", ExitCode: new(0)}
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	data := []*presentation.Receipt{{Thread: "main", Calls: []string{"cmd"}, Text: "Edit `edit.go` +1 -1"}}
	check := func(view *activityAdapter) {
		t.Helper()
		entry := view.Entries[0]
		if !strings.Contains(entry.Source.Text, "Read `read.go`") || !strings.Contains(entry.Source.Text, "Search `/command-dir/query` in `src`") || !strings.Contains(entry.Source.Text, "Edit `edit.go` +1 -1") || entry.Source.native.command != item.Command {
			t.Fatalf("receipt changed neighboring paths, query or source: %q", entry.Source.Text)
		}
	}
	u.shell.saved.data = reviewReceiptSnapshot(t, data)
	u.applyCapturedEdits()
	check(u.view)
	restored := newAppServerSessionTestUI(t, u.session.cwd)
	restored.shell.saved.data = reviewReceiptSnapshot(t, data)
	restored.restoreHistory([]appServerHistoryTurn{{ID: "t", Status: "completed", Items: []appServerItem{item}}})
	check(restored.view)
}
