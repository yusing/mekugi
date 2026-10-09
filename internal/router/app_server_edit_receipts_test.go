package router

import (
	jsonv1 "encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

// These are retained host observations, not guesses from the shell text. The
// command item can arrive on either side of the change publication.
func TestAppServerCapturedEditReceipts(t *testing.T) {
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
		id, err := store.reserveChange(t.Context(), workspace, cmd.thread, cmd.carrier)
		if err != nil {
			t.Fatal(err)
		}
		history := mekugiHistory{
			ToolName: nativeExecCommandToolName, Root: workspace, ExecutingThread: cmd.thread,
			CorrelationID: cmd.carrier, ChangeID: id,
			ReviewFiles: []mekugi.ReviewFile{mekugi.RenderReviewFile(cmd.path, cmd.path, "old\n", "new\n")},
			ExecOutcome: &execOutcome{Status: execStatusCompleted, Coverage: execCoverageExact,
				CodeMode: cmd.codeMode, Labels: []string{cmd.program}},
		}
		if cmd.codeMode {
			history.HostResults = []nativeToolResult{
				{CallID: cmd.item, Tool: nativeExecCommandToolName, Status: "completed"},
				{CallID: "sibling-call", Tool: nativeExecCommandToolName, Status: "completed"},
			}
		} else {
			history.ExecOutcome.Status = execStatusFailed
			history.ExecOutcome.Exit = new(2)
		}
		if err := store.put(t.Context(), workspace, map[string]mekugiHistory{cmd.carrier: history}); err != nil {
			t.Fatal(err)
		}
	}
	// A fresh replay store proves that the UI needs only durable evidence.
	store = &mekugiReplayStore{directory: store.directory}
	scope := liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"main": true, "child": true}}}
	snapshot, err := store.liveDiffSnapshot(t.Context(), scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.order) != len(commands) {
		t.Fatalf("retained attempts = %d, want %d", len(snapshot.order), len(commands))
	}
	u := newAppServerSessionTestUI(t, workspace)
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
	// Main receives its command first, then the retained change. The child
	// receives the retained change first, then its native command item.
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "m", "item": map[string]any{
		"id": "cat-call", "type": "commandExecution", "command": "cat > direct.go <<'EOF'\nnew\nEOF", "exitCode": 2}})
	assertCapturedCommand(t, u.view, "main", "cat-call", "Edit", 2)
	u.shell.diff.data = snapshot
	u.applyCapturedEdits()
	assertCapturedCommand(t, u.view, "main", "cat-call", "Edit", 2)
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "c", "item": map[string]any{
		"id": "cat-call", "type": "commandExecution", "command": "cat unrelated.go", "exitCode": 0}})
	assertCapturedCommand(t, u.agents, "child", "cat-call", "Read", 0)
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "c", "item": map[string]any{
		"id": "sibling-call", "type": "commandExecution", "command": "rg needle src; python3 - <<'PY'\nopen('nested.go', 'w').write('PRIVATE_EDIT_SOURCE')\nPY", "exitCode": 0}})
	assertCapturedCommand(t, u.agents, "child", "sibling-call", "Search", 0)
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "c", "item": map[string]any{
		"id": "python-call", "type": "commandExecution", "command": "python3 - <<'PY'\nopen('nested.go','w').write('new')\nPY", "exitCode": 0}})
	assertCapturedCommand(t, u.agents, "child", "python-call", "Edit", 0)
	assertCapturedCommand(t, u.agents, "child", "sibling-call", "Search", 0)
	assertCapturedCommand(t, u.view, "main", "cat-call", "Edit", 2)
	feed := ansi.Strip(strings.Join(u.agents.renderFeed(120, 80).lines, "\n"))
	if strings.Contains(feed, "PRIVATE_EDIT_SOURCE") || strings.Contains(feed, "included in grouped edit capture") || !strings.Contains(feed, "Search") {
		t.Fatalf("grouped capture leaked sibling source or lost its status row: %q", feed)
	}
	for _, tc := range []struct {
		view                        *liveActivityView
		thread, item, path, program string
	}{
		{u.view, "main", "cat-call", "direct.go", "cat"},
		{u.agents, "child", "python-call", "nested.go", "python3"},
	} {
		for i, entry := range tc.view.entries {
			if entry.native == nil || entry.native.thread != tc.thread || entry.native.item != tc.item {
				continue
			}
			if !strings.Contains(entry.Text, "Edit `"+tc.path+"` +1 -1 · "+tc.program) ||
				len(tc.view.entries[i].blocks) == 0 || tc.view.entries[i].blocks[0].Verb != "Edit" {
				t.Fatalf("receipt for %s: %+v, blocks %+v", tc.item, entry, tc.view.entries[i].blocks)
			}
			for _, theme := range []livediff.Theme{livediff.DarkTheme, livediff.LightTheme} {
				painted := strings.Join((&activityui.Painter{Theme: theme}).Block(tc.view.entries[i].blocks[0], 120), "\n")
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
	restored.shell.diff.data = snapshot
	restored.restoreHistory([]appServerHistoryTurn{{ID: "prior", Status: "completed", Items: []appServerItem{{
		ID: "cat-call", Type: "commandExecution", Command: "cat > direct.go", ExitCode: new(2),
	}}}})
	assertCapturedCommand(t, restored.view, "main", "cat-call", "Edit", 2)
}

func assertCapturedCommand(t *testing.T, view *liveActivityView, thread, item, verb string, exit int) {
	t.Helper()
	for i, entry := range view.entries {
		if entry.native == nil || entry.native.thread != thread || entry.native.item != item || entry.Kind != "tool" {
			continue
		}
		if len(view.entries[i].blocks) == 0 || view.entries[i].blocks[0].Verb != verb || view.entries[i].blocks[0].ExitCode != exit {
			t.Fatalf("%s: text %q, blocks %+v, want %s exit %d", item, entry.Text, view.entries[i].blocks, verb, exit)
		}
		return
	}
	t.Fatalf("missing native command %s", item)
}

func TestAppServerCapturedEditsReuseReceipt(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	item := appServerItem{ID: "cmd", Type: "commandExecution", Command: "cat > a.go <<'EOF'\nnew\nEOF"}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	data := newLiveDiffData()
	data.order = []string{"receipt"}
	receipt := &capturedActivityEdit{thread: "main", calls: []string{"cmd"}, text: "Edit `a.go` +1 -1"}
	data.attempts["receipt"] = liveDiffAttempt{receipt: receipt}
	u.shell.diff.data = data
	u.applyCapturedEdits()
	assertCapturedCommand(t, u.view, "main", "cmd", "Edit", 0)
	if allocations := testing.AllocsPerRun(5, u.applyCapturedEdits); allocations > 1 {
		t.Fatalf("unchanged receipt allocated %.0f times; want at most 1", allocations)
	}
	// A terminal host refresh must still reapply counts and keep its failure.
	item.ExitCode = new(2)
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	assertCapturedCommand(t, u.view, "main", "cmd", "Edit", 2)
	if !strings.Contains(u.view.entries[0].Text, "+1 -1") {
		t.Fatal("host refresh lost captured counts")
	}
	// A new receipt must replace the cached projection.
	updated := *receipt
	updated.text = "Edit `a.go` +2 -1"
	data.attempts["receipt"] = liveDiffAttempt{receipt: &updated}
	u.applyCapturedEdits()
	assertCapturedCommand(t, u.view, "main", "cmd", "Edit", 2)
	if !strings.Contains(u.view.entries[0].Text, "+2 -1") {
		t.Fatal("new receipt did not replace captured counts")
	}
}

func TestAppServerCapturedEditsPreserveTrackedExits(t *testing.T) {
	v := newLiveActivityView()
	v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{
		Seq: 1, Agent: "Main", Kind: "tool", CallID: "cmd", Text: "Run `git show`; Skill `mekugi-owners`; Run `git cherry-pick`",
		native: &liveActivityNativeItem{thread: "main", item: "cmd", phase: "item/completed", segments: []commandSegment{
			{text: "Run `git show`", tail: []string{"commit summary"}},
			{text: "Skill `mekugi-owners`", tail: []string{"owners"}},
			{text: "Run `git cherry-pick`", exit: 1, tail: []string{"conflict"}},
		}},
	}}})
	data := newLiveDiffData()
	data.order = []string{"receipt"}
	data.attempts["receipt"] = liveDiffAttempt{receipt: &capturedActivityEdit{
		thread: "main", calls: []string{"cmd"}, text: "Edit `a.go` +1 -1",
	}}
	for range 2 {
		v.applyCapturedEdits(data)
		blocks := v.entries[0].blocks
		if len(blocks) != 4 || blocks[0].Verb != "Edit" || blocks[0].ExitCode != 0 || blocks[1].ExitCode != 0 || blocks[2].ExitCode != 0 || blocks[3].ExitCode != 1 {
			t.Fatalf("receipt overwrote per-command statuses: %+v", blocks)
		}
	}
	got := mainFeed(&appServerUI{view: v}, 100)
	if strings.Count(got, "exit 1") != 1 || !strings.Contains(got, "git cherry-pick · exit 1") {
		t.Fatalf("receipt changed rendered failure ownership:\n%s", got)
	}
}

