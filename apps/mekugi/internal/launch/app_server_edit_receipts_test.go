package launch

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/backends/codex/nativetrace"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/presentation"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/commandargs"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"

	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	jsonv1 "encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffmodel"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func TestCapturedEditInvocationCountWithoutWriterLabel(t *testing.T) {
	for _, writer := range []string{"", "apply_patch"} {
		t.Run(writer, func(t *testing.T) {
			v := newActivityAdapter()
			v.Conversation = true
			for i, path := range []string{"one.go", "two.go"} {
				call := fmt.Sprint(i)
				v.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{Seq: uint64(i + 1), Agent: "Main", Kind: "tool", CallID: call,
					Native: &sessionpresentation.NativeItem{Thread: "main", Item: call, Phase: "item/completed", Live: true}}}})
				history := legacycall.History{ToolName: "exec_command", Source: "exec_command", ExecutingThread: "main", CorrelationID: call + "\x00exec",
					ReviewFiles: retainedReviewFiles([]diffmodel.ReviewFile{diffmodel.RenderReviewFile(path, path, "old\n", "new\n")}), ExecOutcome: &legacycall.Outcome{}}
				if writer != "" {
					history.ExecOutcome.Labels = []string{writer}
				}
				v.ApplyCapturedEdits([]*presentation.Receipt{testCapturedEditActivity("/workspace", history)})
			}
			v.publishActivity()
			feed := v.RenderConversation(100)
			if text := ansi.Strip(strings.Join(feed.Lines, "\n")); !strings.Contains(text, "×2") {
				t.Fatalf("two captures lost invocation count: %s", text)
			}
			for i, path := range []string{"one.go", "two.go"} {
				if !slices.Contains(feed.Snippets, activityui.Snippet{Run: uint64(i + 1), Block: activityui.EditNavigationSnippet, Path: path}) {
					t.Fatalf("capture lost exact source for %s", path)
				}
			}
		})
	}
}

