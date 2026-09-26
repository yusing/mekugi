package router

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/rivo/uniseg"
	"github.com/yusing/mekugi/internal/livediff"
	"golang.org/x/term"
)

type appServerItem struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Text    string `json:"text"`
	Command string `json:"command"`
	Status  string `json:"status"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Phase             string                   `json:"phase"`
	ExitCode          *int                     `json:"exitCode"`
	CommandActions    []appServerCommandAction `json:"commandActions"`
	Changes           []appServerFileChange    `json:"changes"`
	Tool              string                   `json:"tool"`
	SenderThreadID    string                   `json:"senderThreadId"`
	ReceiverThreadIDs []string                 `json:"receiverThreadIds"`
	Prompt            string                   `json:"prompt"`
	Model             string                   `json:"model"`
	ReasoningEffort   string                   `json:"reasoningEffort"`
}

type appServerUI struct {
	client                    *appServerClient
	view                      *liveActivityView
	agents                    *liveActivityView
	proxy                     *mekugiProxy
	journal                   *nativeJournalSink
	unscopedJournal           *nativeJournalSink
	shell                     *terminalUI
	session                   appServerSession
	ctx                       context.Context
	quitRequested             bool
	mainContentPainted        bool
	thread, turn, status      string
	alert                     bool      // The status reports a failure or blocked request.
	turnStarted               time.Time // Shown as elapsed time while a turn runs.
	model, reasoningEffort    string
	requests                  map[string]string
	draft, submitted          string
	cursorBack, composerWidth int
	cursorColumn              *int
	images, submittedImages   []composerImage
	undoDrafts, redoDrafts    []composerDraft
	typing                    bool
	ownedImages               map[string]bool
	submissionSeq             uint64
	escape                    string
	paste                     bool
	dirty                     bool
	resumeThread              string
	resumeConfig              map[string]any
	resumePending             []appServerMessage
	panes                     *nativePanePersistence
	restoring                 *appServerActivityRestore
	starting                  bool
}

// StartAppServerUI is an opt-in feasibility frontend. It shares the activity
// view and state, not a second transcript or the PTY/VT main screen. The launcher
// still owns routing, environment, invocation-local configuration and cancellation.
func StartAppServerUI(ctx context.Context, cmd *exec.Cmd, stdin, stdout *os.File, resumeThread string) (func() error, error) {
	return startAppServerUI(ctx, cmd, stdin, stdout, nil, resumeThread)
}

func startAppServerUI(ctx context.Context, cmd *exec.Cmd, stdin, stdout *os.File, proxy *mekugiProxy, resumeThread string) (func() error, error) {
	c, err := startAppServer(cmd)
	if err != nil {
		return nil, err
	}
	u := &appServerUI{client: c, view: newLiveActivityView(), agents: newLiveActivityView(), proxy: proxy, requests: make(map[string]string), status: "Connecting…", dirty: true, ctx: ctx, resumeThread: resumeThread}
	u.resumeConfig = appServerResumeConfig(cmd.Args)
	u.panes = new(nativePanePersistence)
	if err := u.request("initialize", nil); err != nil {
		c.close()
		<-c.done
		return nil, err
	}
	u.ensureShell()
	return func() error {
		defer u.shell.diff.close()
		defer u.shell.diffScreen.Close()
		if proxy != nil {
			defer func() { proxy.journals.detachNative(u.journal); proxy.journals.detachNative(u.unscopedJournal) }()
			defer proxy.activity.releasePane()
		}
		defer c.input.Close()
		defer u.discardDraftImages()
		defer c.output.Close()
		exited := false
		var err error
		for {
			err = withRawPane(ctx, stdin, stdout, "\x1b[?1049h\x1b[?25l\x1b[?1003;1006;2004h", "\x1b[?2026l\x1b[?1003;1006;2004l\x1b[0m\x1b[?25h\x1b[?1049l", func(keys <-chan byte) error {
				var sub *liveDiffSubscriber
				var diffEvents <-chan liveDiffEvent
				var diffGap, diffReady, autoChanged <-chan struct{}
				if u.shell.auto != nil {
					auto := u.shell.auto
					auto.enable()
					defer auto.enabled.Store(false)
					sub = auto.events.subscribe()
					defer func() { auto.events.unsubscribe(sub) }()
					diffEvents, diffGap, diffReady, autoChanged = sub.events, sub.gap, sub.previewReady, auto.changed
				}
				tick := time.NewTicker(33 * time.Millisecond)
				defer tick.Stop()
				width, height := 0, 0
				agePaint := time.Now()
				for {
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-autoChanged:
						u.shell.auto.mu.Lock()
						u.shell.diff.workspace = u.shell.auto.workspace
						u.shell.auto.mu.Unlock()
						u.dirty = true
					case <-diffGap:
						u.shell.mainDock, u.shell.agentDock = liveDiffPreviewPane{}, liveDiffPreviewPane{}
						sub = u.shell.auto.events.subscribe()
						diffEvents, diffGap, diffReady = sub.events, sub.gap, sub.previewReady
						u.dirty = true
					case <-diffReady:
						for _, event := range u.shell.auto.events.takePreviews(sub) {
							u.shell.applyDiff(ctx, event)
						}
						if err := u.finishRestoredContent(); err != nil {
							return err
						}
						u.dirty = true
					case event := <-diffEvents:
						u.shell.applyDiff(ctx, event)
						if err := u.finishRestoredContent(); err != nil {
							return err
						}
						u.dirty = true
					case <-u.shell.diff.previewFrameC:
						u.shell.diff.previewFrameC = nil
						u.shell.diff.previewFrameDue = time.Time{}
						u.shell.diff.dirty = true
						u.dirty = true
					case <-u.shell.diff.escapeC:
						u.shell.diff.escapeC = nil
						u.shell.diff.escapeKey()
						u.dirty = true
					case message, ok := <-c.messages:
						if !ok {
							exited = true
							return errors.Join(errors.New("app-server disconnected; active work may be incomplete"), <-c.done)
						}
						if err := u.message(message); err != nil {
							return err
						}
						u.dirty = true
					case key, ok := <-keys:
						if !ok {
							return io.EOF
						}
						err := u.shell.key(key)
						if err != nil || u.quitRequested {
							return err
						}
						u.dirty = true
					case <-tick.C:
						u.paneError(u.panes.save(u.shell, time.Now(), false))
						if u.view.expireFlash(time.Now()) {
							u.dirty = true
						}
						if u.shell.activityOpen && u.agents.hasLiveReasoning() {
							u.dirty = true
						}
						if u.shell.activityOpen && time.Since(agePaint) >= time.Second {
							u.dirty = true
							agePaint = time.Now()
						}
						if err := u.shell.flushEscape(); err != nil {
							return err
						}
						journalPending := make(map[*nativeJournalSink][]nativeJournalPublication)
						for _, sink := range []*nativeJournalSink{u.journal, u.unscopedJournal} {
							if sink == nil {
								continue
							}
							items := sink.snapshot()
							journalPending[sink] = items
							for _, publication := range items {
								u.view.applyJournal(journalKey(sink.workspace, sink.thread), publication)
							}
							u.dirty = u.dirty || len(items) > 0
						}
						if u.shell.animating(time.Now()) {
							u.dirty = true
						}
						w, h, err := term.GetSize(int(stdout.Fd()))
						if err != nil {
							return err
						}
						if u.dirty || w != width || h != height {
							width, height = w, h
							if err := u.paint(stdout, w, h); err != nil {
								return err
							}
							for sink, items := range journalPending {
								if !u.mainContentPainted {
									continue
								}
								if err := sink.acknowledge(ctx, proxy, items); err != nil {
									return fmt.Errorf("journal presentation receipt: %w", err)
								}
							}
							u.dirty = false
						}
					}
				}
			})
			if !errors.Is(err, errOpenComposerEditor) {
				break
			}
			u.openComposerEditor(stdin, stdout)
			u.dirty = true
		}
		if saveErr := u.panes.save(u.shell, time.Now(), true); saveErr != nil {
			fmt.Fprintln(stdout, "Pane layout could not be saved:", livediff.Safe(saveErr.Error(), false))
		}
		if !exited {
			if err == nil {
				err = c.shutdown()
			} else {
				c.close()
				<-c.done
			}
		}
		if u.draft != "" {
			fmt.Fprintln(stdout, "Unsent draft:\n"+livediff.Safe(u.draft, false))
		}
		if u.submitted != "" {
			fmt.Fprintln(stdout, "Submission outcome unknown; not automatically resent:\n"+livediff.Safe(u.submitted, false))
		}
		if err != nil {
			c.diagnostics.Lock()
			defer c.diagnostics.Unlock()
			if len(c.diagnostics.text) > 0 {
				fmt.Fprintln(stdout, livediff.Safe(string(c.diagnostics.text), false))
			}
		}
		return err
	}, nil
}

func (u *appServerUI) request(method string, params any) error {
	var id string
	var err error
	if method == "initialize" {
		id, err = u.client.initialize()
	} else {
		id, err = u.client.send(method, params, true)
	}
	if err == nil {
		u.requests[id] = method
	}
	return err
}

func (u *appServerUI) message(m appServerMessage) error {
	if u.resumeThread != "" && (u.thread == "" || u.restoring != nil) && m.Method != "" {
		if len(u.resumePending) == 256 {
			return errors.New("resume event capacity exceeded; session state is incomplete")
		}
		u.resumePending = append(u.resumePending, m)
		return nil
	}
	if m.Method == "" {
		method := u.requests[string(m.ID)]
		delete(u.requests, string(m.ID))
		if method == "thread/list" || method == "thread/read" {
			return u.restoreActivityResponse(method, m)
		}
		if m.Error != nil {
			u.status, u.alert = method+": "+m.Error.Message, true
			if method == "initialize" || method == "thread/start" || method == "thread/resume" {
				return errors.New(u.status)
			}
			if method == "turn/start" || method == "turn/steer" {
				u.starting = false
				u.view.removePendingInput(u.submissionSeq)
				u.restoreSubmission()
				u.submitted = ""
				u.submissionSeq = 0
			}
			return nil
		}
		switch method {
		case "initialize":
			if _, err := u.client.send("initialized", map[string]any{}, false); err != nil {
				return err
			}
			if u.resumeThread != "" {
				u.status = "Resuming thread…"
				return u.request("thread/resume", map[string]any{"threadId": u.resumeThread, "approvalPolicy": "never", "sandbox": "danger-full-access", "config": u.resumeConfig, "modelProvider": u.resumeConfig["model_provider"]})
			}
			u.status = "Starting thread…"
			return u.request("thread/start", map[string]any{"approvalPolicy": "never", "sandbox": "danger-full-access"})
		case "thread/start", "thread/resume":
			var result struct {
				Model           string              `json:"model"`
				ReasoningEffort string              `json:"reasoningEffort"`
				Thread          appServerThreadInfo `json:"thread"`
			}
			if err := json.Unmarshal(m.Result, &result); err != nil {
				return err
			}
			if result.Thread.ID == "" {
				return fmt.Errorf("%s returned no thread identity", method)
			}
			if method == "thread/resume" && result.Thread.ID != u.resumeThread {
				return errors.New("thread/resume returned a different thread identity")
			}
			u.thread, u.status = result.Thread.ID, "Ready"
			u.model, u.reasoningEffort = result.Model, result.ReasoningEffort
			u.session.start(u.thread, result.Thread.Cwd)
			if u.agents != nil {
				u.agents.apply(activityPaneEvent{Kind: "agents", Agents: slices.Clone(u.session.agents)})
			}
			if u.proxy != nil {
				if result.Thread.Cwd == "" {
					return fmt.Errorf("%s returned no workspace for native journal delivery", method)
				}
				u.journal = u.proxy.journals.attachNative(result.Thread.Cwd, u.thread)
				// Real app-server requests can omit workspace metadata. Keep that
				// journal namespace distinct; never infer filesystem authority from cwd.
				u.unscopedJournal = u.proxy.journals.attachNative("", u.thread)
				u.proxy.activity.attachNativePane(u.thread)
			}
			if method == "thread/resume" {
				u.restoreHistory(result.Thread.Turns)
			}
			if u.panes != nil {
				u.ensureShell()
				u.paneError(u.panes.open(u.shell, result.Thread.Cwd, u.thread, method == "thread/resume"))
			}
			if method == "thread/resume" {
				return u.restorePaneContent(result.Thread)
			}
		case "turn/start", "turn/steer":
			u.submittedImages = nil
			u.submitted = ""
			u.submissionSeq = 0
			// turn/started may precede or follow the response. Do not clear the
			// active turn here or allow another start during that race.
		}
		return nil
	}
	if len(m.ID) != 0 {
		// Never auto-approve an unexpected server request. Leave it pending,
		// visibly blocked, until interrupted. Questions get their own UI next.
		u.status, u.alert = "Blocked on unsupported request "+m.Method+" · Ctrl-C interrupts", true
		u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: u.view.lastSeq + 1, Agent: "Session", Kind: "text", Text: u.status, Observed: time.Now()}}})
		return nil
	}
	if handled, err := u.sessionEvent(m); handled || err != nil {
		return err
	}
	switch m.Method {
	case "turn/started", "turn/completed", "item/started", "item/completed", "item/agentMessage/delta":
	default:
		return nil
	}
	var p struct {
		ThreadID string        `json:"threadId"`
		TurnID   string        `json:"turnId"`
		ItemID   string        `json:"itemId"`
		Delta    string        `json:"delta"`
		Item     appServerItem `json:"item"`
		Turn     struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(m.Params, &p); err != nil {
		return fmt.Errorf("%s: %w", m.Method, err)
	}
	switch m.Method {
	case "turn/started":
		if p.ThreadID == u.thread {
			u.turn, u.status, u.starting, u.alert, u.turnStarted = p.Turn.ID, "Working", false, false, time.Now()
		}
	case "turn/completed":
		if p.ThreadID == u.thread && p.Turn.ID == u.turn {
			u.turn, u.starting = "", false
			u.status, u.alert = strings.ToUpper(p.Turn.Status[:min(1, len(p.Turn.Status))])+p.Turn.Status[min(1, len(p.Turn.Status)):], p.Turn.Status == "failed"
			if p.Turn.Error != nil {
				u.status, u.alert = u.status+": "+p.Turn.Error.Message, true
			}
		}
	case "item/started", "item/completed", "item/agentMessage/delta":
		if p.ItemID == "" {
			p.ItemID = p.Item.ID
		}
		if p.ThreadID == u.thread && (u.journal != nil && u.journal.hides(p.ItemID) || u.unscopedJournal != nil && u.unscopedJournal.hides(p.ItemID)) {
			return nil
		}
		u.view.applyAppServerItem(u.thread, p.ThreadID, p.TurnID, p.ItemID, m.Method, p.Delta, p.Item)
	}
	return nil
}

func (u *appServerUI) key(key byte) (bool, error) {
	if u.escape == "\x1b" && (key == 127 || key == 8) && !u.paste {
		u.escape = ""
		u.deleteWord(true)
		return false, nil
	}
	// A bare Escape dismisses nothing in this preview. Do not eat the next
	// ordinary key while waiting for a CSI sequence that never arrives.
	if u.escape == "\x1b" && key != '[' && key != 'O' {
		u.escape = ""
	}
	if u.escape != "" || key == 27 {
		u.escape += string(key)
		if u.escape == "\x1b" || u.escape == "\x1b[" || u.escape == "\x1bO" {
			return false, nil
		}
		if key >= 0x40 && key <= 0x7e || len(u.escape) > 32 {
			switch u.escape {
			case "\x1b[122;6u":
				if !u.paste {
					u.undoDraft(true)
				}
			case "\x1b[3;3~":
				if !u.paste {
					u.deleteWord(false)
				}
			case "\x1b[127;3u", "\x1b[8;3u":
				if !u.paste {
					u.deleteWord(true)
				}
			case "\x1b[3~":
				if !u.paste {
					u.deleteDraft(false)
				}
			case "\x1b[200~":
				u.paste = true
				u.typing = false
			case "\x1b[201~":
				u.paste = false
			case "\x1b[5~":
				if !u.paste {
					u.view.scrollKey('b')
				}
			case "\x1b[6~":
				if !u.paste {
					u.view.scrollKey(' ')
				}
			}
			if !u.paste {
				u.moveDraft(u.escape)
			}
			u.escape = ""
		}
		return false, nil
	}
	if u.paste {
		if key == '\r' {
			key = '\n'
		}
		if key >= 32 || key == '\n' || key == '\t' {
			u.insertDraft(string([]byte{key}))
		}
		return false, nil
	}
	switch key {
	case 26:
		u.undoDraft(false)
	case 25:
		u.undoDraft(true)
	case 7:
		return false, errOpenComposerEditor
	case 22:
		u.pasteImage()
	case 3:
		if u.turn != "" {
			u.status = "Interrupting…"
			return false, u.request("turn/interrupt", map[string]any{"threadId": u.thread, "turnId": u.turn})
		}
		u.status, u.alert = "Nothing to interrupt · /quit exits", false
	case 127, 8:
		u.deleteDraft(true)
	case '\n':
		u.insertDraft("\n")
	case '\r':
		text := strings.TrimSpace(u.draft)
		if text == "/quit" {
			if u.turn == "" && !u.starting && u.submitted == "" {
				u.draft = ""
				return true, nil
			}
			u.status = "Interrupt the active turn before quitting"
			return false, nil
		}
		if strings.HasPrefix(text, "/") {
			u.status, u.alert = "Unknown command "+strings.Fields(text)[0]+" · only /quit is available", true
			return false, nil
		}
		if text == "" || u.thread == "" || u.restoring != nil || u.starting || u.submitted != "" {
			return false, nil
		}
		method := "turn/start"
		params := map[string]any{"threadId": u.thread, "input": u.composerInput()}
		if u.turn != "" {
			method = "turn/steer"
			params["expectedTurnId"] = u.turn
		} else {
			u.starting = true
		}
		u.submitted, u.status, u.alert = u.draft, "Sending…", false
		u.view.follow()
		u.submissionSeq = u.view.lastSeq + 1
		u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: u.submissionSeq, Agent: "You", Kind: "text", Text: u.draft, Observed: time.Now(),
			native: &liveActivityNativeItem{thread: u.thread, item: fmt.Sprintf("input/%d", u.submissionSeq), phase: "input/pending"}}}})
		for i := range u.images {
			delete(u.ownedImages, u.images[i].path)
		}
		u.submittedImages, u.images = u.images, nil
		u.undoDrafts, u.redoDrafts = nil, nil
		u.typing = false
		u.pruneDraftImages()
		u.draft = ""
		u.cursorBack = 0
		err := u.request(method, params)
		if err != nil {
			u.view.removePendingInput(u.submissionSeq)
			u.restoreSubmission()
			u.submitted = ""
			u.submissionSeq = 0
			u.starting = false
		}
		return false, err
	default:
		if key >= 32 {
			u.insertDraft(string([]byte{key}))
		}
	}
	return false, nil
}

func (u *appServerUI) restoreSubmission() {
	u.undoDrafts, u.redoDrafts = nil, nil
	u.typing = false
	u.cursorBack = 0
	if u.submitted == "" {
		return
	}
	shift := len(u.submitted) + 1
	if u.draft == "" {
		shift = len(u.submitted)
	}
	for i := range u.images {
		u.images[i].start += shift
		u.images[i].end += shift
	}
	u.images = append(u.submittedImages, u.images...)
	u.submittedImages = nil
	if u.draft == "" {
		u.draft = u.submitted
	} else {
		u.draft = u.submitted + "\n" + u.draft
	}
	u.renumberImages()
}

// applyActivity projects the collector once into the two audiences. Ordinary
// root activity stays in Main, but a directed message belongs at both ends.
func (u *appServerUI) applyActivity(entries []activityPaneEntry, agents []activityPaneAgent) {
	paneEntries := slices.Clone(entries)
	for i, entry := range entries {
		main := entry.Agent == "/root" && (entry.Kind == "tool" || entry.Kind == "exit" || entry.Kind == "output_filter")
		if (entry.Kind == "assignment" || entry.Kind == "start") && entry.assignment != nil {
			main = true
			entry.Agent = entry.assignment.from
			if entry.Agent == "" {
				entry.Agent = "Main"
			}
		}
		if entry.Kind == "reply" {
			from, to, _, _, ok := parseLiveActivityEnvelope(entry.Text)
			if ok {
				main = from == "/root" || to == "/root"
				if from == "/root" && to != "/root" {
					paneEntries[i].Agent = to
					entry.Agent = "/root"
				}
			}
		}
		main = main || (entry.Kind == "final" || entry.Kind == "start") && entry.Agent != "/root"
		if main {
			entry.Seq = u.view.lastSeq + 1
			if entry.Agent == "/root" {
				entry.Agent = "Main"
			}
			u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{entry}})
			if entry.Kind == "final" {
				u.view.linkChildAnswers(entry.Seq)
			}
		}
	}
	u.agents.apply(activityPaneEvent{Kind: "entries", Entries: paneEntries, Agents: agents})
}

// mainFrame is the transcript above a boxed composer. The top border carries
// the session state; the bottom border the model. dock rows are left blank
// between the two, starting at the returned row, for the live edit dock.
func (u *appServerUI) mainFrame(width, height, dock int) ([]string, int) {
	width, height = max(1, width), max(1, height)
	boxed := width >= 12 && height >= 3
	borderRows, inset := 0, min(2, width-1)
	if boxed {
		borderRows, inset = 2, 5
	}
	textWidth := width - inset
	u.composerWidth = textWidth
	draft, points := u.draftLayout()
	caret := points[len(points)-1]
	for _, point := range points {
		if point.offset == u.cursor() {
			caret = point
			break
		}
	}
	visible := min(6, height-borderRows)
	firstRow := max(0, caret.row-visible+1)
	draft = draft[firstRow:min(len(draft), firstRow+visible)]

	room := max(0, height-len(draft)-borderRows)
	dock = min(dock, max(0, room-1))
	room -= dock
	u.mainContentPainted = room > 1
	u.view.conversation, u.view.feedOnly, u.view.status = true, true, livediff.Safe(u.status, false)
	var frame []string
	if room > 0 {
		frame = u.view.render(width, room, time.Now())
	}
	dockAt := len(frame)
	frame = append(frame, make([]string, dock)...)
	// Visual reference: Grok CLI PromptStyle / PromptWidget::draw, source
	// crates/codegen/xai-grok-pager/src/views/prompt_widget/mod.rs:217:240,3007:3078
	// @be7ce6e8cffe46d20bef9834b211616082ee866b. Keep continuation rows aligned.
	// Colors: xai-grok-pager-render/src/theme/oscura.rs, same revision.
	const border = "\x1b[38;2;52;48;72m"
	const inputColor = "\x1b[39m"
	focused := u.shell == nil || u.shell.focus == 0
	if boxed {
		frame = append(frame, composerBorder("╭", "╮", u.stateLabel(time.Now()), "", width, border))
	}
	for i, line := range draft {
		prefix := strings.Repeat(" ", min(2, inset))
		if i == 0 && inset >= 2 {
			prefix = liveActivityPrompt + "❯ " + inputColor
		}
		line = ansi.Truncate(line, textWidth, "")
		if focused && textWidth > 1 && i+firstRow == caret.row {
			before := ansi.Cut(line, 0, caret.column)
			cluster, _, _, _ := uniseg.FirstGraphemeClusterInString(u.draft[u.cursor():], -1)
			cellWidth := max(1, ansi.StringWidth(livediff.Safe(cluster, false)))
			cell := ansi.Cut(line, caret.column, caret.column+cellWidth)
			if ansi.StringWidth(cell) == 0 {
				cell = " "
			}
			rest := ansi.Cut(line, caret.column+cellWidth, textWidth)
			line = before + "\x1b[7m" + cell + "\x1b[27m" + rest
		}
		if boxed {
			frame = append(frame, border+"│"+inputColor+" "+prefix+line+strings.Repeat(" ", textWidth-ansi.StringWidth(line))+border+"│"+liveActivityReset)
		} else {
			frame = append(frame, inputColor+prefix+line+liveActivityReset)
		}
	}
	if boxed {
		model := u.model
		if model != "" && u.reasoningEffort != "" {
			model += " (" + u.reasoningEffort + ")"
		}
		model = strings.ReplaceAll(livediff.Safe(model, false), "\n", " ")
		if model != "" {
			model = "\x1b[38;2;115;115;116m" + model + liveActivityReset
		}
		frame = append(frame, composerBorder("╰", "╯", "", model, width, border))
	}
	return frame, dockAt
}

// composerBorder embeds optional left and right labels in a box edge. The
// right label yields first when the edge is too narrow for both.
func composerBorder(open, close, left, right string, width int, color string) string {
	segment := func(label string) string {
		if label == "" {
			return ""
		}
		return " " + label + color + " "
	}
	inner := width - 2
	l, r := segment(left), segment(right)
	if inner-ansi.StringWidth(l)-ansi.StringWidth(r)-2 < 1 {
		r = ""
	}
	if room := inner - ansi.StringWidth(r) - 2; ansi.StringWidth(l) > room {
		l = ""
		if room > 4 {
			l = ansi.Truncate(segment(left), room-1, "…") + color + " "
		}
	}
	fill := max(0, inner-ansi.StringWidth(l)-ansi.StringWidth(r)-2)
	return color + open + "─" + l + strings.Repeat("─", fill) + r + "─" + close + liveActivityReset
}

func (u *appServerUI) stateLabel(now time.Time) string {
	status := strings.ReplaceAll(livediff.Safe(u.status, false), "\n", " ")
	switch {
	case status == "":
		return ""
	case u.alert:
		return liveActivityRed + "✗ " + status + liveActivityReset
	case u.turn != "":
		label := liveActivityAmber + "◐ " + status + liveActivityReset
		if !u.turnStarted.IsZero() {
			label += liveActivityDim + " " + liveActivityAge(now.Sub(u.turnStarted)) + liveActivityUndim
		}
		return label
	case u.starting || u.submitted != "" || u.thread == "":
		return liveActivityAmber + status + liveActivityReset
	}
	return liveActivityDim + status + liveActivityUndim
}

// scrollLabel shows how much of the feed lies above the viewport and, once
// the reader scrolls back, how much lies below and what arrived since.
func scrollLabel(v *liveActivityView) string {
	below := v.feedLines - v.offset - v.feedRows
	switch {
	case !v.following && below > 0:
		label := fmt.Sprintf("▼ %d", below)
		if v.unseen > 0 {
			label += fmt.Sprintf(" · %d new", v.unseen)
		}
		return liveActivityAmber + label + liveActivityReset
	case v.offset > 0:
		return liveActivityDim + fmt.Sprintf("▲ %d", v.offset) + liveActivityUndim
	}
	return ""
}

func (u *appServerUI) ensureShell() {
	if u.shell != nil {
		return
	}
	if u.agents == nil {
		u.agents = newLiveActivityView()
	}
	u.agents.childrenOnly = true
	u.agents.status = "" // Fed by app-server directly, never by a collector connection.
	u.agents.mainView = u.view
	var store *mekugiReplayStore
	var auto *autoLiveDiff
	if u.proxy != nil {
		store = u.proxy.replayStore
		auto = u.proxy.autoLiveDiff
	}
	u.agents.bare = true
	shell := &terminalUI{main: u, agents: u.agents, side: true, activityOpen: true, auto: auto, diffScreen: vt.NewEmulator(1, 3)}
	shell.diff = newLiveDiffTerminalController(store, "", os.Stdout)
	shell.diff.native, shell.diff.diffMode = true, true
	shell.diff.stdout = shell.diffScreen
	shell.diff.size = func() (int, int, error) { return max(1, shell.layout.diff.w), max(3, shell.layout.diff.h), nil }
	u.shell = shell
}

func (u *appServerUI) paint(out io.Writer, width, height int) error {
	u.ensureShell()
	u.agents.mainView = u.view
	u.view.status = livediff.Safe(u.status, false)
	u.shell.width, u.shell.height = max(1, width), max(1, height)
	u.mainContentPainted = false
	ctx := u.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return u.shell.paintNative(ctx, out)
}