func TestAppServerGroupedReceiptOmitsBookkeeping(t *testing.T) {
	v := newLiveActivityView()
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
	data := newLiveDiffData()
	data.order = []string{"group"}
	data.attempts["group"] = liveDiffAttempt{receipt: &capturedActivityEdit{
		thread: "main", calls: calls, text: "Edit `a.go` +1 -0\n\nEdit `b.go` +1 -0",
	}}
	for range 2 {
		v.applyCapturedEdits(data)
		if v.entries[1].Text != "" || len(v.entries[1].blocks) != 0 || v.visible(v.entries[1].activityPaneEntry) {
			t.Fatalf("successful sibling retained a placeholder: %+v", v.entries[1])
		}
		assertCapturedCommand(t, v, "main", "tests", "Run", 2)
		assertCapturedCommand(t, v, "main", "failed-edit", "Edit", 1)
	}
	feed := ansi.Strip(strings.Join(v.renderFeed(120, 80).lines, "\n"))
	if strings.Contains(feed, "included in grouped") || strings.Contains(feed, "PRIVATE_") ||
		!strings.Contains(feed, "go test") || !strings.Contains(feed, "exit 2") || !strings.Contains(feed, "exit 1") {
		t.Fatalf("grouped receipt hid work/failures or exposed bookkeeping: %q", feed)
	}
}