func TestUISnapshotAppServerTemporaryCaptureWithoutWriterLabel(t *testing.T) {
	const workspace = "/workspace"
	files := []diffmodel.ReviewFile{diffmodel.RenderReviewFile("project.go", "project.go", "old\n", "new\n")}
	for i := range 200 {
		files = append(files, diffmodel.RenderReviewFile("", fmt.Sprintf("/tmp/worktree/file%03d.go", i), "", "created\n"))
	}
	files = append(files, diffmodel.RenderReviewFile("after.go", "after.go", "old\n", "new\n"),
		diffmodel.RenderIncompleteReviewFile("", "/tmp/worktree/unknown.go", "capture budget exhausted"))
	// Unclassified stock exec captures deliberately have no writer label.
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.changes.Reserve(t.Context(), store.scoped(t.Context()).changeScope(), workspace, "main", "cmd\x00exec")
	if err != nil {
		t.Fatal(err)
	}
	history := legacycall.History{ToolName: "exec_command", Source: "exec_command", ExecutingThread: "main", CorrelationID: "cmd\x00exec", ChangeID: id, Root: workspace, ReviewFiles: retainedReviewFiles(files),
		ExecOutcome: &legacycall.Outcome{Status: changecapture.StatusCompleted, Coverage: changecapture.CoverageExact}}
	if err := store.put(t.Context(), workspace, map[string]legacycall.History{history.CorrelationID: history}); err != nil {
		t.Fatal(err)
	}
	// Reload only durable capture evidence, without relying on a live producer.
	store = testReplayStore(mekugiReplayStore{directory: store.directory})
	snapshot, err := store.liveDiffSnapshot(t.Context(), previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"main": true}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(snapshot.Receipts()[0].Text, " · exec_command") {
		t.Fatal("stock execution acquired an invented writer label")
	}
	for _, state := range []string{"live", "settled", "restored"} {
		t.Run(state, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, workspace)
			v := u.shell.Transcript
			v.Painter.Theme = diffrender.DarkTheme
			v.Clock = func() time.Time { return time.Unix(1000, 0) }
			item := binding.ThreadItem{ID: "cmd", Type: "commandExecution", Cwd: workspace, Command: "git worktree add /tmp/worktree HEAD", Status: "completed", ExitCode: new(0), AggregatedOutput: new("Preparing worktree\n")}
			u.review.Projection.Data = snapshot
			publishFixtureReview(t, u)
			if state == "restored" {
				u.restoreHistory([]binding.HistoryTurn{{ID: "t", Status: "completed", Items: []binding.ThreadItem{item}}})
			} else {
				appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "t"}})
				phase := "item/completed"
				if state == "live" {
					phase, item.Status, item.ExitCode = "item/started", "inProgress", nil
				}
				appServerTestNotify(t, u, phase, map[string]any{"threadId": "main", "turnId": "t", "item": item})
				applyCapturedEdits(&u.attached, u.review, &u.pendingEditReceipts)
			}
			v.RenderConversation(110)
			v.Pace(time.Unix(1000, 0).Add(time.Hour))
			for _, expansion := range []uint8{0, 1, 2} {
				v.Expansion = expansion
				feed := v.RenderConversation(110)
				text := ansi.Strip(strings.Join(feed.Lines, "\n"))
				if expansion != 2 {
					if !strings.Contains(text, "… +190 files") || strings.Contains(text, "file010.go") {
						t.Fatalf("expansion %d left temporary capture unbounded (%d rows): %s", expansion, len(feed.Lines), text)
					}
					row := slices.IndexFunc(feed.Lines, func(line string) bool { return strings.Contains(line, "… +190 files") })
					target := feed.Snippets[row]
					if target.Block != activityui.EditNavigationSnippet || target.Path != "" || target.Run == 0 {
						t.Fatalf("omission lost retained capture navigation: %+v", target)
					}
					pages, selected := activityEditPages(&u.attached, u.shell, u.activity, v, target.Run, target.Path)
					if len(pages) != len(files) || selected != pages[0].ID || pages[200].Path != "/tmp/worktree/file199.go" || !strings.Contains(pages[200].Code, "+created") {
						t.Fatalf("omitted files lost durable pages: %d pages, selected %q", len(pages), selected)
					}
					if state == "settled" && expansion == 0 {
						assertNativeUISnapshot(t, "temporary-capture-without-writer", feed.Lines)
					}
				} else {
					for i := range 200 {
						path := fmt.Sprintf("/tmp/worktree/file%03d.go", i)
						if !slices.ContainsFunc(feed.Snippets, func(s activityui.Snippet) bool { return s.Block == activityui.EditNavigationSnippet && s.Path == path }) {
							t.Fatalf("expanded capture lost %s", path)
						}
					}
				}
				if state != "live" && !strings.Contains(text, "Preparing worktree") {
					t.Fatalf("capture lost command output: %s", text)
				}
				for _, want := range []string{"project.go", "after.go", "capture budget exhausted"} {
					if !strings.Contains(text, want) {
						t.Fatalf("capture lost %q: %s", want, text)
					}
				}
			}
		})
	}
}

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
		history := legacycall.History{
			ToolName: nativeExecCommandToolName, Root: workspace, ExecutingThread: cmd.thread,
			CorrelationID: cmd.carrier, ChangeID: id,
			ReviewFiles: retainedReviewFiles([]diffmodel.ReviewFile{diffmodel.RenderReviewFile(cmd.path, cmd.path, "old\n", "new\n")}),
			ExecOutcome: (*legacycall.Outcome)(&changecapture.Outcome{Status: changecapture.StatusCompleted, Coverage: changecapture.CoverageExact,
				CodeMode: cmd.codeMode, Labels: []string{cmd.program}}),
		}
		if cmd.codeMode {
			history.HostResults = []legacycall.HostResult{
				{CallID: cmd.item, Tool: nativeExecCommandToolName, Status: "completed"},
				{CallID: "sibling-call", Tool: nativeExecCommandToolName, Status: "completed"},
			}
		} else {
			history.ExecOutcome.Status = changecapture.StatusFailed
			history.ExecOutcome.Exit = new(2)
		}
		if err := store.put(t.Context(), workspace, map[string]legacycall.History{cmd.carrier: history}); err != nil {
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
	assertCapturedCommand(t, u.testActivity(u.shell.Transcript), "main", "cat-call", "Edit", 2)
	u.review.Projection.Data = snapshot
	publishFixtureReview(t, u)
	applyCapturedEdits(&u.attached, u.review, &u.pendingEditReceipts)
	assertCapturedCommand(t, u.testActivity(u.shell.Transcript), "main", "cat-call", "Edit", 2)
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "c", "item": map[string]any{
		"id": "cat-call", "type": "commandExecution", "command": "cat unrelated.go", "exitCode": 0}})
	assertCapturedCommand(t, u.testActivity(u.shell.Agents), "child", "cat-call", "Read", 0)
	sibling := binding.ThreadItem{ID: "sibling-call", Type: "commandExecution", Command: "/bin/bash -lc " + commandargs.QuoteArgument("rg needle src; python3 - <<'PY'\nopen('nested.go', 'w').write('PRIVATE_EDIT_SOURCE')\nPY"), ExitCode: new(0), DurationMS: new(int64(64))}
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "c", "item": sibling})
	assertCapturedCommand(t, u.testActivity(u.shell.Agents), "child", "sibling-call", "Search", 0)
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "c", "item": map[string]any{
		"id": "python-call", "type": "commandExecution", "command": "python3 - <<'PY'\nopen('nested.go','w').write('new')\nPY", "exitCode": 0}})
	assertCapturedCommand(t, u.testActivity(u.shell.Agents), "child", "python-call", "Edit", 0)
	assertCapturedCommand(t, u.testActivity(u.shell.Agents), "child", "sibling-call", "Search", 0)
	assertCapturedCommand(t, u.testActivity(u.shell.Transcript), "main", "cat-call", "Edit", 2)
	feed := ansi.Strip(strings.Join(u.testActivity(u.shell.Agents).RenderFeed(120, 80).Lines, "\n"))
	if strings.Contains(feed, "PRIVATE_EDIT_SOURCE") || strings.Contains(feed, "included in grouped edit capture") || !strings.Contains(feed, "Search") {
		t.Fatalf("grouped capture leaked sibling source or lost its status row: %q", feed)
	}
	for _, tc := range []struct {
		view                        *activityAdapter
		thread, item, path, program string
	}{
		{u.testActivity(u.shell.Transcript), "main", "cat-call", "direct.go", "cat"},
		{u.testActivity(u.shell.Agents), "child", "python-call", "nested.go", "python3"},
	} {
		for i, entry := range tc.view.Entries {
			if entry.Source.Native == nil || entry.Source.Native.Thread != tc.thread || entry.Source.Native.Item != tc.item {
				continue
			}
			blocks := tc.view.blocks(i)
			effect := slices.IndexFunc(blocks, func(b activityui.Block) bool { return b.EditPath() == tc.path && strings.Contains(b.Label, "+1 -1") })
			if !strings.Contains(entry.Source.Text, "Edit `"+tc.path+"` +1 -1 · "+tc.program) || effect < 0 {
				t.Fatalf("receipt for %s: %+v, blocks %+v", tc.item, entry, tc.view.blocks(i))
			}
			for _, theme := range []diffrender.Theme{diffrender.DarkTheme, diffrender.LightTheme} {
				painted := strings.Join((&activityui.Painter{Theme: theme}).Block(blocks[effect], 120), "\n")
				// The file row names its outcome and source.
				heading := "Edited " + tc.path + " +1 -1 via " + tc.program
				if tc.program == "apply_patch" {
					heading = "Edited " + tc.path + " +1 -1"
				}
				if !strings.HasPrefix(strings.TrimSpace(ansi.Strip(painted)), heading) || strings.Contains(ansi.Strip(painted), " · "+tc.program) {
					t.Fatalf("source was not a shared edit header: %q", painted)
				}
			}
		}
	}
	// History restored into a new UI still reconciles against retained receipts.
	restored := newAppServerSessionTestUI(t, workspace)
	restored.review.Projection.Data = snapshot
	publishFixtureReview(t, restored)
	restored.restoreHistory([]binding.HistoryTurn{{ID: "prior", Status: "completed", Items: []binding.ThreadItem{{
		ID: "cat-call", Type: "commandExecution", Command: "cat > direct.go", ExitCode: new(2),
	}}}})
	assertCapturedCommand(t, restored.testActivity(restored.shell.Transcript), "main", "cat-call", "Edit", 2)
	siblingRuntime := newAppServerSessionTestUI(t, workspace)
	siblingRuntime.Thread = "child"
	siblingRuntime.startSession("child", workspace)
	siblingRuntime.review.Projection.Data = snapshot
	publishFixtureReview(t, siblingRuntime)
	siblingRuntime.restoreHistory([]binding.HistoryTurn{{ID: "c", Status: "completed", Items: []binding.ThreadItem{sibling}}})
	assertCapturedCommand(t, siblingRuntime.testActivity(siblingRuntime.shell.Transcript), "child", sibling.ID, "Search", 0)
	for _, view := range []*activityAdapter{u.testActivity(u.shell.Agents), siblingRuntime.testActivity(siblingRuntime.shell.Transcript)} {
		for _, entry := range view.Entries {
			if entry.Source.Native == nil || entry.Source.Native.Item != sibling.ID {
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
		if entry.Source.Native == nil || entry.Source.Native.Thread != thread || entry.Source.Native.Item != item || entry.Source.Kind != "tool" {
			continue
		}
		blocks := view.blocks(i)
		at := slices.IndexFunc(blocks, func(b activityui.Block) bool { return b.Verb == verb })
		if at < 0 || blocks[0].ExitCode != exit || blocks[at].Effect && blocks[at].ExitCode != 0 {
			t.Fatalf("%s: text %q, blocks %+v, want %s exit %d", item, entry.Source.Text, view.blocks(i), verb, exit)
		}
		return
	}
	t.Fatalf("missing native command %s", item)
}

func TestAppServerCapturedEditsReuseReceipt(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	item := binding.ThreadItem{ID: "cmd", Type: "commandExecution", Command: "cat > a.go <<'EOF'\nnew\nEOF"}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	var data []*presentation.Receipt
	receipt := &presentation.Receipt{Thread: "main", Calls: []string{"cmd"}, Text: "Edit `a.go` +1 -1"}
	data = []*presentation.Receipt{receipt}
	u.review.Projection.Data = reviewReceiptSnapshot(t, data)
	applyCapturedEdits(&u.attached, u.review, &u.pendingEditReceipts)
	assertCapturedCommand(t, u.testActivity(u.shell.Transcript), "main", "cmd", "Edit", 0)
	if allocations := testing.AllocsPerRun(5, func() { applyCapturedEdits(&u.attached, u.review, &u.pendingEditReceipts) }); allocations > 1 {
		t.Fatalf("unchanged receipt allocated %.0f times; want at most 1", allocations)
	}
	// A terminal host refresh must still reapply counts and keep its failure.
	item.ExitCode = new(2)
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	assertCapturedCommand(t, u.testActivity(u.shell.Transcript), "main", "cmd", "Edit", 2)
	if !strings.Contains(u.attached.source.Entries[0].Source.Text, "+1 -1") {
		t.Fatal("host refresh lost captured counts")
	}
	// A new receipt must replace the cached projection.
	updated := *receipt
	updated.Text = "Edit `a.go` +2 -1"
	data[0] = &updated
	u.review.Projection.Data = reviewReceiptSnapshot(t, data)
	applyCapturedEdits(&u.attached, u.review, &u.pendingEditReceipts)
	assertCapturedCommand(t, u.testActivity(u.shell.Transcript), "main", "cmd", "Edit", 2)
	if !strings.Contains(u.attached.source.Entries[0].Source.Text, "+2 -1") {
		t.Fatal("new receipt did not replace captured counts")
	}
}

func TestAppServerCapturedEditsPreserveTrackedExits(t *testing.T) {
	v := newActivityAdapter()
	v.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{
		Seq: 1, Agent: "Main", Kind: "tool", CallID: "cmd", Text: "Run `git show`; Skill `mekugi-owners`; Run `git cherry-pick`",
		Native: &sessionpresentation.NativeItem{Thread: "main", Item: "cmd", Phase: "item/completed", Segments: []sessionpresentation.ActivitySegment{
			{Text: "Run `git show`", Tail: []string{"commit summary"}},
			{Text: "Skill `mekugi-owners`", Tail: []string{"owners"}},
			{Text: "Run `git cherry-pick`", Exit: 1, Tail: []string{"conflict"}},
		}},
	}}})
	data := []*presentation.Receipt{{
		Thread: "main", Calls: []string{"cmd"}, Text: "Edit `a.go` +1 -1",
	}}
	for range 2 {
		v.ApplyCapturedEdits(data)
		blocks := v.blocks(0)
		if len(blocks) != 5 || blocks[2].ExitCode != 1 || !blocks[4].Effect || blocks[4].Verb != "Edit" || blocks[4].ExitCode != 0 {
			t.Fatalf("receipt overwrote per-command statuses: %+v", blocks)
		}
	}
	got := mainFeed(appServerTestFeatures(&appServerUI{activity: new(activitymodel.Service), shell: &terminalsession.Attachment{Transcript: v.View}, attached: attachedPresentation{executionPresentation: &executionPresentation{source: v.ActivitySource}}}), 100)
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
		v.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{
			Seq: uint64(i + 1), Agent: "Main", Kind: "tool", CallID: calls[i], Text: toolActivityShell(source),
			Native: &sessionpresentation.NativeItem{Thread: "main", Item: calls[i], Phase: "item/completed"},
		}}})
	}
	v.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{
		{Seq: 5, Agent: "Main", Kind: "exit", CallID: "tests", Text: "2"},
		{Seq: 6, Agent: "Main", Kind: "exit", CallID: "failed-edit", Text: "1"},
	}})
	data := []*presentation.Receipt{{
		Thread: "main", Calls: calls, Text: "Edit `a.go` +1 -0\n\nEdit `b.go` +1 -0",
	}}
	for range 2 {
		v.ApplyCapturedEdits(data)
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
func TestUISnapshotCodeModeCellEditReceipt(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	trace := newNativeTraceFixture(t)
	proxy.nativeTrace = nativetrace.New(trace.root)
	transform, _, _, workspace := newMekugiTestTransformWithProxy(t, proxy)
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
	retained, found, err := testProxyStore(proxy).lookup(t.Context(), workspace, "cell")
	if err != nil || !found || retained.CarrierPayload != source {
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
	request.Fields["tools"] = mustTestJSON(t, testExecResponsesTools())
	next, err := proxy.prepareRequest(t.Context(), &request, "session-1", "thread-1", codexresponses.TurnMetadata{RequestKind: "turn", Directories: map[string]jsonv1.RawMessage{workspace: nil}}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	record, found, err := testProxyStore(proxy).lookup(t.Context(), workspace, legacycall.ExecCallID("cell", true))
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

	store := testReplayStore(mekugiReplayStore{directory: testProxyStore(proxy).directory})
	snapshot, err := store.liveDiffSnapshot(t.Context(), previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"thread-1": true}}})
	if err != nil {
		t.Fatal(err)
	}
	item := binding.ThreadItem{ID: "edit-exec", Type: "commandExecution", Command: edit, ExitCode: new(0)}
	live := newAppServerSessionTestUI(t, workspace)
	live.Thread = "thread-1"
	live.startSession(live.Thread, workspace)
	live.audience = proxy.activity
	live.audience.Attach(live.Thread)
	// Deliver the real producer's annotation before the host item, with no
	// saved-review publication available to mask a dropped supplement.
	applyObservedActivity(&live.attached, live.shell, &live.Client, &live.streams, live.usage, live.review, &live.approvals, &live.pendingEditReceipts, live.clock, live.restoring, live.audience)
	if len(live.pendingEditReceipts) != 1 || !slices.Equal(live.pendingEditReceipts[0].Calls, calls) {
		t.Fatalf("receipt did not retain nested host identities: %+v", live.pendingEditReceipts)
	}
	observedReceipt := live.pendingEditReceipts[0]
	appServerTestNotify(t, live, "item/completed", map[string]any{"threadId": "thread-1", "turnId": "turn", "item": item})
	assertCapturedCommand(t, live.testActivity(live.shell.Transcript), "thread-1", "edit-exec", "Edit", 0)
	sibling := binding.ThreadItem{ID: "read-exec", Type: "commandExecution", Command: read, ExitCode: new(0)}
	appServerTestNotify(t, live, "item/completed", map[string]any{"threadId": "thread-1", "turnId": "turn", "item": sibling})
	if len(live.pendingEditReceipts) != 0 || len(live.attached.source.Entries) != 2 {
		t.Fatalf("receipt duplicated host rows or remained pending: entries=%+v pending=%+v", live.attached.source.Entries, live.pendingEditReceipts)
	}
	assertNativeUISnapshot(t, "supplement-edit-receipt", live.testActivity(live.shell.Transcript).RenderFeed(100, 30).Lines)
	// A later retained publication reconciles the same identity, not another row.
	live.review.Projection.Data = snapshot
	applyCapturedEdits(&live.attached, live.review, &live.pendingEditReceipts)
	assertCapturedCommand(t, live.testActivity(live.shell.Transcript), "thread-1", "edit-exec", "Edit", 0)
	if len(live.attached.source.Entries) != 2 {
		t.Fatal("retained publication duplicated receipt")
	}
	restored := newAppServerSessionTestUI(t, workspace)
	restored.Thread = "thread-1"
	restored.startSession(restored.Thread, workspace)
	restored.review.Projection.Data = snapshot
	publishFixtureReview(t, restored)
	restored.restoreHistory([]binding.HistoryTurn{{ID: "turn", Status: "completed", Items: []binding.ThreadItem{item}}})
	assertCapturedCommand(t, restored.testActivity(restored.shell.Transcript), "thread-1", "edit-exec", "Edit", 0)
	// An annotation queued while history was loading must enrich that restored
	// invocation without replacing its identity or adding an independent row.
	restored.audience = sessioncontrol.NewAudience(nil)
	restored.audience.Attach(restored.Thread)
	event := activitypresentation.Supplement{Thread: restored.Thread, Source: "late-receipt", Kind: activitypresentation.SupplementEditReceipt, Text: observedReceipt.Text, Calls: observedReceipt.Calls}
	for range 2 {
		restored.audience.Collect(event)
		applyObservedActivity(&restored.attached, restored.shell, &restored.Client, &restored.streams, restored.usage, restored.review, &restored.approvals, &restored.pendingEditReceipts, restored.clock, nil, restored.audience)
	}
	assertCapturedCommand(t, restored.testActivity(restored.shell.Transcript), "thread-1", "edit-exec", "Edit", 0)
	if len(restored.attached.source.Entries) != 1 {
		t.Fatalf("late receipt duplicated restored invocation: %+v", restored.attached.source.Entries)
	}
}

