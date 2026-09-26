package router

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi"
	"golang.org/x/term"
)

// TestNativeUIPreview plays a session through the actual shell and painters,
// driven by the notifications Codex app-server sends. It is local and makes
// no model or network requests; edits land in a temporary workspace.
func TestNativeUIPreview(t *testing.T) {
	if os.Getenv("MEKUGI_NATIVE_UI_PREVIEW") != "1" {
		t.Skip("interactive native UI preview")
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no controlling terminal: %v", err)
	}
	defer tty.Close()
	p := newNativePreview(t)
	defer p.close()
	err = withRawPane(t.Context(), tty, tty, "\x1b[?1049h\x1b[?25l\x1b[?1003;1006;2004h\x1b]10;?\x1b\\\x1b]11;?\x1b\\", "\x1b[?2026l\x1b[?1003;1006;2004l\x1b[0m\x1b[?25h\x1b[?1049l", func(keys <-chan byte) error {
		tick := time.NewTicker(33 * time.Millisecond)
		defer tick.Stop()
		lastWidth, lastHeight := 0, 0
		lastPaint := time.Time{}
		lastStep := time.Now()
		paint := func() error {
			w, h, err := term.GetSize(int(tty.Fd()))
			if err != nil {
				return err
			}
			if err := p.ui.paint(tty, w, h); err != nil {
				return err
			}
			lastWidth, lastHeight, lastPaint = w, h, time.Now()
			return nil
		}
		if err := paint(); err != nil {
			return err
		}
		for {
			select {
			case <-t.Context().Done():
				return t.Context().Err()
			case <-p.ui.shell.diff.escapeC:
				p.ui.shell.diff.escapeC = nil
				p.ui.shell.diff.escapeKey()
				if err := paint(); err != nil {
					return err
				}
			case <-tick.C:
				now := time.Now()
				if err := p.ui.shell.flushEscape(); err != nil {
					return err
				}
				flashExpired := p.ui.view.expireFlash(now)
				if now.Sub(lastStep) >= p.pace() && p.advance() {
					lastStep = now
					if err := paint(); err != nil {
						return err
					}
					continue
				}
				w, h, err := term.GetSize(int(tty.Fd()))
				if err != nil {
					return err
				}
				if w != lastWidth || h != lastHeight || now.Sub(lastPaint) >= time.Second || p.ui.agents.hasLiveReasoning() || flashExpired || p.ui.shell.animating(now) {
					if err := paint(); err != nil {
						return err
					}
				}
			case key, ok := <-keys:
				if !ok {
					return io.EOF
				}
				// Keys take the real UI path; the preview answers the resulting
				// requests the way app-server would.
				if err := p.ui.shell.key(key); err != nil {
					return err
				}
				p.serve()
				if p.ui.quitRequested {
					return nil
				}
				if err := paint(); err != nil {
					return err
				}
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNativeUIPreviewRenderedFrame(t *testing.T) {
	p := newNativePreview(t)
	defer p.close()
	p.ui.draft = "Keep my draft"
	render := func(name string, want ...string) string {
		t.Helper()
		screen := vt.NewEmulator(160, 48)
		defer screen.Close()
		p.ui.shell.paintedRows = nil // A new screen needs every row.
		if err := p.ui.paint(screen, 160, 48); err != nil {
			t.Fatal(err)
		}
		frame := screen.String()
		for _, content := range want {
			if !strings.Contains(frame, content) {
				t.Fatalf("%s does not show %q:\n%s", name, content, frame)
			}
		}
		t.Logf("%s:\n%s", name, frame)
		return frame
	}
	p.until("main streams its patch")
	render("main dock", "LIVE · main", "M internal/broker/broker.go", "1 Main", "3 Activity", "4 Agents", "├ Read")
	p.until("three agents edit at once")
	frame := render("agent dock accordion", "LIVE · 3 agents", "^B e next", "▸", "reviewer → main", "2 Diff●")
	if strings.Contains(frame, "3 Activity ─") && strings.Contains(frame, "no captured edits yet") {
		t.Fatal("the saved diff replaced Activity without being opened")
	}
	for p.advance() {
	}
	if p.ui.draft != "Keep my draft" {
		t.Fatal("playback overwrote typed input")
	}
	render("finished session", "● main", "↩ re: your message", "↩ re: assignment", "✓ answer", "╭─ ✓ answer", "finished", "$")
	for _, key := range []byte{2, '2'} {
		if err := p.ui.shell.key(key); err != nil {
			t.Fatal(err)
		}
	}
	render("saved diff", "2 Diff · saved · 5 files", "broker.go", "launch.go", "broker_race_test.go", "Unsubscribe")
	if !p.ui.shell.diffOpen || p.ui.shell.diffUnseen {
		t.Fatal("opening the saved diff did not clear its badge")
	}
	// The narrow pane's stacked list switches to changes and takes focus
	// without covering the diff.
	if err := p.ui.shell.key('\t'); err != nil {
		t.Fatal(err)
	}
	if !p.ui.shell.diff.navigation.focused || !p.ui.shell.diff.navigation.changesTab {
		t.Fatal("Tab did not focus the change list")
	}
	render("saved diff changes", "Unsubscribe", "─────")
	if p.ui.shell.diff.stack == 0 {
		t.Fatal("Tab replaced the stacked list with a full-pane picker")
	}
	for _, key := range []byte{'\t', 's'} {
		if err := p.ui.shell.key(key); err != nil {
			t.Fatal(err)
		}
	}
	render("saved diff hidden list", "Unsubscribe")
	if p.ui.shell.diff.stack != 0 || !p.ui.shell.diff.navigation.hidden {
		t.Fatal("s did not hide the focused stacked list")
	}
	// Picking an agent in the roster shows its Activity instead of the diff.
	for _, key := range []byte{2, '4'} {
		if err := p.ui.shell.key(key); err != nil {
			t.Fatal(err)
		}
	}
	render("focused roster", "4 Agents")
	if !p.ui.shell.diffOpen {
		t.Fatal("focusing the roster closed the saved diff")
	}
	shell := p.ui.shell
	index := slices.IndexFunc(shell.agents.hits, func(hit liveActivityHit) bool { return hit.agent == "/root/tester" })
	if index < 0 {
		t.Fatal("the focused roster has no tester row")
	}
	hit, roster := shell.agents.hits[index], shell.layout.roster
	for _, key := range []byte(fmt.Sprintf("\x1b[<0;%d;%dM", roster.x+hit.first, roster.y+hit.row)) {
		if err := shell.key(key); err != nil {
			t.Fatal(err)
		}
	}
	render("roster pick", "3 Activity", "tester")
	if shell.diffOpen || !shell.agents.only || shell.agents.selected != "/root/tester" {
		t.Fatal("clicking a roster agent did not show its Activity")
	}
}

// The preview answers keys through the real composer and app-server
// requests: steering, clearing, interrupting and quitting all work mid-playback.
func TestNativeUIPreviewKeysBehaveLikeUI(t *testing.T) {
	p := newNativePreview(t)
	defer p.close()
	keys := func(s string) {
		t.Helper()
		for _, key := range []byte(s) {
			if err := p.ui.shell.key(key); err != nil {
				t.Fatal(err)
			}
			p.serve()
		}
	}
	p.until("main streams its patch")
	keys("also check Unsubscribe\r")
	if p.ui.submitted != "" || p.ui.turn == "" || !slices.ContainsFunc(p.ui.view.entries, func(e activityPaneEntry) bool {
		return e.Agent == "You" && e.Text == "also check Unsubscribe"
	}) {
		t.Fatalf("steer not accepted: submitted=%q turn=%q", p.ui.submitted, p.ui.turn)
	}
	keys("scratch\x03")
	if p.ui.draft != "" || p.ui.turn == "" || p.ui.quitRequested {
		t.Fatal("first Ctrl-C did not only clear the draft")
	}
	keys("\x03")
	if p.ui.turn != "" || p.ui.status != "Interrupted" || p.advance() || len(p.active) != 0 {
		t.Fatalf("interrupt left playback running: turn=%q status=%q active=%v", p.ui.turn, p.ui.status, p.active)
	}
	for _, agent := range p.ui.agents.agents {
		if agent.Responding {
			t.Fatalf("%s still responding after interrupt", agent.Name)
		}
	}
	keys("hello\r")
	if p.ui.status != "Completed" || !slices.ContainsFunc(p.ui.view.entries, func(e activityPaneEntry) bool { return strings.Contains(e.Text, "I heard: hello") }) {
		t.Fatalf("new turn not answered: %q", p.ui.status)
	}
	keys("\x03")
	if !p.ui.quitRequested {
		t.Fatal("idle Ctrl-C did not quit")
	}
}

type nativePreviewStep struct {
	name string
	run  func()
}

type nativePreview struct {
	t         *testing.T
	ui        *appServerUI
	input     *appServerTestInput
	store     *mekugiReplayStore
	usage     *threadUsage
	workspace string
	message   int
	calls     int
	steps     []nativePreviewStep
	step      int
	active    map[string]string // Running turn by thread.
}

func newNativePreview(t *testing.T) *nativePreview {
	u, input := newAppServerTestUI()
	u.ctx = t.Context()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	for path, content := range nativePreviewFiles {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(workspace, path)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(workspace, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	usage := newThreadUsage()
	u.proxy = &mekugiProxy{usage: usage}
	u.model, u.reasoningEffort = "gpt-6-luna", "medium"
	u.ensureShell()
	u.shell.diff.close()
	u.shell.diff = newLiveDiffTerminalController(store, workspace, os.Stdout)
	u.shell.diff.native, u.shell.diff.diffMode = true, true
	u.shell.diff.stdout = u.shell.diffScreen
	u.shell.diff.size = func() (int, int, error) {
		return max(1, u.shell.layout.diff.w), max(3, u.shell.layout.diff.h), nil
	}
	u.session.start("main", workspace)
	p := &nativePreview{t: t, ui: u, input: input, store: store, usage: usage, workspace: workspace, active: make(map[string]string)}
	store.liveDiff = func(changes []liveDiffChange) {
		u.shell.applyDiff(t.Context(), liveDiffEvent{Kind: "change", Changes: changes})
	}
	scope := liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {}}}
	for _, thread := range []string{"main", "t-reviewer", "t-explorer", "t-tester"} {
		scope.Workspaces[workspace][thread] = true
	}
	u.shell.applyDiff(t.Context(), liveDiffEvent{Kind: "scope", Scope: &scope})
	u.agents.apply(activityPaneEvent{Kind: "agents", Agents: u.session.agents})
	u.status = "Ready"
	p.populate()
	return p
}

func (p *nativePreview) close() {
	p.ui.shell.diff.close()
	p.ui.shell.diffScreen.Close()
}

// pace slows playback while patches stream, so each dock frame is readable.
func (p *nativePreview) pace() time.Duration {
	if p.step < len(p.steps) && strings.HasPrefix(p.steps[p.step].name, "stream") {
		return 260 * time.Millisecond
	}
	return 650 * time.Millisecond
}

func (p *nativePreview) advance() bool {
	if p.step >= len(p.steps) {
		return false
	}
	p.steps[p.step].run()
	p.step++
	return true
}

// until plays through the named step.
func (p *nativePreview) until(name string) {
	p.t.Helper()
	for p.step < len(p.steps) {
		current := p.steps[p.step].name
		p.advance()
		if current == name {
			return
		}
	}
	p.t.Fatalf("no preview step %q", name)
}

func (p *nativePreview) add(name string, run func()) {
	p.steps = append(p.steps, nativePreviewStep{name, run})
}

// Each preview edit is one apply_patch call. Codex streams its file changes
// while the model writes, then applies it and reports the item.
type nativePreviewEdit struct {
	thread, item, patch string
}

func (e nativePreviewEdit) change(lines int) map[string]any {
	var path, kind string
	var body []string
	for line := range strings.Lines(e.patch) {
		switch {
		case strings.HasPrefix(line, "*** Update File: "):
			path, kind = strings.TrimSpace(strings.TrimPrefix(line, "*** Update File: ")), "update"
		case strings.HasPrefix(line, "*** Add File: "):
			path, kind = strings.TrimSpace(strings.TrimPrefix(line, "*** Add File: ")), "add"
		case strings.HasPrefix(line, "***"):
		case kind == "add":
			body = append(body, strings.TrimPrefix(line, "+"))
		default:
			body = append(body, line)
		}
	}
	if lines >= 0 {
		body = body[:min(lines, len(body))]
	}
	return map[string]any{"path": path, "kind": map[string]any{"type": kind}, "diff": strings.Join(body, "")}
}

// stream adds the edits' patch updates as steps, a few lines at a time,
// interleaving concurrent writers.
func (p *nativePreview) stream(name string, edits ...nativePreviewEdit) {
	for lines := 3; ; lines += 3 {
		more := false
		for _, edit := range edits {
			if lines-3 >= strings.Count(edit.change(-1)["diff"].(string), "\n") {
				continue
			}
			more = true
			change := edit.change(lines)
			p.add("stream "+name, func() {
				p.notify("item/fileChange/patchUpdated", map[string]any{"threadId": edit.thread, "turnId": "turn-" + edit.thread, "itemId": edit.item, "changes": []any{change}})
			})
		}
		if !more {
			return
		}
	}
}

// apply reports the call applied, writes the file, and records it the way
// the router's capturer does, so the saved diff gains the change.
func (p *nativePreview) apply(edit nativePreviewEdit) {
	change := edit.change(-1)
	item := map[string]any{"id": edit.item, "type": "fileChange", "status": "inProgress", "changes": []any{change}}
	p.notify("item/started", map[string]any{"threadId": edit.thread, "turnId": "turn-" + edit.thread, "item": item})
	path := change["path"].(string)
	absolute := filepath.Join(p.workspace, path)
	before, err := os.ReadFile(absolute)
	beforePath := absolute
	if os.IsNotExist(err) {
		beforePath = ""
	}
	_, after, _, _, ok := applyPreviewPatch(edit.patch, string(before))
	if !ok {
		p.t.Fatalf("preview patch does not apply to %s", path)
	}
	if err := os.WriteFile(absolute, []byte(after), 0o644); err != nil {
		p.t.Fatal(err)
	}
	p.calls++
	id := fmt.Sprintf("call-%d", p.calls)
	caller := p.ui.session.path(edit.thread)
	if change, err := p.store.reserveChange(p.ui.ctx, p.workspace, edit.thread, id); err == nil {
		_ = p.store.put(p.ui.ctx, p.workspace, map[string]mekugiHistory{id: {
			ToolName: applyPatchToolName, Caller: caller, ChangeID: change, CorrelationID: id, ExecutingThread: edit.thread,
			ReviewFiles: []mekugi.ReviewFile{mekugi.RenderReviewFile(beforePath, absolute, string(before), after)},
		}})
	}
	item["status"] = "completed"
	p.notify("item/completed", map[string]any{"threadId": edit.thread, "turnId": "turn-" + edit.thread, "item": item})
}

func (p *nativePreview) command(thread, id, command string, actions []map[string]any, exit int) {
	item := map[string]any{"id": id, "type": "commandExecution", "command": command, "cwd": p.workspace, "status": "inProgress", "commandActions": actions}
	p.notify("item/started", map[string]any{"threadId": thread, "turnId": "turn-" + thread, "item": item})
	item["status"], item["exitCode"] = "completed", exit
	p.notify("item/completed", map[string]any{"threadId": thread, "turnId": "turn-" + thread, "item": item})
}

func (p *nativePreview) spawn(id, thread, path, role, prompt string) {
	p.notify("thread/started", map[string]any{"thread": map[string]any{"id": thread, "parentThreadId": "main", "agentRole": role, "cwd": p.workspace,
		"source": map[string]any{"subAgent": map[string]any{"thread_spawn": map[string]any{"parent_thread_id": "main", "depth": 1, "agent_path": path, "agent_role": role}}}}})
	p.notify("item/completed", map[string]any{"threadId": "main", "turnId": "turn-main", "item": map[string]any{"id": id, "type": "collabAgentToolCall", "tool": "spawnAgent", "status": "completed",
		"senderThreadId": "main", "receiverThreadIds": []string{thread}, "prompt": prompt, "model": "gpt-6-luna", "reasoningEffort": "medium"}})
	p.notify("turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "turn-" + thread}})
}

func (p *nativePreview) collab(id, tool, from, to, prompt string) {
	p.notify("item/completed", map[string]any{"threadId": from, "turnId": "turn-" + from, "item": map[string]any{"id": id, "type": "collabAgentToolCall", "tool": tool, "status": "completed",
		"senderThreadId": from, "receiverThreadIds": []string{to}, "prompt": prompt}})
}

func (p *nativePreview) say(thread, id, phase, text string) {
	item := map[string]any{"id": id, "type": "agentMessage", "text": text}
	if phase != "" {
		item["phase"] = phase
	}
	p.notify("item/completed", map[string]any{"threadId": thread, "turnId": "turn-" + thread, "item": item})
}

func (p *nativePreview) think(thread, id, text string) {
	for _, delta := range strings.SplitAfter(text, " ") {
		p.notify("item/reasoning/summaryTextDelta", map[string]any{"threadId": thread, "turnId": "turn-" + thread, "itemId": id, "delta": delta, "summaryIndex": 0})
	}
}

// usage reports a thread's cumulative tokens to app-server and to the
// router's accounting, which prices them.
func (p *nativePreview) tokens(thread string, input, output uint64) {
	p.usage.observation(thread, thread, "gpt-6-luna", "").observe(tokenCounts{InputTokens: input, UncachedInputTokens: input, OutputTokens: output})
	p.notify("thread/tokenUsage/updated", map[string]any{"threadId": thread, "turnId": "turn-" + thread, "tokenUsage": map[string]any{
		"total": map[string]any{"inputTokens": input, "outputTokens": output, "totalTokens": input + output}}})
}

func (p *nativePreview) finish(thread string) {
	p.notify("turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": "turn-" + thread, "status": "completed"}})
}

var nativePreviewFiles = map[string]string{
	"internal/broker/broker.go":      previewFiles["internal/broker/broker.go"],
	"internal/broker/broker_test.go": previewFiles["internal/broker/broker_test.go"],
	"internal/pane/launch.go":        previewFiles["internal/pane/launch.go"],
}

const nativePreviewLaunchPatch = `*** Begin Patch
*** Update File: internal/pane/launch.go
@@
 // Launch opens the side pane for a requested preview.
 func Launch(requested bool, frames <-chan string) string {
 	if requested {
 		return "open"
 	}
-	return <-frames
+	select {
+	case frame := <-frames:
+		return frame
+	default:
+		return "pending"
+	}
 }
*** End Patch
`

const nativePreviewRacePatch = `*** Begin Patch
*** Add File: internal/broker/broker_race_test.go
+package broker
+
+import (
+	"sync"
+	"testing"
+)
+
+func TestConcurrentPublish(t *testing.T) {
+	b := New()
+	messages := b.Subscribe("topic")
+	var wg sync.WaitGroup
+	for range 8 {
+		wg.Go(func() { b.Publish("topic", "hello") })
+		<-messages
+	}
+	wg.Wait()
+}
*** End Patch
`

func (p *nativePreview) populate() {
	task := "Make the broker safe for concurrent publishers, add a regression test, and stop the pane from blocking on its first frame. Split the work so it goes quickly."
	mainEdit := nativePreviewEdit{"main", "patch-main", previewPatches[1]}
	testEdit := nativePreviewEdit{"t-tester", "patch-tester", nativePreviewRacePatch}
	paneEdit := nativePreviewEdit{"t-explorer", "patch-explorer", nativePreviewLaunchPatch}
	docEdit := nativePreviewEdit{"t-reviewer", "patch-reviewer", previewPatches[2]}
	followEdit := nativePreviewEdit{"t-tester", "patch-tester-2", previewPatches[0]}

	p.add("prompt", func() { p.beginMessage(task) })
	p.add("plan", func() {
		p.say("main", "main-plan", "commentary", "I'll lock the broker myself and split the rest:\n\n- **tester** writes a race test\n- **explorer** unblocks the pane\n- **reviewer** checks the lock and documents the package")
	})
	p.add("read", func() {
		p.command("main", "cmd-1", "sed -n 1,80p internal/broker/broker.go", []map[string]any{{"type": "read", "command": "sed", "name": "broker.go", "path": filepath.Join(p.workspace, "internal/broker/broker.go")}}, 0)
		p.command("main", "cmd-2", "rg -n 'subs\\[' internal", []map[string]any{{"type": "search", "command": "rg", "query": "subs\\[", "path": "internal"}}, 0)
	})
	p.add("spawn", func() {
		p.spawn("spawn-1", "t-tester", "/root/tester", "test", "Add a race test for concurrent Publish calls in internal/broker. Run it with -race and report whether it fails before the lock lands.")
		p.spawn("spawn-2", "t-explorer", "/root/explorer", "explore", "Find why the pane blocks on its first frame in internal/pane and fix it without changing Launch's signature.")
		p.spawn("spawn-3", "t-reviewer", "/root/reviewer", "review", "Review main's broker locking once it lands. Add package documentation if it is missing.")
		p.tokens("main", 182_000, 2_100)
	})
	p.stream("main", mainEdit)
	p.add("main streams its patch", func() {})
	p.add("children start", func() {
		p.think("t-tester", "think-1", "**Planning the race test**\n\nEight publishers against one subscriber is enough to trip -race.")
		p.command("t-explorer", "cmd-3", "cat internal/pane/launch.go", []map[string]any{{"type": "read", "command": "cat", "name": "launch.go", "path": filepath.Join(p.workspace, "internal/pane/launch.go")}}, 0)
		p.command("t-reviewer", "cmd-4", "sed -n 1,40p internal/broker/broker.go", []map[string]any{{"type": "read", "command": "sed", "name": "broker.go", "path": filepath.Join(p.workspace, "internal/broker/broker.go")}}, 0)
	})
	p.add("main applies", func() {
		p.apply(mainEdit)
		p.tokens("main", 241_000, 4_800)
	})
	p.add("reviewer reports", func() {
		p.collab("msg-1", "sendMessage", "t-reviewer", "main", "The lock covers Subscribe and Publish, and Publish now sends outside it, so a slow subscriber no longer blocks other publishers.")
	})
	p.stream("agents", testEdit, paneEdit, docEdit)
	p.add("three agents edit at once", func() {})
	p.add("agents apply", func() {
		p.apply(testEdit)
		p.apply(paneEdit)
		p.apply(docEdit)
		p.tokens("t-tester", 64_000, 1_900)
		p.tokens("t-explorer", 41_000, 900)
		p.tokens("t-reviewer", 38_000, 700)
	})
	p.add("test fails", func() {
		p.command("t-tester", "cmd-5", "go test -race ./internal/broker", nil, 1)
	})
	p.add("follow-up", func() {
		p.collab("follow-1", "followupTask", "main", "t-tester", "Also cover Unsubscribe racing a publish; the reviewer's note suggests it is the next hole.")
	})
	p.stream("tester follow-up", followEdit)
	p.add("tester applies", func() {
		p.apply(followEdit)
		p.command("t-tester", "cmd-6", "go test -race ./internal/broker", nil, 0)
		p.tokens("t-tester", 97_000, 3_400)
	})
	p.add("explorer answers", func() {
		p.say("t-explorer", "answer-explorer", "final_answer", "Launch now returns `pending` instead of waiting on the first frame. The signature is unchanged.")
		p.finish("t-explorer")
	})
	p.add("reviewer answers", func() {
		p.say("t-reviewer", "answer-reviewer", "", "The locking is correct. I added `doc.go` with package documentation.")
		p.finish("t-reviewer")
	})
	p.add("tester answers", func() {
		p.say("t-tester", "answer-tester", "final_answer", "Both race tests pass with -race:\n\n- `TestConcurrentPublish`\n- `TestUnsubscribeRace`")
		p.finish("t-tester")
	})
	p.add("journal", func() {
		p.ui.view.applyJournal("main", nativeJournalPublication{item: journalItem{ID: "done", Question: task, Author: "/root", Created: 1,
			Text: "The broker is safe for concurrent publishers.\n\n1. `Publish` copies subscribers under the lock and sends outside it.\n2. Race tests cover publish and unsubscribe.\n3. The pane no longer blocks on its first frame."}, terminal: true, batch: 1})
	})
	p.add("done", func() {
		p.tokens("main", 305_000, 7_200)
		p.finish("main")
		p.notify("turn/completed", map[string]any{"threadId": "main", "turn": map[string]any{"id": "preview-turn-1", "status": "completed"}})
	})
}

func (p *nativePreview) notify(method string, params any) {
	wire, err := json.Marshal(params)
	if err != nil {
		p.t.Fatal(err)
	}
	var turn struct {
		ThreadID string `json:"threadId"`
		Turn     struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(wire, &turn); err == nil {
		switch method {
		case "turn/started":
			p.active[turn.ThreadID] = turn.Turn.ID
		case "turn/completed":
			if p.active[turn.ThreadID] == turn.Turn.ID {
				delete(p.active, turn.ThreadID)
			}
		}
	}
	if err := p.ui.message(appServerMessage{Method: method, Params: jsontext.Value(wire)}); err != nil {
		p.t.Fatal(err)
	}
}

// serve answers the UI's pending requests as app-server would: a new turn
// echoes the prompt, a steer joins the running turn, and an interrupt ends
// every running turn and the scripted playback with it.
func (p *nativePreview) serve() {
	for {
		line, err := p.input.ReadBytes('\n')
		if err != nil {
			return
		}
		var request struct {
			ID     jsontext.Value `json:"id"`
			Method string         `json:"method"`
			Params struct {
				Input []map[string]any `json:"input"`
			} `json:"params"`
		}
		if err := json.Unmarshal(line, &request); err != nil || len(request.ID) == 0 {
			continue
		}
		result := map[string]any{}
		turn := ""
		switch request.Method {
		case "turn/start":
			p.message++
			turn = fmt.Sprintf("preview-turn-%d", p.message)
			result["turn"] = map[string]any{"id": turn}
		case "turn/steer":
			turn = p.active["main"]
			result["turnId"] = turn
		}
		p.reply(request.ID, result)
		switch request.Method {
		case "turn/start":
			p.notify("turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": turn}})
			p.userMessage(turn, request.Params.Input)
			var text []string
			for _, input := range request.Params.Input {
				if s, ok := input["text"].(string); ok {
					text = append(text, s)
				}
			}
			p.notify("item/completed", map[string]any{"threadId": "main", "turnId": turn, "item": map[string]any{
				"id": fmt.Sprintf("preview-answer-%d", p.message), "type": "agentMessage", "text": "I heard: " + strings.Join(text, ""),
			}})
			p.notify("turn/completed", map[string]any{"threadId": "main", "turn": map[string]any{"id": turn, "status": "completed"}})
		case "turn/steer":
			p.userMessage(turn, request.Params.Input)
		case "turn/interrupt":
			p.step = len(p.steps)
			for _, thread := range slices.Sorted(maps.Keys(p.active)) {
				p.notify("turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": p.active[thread], "status": "interrupted"}})
			}
		}
	}
}

func (p *nativePreview) reply(id jsontext.Value, result any) {
	wire, err := json.Marshal(result)
	if err != nil {
		p.t.Fatal(err)
	}
	if err := p.ui.message(appServerMessage{ID: id, Result: jsontext.Value(wire)}); err != nil {
		p.t.Fatal(err)
	}
}

func (p *nativePreview) userMessage(turn string, content any) {
	p.notify("item/completed", map[string]any{"threadId": "main", "turnId": turn, "item": map[string]any{
		"id": fmt.Sprintf("preview-user-%d-%d", p.message, len(p.ui.view.entries)), "type": "userMessage", "content": content,
	}})
}

// beginMessage plays the scripted opening prompt as if it had been submitted.
func (p *nativePreview) beginMessage(body string) {
	p.message++
	turn := fmt.Sprintf("preview-turn-%d", p.message)
	p.notify("turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": turn}})
	p.userMessage(turn, []map[string]string{{"type": "text", "text": body}})
}

