package router

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	auto, stop := newAutoLiveDiff(ctx, store.directory)
	defer stop()
	// As in server startup, committed changes reach the diff pane.
	store.liveDiff = auto.events.publish
	activity := newSubagentActivity()
	activity.attachPane(newActivityPane(ctx, auto.requestActivity))
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "internal/broker"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, content := range previewFiles {
		if err := os.WriteFile(filepath.Join(workspace, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestTerminalUIPreviewCodex$")
	cmd.Env = append(os.Environ(), "MEKUGI_UI_PREVIEW_CODEX=1")
	wait, err := startTerminalUI(ctx, cmd, tty, tty, auto, store, activity)
	if err != nil {
		t.Fatal(err)
	}
	go replayPreviewSession(ctx, auto, store, activity, workspace, step)
	if err := wait(); err != nil {
		t.Fatal(err)
	}
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
	"internal/broker/broker_test.go": `package broker

import "testing"

func TestPublish(t *testing.T) {
	b := New()
	b.Publish("topic", "hello")
}
`,
}

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
func replayPreviewSession(ctx context.Context, auto *autoLiveDiff, store *mekugiReplayStore, activity *subagentActivity, workspace string, step time.Duration) {
	usage := newThreadUsage()
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
		report, complete := usage.snapshot(thread)
		activity.syncUsage(thread, counts, report, complete, tokenCost{})
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
	tool := func(thread, text string) string {
		calls++
		id := fmt.Sprintf("call-%d", calls)
		activity.collect(thread, "tool-call\x00"+id, "tool", text)
		return id
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
		calls++
		call := fmt.Sprintf("patch-%d", calls)
		if id, err := store.reserveChange(ctx, workspace, thread, call); err == nil {
			_ = store.put(ctx, workspace, map[string]mekugiHistory{call: {
				ToolName: applyPatchToolName, ChangeID: id, CorrelationID: call, Applied: true, ExecutingThread: thread,
				ReviewFiles: []mekugi.ReviewFile{mekugi.RenderReviewFile(beforePath, absolute, string(before), after)},
			}})
		}
		verb := "Edit"
		if beforePath == "" {
			verb = "Create"
		}
		activity.collect(thread, "tool-call\x00"+call, "tool", fmt.Sprintf("%s `%s` +%d -%d", verb, path, added, removed))
		return pause(0.5)
	}
	spawn := func(thread, name, assignment string) {
		activity.observe(thread, "root", name, true)
		activity.collect(thread, "start\x00"+thread, "start", "Started · `gpt-6-luna` `max`\nSpawn assignment:\n"+assignment)
	}

	activity.observe("root", "", "/root", false)
	auto.observe(workspace, "root", codexTurnMetadata{RequestKind: "turn"})
	auto.observe(workspace, "tests", codexTurnMetadata{RequestKind: "turn"})
	activity.beginResponse("root")
	spawn("trace", "/root/trace", "Trace how the live diff preview worker paces streamed input.")
	spawn("pane", "/root/pane_trace", "Find why the side pane opens before preview content arrives.")
	spawn("tests", "/root/script_tests", "Write broker subscription tests; cover the unsubscribe race.")
	activity.syncPaneRoles("root", map[string]journalSpawnRole{
		"/root/trace": {Role: "explorer"}, "/root/pane_trace": {Role: "explorer"}, "/root/script_tests": {Role: "worker"},
	})
	if !pause(1) {
		return
	}
	tool("trace", "Read `internal/router/live_diff_preview.go` 1:120 200:260")
	tool("pane", "Search `requestLaunch` in `internal/router`")
	respond("tests", "gpt-6-luna", 42_000, 900)
	tool("tests", "Read `internal/broker/broker.go` 1:80")
	if !pause(1) {
		return
	}
	respond("trace", "gpt-6-luna", 120_000, 2_400)
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
	if !pause(1) {
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
	// A later error outranks the final answer in the roster status.
	activity.collect("pane", "error-1", "error", "Provider stream closed: 502 Bad Gateway")
	settle("root", "gpt-6-astra", 380_000, 4_200)
	activity.beginResponse("tests")
	for ctx.Err() == nil {
		activity.streamOutput("tests", 200)
		pause(0.5)
	}
}