func TestAppServerCapturedRemovalKeepsRunNeighbors(t *testing.T) {
	for _, source := range []string{"rm -- FILE; make check", "make check; rm -- FILE"} {
		for _, thread := range []string{"main", "child"} {
			t.Run(thread+source, func(t *testing.T) {
				u := newAppServerSessionTestUI(t, t.TempDir())
				if thread == "child" {
					appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": thread, "agentNickname": "worker"}})
				}
				view := u.testActivity(u.shell.Transcript)
				if thread == "child" {
					view = u.testActivity(u.shell.Agents)
				}
				item := binding.ThreadItem{ID: "cmd", Type: "commandExecution", Command: "/bin/bash -lc " + commandargs.QuoteArgument(source), ExitCode: new(0), AggregatedOutput: new(" M tracked.go\n")}
				appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": "t", "item": item})
				data := []*presentation.Receipt{{Thread: thread, Calls: []string{"cmd"}, Text: "Delete `FILE` +0 -1 · rm"}}
				check := func(view *activityAdapter) {
					for range 2 {
						view.ApplyCapturedEdits(data)
						blocks := view.blocks(0)
						if len(blocks) != 2 {
							t.Fatalf("lost neighbor: %+v", blocks)
						}
						run, edit := 0, 1
						if blocks[run].Verb != "Run" || !strings.Contains(blocks[run].Label+blocks[run].Code, "make check") || blocks[edit].Verb != "Delete" {
							t.Fatalf("incorrect operations: %+v", blocks)
						}
						if !strings.Contains(strings.Join(blocks[run].Tail, "\n"), "tracked.go") {
							t.Fatalf("lost aggregate output: %+v", blocks)
						}
					}
				}
				check(view)
				if thread == "main" {
					restored := newAppServerSessionTestUI(t, t.TempDir())
					restored.restoreHistory([]binding.HistoryTurn{{ID: "t", Status: "completed", Items: []binding.ThreadItem{item}}})
					check(restored.testActivity(restored.shell.Transcript))
				}
			})
		}
	}
}