// A cell that publishes a journal milestone, edits through an interpreter, and
// reads in the same cell still yields host identities for its confirmed edit,
// live and after history is restored into a new UI.
func TestCodeModeMCPJournalCellEditReceipt(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	trace := newNativeTraceFixture(t)
	proxy.nativeTrace = &nativeToolTrace{directory: trace.root}
	transform, _, _, workspace := newMekugiTestTransformWithProxy(t, proxy)
	transform.sessionShell = "bash"
	target := filepath.Join(workspace, "file.txt")
	if err := os.WriteFile(target, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	const edit = "python3 - <<'PY'\nopen('file.txt','w').write('new\\n')\nPY\ncat file.txt"
	const read = "git diff --stat"
	source := `await tools.mcp__mekugi__journal_mutate({mutations:[{op:"log",text:"Editing"}]});` +
		`text(await tools.exec_command({cmd: ` + string(mustMarshalJSON(edit)) + `}));` +
		`text(await tools.exec_command({cmd: ` + string(mustMarshalJSON(read)) + `}));`
	call := map[string]any{"type": "custom_tool_call", "id": "cell-item", "call_id": "cell", "name": "exec", "input": source, "status": "completed"}
	if _, err := transform.TransformJSON(mustTestJSON(t, map[string]any{"id": "response", "status": "completed", "output": []any{call}})); err != nil {
		t.Fatal(err)
	}
	if transform.local["cell"].CarrierPayload != source {
		t.Fatal("stock exec source changed")
	}
	trace.start("thread-1", "runtime", "cell", source)
	for _, tool := range []struct{ id, cmd string }{{"edit-exec", edit}, {"read-exec", read}} {
		trace.tool("thread-1", "runtime", tool.id, "exec_command", string(mustMarshalJSON(map[string]any{"cmd": tool.cmd})))
		trace.result("thread-1", tool.id, "completed", map[string]any{"exit_code": 0})
	}
	trace.end("thread-1", "runtime")
	if err := os.WriteFile(target, []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	visible := maps.Clone(call)
	visible["input"] = source
	request := parsedResponsesRequest{fields: map[string]jsonv1.RawMessage{"input": mustTestJSON(t, []any{visible, map[string]any{
		"type": "custom_tool_call_output", "call_id": "cell", "output": "Script completed\nWall time 0.1 seconds\nOutput:\n",
	}})}}
	if _, err := proxy.reconcileVisibleInput(transform.ctx, &request, workspace, transform.historySessionID); err != nil {
		t.Fatal(err)
	}
	record, found, err := proxy.replayStore.lookup(t.Context(), workspace, execDerivedCallID("cell", true))
	if err != nil || !found || record.ChangeID == "" || record.ExecOutcome.Status != execStatusCompleted {
		t.Fatalf("exec record: found=%v err=%v record=%+v", found, err, record)
	}
	var calls []string
	for _, result := range record.HostResults {
		calls = append(calls, result.CallID)
	}
	if !slices.Equal(calls, []string{"edit-exec", "read-exec"}) {
		t.Fatalf("host results = %+v", record.HostResults)
	}

	store := &mekugiReplayStore{directory: proxy.replayStore.directory}
	snapshot, err := store.liveDiffSnapshot(t.Context(), liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"thread-1": true}}})
	if err != nil {
		t.Fatal(err)
	}
	item := appServerItem{ID: "edit-exec", Type: "commandExecution", Command: edit, ExitCode: new(0)}
	live := newAppServerSessionTestUI(t, workspace)
	live.thread = "thread-1"
	live.session.start(live.thread, workspace)
	live.shell.diff.data = snapshot
	appServerTestNotify(t, live, "item/completed", map[string]any{"threadId": "thread-1", "turnId": "turn", "item": item})
	live.applyCapturedEdits()
	assertCapturedCommand(t, live.view, "thread-1", "edit-exec", "Edit", 0)
	restored := newAppServerSessionTestUI(t, workspace)
	restored.thread = "thread-1"
	restored.session.start(restored.thread, workspace)
	restored.shell.diff.data = snapshot
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
				item := appServerItem{ID: "cmd", Type: "commandExecution", Command: "/bin/bash -lc " + shellQuoteArgument(source), ExitCode: new(0), AggregatedOutput: new(" M tracked.go\n")}
				appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": "t", "item": item})
				data := newLiveDiffData()
				data.order = []string{"receipt"}
				data.attempts["receipt"] = liveDiffAttempt{receipt: &capturedActivityEdit{thread: thread, calls: []string{"cmd"}, text: "Delete `FILE` +0 -1 · rm"}}
				check := func(view *liveActivityView) {
					for range 2 {
						view.applyCapturedEdits(data)
						blocks := view.entries[0].blocks
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
	data := newLiveDiffData()
	data.order = []string{"receipt"}
	data.attempts["receipt"] = liveDiffAttempt{receipt: &capturedActivityEdit{thread: "main", calls: []string{"cmd"}, text: "Edit `edit.go` +1 -1"}}
	check := func(view *liveActivityView) {
		t.Helper()
		entry := view.entries[0]
		if !strings.Contains(entry.Text, "Read `read.go`") || !strings.Contains(entry.Text, "Search `/command-dir/query` in `src`") || !strings.Contains(entry.Text, "Edit `edit.go` +1 -1") || entry.native.command != item.Command {
			t.Fatalf("receipt changed neighboring paths, query or source: %q", entry.Text)
		}
	}
	u.shell.diff.data = data
	u.applyCapturedEdits()
	check(u.view)
	restored := newAppServerSessionTestUI(t, u.session.cwd)
	restored.shell.diff.data = data
	restored.restoreHistory([]appServerHistoryTurn{{ID: "t", Status: "completed", Items: []appServerItem{item}}})
	check(restored.view)
}
