package router

import (
	"cmp"
	"encoding/json/jsontext"

	previewview "github.com/yusing/mekugi/modules/livepreview/terminalview"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/libraries/legacycall"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	"github.com/yusing/mekugi/modules/changehistory/changes"

	"github.com/yusing/mekugi/modules/changehistory/presentation"

	"github.com/yusing/mekugi/backends/codex/binding"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"

	"github.com/yusing/mekugi/modules/usage/accounting"

	json "encoding/json/v2"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/contracts/inference"
	"github.com/yusing/mekugi/libraries/pathdisplay"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"

	"github.com/yusing/mekugi/libraries/diffmodel"
	terminalui "github.com/yusing/mekugi/libraries/terminalui"

	responseevents "github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"golang.org/x/term"
)

// TestNativeUIPreview plays a session through the actual shell and painters,
// driven by the notifications Codex app-server sends. It is local and makes
// no model or network requests; edits land in a temporary workspace.
func TestNativeUIPreview(t *testing.T) {
	if os.Getenv("MEKUGI_NATIVE_UI_PREVIEW") != "1" {
		t.Skip("interactive UI preview")
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no controlling terminal: %v", err)
	}
	defer tty.Close()
	p := newNativePreview(t)
	defer p.close()
	p.delay = "0.6"
	err = terminalui.WithRawPane(t.Context(), tty, tty, "\x1b[?1049h\x1b[?25l\x1b[?1003;1006;2004h\x1b]10;?\x1b\\\x1b]11;?\x1b\\", "\x1b[?2026l\x1b[?1003;1006;2004l\x1b[0m\x1b[?25h\x1b[?1049l", func(keys <-chan byte) error {
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
			if err := p.ui.testPaint(tty, w, h); err != nil {
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
			case <-tick.C:
				now := time.Now()
				if err := p.ui.drainKeys(keys); err != nil || p.ui.quitRequested {
					return err
				}
				p.serve()
				flashExpired := p.ui.shell.Transcript.ExpireFlash(now)
				settled := settleActivity(now, p.ui.testActivity(p.ui.shell.Transcript), p.ui.testActivity(p.ui.shell.Agents))
				settled = p.ui.testActivity(p.ui.shell.Transcript).testPace(now) || settled
				settled = p.ui.testActivity(p.ui.shell.Agents).testPace(now) || settled
				settled = p.ui.dirty || settled // Includes keys drained by this tick.
				p.ui.dirty = false
				p.ui.flushStreamOutput() // Rolls output bursts, as each real frame does.
				settled = p.ui.dirty || settled
				noticeExpired := p.ui.expireNotice(now)
				if p.pump(0) {
					lastStep = now // The script resumes a pace after the command ends.
					if err := paint(); err != nil {
						return err
					}
					continue
				}
				if p.live == nil && now.Sub(lastStep) >= p.pace() && p.advance() {
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
				if w != lastWidth || h != lastHeight || now.Sub(lastPaint) >= time.Second || p.ui.publishPresentation(p.ui.now()).Status.Animating || p.ui.shell.Agents.HasLiveStatus() || flashExpired || settled || noticeExpired || terminalTest(p.ui).Live.Tick(now) {
					if err := paint(); err != nil {
						return err
					}
				}
			case result := <-p.ui.picker.scanResults:
				p.ui.applyPickerScan(result)
				if err := paint(); err != nil {
					return err
				}
			case key, ok := <-keys:
				if !ok {
					return io.EOF
				}
				// Keys take the real UI path; the preview answers the resulting
				// requests the way app-server would.
				if err := terminalTest(p.ui).key(key); err != nil {
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
	t.Parallel()
	p := newNativePreview(t)
	defer p.close()
	// These renderer assertions exercise Activity, not automatic pane choice.
	p.ui.Text = "Keep my draft"
	render := func(name string, want ...string) string {
		t.Helper()
		screen := vt.NewEmulator(160, 48)
		defer screen.Close()
		terminalTest(p.ui).Frame.Rows = nil // A new screen needs every row.
		rollCommandOutput(p.ui)
		finishPacing(p.ui.testActivity(p.ui.shell.Transcript), p.ui.testActivity(p.ui.shell.Agents))
		if err := p.ui.testPaint(screen, 160, 48); err != nil {
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
	render("main replacement", "LIVE ·", "M internal/broker/broker.go", "1 Main", "3 Agents")
	terminalTest(p.ui).LiveHidden = true // Subsequent assertions inspect transcripts, not their temporary replacements.
	// Grok reasoning streams through the router's translation into Activity.
	p.until("children start")
	for range 12 {
		p.advance()
	}
	p.ui.testActivity(p.ui.shell.Agents).setDisplayFilter(true, "/root/explorer")
	render("grok thinking", "• Thinking…")
	p.until("explorer thought")
	render("grok thought readable", "• Thought")
	retained := false
	for _, record := range p.ui.childSource.Entries {
		blocks := p.ui.shell.Agents.Disclosure.Blocks(record.Values)
		for _, block := range blocks {
			if block.Kind == "summary" && block.Body == nativePreviewGrokReasoning {
				retained = p.ui.shell.Agents.Painter.DialogPage(block, 120).Text == nativePreviewGrokReasoning
			}
		}
	}
	if !retained {
		t.Fatal("collapsed reasoning lost its complete dialog body")
	}
	p.until("three agents edit at once")
	p.ui.testActivity(p.ui.shell.Agents).setDisplayFilter(false, p.ui.shell.Agents.Selected)
	terminalTest(p.ui).LiveHidden = false
	frame := render("caller-local live previews", "LIVE ·", "3 Agents", "2 Diff ●")
	if strings.Contains(frame, "3 Activity ─") && strings.Contains(frame, "no captured edits yet") {
		t.Fatal("the saved diff replaced Activity without being opened")
	}
	agents := func() string {
		rollCommandOutput(p.ui)
		finishPacing(p.ui.testActivity(p.ui.shell.Agents))
		return ansi.Strip(strings.Join(p.ui.testActivity(p.ui.shell.Agents).RenderFeed(120, 80).Lines, "\n"))
	}
	terminalTest(p.ui).LiveHidden = true
	p.ui.shell.Agents.LivePreviews = nil
	p.until("tester applies")
	// Hold the real rerun process at its first output boundary until the live
	// frame has been inspected. A zero-delay script can otherwise finish before
	// the renderer sees its running state, depending on output chunking.
	gate, release, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Close()
	defer release.Close()
	p.commandGate = gate
	p.until("tester reruns")
	p.commandGate = nil
	if got := agents(); !strings.Contains(got, "exit 1") || !strings.Contains(got, "┆ --- FAIL: TestUnsubscribeRace (0.37s)") {
		t.Fatalf("failed test run lacks its failure tail:\n%s", got)
	}
	for p.live != nil && !strings.Contains(agents(), "┆ === RUN   TestConcurrentPublish") {
		p.pump(time.Second)
	}
	if got := agents(); p.live == nil || !regexp.MustCompile(`Running +│ for test in`).MatchString(got) {
		t.Fatalf("streamed test run lacks its live state:\n%s", got)
	}
	if err := release.Close(); err != nil {
		t.Fatal(err)
	}
	for p.live != nil {
		p.pump(time.Second)
	}
	if got := agents(); strings.Contains(got, "Running") || !strings.Contains(got, "┆ ok      example.com") || !regexp.MustCompile(`Ran +│ for test in`).MatchString(got) {
		t.Fatalf("passed test run lacks its settled state:\n%s", got)
	}
	nextEvent(t, p.ui, p.ui.childSource.Entries[len(p.ui.childSource.Entries)-1].Source.Native.Thread)
	settleActivity(time.Now().Add(activityui.OutputDebounce), p.ui.testActivity(p.ui.shell.Agents))
	if got := agents(); strings.Contains(got, "┆ ok ") || !strings.Contains(got, "┆ … +6 lines") {
		t.Fatalf("passed test run output did not collapse:\n%s", got)
	}
	for p.advance() {
	}
	if p.ui.Text != "Keep my draft" {
		t.Fatal("playback overwrote typed input")
	}
	render("finished session", "● main", "↩ re: your message", "↩ re: assignment", "finished", "$")
	for _, key := range []byte{2, '2'} {
		if err := terminalTest(p.ui).key(key); err != nil {
			t.Fatal(err)
		}
	}
	render("saved diff", "2 Diff · saved · 5 files", "broker.go", "launch.go", "broker_race_test.go", "Unsubscribe")
	if !terminalTest(p.ui).DiffOpen || terminalTest(p.ui).DiffUnseen {
		t.Fatal("opening the saved diff did not clear its badge")
	}
	// The narrow pane's stacked list switches to changes and takes focus
	// without covering the diff.
	if err := terminalTest(p.ui).key('\t'); err != nil {
		t.Fatal(err)
	}
	render("saved diff changes", "Unsubscribe", "Changes", "files focus", "─────")
	if source := savedPane(terminalTest(p.ui)).Selection(terminalTest(p.ui).Layout.Diff.W-1, terminalTest(p.ui).Layout.Diff.H-1); source.Y == 0 || len(source.Painted) == 0 {
		t.Fatal("Tab replaced the stacked list with a full-pane picker")
	}
	for _, key := range []byte{'\t', 's'} {
		if err := terminalTest(p.ui).key(key); err != nil {
			t.Fatal(err)
		}
	}
	render("saved diff hidden list", "Unsubscribe")
	if source := savedPane(terminalTest(p.ui)).Selection(terminalTest(p.ui).Layout.Diff.W-1, terminalTest(p.ui).Layout.Diff.H-1); source.Y != 0 || len(source.Painted) == 0 {
		t.Fatal("s did not hide the focused stacked list")
	}
	// Picking an agent in the roster shows its Activity instead of the diff.
	for _, key := range []byte{2, '3'} {
		if err := terminalTest(p.ui).key(key); err != nil {
			t.Fatal(err)
		}
	}
	render("focused roster", "3 Agents")
	if !terminalTest(p.ui).DiffOpen {
		t.Fatal("focusing the roster closed the saved diff")
	}
	shell := terminalTest(p.ui)
	index := slices.IndexFunc(shell.Agents.Hits, func(hit activityui.Hit) bool { return hit.Agent == "/root/tester" })
	if index < 0 {
		t.Fatal("the focused roster has no tester row")
	}
	hit, roster := shell.Agents.Hits[index], shell.Layout.Roster
	for _, key := range []byte(fmt.Sprintf("\x1b[<0;%d;%dM", roster.X+hit.First, roster.Y+hit.Row)) {
		if err := shell.key(key); err != nil {
			t.Fatal(err)
		}
	}
	render("roster pick", "1 Thread", "tester")
	if !shell.DiffOpen || !shell.main.sessionPresentation().DisplayingChild() || shell.Agents.Selected != "/root/tester" {
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
			if err := terminalTest(p.ui).key(key); err != nil {
				t.Fatal(err)
			}
			p.serve()
		}
	}
	p.until("main streams its patch")
	keys("also check Unsubscribe\r")
	if p.ui.conversation().Submitted[p.ui.Submission().ID].Text != "" || p.ui.Turn == "" || !slices.ContainsFunc(p.ui.source.Entries, func(e sessionpresentation.ActivityRecord) bool {
		return e.Source.Agent == "You" && e.Source.Text == "also check Unsubscribe"
	}) {
		t.Fatalf("steer not accepted: submitted=%q turn=%q", p.ui.conversation().Submitted[p.ui.Submission().ID].Text, p.ui.Turn)
	}
	keys("queued follow-up\t")
	if len(p.ui.conversation().Queued) != 1 || p.ui.Text != "" {
		t.Fatal("Tab did not queue during playback")
	}
	keys("scratch\x03")
	if p.ui.Text != "" || p.ui.Turn == "" || p.ui.quitRequested {
		t.Fatal("first Ctrl-C did not only clear the draft")
	}
	keys("\x03")
	if p.ui.Turn != "" || p.ui.sessionPresentation().Status.Text != "Interrupted" || p.advance() || len(p.active) != 0 {
		t.Fatalf("interrupt left playback running: turn=%q status=%q active=%v", p.ui.Turn, p.ui.sessionPresentation().Status.Text, p.active)
	}
	if p.ui.Text != "queued follow-up" || len(p.ui.conversation().Queued) != 0 {
		t.Fatalf("interrupt did not return queued input: %q", p.ui.Text)
	}
	keys("\x03")
	for _, agent := range p.ui.childSource.Agents {
		if agent.Responding {
			t.Fatalf("%s still responding after interrupt", agent.Name)
		}
	}
	keys("hello\r")
	if !strings.Contains(p.ui.sessionLabel(p.ui.now()), "Completed in ") || !slices.ContainsFunc(p.ui.source.Entries, func(e sessionpresentation.ActivityRecord) bool {
		return strings.Contains(e.Source.Text, "I heard: hello")
	}) {
		t.Fatalf("new turn not answered: %q", p.ui.sessionPresentation().Status.Text)
	}
	keys("\x03")
	if !p.ui.quitRequested {
		t.Fatal("idle Ctrl-C did not quit")
	}
}

func TestNativeUIPreviewSessionControlsRendered(t *testing.T) {
	p := newNativePreview(t)
	defer p.close()
	keys := func(s string) {
		t.Helper()
		for _, key := range []byte(s) {
			if err := terminalTest(p.ui).key(key); err != nil {
				t.Fatal(err)
			}
			p.serve()
		}
	}
	render := func() string {
		t.Helper()
		screen := vt.NewEmulator(100, 28)
		defer screen.Close()
		terminalTest(p.ui).Frame.Rows = nil
		finishPacing(p.ui.testActivity(p.ui.shell.Transcript), p.ui.testActivity(p.ui.shell.Agents))
		if err := p.ui.testPaint(screen, 100, 28); err != nil {
			t.Fatal(err)
		}
		return screen.String()
	}
	p.until("main streams its patch")
	keys("/compact\t")
	if p.ui.Turn == "" || len(p.ui.conversation().Unsent) != 1 || p.ui.conversation().Unsent[0].Text != "/compact" {
		t.Fatal("busy compact was not queued locally")
	}
	if frame := render(); !strings.Contains(frame, "/compact") {
		t.Fatalf("queued compact absent from rendered frame:\n%s", frame)
	}
	p.until("done")
	p.serve()
	terminalTest(p.ui).Live.Tick(time.Now().Add(previewview.CompletionHold + time.Second))
	if p.ui.Turn != "" || p.ui.Compaction.AcknowledgementPending() || !strings.Contains(render(), "Context compacted") {
		t.Fatal("queued compaction did not complete visibly")
	}
	keys("/clear\r")
	if p.ui.Thread != "preview-clear-1" || len(p.ui.source.Entries) != 0 {
		t.Fatalf("clear did not open empty thread: thread=%q entries=%+v", p.ui.Thread, p.ui.source.Entries)
	}
	if frame := render(); strings.Contains(frame, "Context compacted") || strings.Contains(frame, "old transcript") {
		t.Fatalf("clear retained previous presentation:\n%s", frame)
	}
	keys("fresh prompt\r")
	if p.ui.Thread != "preview-clear-1" || !strings.Contains(render(), "I heard: fresh prompt") {
		t.Fatal("fresh thread did not answer the next composer prompt")
	}
}

type nativePreviewStep struct {
	name string
	run  func()
}

type nativePreview struct {
	t           *testing.T
	ui          *appServerUI
	proxy       *observationFixture
	input       *appServerTestInput
	store       *mekugiReplayStore
	usage       *accounting.Service
	workspace   string
	message     int
	calls       int
	clears      int
	steps       []nativePreviewStep
	step        int
	active      map[string]string // Running turn by thread.
	assignments map[string]string
	models      map[string]string     // Provider model by thread, when not the default.
	delay       string                // Seconds each preview command sleeps between outputs.
	commandGate *os.File              // Optional input gate instead of timed output pacing.
	live        *nativePreviewProcess // The running command; the script waits for it.
}

// nativePreviewProcess is a real local process whose output reaches the UI
// only as app-server command notifications.
type nativePreviewProcess struct {
	thread string
	item   map[string]any
	output string
	events chan nativePreviewOutput
}

type nativePreviewOutput struct {
	chunk string
	exit  *int // Set on the final event, after all output.
}

func TestNativePreviewCleansBlockedCommand(t *testing.T) {
	gate, release, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Close()
	defer release.Close()
	t.Run("owner exits before releasing command", func(t *testing.T) {
		p := newNativePreview(t)
		defer p.close()
		p.commandGate = gate
		p.run("main", "blocked", "printf ready; sleep $DELAY")
		if !p.pump(time.Second) || p.live == nil || p.live.output != "ready" {
			t.Fatal("command did not reach its output gate")
		}
		// The writer stays open until this subtest's cleanup has canceled and
		// joined the process. Cleanup must not depend on pumping another event.
	})
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
	usage := newTestUsage(t)
	proxy := newManagedMekugiProxy(t)
	setTestAppServerIntegration(u, proxy)
	proxy.usage = usage
	u.usage = usage
	u.audience.Observe("main", "", "/root", false)
	u.audience.Attach("main")
	u.model, u.reasoningEffort = "gpt-6-luna", "medium"
	if err := json.Unmarshal([]byte(`[{"model":"gpt-6-luna","defaultReasoningEffort":"medium","supportedReasoningEfforts":[{"reasoningEffort":"low"},{"reasoningEffort":"medium"},{"reasoningEffort":"high"}],"serviceTiers":[{"id":"priority"}]}]`), &u.models); err != nil {
		t.Fatal(err)
	}

	u.reviewSource = store.captured
	u.terminalViews = testTerminalViews(u.reviewSource)
	label, attachment := u.terminalViews.Attach(terminalTest(u).Theme)
	terminalTest(u).AuxiliaryLabel, terminalTest(u).Auxiliary, u.preparePane = label, attachment.Pane, attachment.Prepare
	u.review.Projection = sessionreview.Projection{Workspace: workspace, Changes: store.changes, Retained: store.changeScope(), Data: new(presentation.Snapshot), Coverage: "CONNECTING"}
	u.startSession("main", workspace)
	p := &nativePreview{t: t, ui: u, proxy: proxy, input: input, store: store, usage: usage, workspace: workspace, delay: "0", active: make(map[string]string), assignments: make(map[string]string), models: map[string]string{"t-explorer": nativePreviewGrokModel}}
	store.liveDiff = func(updates []changes.Update) {
		var delivered []sessionreview.Change
		for _, update := range updates {
			delivered = append(delivered, sessionreview.FromUpdate(update))
		}
		u.applyReview(t.Context(), sessionreview.Event{Kind: "change", Changes: delivered})
	}
	scope := previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {}}}
	for _, thread := range []string{"main", "t-reviewer", "t-explorer", "t-tester"} {
		scope.Workspaces[workspace][thread] = true
	}
	u.applyReview(t.Context(), sessionreview.Event{Kind: "scope", Scope: &scope})
	u.testActivity(u.shell.Agents).Apply(sessionpresentation.ActivityEvent{Kind: "agents", Agents: u.session.Roster()})
	u.sessionPresentation().Status.Text = "Ready"
	p.populate()
	return p
}

func (p *nativePreview) close() {
	p.ui.cancelPickerScan()
	terminalTest(p.ui).Diff.Close()
}

// pace slows playback while patches stream, so each dock frame is readable.
func (p *nativePreview) pace() time.Duration {
	if p.step < len(p.steps) && strings.HasPrefix(p.steps[p.step].name, "stream") {
		return 260 * time.Millisecond
	}
	return 650 * time.Millisecond
}

// advance runs the next step, or first relays the live command's output.
func (p *nativePreview) advance() bool {
	if p.live != nil {
		p.pump(time.Second)
		return true
	}
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
		current, step := p.steps[p.step].name, p.step
		p.advance()
		if p.step > step && current == name {
			return
		}
	}
	p.t.Fatalf("no preview step %q", name)
}

func (p *nativePreview) add(name string, run func()) {
	p.steps = append(p.steps, nativePreviewStep{name, run})
}

// Each preview edit simulates one apply_patch call: shared preview streaming,
// temporary workspace effects, and the resulting app-server file-change item.
// This fake session does not invoke Codex or execute model-written commands.
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

// stream adds the shared preview updates as steps, a few lines at a time,
// interleaving concurrent writers.
func (p *nativePreview) stream(name string, edits ...nativePreviewEdit) {
	for lines := 3; ; lines += 3 {
		more := false
		for _, edit := range edits {
			if lines-3 >= strings.Count(edit.change(-1)["diff"].(string), "\n") {
				continue
			}
			more = true
			patchLines := strings.SplitAfter(edit.patch, "\n")
			input := strings.Join(patchLines[:min(lines+2, len(patchLines))], "")
			p.add("stream "+name, func() {
				preview := projectStockPatchPreview(p.ui.ctx, p.workspace, previewmodel.Preview{ID: edit.item, Workspace: p.workspace, Thread: edit.thread, Caller: p.ui.session.Caller(edit.thread), Tool: applyPatchToolName, Status: previewmodel.PreviewEdit, Input: input})
				p.ui.applyReview(p.ui.ctx, sessionreview.Event{Kind: "preview", Preview: &preview})
				revealNativeDock(terminalTest(p.ui))
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
	preview := projectStockPatchPreview(p.ui.ctx, p.workspace, previewmodel.Preview{ID: edit.item, Workspace: p.workspace, Thread: edit.thread, Caller: p.ui.session.Caller(edit.thread), Tool: applyPatchToolName, Status: previewmodel.PreviewEdit, Input: edit.patch, Complete: true})
	p.ui.applyReview(p.ui.ctx, sessionreview.Event{Kind: "preview", Preview: &preview})
	revealNativeDock(terminalTest(p.ui))
	change := edit.change(-1)
	item := map[string]any{"id": edit.item, "type": "fileChange", "status": "inProgress", "changes": []any{change}}
	p.notify("item/started", map[string]any{"threadId": edit.thread, "turnId": p.active[edit.thread], "item": item})
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
	caller := p.ui.session.Caller(edit.thread)
	if change, err := p.store.changes.Reserve(p.ui.ctx, p.store.scoped(p.ui.ctx).changeScope(), p.workspace, edit.thread, edit.item+"\x000"); err == nil {
		_ = p.store.put(p.ui.ctx, p.workspace, map[string]legacycall.History{id: {
			ToolName: applyPatchToolName, Caller: caller, ChangeID: change, CorrelationID: edit.item + "\x000", ExecutingThread: edit.thread,
			ReviewFiles: retainedReviewFiles([]diffmodel.ReviewFile{diffmodel.RenderReviewFile(beforePath, absolute, string(before), after)}),
		}})
	}
	item["status"] = "completed"
	p.notify("item/completed", map[string]any{"threadId": edit.thread, "turnId": p.active[edit.thread], "item": item})
}

func (p *nativePreview) command(thread, id, command string, actions []map[string]any, exit int) {
	p.finishCommand(thread, p.startCommand(thread, id, command, actions), exit)
}

func (p *nativePreview) startCommand(thread, id, command string, actions []map[string]any) map[string]any {
	item := map[string]any{"id": id, "type": "commandExecution", "command": command, "cwd": p.workspace, "status": "inProgress", "commandActions": actions}
	p.notify("item/started", map[string]any{"threadId": thread, "turnId": p.active[thread], "item": item})
	return item
}

// run starts script as a real shell process. Its output and exit reach the UI
// through app-server notifications, as a model's command would.
func (p *nativePreview) run(thread, id, script string) {
	if p.commandGate != nil {
		script = strings.ReplaceAll(script, "sleep $DELAY", "read -r resume")
	}
	script = strings.ReplaceAll(script, "$DELAY", p.delay)
	cmd := exec.CommandContext(p.t.Context(), "sh", "-c", script)
	cmd.Dir = p.workspace
	if p.commandGate != nil {
		cmd.Stdin = p.commandGate
	}
	read, write, err := os.Pipe()
	if err != nil {
		p.t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = write, write
	if err := cmd.Start(); err != nil {
		p.t.Fatal(err)
	}
	write.Close()
	live := &nativePreviewProcess{thread: thread, item: p.startCommand(thread, id, script, nil), events: make(chan nativePreviewOutput, 16)}
	done := make(chan struct{})
	p.t.Cleanup(func() { read.Close(); <-done })
	go func() {
		defer close(done)
		send := func(event nativePreviewOutput) bool {
			select {
			case live.events <- event:
				return true
			case <-p.t.Context().Done():
				return false
			}
		}
		buffer := make([]byte, 4096)
		for {
			n, err := read.Read(buffer)
			if n > 0 && !send(nativePreviewOutput{chunk: string(buffer[:n])}) {
				break
			}
			if err != nil {
				break
			}
		}
		read.Close()
		_ = cmd.Wait()
		send(nativePreviewOutput{exit: new(cmd.ProcessState.ExitCode())})
	}()
	p.live = live
}

// pump relays one event of the live command, waiting up to wait for it, and
// shows it as the next frame would.
func (p *nativePreview) pump(wait time.Duration) bool {
	if p.live == nil {
		return false
	}
	var event nativePreviewOutput
	select {
	case event = <-p.live.events:
	default:
		if wait == 0 {
			return false
		}
		select {
		case event = <-p.live.events:
		case <-time.After(wait):
			return false
		}
	}
	live := p.live
	if event.exit == nil {
		live.output += event.chunk
		p.notify("item/commandExecution/outputDelta", map[string]any{"threadId": live.thread, "turnId": p.active[live.thread], "itemId": live.item["id"], "delta": event.chunk})
	} else {
		live.item["aggregatedOutput"] = live.output
		p.finishCommand(live.thread, live.item, *event.exit)
		p.live = nil
	}
	p.ui.flushStreamOutput()
	return true
}

func (p *nativePreview) finishCommand(thread string, item map[string]any, exit int) {
	item["status"], item["exitCode"] = "completed", exit
	p.notify("item/completed", map[string]any{"threadId": thread, "turnId": p.active[thread], "item": item})
}

func (p *nativePreview) spawn(id, thread, path, role, prompt string) {
	p.assignments[thread] = prompt
	p.notify("thread/started", map[string]any{"thread": map[string]any{"id": thread, "parentThreadId": "main", "agentRole": role, "cwd": p.workspace,
		"source": map[string]any{"subAgent": map[string]any{"thread_spawn": map[string]any{"parent_thread_id": "main", "depth": 1, "agent_path": path, "agent_role": role}}}}})
	p.notify("item/completed", map[string]any{"threadId": "main", "turnId": p.active["main"], "item": map[string]any{"id": id, "type": "subAgentActivity", "agentThreadId": thread, "agentPath": path, "kind": "spawned"}})
	p.receive(thread, "main", id, "NEW_TASK", prompt)
	p.notify("turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "turn-" + thread}})
}

// V2 collaboration emits only a body-free activity item. The prompt becomes
// visible when the router observes the authenticated recipient request.
func (p *nativePreview) collab(id, tool, from, to, prompt string) {
	p.notify("item/completed", map[string]any{"threadId": from, "turnId": p.active[from], "item": map[string]any{"id": id, "type": "subAgentActivity", "agentThreadId": to, "agentPath": p.ui.session.Caller(to), "kind": "interacted"}})
	kind := "MESSAGE"
	if tool == "followupTask" {
		p.assignments[to] = prompt
		kind = "NEW_TASK"
	}
	p.receive(to, from, id, kind, prompt)
	if tool == "followupTask" {
		p.notify("turn/started", map[string]any{"threadId": to, "turn": map[string]any{"id": "turn-" + to + "-" + id}})
	}
}

func (p *nativePreview) receive(to, from, id, kind, body string) {
	path, sender := p.ui.session.Caller(to), p.ui.session.Caller(from)
	envelope := map[string]any{"type": "agent_message", "id": id, "author": sender, "recipient": path,
		"content": []any{map[string]any{"type": "input_text", "text": "Message Type: " + kind + "\nTask name: " + path + "\nSender: " + sender + "\nPayload:\n" + body}}}
	parent := "main"
	if to == "main" {
		parent = ""
	}
	transform, _ := prepareActivityModelTest(p.t, p.proxy, cmp.Or(p.models[to], "gpt-test"), "preview-"+to, to, parent, path, []any{envelope})
	transform.Close()
	p.ui.applyObservedActivity()
}

func (p *nativePreview) say(thread, id, phase, text string) {
	item := map[string]any{"id": id, "type": "agentMessage", "text": text}
	if phase != "" {
		item["phase"] = phase
	}
	p.notify("item/completed", map[string]any{"threadId": thread, "turnId": p.active[thread], "item": item})
}

func (p *nativePreview) think(thread, id, text string) {
	for _, delta := range strings.SplitAfter(text, " ") {
		p.notify("item/reasoning/summaryTextDelta", map[string]any{"threadId": thread, "turnId": p.active[thread], "itemId": id, "delta": delta, "summaryIndex": 0})
	}
}

const nativePreviewGrokModel = "grok:grok-4.7-build-fast"

// Grok streams plaintext reasoning rather than titled summaries.
const nativePreviewGrokReasoning = "The pane blocks on its first frame, so Launch probably waits on a channel only the render loop fills. " +
	"If it receives from p.ready before returning, the caller deadlocks whenever rendering needs the caller's goroutine.\n\n" +
	"The signature must stay the same. I could start the render loop before waiting, or return a pending state and let the first frame arrive asynchronously. " +
	"Returning pending is simpler and never blocks callers.\n\n" +
	"I should read launch.go first to confirm where it waits."

// grokThink plays a Grok Chat Completions reasoning stream through the
// router's own translation, one provider chunk per step, and reports the
// resulting reasoning item the way Codex app-server does.
func (p *nativePreview) grokThink(thread, reasoning string) {
	request := mustTestJSON(p.t, map[string]any{"model": nativePreviewGrokModel, "stream": true,
		"input": []any{map[string]string{"role": "user", "content": "Unblock the pane"}}})
	var chunks []any
	words := strings.SplitAfter(reasoning, " ")
	for len(words) > 0 {
		n := min(3, len(words))
		chunks = append(chunks, map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]string{"reasoning_content": strings.Join(words[:n], "")}}}})
		words = words[n:]
	}
	chunks = append(chunks, map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]string{}, "finish_reason": "stop"}}})
	var events []map[string]any
	if _, err := readGrokTestEvents(p.t, request, strings.NewReader(grokTestSSE(chunks...)), func(event map[string]any) error {
		events = append(events, event)
		return nil
	}); err != nil {
		p.t.Fatal(err)
	}
	for _, event := range events {
		item, _ := event["item"].(map[string]any)
		appServerItem := func() map[string]any {
			var summary []string
			for _, part := range item["summary"].([]any) {
				summary = append(summary, part.(map[string]any)["text"].(string))
			}
			return map[string]any{"id": item["id"], "type": "reasoning", "summary": summary, "content": []string{}}
		}
		switch event["type"] {
		case responseevents.OutputItemAdded:
			if item["type"] == "reasoning" {
				p.add("stream explorer thinking", func() {
					p.notify("item/started", map[string]any{"threadId": thread, "turnId": p.active[thread], "item": appServerItem()})
				})
			}
		case responseevents.ReasoningTextDelta:
			p.add("stream explorer thinking", func() {
				p.notify("item/reasoning/summaryTextDelta", map[string]any{"threadId": thread, "turnId": p.active[thread], "itemId": event["item_id"], "delta": event["delta"], "summaryIndex": event["summary_index"]})
			})
		case responseevents.OutputItemDone:
			if item["type"] == "reasoning" {
				p.add("stream explorer thinking", func() {
					p.notify("item/completed", map[string]any{"threadId": thread, "turnId": p.active[thread], "item": appServerItem()})
				})
			}
		}
	}
}

// usage reports a thread's cumulative tokens to app-server and to the
// router's accounting, which prices them.
func (p *nativePreview) tokens(thread string, input, output uint64) {
	p.usage.Observation(thread, thread, "gpt-6-luna", "").Observe(inference.Usage{InputTokens: input, UncachedInputTokens: input, OutputTokens: output})
	p.notify("thread/tokenUsage/updated", map[string]any{"threadId": thread, "turnId": p.active[thread], "tokenUsage": map[string]any{
		"last": map[string]any{"totalTokens": input/4 + output}, "modelContextWindow": 200000,
		"total": map[string]any{"inputTokens": input, "outputTokens": output, "totalTokens": input + output}}})
}

func (p *nativePreview) finish(thread string) {
	p.notify("turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": p.active[thread], "status": "completed"}})
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
		p.command("t-reviewer", "cmd-4", "sed -n 1,40p internal/broker/broker.go", []map[string]any{{"type": "read", "command": "sed", "name": "broker.go", "path": filepath.Join(p.workspace, "internal/broker/broker.go")}}, 0)
	})
	p.grokThink("t-explorer", nativePreviewGrokReasoning)
	p.add("explorer thought", func() {})
	p.add("explorer reads", func() {
		p.command("t-explorer", "cmd-3", "cat internal/pane/launch.go", []map[string]any{{"type": "read", "command": "cat", "name": "launch.go", "path": filepath.Join(p.workspace, "internal/pane/launch.go")}}, 0)
	})
	p.add("main applies", func() {
		p.apply(mainEdit)
		p.tokens("main", 241_000, 4_800)
	})
	p.add("main builds", func() {
		p.run("main", "cmd-build", `for pkg in broker pane cmd/broker; do echo "compiling ./internal/$pkg"; sleep $DELAY; done; echo "build ok: 3 packages"`)
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
		p.run("t-tester", "cmd-5", `echo "=== RUN   TestConcurrentPublish"; sleep $DELAY
echo "--- PASS: TestConcurrentPublish (0.41s)"
echo "=== RUN   TestUnsubscribeRace"; sleep $DELAY
printf 'WARNING: DATA RACE\nWrite at broker.go:41 by goroutine 9\n'; sleep $DELAY
echo "--- FAIL: TestUnsubscribeRace (0.37s)"; echo FAIL; exit 1`)
	})
	p.add("tester reports the race", func() {
		p.say("t-tester", "answer-tester-first", "final_answer", "The initial race test exposed an Unsubscribe race. A follow-up is needed.")
		p.finish("t-tester")
	})
	p.add("follow-up", func() {
		p.collab("follow-1", "followupTask", "main", "t-tester", "Also cover Unsubscribe racing a publish; the reviewer's note suggests it is the next hole.")
		p.collab("message-to-tester", "sendMessage", "main", "t-tester", "Keep the test focused; I will handle the integration coverage.")
	})
	p.stream("tester follow-up", followEdit)
	p.add("tester applies", func() {
		p.apply(followEdit)
		p.tokens("t-tester", 97_000, 3_400)
	})
	p.add("tester reruns", func() {
		p.run("t-tester", "cmd-6", `for test in TestConcurrentPublish TestUnsubscribeRace; do
  echo "=== RUN   $test"; sleep $DELAY
  echo "--- PASS: $test (0.4s)"
done
printf 'PASS\nok  \texample.com/broker/internal/broker\t1.2s\n'`)
	})
	p.add("explorer answers", func() {
		p.say("t-explorer", "answer-explorer", "final_answer", "Launch now returns `pending` instead of waiting on the first frame. The signature is unchanged.")
		p.finish("t-explorer")
	})
	p.add("reviewer answers", func() {
		p.say("t-reviewer", "answer-reviewer", "final_answer", "The locking is correct. I added `doc.go` with package documentation.")
		p.finish("t-reviewer")
	})
	p.add("tester answers", func() {
		p.say("t-tester", "answer-tester", "final_answer", "Both race tests pass with -race:\n\n- `TestConcurrentPublish`\n- `TestUnsubscribeRace`")
		p.finish("t-tester")
	})
	p.add("final answer", func() {
		p.say("main", "main-answer", "final_answer", "The broker is safe for concurrent publishers.\n\n1. `Publish` copies subscribers under the lock and sends outside it.\n2. Race tests cover publish and unsubscribe.\n3. The pane no longer blocks on its first frame.")
	})
	p.add("done", func() {
		p.tokens("main", 305_000, 7_200)
		p.finish("main")
	})
	// A separate turn demonstrates both question surfaces without changing the
	// broker walkthrough's final-answer assertions.
	p.add("questions turn", func() { p.beginMessage("Decide who receives the release update") })
	p.add("async question", func() {
		p.notify("item/completed", map[string]any{"threadId": "main", "turnId": p.active["main"], "item": map[string]any{
			"id": "preview-async-question", "type": "agentMessage", "text": "Who should receive the release update?",
			"phase": "finalAnswer", "delivery": "async", "questions": []any{map[string]any{
				"title": "Who should receive the release update?", "options": []string{"Customers", "Internal team"},
			}},
		}})
	})
	p.add("sync question", func() {
		params, err := json.Marshal(map[string]any{
			"threadId": "main", "turnId": p.active["main"], "itemId": "preview-sync-question", "isBlocking": false,
			"questions": []any{map[string]any{"id": "scope", "header": "Scope", "question": "Which release scope?", "isOther": true,
				"options": []any{map[string]any{"label": "Narrow", "description": "Only the affected path"}, map[string]any{"label": "Broad", "description": "Every path"}},
			}},
		})
		if err != nil {
			p.t.Fatal(err)
		}
		if err := p.ui.message(appserver.Message{ID: jsontext.Value(`"preview-sync-request"`), Method: "item/tool/requestUserInput", Params: jsontext.Value(params)}); err != nil {
			p.t.Fatal(err)
		}
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
	if err := p.ui.message(appserver.Message{Method: method, Params: jsontext.Value(wire)}); err != nil {
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
				Input       []map[string]any `json:"input"`
				ClientID    string           `json:"clientUserMessageId"`
				ThreadID    string           `json:"threadId"`
				Model       string           `json:"model"`
				Effort      string           `json:"effort"`
				ServiceTier jsontext.Value   `json:"serviceTier"`
			} `json:"params"`
		}
		if err := json.Unmarshal(line, &request); err != nil || len(request.ID) == 0 {
			continue
		}
		if request.Method == "" {
			if string(request.ID) == `"preview-sync-request"` {
				p.notify("serverRequest/resolved", map[string]any{"threadId": "main", "requestId": request.ID})
			}
			continue
		}
		result := map[string]any{}
		turn := ""
		switch request.Method {
		case "thread/start":
			p.step = len(p.steps)
			p.live = nil
			clear(p.active)
			p.clears++
			result["thread"] = map[string]any{"id": fmt.Sprintf("preview-clear-%d", p.clears), "cwd": p.workspace}
			result["model"] = p.ui.model
			result["reasoningEffort"] = p.ui.reasoningEffort
			result["serviceTier"] = p.ui.serviceTier
		case "skills/list":
			result["data"] = []any{map[string]any{"cwd": p.ui.cwd, "skills": []any{
				map[string]any{"name": "code-review", "description": "Review a change for correctness", "path": filepath.Join(p.ui.cwd, "skills/code-review/SKILL.md"), "enabled": true},
				map[string]any{"name": "test-plan", "description": "Plan focused validation", "path": filepath.Join(p.ui.cwd, "skills/test-plan/SKILL.md"), "enabled": true},
			}}}
		case "fuzzyFileSearch":
			files := []any{}
			for _, path := range slices.Sorted(maps.Keys(nativePreviewFiles)) {
				files = append(files, map[string]any{"root": p.ui.cwd, "path": path})
			}
			result["files"] = files
		case "turn/settings/update":
			result["status"] = "applied"
		case "turn/start":
			p.message++
			turn = fmt.Sprintf("preview-turn-%d", p.message)
			result["turn"] = map[string]any{"id": turn}
		case "turn/steer":
			turn = p.active[request.Params.ThreadID]
			result["turnId"] = turn
		case "thread/compact/start":
			p.message++
			turn = fmt.Sprintf("preview-compact-%d", p.message)
		}
		p.reply(request.ID, result)
		switch request.Method {
		case "thread/compact/start":
			thread := request.Params.ThreadID
			p.notify("turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": turn}})
			item := map[string]any{"id": fmt.Sprintf("preview-compaction-%d", p.message), "type": "contextCompaction"}
			p.notify("item/started", map[string]any{"threadId": thread, "turnId": turn, "item": item})
			p.notify("item/completed", map[string]any{"threadId": thread, "turnId": turn, "item": item})
			p.notify("turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": turn, "status": "completed"}})
		case "thread/settings/update":
			model, effort, tier := p.ui.model, p.ui.reasoningEffort, p.ui.serviceTier
			if request.Params.Model != "" {
				model = request.Params.Model
			}
			if request.Params.Effort != "" {
				effort = request.Params.Effort
			}
			if len(request.Params.ServiceTier) > 0 {
				if err := json.Unmarshal(request.Params.ServiceTier, &tier); err != nil {
					p.t.Fatal(err)
				}
			}
			p.notify("thread/settings/updated", map[string]any{"threadId": request.Params.ThreadID, "threadSettings": map[string]any{"model": model, "effort": effort, "serviceTier": tier}})
		case "turn/start":
			p.notify("turn/started", map[string]any{"threadId": request.Params.ThreadID, "turn": map[string]any{"id": turn}})
			p.userMessage(turn, request.Params.ClientID, request.Params.Input)
			var text []string
			for _, input := range request.Params.Input {
				if s, ok := input["text"].(string); ok {
					text = append(text, s)
				}
			}
			p.notify("item/completed", map[string]any{"threadId": request.Params.ThreadID, "turnId": turn, "item": map[string]any{
				"id": fmt.Sprintf("preview-answer-%d", p.message), "type": "agentMessage", "text": "I heard: " + strings.Join(text, ""),
			}})
			p.notify("turn/completed", map[string]any{"threadId": request.Params.ThreadID, "turn": map[string]any{"id": turn, "status": "completed"}})
		case "turn/steer":
			p.userMessage(turn, request.Params.ClientID, request.Params.Input)
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
	if err := p.ui.message(appserver.Message{ID: id, Result: jsontext.Value(wire)}); err != nil {
		p.t.Fatal(err)
	}
}