func TestNativeDiffNavigatorPointerMovesAndBack(t *testing.T) {
	p := newNativePreview(t)
	defer p.close()
	for p.advance() {
	}
	shell, d := p.ui.shell, p.ui.shell.diff
	keys := func(s string) {
		t.Helper()
		for _, key := range []byte(s) {
			if err := shell.key(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	render := func() string {
		t.Helper()
		screen := vt.NewEmulator(160, 48)
		defer screen.Close()
		shell.paintedRows = nil
		if err := p.ui.paint(screen, 160, 48); err != nil {
			t.Fatal(err)
		}
		return screen.String()
	}
	keys("\x02" + "2\t")
	frame := render()
	l := &d.navigation.changes
	single := slices.IndexFunc(l.rows, func(row liveDiffChangeRow) bool { return row.kind == 'c' && len(l.nodes[row.node].files) == 1 })
	if single < 0 || strings.Contains(frame, " 1f ") || !strings.Contains(frame, "broker_race_test.go") {
		t.Fatalf("a single-file change does not name its file:\n%s", frame)
	}
	// The pointer underlines the row it rests on.
	r := shell.layout.diff
	keys(fmt.Sprintf("\x1b[<35;%d;%dM", r.x+3, r.y+3+single-l.top))
	if l.hover != single+1 {
		t.Fatalf("hover = %d, want row %d", l.hover, single)
	}
	keys(fmt.Sprintf("\x1b[<35;%d;%dM", 2, 2))
	if l.hover != 0 {
		t.Fatal("leaving the diff pane kept its hover")
	}
	// Moving the cursor shows its file while the list keeps focus.
	keys("G")
	for l.cursor != single {
		keys("k")
	}
	if want := l.nodes[l.rows[single].node].files[0].file; d.view.Selected != want || !d.navigation.focused {
		t.Fatalf("moving to a change showed file %d, want %d", d.view.Selected, want)
	}
	// Enter opens the file; Esc returns to the list.
	keys("\r")
	if d.navigation.focused || d.back.kind != 'f' {
		t.Fatal("Enter on a single-file change did not open its file")
	}
	d.escapeKey()
	if !d.navigation.focused || l.cursor != single {
		t.Fatal("Esc did not return to the list")
	}
	// Enter on a branch filters its caller; Esc restores the previous filter.
	branch := slices.IndexFunc(l.rows, func(row liveDiffChangeRow) bool { return row.kind == 'b' })
	if branch < 0 {
		t.Fatal("no branch row")
	}
	for l.cursor < branch {
		keys("j")
	}
	keys("\r")
	if d.view.Caller == "" {
		t.Fatal("Enter on a branch did not filter its caller")
	}
	d.escapeKey()
	if d.view.Caller != "" || !d.navigation.focused {
		t.Fatal("Esc did not restore the caller filter")
	}
	// The file tree previews files the same way.
	keys("\t")
	n := &d.navigation
	selected := d.view.Selected
	for range len(n.entries) {
		keys("j")
		if d.view.Selected != selected {
			break
		}
	}
	if d.view.Selected == selected || n.entries[n.cursor].file != d.view.Selected {
		t.Fatal("moving through the tree did not show the file under the cursor")
	}
}