func TestAppServerCapturedEditKeepsCommandPaths(t *testing.T) {
	u := newAppServerSessionTestUI(t, "/workspace")
	item := binding.ThreadItem{ID: "cmd", Type: "commandExecution", Cwd: "/command-dir", Command: "mcat /command-dir/read.go; rg '/command-dir/query' /command-dir/src; printf new > /command-dir/edit.go", Status: "completed", ExitCode: new(0)}
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	data := []*presentation.Receipt{{Thread: "main", Calls: []string{"cmd"}, Text: "Edit `edit.go` +1 -1"}}
	check := func(view *activityAdapter) {
		t.Helper()
		entry := view.Entries[0]
		if !strings.Contains(entry.Source.Text, "Read `read.go`") || !strings.Contains(entry.Source.Text, "Search `/command-dir/query` in `src`") || !strings.Contains(entry.Source.Text, "Edit `edit.go` +1 -1") || entry.Source.Native.Command != item.Command {
			t.Fatalf("receipt changed neighboring paths, query or source: %q", entry.Source.Text)
		}
	}
	u.review.Projection.Data = reviewReceiptSnapshot(t, data)
	applyCapturedEdits(&u.attached, u.review, &u.pendingEditReceipts)
	check(u.testActivity(u.shell.Transcript))
	restored := newAppServerSessionTestUI(t, u.attached.cwd)
	restored.review.Projection.Data = reviewReceiptSnapshot(t, data)
	restored.restoreHistory([]binding.HistoryTurn{{ID: "t", Status: "completed", Items: []binding.ThreadItem{item}}})
	check(restored.testActivity(restored.shell.Transcript))
}