func (p *nativePreview) userMessage(turn, clientID string, content any) {
	p.notify("item/completed", map[string]any{"threadId": p.ui.Thread, "turnId": turn, "item": map[string]any{
		"id": fmt.Sprintf("preview-user-%d-%d", p.message, len(p.ui.source.Entries)), "type": "userMessage", "clientId": clientID, "content": content,
	}})
}

// beginMessage plays the scripted opening prompt as if it had been submitted.
func (p *nativePreview) beginMessage(body string) {
	p.message++
	turn := fmt.Sprintf("preview-turn-%d", p.message)
	p.notify("turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": turn}})
	p.userMessage(turn, "", []map[string]string{{"type": "text", "text": body}})
}

func TestNativeDiffNavigatorPointerMovesAndBack(t *testing.T) {
	p := newNativePreview(t)
	defer p.close()
	for p.advance() {
	}
	shell, d := terminalTest(p.ui), savedPane(terminalTest(p.ui))
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
		shell.Frame.Rows = nil
		if err := p.ui.testPaint(screen, 160, 48); err != nil {
			t.Fatal(err)
		}
		return screen.String()
	}
	keys("\x02" + "2\t")
	frame := render()
	if strings.Contains(frame, " 1f ") || !strings.Contains(frame, "broker_race_test.go") {
		t.Fatalf("a single-file change does not name its file:\n%s", frame)
	}
	selectedPath := func() string {
		t.Helper()
		source := d.Selection(shell.Layout.Diff.W-1, shell.Layout.Diff.H-1)
		for _, row := range source.Painted {
			if row.Path != "" {
				return row.Path
			}
		}
		t.Fatal("Diff has no painted source attribution")
		return ""
	}
	focused := func() bool {
		_, right := d.Title()
		return strings.Contains(right, "files focus")
	}
	escape := func() {
		t.Helper()
		keys("\x1b")
		ageTerminalEscape(shell)
		if err := p.ui.drainKeys(make(chan byte)); err != nil {
			t.Fatal(err)
		}
	}
	// Find a visible navigator row through the composed terminal frame.
	navigatorRow := func(frame, label string) int {
		t.Helper()
		r := shell.Layout.Diff
		source := d.Selection(shell.Layout.Diff.W-1, shell.Layout.Diff.H-1)
		for row, text := range strings.Split(frame, "\n") {
			if row < r.Y || row >= r.Y+source.Y {
				continue
			}
			if strings.Contains(ansi.Cut(text, r.X, r.X+r.W), label) {
				return row
			}
		}
		t.Fatalf("navigator has no visible %q row:\n%s", label, frame)
		return -1
	}
	// Moving the cursor shows its file while the list keeps focus.
	keys("g")
	for range 40 {
		render()
		if strings.HasSuffix(selectedPath(), "/broker_race_test.go") {
			break
		}
		keys("j")
	}
	render()
	if !strings.HasSuffix(selectedPath(), "/broker_race_test.go") || !focused() {
		t.Fatal("moving to a change did not preview its source while retaining list focus")
	}
	// Enter opens the file; Esc returns to the list.
	keys("\r")
	if focused() || !d.Back() {
		t.Fatal("Enter on a single-file change did not open its file")
	}
	escape()
	render()
	if !focused() || !strings.HasSuffix(selectedPath(), "/broker_race_test.go") {
		t.Fatal("Esc did not return to the list")
	}
	// Pointer activation uses the displayed change row and preserves its source.
	frame = render()
	row := navigatorRow(frame, "broker_race_test.go")
	r := shell.Layout.Diff
	keys(fmt.Sprintf("\x1b[<35;%d;%dM", r.X+4, row+1))
	keys(fmt.Sprintf("\x1b[<0;%d;%dM", r.X+4, row+1))
	render()
	if !focused() || !strings.HasSuffix(selectedPath(), "/broker_race_test.go") || !d.Back() {
		t.Fatal("pointer activation lost the selected change, list focus or back action")
	}
	// Clicking a branch filters its caller; Esc restores the previous filter.
	keys("g")
	frame = render()
	row = navigatorRow(frame, "╮ tester")
	keys(fmt.Sprintf("\x1b[<0;%d;%dM", r.X+4, row+1))
	render()
	_, title := d.Title()
	if !strings.Contains(title, "@tester") || !d.Back() {
		t.Fatal("clicking a branch did not filter its caller")
	}
	escape()
	render()
	_, title = d.Title()
	if strings.Contains(title, "@tester") || !focused() {
		t.Fatal("Esc did not restore the caller filter")
	}
	// The file tree previews files the same way.
	keys("\t")
	render()
	selected := selectedPath()
	for range 40 {
		keys("j")
		render()
		if selectedPath() != selected {
			break
		}
	}
	if selectedPath() == selected || d.EditorPath() != selectedPath() || !focused() {
		t.Fatal("moving through the tree did not show the file under the cursor")
	}
}

