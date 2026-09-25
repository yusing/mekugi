package router

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi"
	"golang.org/x/term"
)

// TestTerminalUIPreview replays a scripted session through the real wrapped
// Codex UI on the caller's terminal, so layout changes can be reviewed by eye.
// It is opt-in and interactive: run `make preview-ui`. A stand-in process
// takes Codex's place; Ctrl-D in the Codex pane quits.
func TestTerminalUIPreview(t *testing.T) {
	if os.Getenv("MEKUGI_UI_PREVIEW") != "1" {
		t.Skip("interactive preview; run make preview-ui")
	}
	// go test pipes stdout, so the UI owns the controlling terminal directly.
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no controlling terminal: %v", err)
	}
	defer tty.Close()
	step := 1200 * time.Millisecond
	if value := os.Getenv("MEKUGI_UI_PREVIEW_STEP"); value != "" {
		if step, err = time.ParseDuration(value); err != nil {
			t.Fatalf("MEKUGI_UI_PREVIEW_STEP: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	auto, store, activity, workspace := newPreviewSession(t, ctx, false)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestTerminalUIPreviewCodex$")
	cmd.Env = append(os.Environ(), "MEKUGI_UI_PREVIEW_CODEX=1")
	wait, err := startTerminalUI(ctx, cmd, tty, tty, auto, store, activity)
	if err != nil {
		t.Fatal(err)
	}
	go replayPreviewSession(ctx, auto, store, activity, workspace, step, nil)
	if err := wait(); err != nil {
		t.Fatal(err)
	}
}

// TestTerminalUIPreviewFrames replays the preview session headless and prints
// changed roster/activity frames, the responsive role legend, and the canonical
// usage report behind every agent.
// It is opt-in: run `make preview-roster`.
func TestTerminalUIPreviewFrames(t *testing.T) {
	if os.Getenv("MEKUGI_UI_PREVIEW_FRAMES") != "1" {
		t.Skip("debug replay; run make preview-roster")
	}
	width, rows := previewEnvInt(t, "MEKUGI_UI_PREVIEW_WIDTH", 160), previewEnvInt(t, "MEKUGI_UI_PREVIEW_ROWS", 8)
	step := 20 * time.Millisecond
	if value := os.Getenv("MEKUGI_UI_PREVIEW_STEP"); value != "" {
		var err error
		if step, err = time.ParseDuration(value); err != nil {
			t.Fatalf("MEKUGI_UI_PREVIEW_STEP: %v", err)
		}
	}
	styled := os.Getenv("MEKUGI_UI_PREVIEW_ANSI") == "1"
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	auto, store, activity, workspace := newPreviewSession(t, ctx, true)
	scripted := make(chan struct{})
	go replayPreviewSession(ctx, auto, store, activity, workspace, step, func() { close(scripted) })

	view := newLiveActivityView()
	var generation uint64
	var last string
	started := time.Now()
	// After the script ends, keep a few frames of the final streaming response.
	remaining := -1
	for remaining != 0 {
		select {
		case <-scripted:
			scripted, remaining = nil, 5
		case <-time.After(step):
		}
		if remaining > 0 {
			remaining--
		}
		if generation == 0 {
			var snapshot activityPaneEvent
			var ok bool
			if generation, snapshot, ok = activity.subscribePane(); !ok {
				continue
			}
			view.apply(snapshot)
		}
		if entries, agents, ok := activity.takePane(generation); ok {
			view.apply(activityPaneEvent{Kind: "entries", Entries: entries, Agents: agents})
		}
		lines := view.renderRosterPane(width, rows, time.Now())
		feed := view.renderFeed(max(20, width-1), 20)
		status, legend := (&terminalUI{width: width, activityOpen: true, agents: view}).statusLines()
		if !styled {
			lines = plainLines(lines)
			for i := range feed.lines {
				feed.lines[i] = ansi.Strip(feed.lines[i])
			}
			status, legend = ansi.Strip(status), ansi.Strip(legend)
		}
		frame := strings.Join(lines, "\n") + "\n" + strings.Join(feed.lines, "\n") + "\n" + status + "\n" + legend
		if frame == last {
			continue
		}
		last = frame
		fmt.Printf("── %s ", time.Since(started).Round(time.Millisecond))
		fmt.Println(strings.Repeat("─", max(0, width-12)))
		for len(lines) > 0 && ansi.Strip(lines[len(lines)-1]) == "" {
			lines = lines[:len(lines)-1]
		}
		for _, line := range lines {
			if styled {
				line += "\x1b[0m"
			}
			fmt.Println(line)
		}
		fmt.Println("usage owner:")
		for _, agent := range view.agents {
			report, observed := activity.usage.snapshot(previewThread(activity, agent.Name))
			fmt.Printf("  %-20s turns=%d observed=%v in=%d out=%d cost=%.4f known=%v missing=%d\n",
				agentDisplayName(agent.Name), agent.Turns, observed, report.InputTokens, report.OutputTokens,
				report.cost.cachedInput+report.cost.uncachedInput+report.cost.output, report.cost.known, report.missingUsage)
		}
		// Show the real feed renderer too: grouping, reasoning replacement,
		// waiting, and MCP events cannot be reviewed from roster rows alone.
		fmt.Println("activity:")
		for _, line := range feed.lines[max(0, len(feed.lines)-12):] {
			if styled {
				line += "\x1b[0m"
			}
			fmt.Println(line)
		}
		if legend != "" {
			fmt.Println("legend:", legend)
		}
		fmt.Println("status:", status)
	}
	var reasoning int
	for _, entry := range view.entries {
		if entry.Kind == "reasoning" && entry.CallID == "reasoning-preview" {
			reasoning++
			if strings.Contains(entry.Text, "response…") {
				t.Fatal("preview retained the superseded reasoning snapshot")
			}
		}
	}
	if reasoning != 1 {
		t.Fatalf("preview reasoning entries = %d, want one updating entry", reasoning)
	}
	var metrics *activityPaneAgent
	for i := range view.agents {
		if view.agents[i].Name == "/root/usage_review" {
			metrics = &view.agents[i]
		}
	}
	if metrics == nil || metrics.Turns != 10 || metrics.InputTokens != 140_600 || metrics.OutputTokens != 844 {
		t.Fatalf("preview did not complete metric boundary transitions: %+v", metrics)
	}
}

func previewThread(activity *subagentActivity, name string) string {
	activity.mu.Lock()
	defer activity.mu.Unlock()
	for thread, node := range activity.threads {
		if node.name == name {
			return thread
		}
	}
	return ""
}

func previewEnvInt(t *testing.T, name string, fallback int) int {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 {
		t.Fatalf("%s: want a positive integer, got %q", name, value)
	}
	return n
}

// newPreviewSession prepares the stores and workspace the scripted session edits.
// A headless session has no terminal UI to launch, so its pane always attaches.
func newPreviewSession(t *testing.T, ctx context.Context, headless bool) (*autoLiveDiff, *mekugiReplayStore, *subagentActivity, string) {
	t.Helper()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	auto, stop := newAutoLiveDiff(ctx, store.directory)
	t.Cleanup(stop)
	// As in server startup, committed changes reach the diff pane.
	store.liveDiff = auto.events.publish
	activity := newSubagentActivity()
	activity.usage = newThreadUsage()
	launch := auto.requestActivity
	if headless {
		launch = func() bool { return true }
	}
	activity.attachPane(newActivityPane(ctx, launch))
	workspace := t.TempDir()
	for _, dir := range []string{"internal/broker", "internal/pane"} {
		if err := os.MkdirAll(filepath.Join(workspace, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for path, content := range previewFiles {
		if err := os.WriteFile(filepath.Join(workspace, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return auto, store, activity, workspace
}

// TestTerminalUIPreviewCodex stands in for Codex inside the preview's PTY.
func TestTerminalUIPreviewCodex(t *testing.T) {
	if os.Getenv("MEKUGI_UI_PREVIEW_CODEX") != "1" {
		return
	}
	old, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		os.Exit(2)
	}
	defer term.Restore(int(os.Stdin.Fd()), old)
	fmt.Print(strings.ReplaceAll(`╭────────────────────────────────────────────────────────╮
│ >_ Codex (preview stand-in)                            │
│ model: gpt-6-astra medium   /workspace                 │
╰────────────────────────────────────────────────────────╯

› Write broker subscription tests, and trace why the live diff pane opens early.

• Spawned trace, pane_trace, and script_tests.

• Waiting for agents…

  Ctrl-B 1–4 focus panes · Ctrl-B arrows resize · Ctrl-B 1 then Ctrl-D quits
  Diff pane, once the turn ends: Tab changes by caller · { } step changes
  · a / 0 filter by caller / all · n p files · Enter opens at the file header

› `, "\n", "\r\n"))
	var b [1]byte
	for {
		if _, err := os.Stdin.Read(b[:]); err != nil {
			return
		}
		switch {
		case b[0] == 4:
			return
		case b[0] == '\r':
			fmt.Print("\r\n› ")
		case b[0] == 127:
			fmt.Print("\b \b")
		case b[0] >= ' ':
			os.Stdout.Write(b[:])
		}
	}
}

// previewFiles seed the workspace that the replayed patches edit.
var previewFiles = map[string]string{
	"internal/broker/broker.go": `package broker

import "sync"

type Broker struct {
	mu   sync.Mutex
	subs map[string][]chan string
}

func New() *Broker { return &Broker{subs: make(map[string][]chan string)} }

func (b *Broker) Subscribe(topic string) chan string {
	ch := make(chan string, 1)
	b.subs[topic] = append(b.subs[topic], ch)
	return ch
}

func (b *Broker) Publish(topic, message string) {
	for _, ch := range b.subs[topic] {
		ch <- message
	}
}
`,
	"internal/pane/launch.go": `package pane

// Launch opens the side pane for a requested preview.
func Launch(requested bool, frames <-chan string) string {
	if requested {
		return "open"
	}
	return <-frames
}
`,
	"internal/broker/broker_test.go": `package broker

import "testing"

func TestPublish(t *testing.T) {
	b := New()
	b.Publish("topic", "hello")
}
`,
}

// previewMainPatch is main's own stock patch, before the agents edit.
const previewMainPatch = `*** Begin Patch
*** Update File: internal/broker/broker.go
@@
 import "sync"
 
+// Broker fans published messages out to each topic's subscribers.
 type Broker struct {
*** End Patch
`

// previewPatches are streamed in order, as a worker's apply_patch calls.
var previewPatches = []string{
	`*** Begin Patch
*** Update File: internal/broker/broker_test.go
@@
 func TestPublish(t *testing.T) {
 	b := New()
 	b.Publish("topic", "hello")
 }
+
+func TestSubscribeBeforePublish(t *testing.T) {
+	b := New()
+	messages := b.Subscribe("topic")
+	b.Publish("topic", "hello")
+	if got := <-messages; got != "hello" {
+		t.Fatalf("got %q", got)
+	}
+}
+
+func TestUnsubscribeRace(t *testing.T) {
+	b := New()
+	messages := b.Subscribe("topic")
+	go b.Publish("topic", "late")
+	b.Unsubscribe("topic", messages)
+}
*** End Patch
`,
	`*** Begin Patch
*** Update File: internal/broker/broker.go
@@
 func (b *Broker) Subscribe(topic string) chan string {
 	ch := make(chan string, 1)
+	b.mu.Lock()
+	defer b.mu.Unlock()
 	b.subs[topic] = append(b.subs[topic], ch)
 	return ch
 }
 
 func (b *Broker) Publish(topic, message string) {
-	for _, ch := range b.subs[topic] {
+	b.mu.Lock()
+	subs := append([]chan string(nil), b.subs[topic]...)
+	b.mu.Unlock()
+	for _, ch := range subs {
 		ch <- message
 	}
 }
+
+func (b *Broker) Unsubscribe(topic string, ch chan string) {
+	b.mu.Lock()
+	defer b.mu.Unlock()
+	subs := b.subs[topic]
+	for i, c := range subs {
+		if c == ch {
+			b.subs[topic] = append(subs[:i], subs[i+1:]...)
+			return
+		}
+	}
+}
*** End Patch
`,
	`*** Begin Patch
*** Add File: internal/broker/doc.go
+// Package broker fans published messages out to topic subscribers.
+package broker
*** End Patch
`,
}

// applyPreviewPatch applies one single-hunk fixture patch to before.
func applyPreviewPatch(patch, before string) (path, after string, added, removed int, ok bool) {
	var old, next strings.Builder
	create := false
	for line := range strings.Lines(patch) {
		switch {
		case strings.HasPrefix(line, "*** Update File: "):
			path = strings.TrimSpace(strings.TrimPrefix(line, "*** Update File: "))
		case strings.HasPrefix(line, "*** Add File: "):
			path, create = strings.TrimSpace(strings.TrimPrefix(line, "*** Add File: ")), true
		case strings.HasPrefix(line, "***"), strings.HasPrefix(line, "@@"):
		case strings.HasPrefix(line, "+"):
			next.WriteString(line[1:])
			added++
		case strings.HasPrefix(line, "-"):
			old.WriteString(line[1:])
			removed++
		case strings.HasPrefix(line, " "):
			old.WriteString(line[1:])
			next.WriteString(line[1:])
		case line == "\n":
			old.WriteString(line)
			next.WriteString(line)
		}
	}
	if create {
		return path, next.String(), added, removed, true
	}
	if !strings.Contains(before, old.String()) {
		return path, "", 0, 0, false
	}
	return path, strings.Replace(before, old.String(), next.String(), 1), added, removed, true
}

// replayPreviewSession feeds the collector and live diff the same events a
// real session produces, paced so the panes can be watched as they change.
// scripted, when set, runs once the script ends and only streaming continues.
func replayPreviewSession(ctx context.Context, auto *autoLiveDiff, store *mekugiReplayStore, activity *subagentActivity, workspace string, step time.Duration, scripted func()) {
	usage := activity.usage
	pause := func(n float64) bool {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(time.Duration(n * float64(step))):
			return true
		}
	}
	settle := func(thread, model string, input, output uint64) {
		counts := tokenCounts{InputTokens: input, UncachedInputTokens: input, OutputTokens: output}
		usage.observation(thread, thread, model, "").observe(counts)
		activity.syncUsage(thread)
		activity.endResponse(thread)
	}
	respond := func(thread, model string, input, output uint64) {
		activity.beginResponse(thread)
		for range 3 {
			activity.streamOutput(thread, int(output))
			if !pause(0.3) {
				return
			}
		}
		settle(thread, model, input, output)
	}
	calls := 0
	call := func() string {
		calls++
		return fmt.Sprintf("call-%d", calls)
	}
	tool := func(thread, text string) string {
		id := call()
		activity.collect(thread, "tool-call\x00"+id, "tool", text)
		return id
	}
	// record commits one applied change, as a confirmed call's history does.
	record := func(thread, caller, tool, source, beforePath, afterPath, before, after string) {
		id := call()
		if change, err := store.reserveChange(ctx, workspace, thread, id); err == nil {
			_ = store.put(ctx, workspace, map[string]mekugiHistory{id: {
				ToolName: tool, Source: source, Caller: caller, ChangeID: change, CorrelationID: id, ExecutingThread: thread,
				ReviewFiles: []mekugi.ReviewFile{mekugi.RenderReviewFile(beforePath, afterPath, before, after)},
			}})
		}
	}
	// patch streams one apply_patch preview, then records its applied result
	// the way a confirmed stock call does, so diff review and file nav fill in.
	patch := func(thread, caller, text string) bool {
		worker := startLiveDiffPreview(ctx, auto.events, workspace, thread, applyPatchToolName)
		worker.mu.Lock()
		worker.preview.Caller = caller
		worker.mu.Unlock()
		for chunk := range strings.Lines(text) {
			worker.appendDelta(chunk)
			if !pause(0.12) {
				return false
			}
		}
		worker.finish(text)
		// The host applies the patch only after the call completes; writing
		// earlier would race the worker's final projection.
		select {
		case <-worker.done:
		case <-ctx.Done():
			return false
		}
		path, _, _, _, _ := applyPreviewPatch(text, "")
		absolute := filepath.Join(workspace, path)
		before, err := os.ReadFile(absolute)
		beforePath := absolute
		if os.IsNotExist(err) {
			beforePath = ""
		}
		_, after, added, removed, ok := applyPreviewPatch(text, string(before))
		if !ok || os.WriteFile(absolute, []byte(after), 0o644) != nil {
			return true
		}
		record(thread, caller, applyPatchToolName, "", beforePath, absolute, string(before), after)
		verb := "Edit"
		if beforePath == "" {
			verb = "Create"
		}
		activity.collect(thread, "tool-call\x00"+call(), "tool", fmt.Sprintf("%s `%s` +%d -%d", verb, path, added, removed))
		return pause(0.5)
	}
	// script records an interpreter's edit the way an observed exec_command
	// does: attributed to its caller and to the program that wrote it.
	script := func(thread, caller, source, path, command string, edit func(string) string) bool {
		absolute := filepath.Join(workspace, path)
		before, err := os.ReadFile(absolute)
		if err != nil {
			return true
		}
		after := edit(string(before))
		if os.WriteFile(absolute, []byte(after), 0o644) != nil {
			return true
		}
		activity.collect(thread, "tool-call\x00"+call(), "tool", "Run\n```bash\n"+command+"\n```")
		record(thread, caller, nativeExecCommandToolName, source, absolute, absolute, string(before), after)
		return pause(0.8)
	}
	spawn := func(thread, name, assignment string) {
		activity.observe(thread, "root", name, true)
		activity.collect(thread, "start\x00"+thread, "start", "Started · `gpt-6-luna` `max`\nSpawn assignment:\n"+assignment)
	}

	activity.observe("root", "", "/root", false)
	turn := codexTurnMetadata{RequestKind: "turn", TurnID: "preview-turn"}
	for _, thread := range []string{"root", "tests", "pane"} {
		auto.observe(workspace, thread, turn)
	}
	auto.beginTurn(workspace, "root", turn)
	activity.beginResponse("root")
	// The very first response must request and populate the panes before any
	// shell activity. Consecutive Inspect calls exercise the Read-like group.
	tool("root", "Inspect `internal/broker/broker.go`\n\nInspect `internal/pane/launch.go`")
	if !pause(0.5) {
		return
	}
	tool("root", "Inspect `internal/broker/broker_test.go`")
	activity.collectEvent(activityEvent{thread: "root", source: "reasoning-preview", callID: "reasoning-preview", kind: "reasoning", text: "Checking which pane owns the first response…"})
	if !pause(0.5) {
		return
	}
	activity.collectEvent(activityEvent{thread: "root", source: "reasoning-preview", callID: "reasoning-preview", kind: "reasoning", text: "Checking which pane owns the first response and how grouped reads render."})
	tool("root", "MCP `docs.lookup`\n`{\"query\":\"pane launch\"}`")
	if !pause(0.5) {
		return
	}
	// main edits first, so the change graph's agent lanes branch from it.
	if !patch("root", "/root", previewMainPatch) {
		return
	}
	spawn("trace", "/root/trace", "Trace how the live diff preview worker paces streamed input.")
	spawn("pane", "/root/pane_trace", "Make the side pane wait for its first preview frame before opening.")
	spawn("tests", "/root/script_tests", "Write broker subscription tests; cover the unsubscribe race.")
	spawn("metrics", "/root/usage_review", "Review the compact roster while metrics cross width boundaries.")
	activity.syncPaneRoles("root", map[string]journalSpawnRole{
		"/root/trace": {Role: "explorer"}, "/root/pane_trace": {Role: "worker"}, "/root/script_tests": {Role: "worker"},
		"/root/usage_review": {Role: "review-correctness"},
	})
	// Backdate only display ages, so the preview includes both an established
	// agent and a recent response without making the interactive replay wait.
	activity.mu.Lock()
	activity.threads["metrics"].started = time.Now().Add(-52 * time.Second)
	activity.threads["trace"].started = time.Now().Add(-33 * time.Second)
	activity.mu.Unlock()
	tool("root", "Waiting for agent")
	// Nine to ten turns, 73.7K to 140.6K input, and 789 to 844 output
	// intentionally cross the widths shown in the reported shifting example.
	// All totals come from the same usage owner as the normal roster.
	for range 8 {
		activity.beginResponse("metrics")
		settle("metrics", "gpt-6-luna", 0, 0)
	}
	activity.beginResponse("metrics")
	settle("metrics", "gpt-6-luna", 73_700, 789)
	activity.mu.Lock()
	activity.threads["metrics"].lastResponse = time.Now().Add(-4 * time.Second)
	activity.mu.Unlock()
	if !pause(1) {
		return
	}
	activity.beginResponse("metrics")
	settle("metrics", "gpt-6-luna", 66_900, 55)
	if !pause(1) {
		return
	}
	tool("trace", "Read `internal/router/live_diff_preview.go` 1:120 200:260")
	tool("pane", "Search `requestLaunch` in `internal/router`")
	respond("tests", "gpt-6-luna", 42_000, 900)
	tool("tests", "Inspect `internal/broker/broker.go`\n\nInspect `internal/broker/broker_test.go`")
	tool("tests", "Inspect `internal/pane/launch.go`")
	tool("tests", "Read `internal/broker/broker.go` 1:80")
	if !pause(1) {
		return
	}
	respond("trace", "gpt-6-luna", 120_000, 2_400)
	activity.mu.Lock()
	activity.threads["trace"].lastResponse = time.Now().Add(-4 * time.Second)
	activity.mu.Unlock()
	tool("pane", "Run\n```bash\nrg -n 'auto.requested' internal/router | head -20\n```")
	tool("tests", "Read `internal/broker/broker_test.go`")
	if !pause(1) {
		return
	}

	// script_tests streams its patches; the diff pane follows the live input,
	// then reviews each applied change.
	auto.requestLaunch(workspace, "tests")
	for _, text := range previewPatches {
		if !patch("tests", "/root/script_tests", text) {
			return
		}
	}
	respond("pane", "gpt-6-luna", 64_000, 1_100)
	// pane_trace edits with a Python script, a third lane with another source.
	if !script("pane", "/root/pane_trace", "python3", "internal/pane/launch.go", "python3 - <<'EOF'\n# wait for the first frame before opening\nEOF",
		func(source string) string {
			return strings.Replace(source, "\tif requested {\n\t\treturn \"open\"\n\t}\n\treturn <-frames", "\tframe := <-frames\n\tif requested {\n\t\treturn \"open: \" + frame\n\t}\n\treturn frame", 1)
		}) {
		return
	}
	activity.collect("tests", "reply-1", "reply", "[`/root/script_tests` -> `/root`] Message received:\nDraft tests are in broker_test.go; please review before I extend them.")
	activity.markFinal("root", subagentFinal{sender: "/root/trace", source: "final-trace",
		text: "`liveDiffPreviewWorker.run` paces incoming input with a 50ms frame delay and reveals buffered deltas at a steady rate."})
	if !pause(1.5) {
		return
	}
	activity.collect("tests", "reply-2", "reply", "[`/root` -> `/root/script_tests`] Message received:\nCurrent tests look good. Ensure the subscription exists before publish, and cover the unsubscribe race.")
	respond("tests", "gpt-6-luna", 97_000, 3_600)
	run := tool("tests", "Run `go test ./internal/broker -run 'Subscribe|Unsubscribe' -race`")
	if !pause(1) {
		return
	}
	activity.collect("tests", "tool-exit\x00"+run, "exit", "1")
	activity.markFinal("root", subagentFinal{sender: "/root/pane_trace", source: "final-pane",
		text: "The side pane opens before preview content arrives: `requestLaunch` sets `auto.requested`, then the first frame waits for data."})
	// A later error outranks the final answer in the roster status. The dropped
	// stream reported no usage, so pane_trace's cost becomes a lower bound.
	activity.beginResponse("pane")
	usage.observation("pane", "pane", "gpt-6-luna", "").finish()
	activity.endResponse("pane")
	activity.collect("pane", "error-1", "error", "Provider stream closed: 502 Bad Gateway")
	// main renames a test string with sed, then its turn ends and the diff
	// pane switches from the live stream to review.
	if !script("root", "/root", "sed", "internal/broker/broker_test.go", "sed -i 's/\"hello\"/\"greeting\"/' internal/broker/broker_test.go",
		func(source string) string { return strings.ReplaceAll(source, `"hello"`, `"greeting"`) }) {
		return
	}
	// main renames pane_trace's edited file (RM in the tree, R in its change),
	// then an mchanges revert leaves a conflict for resolution (UU).
	activity.collect("root", "tool-call\x00"+call(), "tool", "Run\n```bash\ngit mv internal/pane/launch.go internal/pane/launcher.go\n```")
	from, to := filepath.Join(workspace, "internal/pane/launch.go"), filepath.Join(workspace, "internal/pane/launcher.go")
	if content, err := os.ReadFile(from); err == nil && os.Rename(from, to) == nil {
		record("root", "/root", nativeExecCommandToolName, "git", from, to, string(content), string(content))
	}
	if !pause(0.8) {
		return
	}
	if !script("root", "/root", "mchanges", "internal/broker/broker.go", "mchanges revert apple2",
		func(source string) string {
			return strings.Replace(source, "func (b *Broker) Publish(topic, message string) {\n",
				"<<<<<<< workspace\nfunc (b *Broker) Publish(topic, message string) {\n=======\nfunc (b *Broker) Publish(topic, message string) error {\n>>>>>>> mchanges revert apple2\n", 1)
		}) {
		return
	}
	settle("root", "gpt-6-astra", 380_000, 4_200)
	auto.finishTurn(workspace, "root", turn.TurnID)
	activity.beginResponse("tests")
	if scripted != nil {
		scripted()
	}
	for ctx.Err() == nil {
		activity.streamOutput("tests", 200)
		pause(0.5)
	}
}