func TestAppServerUserShellCaptureRetainsCommandOutcome(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	item := binding.ThreadItem{ID: "cmd", Type: "commandExecution", Source: "userShell", Command: "printf changed > file; false", Status: "failed", ExitCode: new(2), AggregatedOutput: new("retained failure\n")}
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	u.review.Projection.Data = reviewReceiptSnapshot(t, []*presentation.Receipt{{Thread: "main", Calls: []string{"cmd"}, Text: "Edit `file` +1 -0"}})
	applyCapturedEdits(&u.attached, u.review, &u.pendingEditReceipts)
	blocks := u.testActivity(u.shell.Transcript).blocks(0)
	if len(blocks) != 2 || blocks[0].Code != item.Command || blocks[0].ExitCode != 2 || len(blocks[0].Tail) != 1 ||
		blocks[1].EditPath() != "file" || blocks[1].ExitCode != 0 {
		t.Fatalf("receipt replaced user command or attributed its failure to the captured edit: %+v", blocks)
	}
}

// Assemble captured-effect values for renderer-only fixtures.
func testCapturedEditActivity(workspace string, history legacycall.History) *changepresentation.Receipt {
	if history.ExecOutcome == nil {
		return nil
	}
	receipt := &changepresentation.Receipt{Thread: history.ExecutingThread, Text: changepresentation.ReceiptText(workspace, receiptEvidence(history))}
	if !history.ExecOutcome.CodeMode {
		if call, ok := strings.CutSuffix(history.CorrelationID, "\x00exec"); ok {
			receipt.Calls = append(receipt.Calls, call)
		}
	}
	for _, result := range history.HostResults {
		if result.Tool == "exec_command" || result.Tool == "userShell" {
			receipt.Calls = append(receipt.Calls, result.CallID)
		}
	}
	if receipt.Text == "" && len(receipt.Calls) == 0 {
		return nil
	}
	return receipt
}