// The fake session exercises production event projection, turn association,
// native final answers and the real mouse path.
func TestNativeUIPreviewSessionReplyIdentity(t *testing.T) {
	p := newNativePreview(t)
	defer p.close()
	for p.advance() {
	}
	main := p.ui.testActivity(p.ui.shell.Transcript)
	main.Conversation = true
	main.testAudience()
	feed := main.RenderFeed(100, 100)
	text := ansi.Strip(strings.Join(feed.Lines, "\n"))
	if strings.Contains(text, "Message received:") || strings.Contains(text, "not loaded") || strings.Count(text, "The initial race test exposed") != 1 {
		t.Fatalf("fake session repeated or misformatted reply: %s", text)
	}
	for _, entry := range main.Entries {
		if entry.Source.Kind == "final" && entry.Source.ActivitySeq == 0 {
			t.Fatal("preview child reply has no real Activity target")
		}
		if entry.Source.Native != nil && entry.Source.Native.Item == "main-answer" {
			if entry.Source.Native.Turn != "preview-turn-1" || entry.Source.Native.Question == 0 {
				t.Fatalf("preview used a fabricated turn or omitted ordinary reply link: %+v", entry.Source.Native)
			}
		}
	}
	var assignments int
	for _, entry := range main.Entries {
		if entry.Source.Assignment != nil && entry.Source.Assignment.To == "/root/tester" {
			assignments++
		}
	}
	if assignments != 2 {
		t.Fatalf("fake session did not retain distinct tester assignments: %d", assignments)
	}
}

