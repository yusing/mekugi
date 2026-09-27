package router

import (
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
				len(tc.view.blocks[i]) == 0 || tc.view.blocks[i][0].Verb != "Edit" {
				t.Fatalf("receipt for %s: %+v, blocks %+v", tc.item, entry, tc.view.blocks[i])
			}
			for _, theme := range []livediff.Theme{livediff.DarkTheme, livediff.LightTheme} {
				painted := strings.Join((&activityui.Painter{Theme: theme}).Block(tc.view.blocks[i][0], 120), "\n")
				if !strings.HasPrefix(ansi.Strip(painted), "• "+tc.program+"\n    Edit") || strings.Contains(ansi.Strip(painted), " · "+tc.program) {
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
		if len(view.blocks[i]) == 0 || view.blocks[i][0].Verb != verb || view.blocks[i][0].ExitCode != exit {
			t.Fatalf("%s: text %q, blocks %+v, want %s exit %d", item, entry.Text, view.blocks[i], verb, exit)
		}
		return
	}
	t.Fatalf("missing native command %s", item)
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
		if v.entries[1].Text != "" || len(v.blocks[1]) != 0 || v.visible(v.entries[1]) {
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