func TestNativePreviewEditMouseNavigation(t *testing.T) {
	t.Parallel()
	for _, activity := range []bool{false, true} {
		t.Run(fmt.Sprint(activity), func(t *testing.T) {
			p := newNativePreview(t)
			defer p.close()
			p.until("agents apply")
			u := p.ui
			publishFixtureReview(t, u)
			// Captured-edit links return with the transcript after the live batch settles.
			terminalTest(u).Live.Tick(time.Now().Add(previewview.CompletionHold + time.Second))
			if len(terminalTest(u).Live.Order) != 0 {
				t.Fatal("completed live batch did not restore the transcript")
			}
			view, item := u.testActivity(u.shell.Transcript), "patch-main"
			if activity {
				view, item = u.testActivity(u.shell.Agents), "patch-tester"
				view.setDisplayFilter(true, "/root/tester")
			}
			terminalTest(u).DiffOpen = activity
			paint := func() {
				t.Helper()
				if err := u.testPaint(io.Discard, 160, 160); err != nil {
					t.Fatal(err)
				}
			}
			paint()
			var seq uint64
			for _, entry := range view.Entries {
				if entry.Source.Native != nil && entry.Source.Native.Item == item {
					seq = entry.Source.Seq
				}
			}
			if seq == 0 {
				t.Fatal("preview edit missing")
			}
			row := slices.IndexFunc(view.FeedSnippets, func(s activityui.Snippet) bool { return s.Run == seq && s.Block == activityui.EditNavigationSnippet })
			if row < 0 {
				t.Fatalf("preview edit has no click target: %v", view.FeedSnippets)
			}
			rect := terminalTest(u).Layout.Main
			if activity {
				rect = terminalTest(u).Layout.Main
			}
			x, y := rect.X+view.FeedLeft+1, rect.Y+view.FeedTop+row
			if err := terminalTest(u).mouse(fmt.Sprintf("\x1b[<35;%d;%dM", x, y)); err != nil {
				t.Fatal(err)
			}
			paint()
			if !strings.Contains(terminalTest(u).Frame.Rows[y-1], "\x1b[4m") {
				t.Fatal("edit has no hover underline")
			}
			for _, ending := range []string{"M", "m"} {
				if err := terminalTest(u).mouse(fmt.Sprintf("\x1b[<0;%d;%d%s", x, y, ending)); err != nil {
					t.Fatal(err)
				}
			}
			if terminalTest(u).Dialog == nil || terminalTest(u).DiffOpen != activity {
				t.Fatal("preview edit did not open dialog over prior panes")
			}
			paint()
			found := false
			for attempt := range u.review.Projection.Data.Attempts() {
				if attempt.Correlation != item+"\x000" {
					continue
				}
				for _, chunk := range attempt.Chunks {
					if terminalTest(u).Dialog.Pages[terminalTest(u).Dialog.Page].Code == chunk.Review.UnifiedDiffWithPaths(pathdisplay.ForWorkspace(savedPane(terminalTest(u)).Workspace(), chunk.Review.BeforePath), pathdisplay.ForWorkspace(savedPane(terminalTest(u)).Workspace(), chunk.Review.AfterPath)) {
						found = true
					}
				}
			}
			if !found {
				t.Fatal("preview dialog did not show exact captured edit")
			}
			if err := terminalTest(u).key(27); err != nil {
				t.Fatal(err)
			}
			ageTerminalEscape(terminalTest(u))
			if err := terminalTest(u).flushEscape(); err != nil {
				t.Fatal(err)
			}
			if terminalTest(u).Dialog != nil || terminalTest(u).DiffOpen != activity {
				t.Fatal("Escape did not dismiss dialog while preserving panes")
			}

		})
	}
}

func TestNativeUIPreviewQuestions(t *testing.T) {
	p := newNativePreview(t)
	defer p.close()
	p.ui.Text = "Keep my draft"
	p.until("sync question")
	p.ui.openQuestions()
	rows, _ := p.ui.testMainFrame(72, 28, 0)
	t.Logf("question dock:\n%s", ansi.Strip(strings.Join(rows, "\n")))
	questionTestPaint(t, p.ui, 72)
	appServerTestKeys(t, p.ui, "1\r")
	p.serve()
	p.ui.openQuestions()
	questionTestPaint(t, p.ui, 72)
	appServerTestKeys(t, p.ui, "2\r")
	p.serve()
	if p.ui.questionCount() != 0 || p.ui.Text != "Keep my draft" {
		t.Fatal("preview did not resolve both questions and restore its draft")
	}
	for _, width := range []int{24, 40, 72} {
		rows, _ = p.ui.testMainFrame(width, 28, 0)
		if len(rows) > 28 {
			t.Fatalf("width %d: frame has %d rows", width, len(rows))
		}
		for _, row := range rows {
			if ansi.StringWidth(row) > width {
				t.Fatalf("width %d: overflowing row %q", width, row)
			}
		}
	}
	frame := ansi.Strip(strings.Join(rows, "\n"))
	if strings.Count(frame, "Who should receive the release update?") != 1 || strings.Count(frame, "Which release scope?") != 1 || strings.Contains(frame, binding.QuestionReplyStart) {
		t.Fatalf("duplicate or leaked answer records:\n%s", frame)
	}
	t.Logf("answered records:\n%s", frame)
}
