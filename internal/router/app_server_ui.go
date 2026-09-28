package router

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/rivo/uniseg"
	"github.com/yusing/mekugi/internal/appserver"
	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"github.com/yusing/mekugi/internal/ui/diffview"
	terminalui "github.com/yusing/mekugi/internal/ui/terminal"
	"golang.org/x/term"
)

type appServerItem struct {
	ID               string                    `json:"id"`
	ClientID         string                    `json:"clientId"` // userMessage: the submission's clientUserMessageId.
	Type             string                    `json:"type"`
	Text             string                    `json:"text"`
	Summary          []string                  `json:"summary"`
	AgentThreadID    string                    `json:"agentThreadId"`
	AgentPath        string                    `json:"agentPath"`
	Command          string                    `json:"command"`
	ProcessID        string                    `json:"processId"`
	Path             string                    `json:"path"`
	Status           string                    `json:"status"`
	AggregatedOutput *string                   `json:"aggregatedOutput"`
	Query            string                    `json:"query"`
	Action           *appServerWebSearchAction `json:"action"`
	Results          []jsontext.Value          `json:"results"`
	// Content is variant-specific: user input blocks or raw reasoning strings.
	// Decode only userMessage content; reasoning presentation uses Summary.
	Content           jsontext.Value                 `json:"content"`
	Phase             string                         `json:"phase"`
	ExitCode          *int                           `json:"exitCode"`
	CommandActions    []appServerCommandAction       `json:"commandActions"`
	Changes           []appServerFileChange          `json:"changes"`
	Tool              string                         `json:"tool"`
	SenderThreadID    string                         `json:"senderThreadId"`
	ReceiverThreadIDs []string                       `json:"receiverThreadIds"`
	AgentsStates      map[string]appServerAgentState `json:"agentsStates"`
	Prompt            string                         `json:"prompt"`
	Model             string                         `json:"model"`
	ReasoningEffort   string                         `json:"reasoningEffort"`
}

type appServerUI struct {
	picker                    composerPicker
	files                     []composerFile
	skills                    []composerSkill
	client                    *appserver.Client
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
	compacting                *[2]string // Main turn and compaction item.
	polling                   *[2]string // Main turn and polled process.
	alert                     bool       // The status reports a failure or blocked request.
	notice                    string     // Composer feedback; errors persist until the next draft edit.
	noticeAlert               bool
	noticeUntil               time.Time
	turnStarted               time.Time // Shown as elapsed time while a turn runs.
	model, reasoningEffort    string
	serviceTier               string
	models                    []appServerModel
	modelsLoading             bool
	reasoningKey              *bool
	settingsChoices           string
	settingsPending           bool
	settingsChange            map[string]any
	settingsTurn              string
	requests                  map[string]string
	draft                     string
	submission                composerSubmission   // The unresolved turn/start or turn/steer request.
	steers                    []composerSubmission // Accepted steers the turn has not yet committed.
	unsent, queued            []composerDraft      // Stacked steers and next-turn input.
	interrupting              string               // The turn Ctrl-C interrupted.
	resendSteers              bool                 // The interrupt sends pending steers as the next turn.
	composerRect              terminalRect         // Visible draft text, relative to Main.
	cursorBack, composerWidth int
	cursorColumn              *int
	images                    []composerImage
	undoDrafts, redoDrafts    []composerUndo
	inputHistory              []composerDraft
	historyDraft              composerDraft
	historyBack               int
	run                       composerRun // The edit that the next like keystroke extends.
	ownedImages               map[string]bool
	escape                    string
	paste                     bool
	pasted                    []byte // Bracketed paste text, inserted when the paste ends.
	dirty                     bool
	keybindings               bool
	resumeThread              string
	resumeCwd                 string
	resumeConfig              map[string]any
	resumePending             []appserver.Message
	panes                     *nativePanePersistence
	restoring                 *appServerActivityRestore
	starting                  bool
}

// StartAppServerUI starts the native terminal frontend without router observers.
// Codex app-server owns execution; the launcher
// still owns routing, environment, invocation-local configuration and cancellation.
func StartAppServerUI(ctx context.Context, cmd *exec.Cmd, stdin, stdout *os.File, resumeThread string) (func() error, error) {
	return startAppServerUI(ctx, cmd, stdin, stdout, nil, resumeThread)
}

func startAppServerUI(ctx context.Context, cmd *exec.Cmd, stdin, stdout *os.File, proxy *mekugiProxy, resumeThread string) (func() error, error) {
	var resumeCwd string
	if resumeThread == "--last" {
		var err error
		resumeCwd, err = filepath.Abs(cmd.Dir)
		if err != nil {
			return nil, fmt.Errorf("resolve resume workspace: %w", err)
		}
	}
	// Live settings and trusted reasoning configuration_update items are Codex
	// opt-ins. Enable them only for this native app-server invocation.
	cmd.Args = append(cmd.Args, "-c", "features.step_model_switching=true", "-c", "features.reasoning_effort_override=true")
	c, err := appserver.Start(cmd)
	if err != nil {
		return nil, err
	}
	u := &appServerUI{client: c, view: newLiveActivityView(), agents: newLiveActivityView(), proxy: proxy, requests: make(map[string]string), status: "Connecting…", dirty: true, ctx: ctx, resumeThread: resumeThread}
	u.resumeConfig = appServerResumeConfig(cmd.Args)
	u.resumeCwd = resumeCwd
	u.panes = new(nativePanePersistence)
	if err := u.request("initialize", nil); err != nil {
		c.Close()
		<-c.Done
		return nil, err
	}
	u.ensureShell()
	return func() error {
		defer u.cancelPickerScan()
		defer u.shell.diff.close()
		defer u.shell.diffScreen.Close()
		if proxy != nil {
			defer func() { proxy.journals.detachNative(u.journal); proxy.journals.detachNative(u.unscopedJournal) }()
			defer proxy.activity.releasePane()
		}
		defer c.Input.Close()
		defer u.discardDraftImages()
		defer c.Output.Close()
		exited := false
		var err error
		for {
			err = terminalui.WithRawPane(ctx, stdin, stdout, "\x1b[?1049h\x1b[?25l\x1b[?1003;1006;2004h\x1b]10;?\x1b\\\x1b]11;?\x1b\\", "\x1b[?2026l\x1b[?1003;1006;2004l\x1b[0m\x1b[?25h\x1b[?1049l", func(keys <-chan byte) error {
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
						u.shell.liveDock = diffview.PreviewPane{}
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
					case result := <-u.picker.scanResults:
						u.applyPickerScan(result)
					case message, ok := <-c.Messages:
						if !ok {
							exited = true
							return errors.Join(errors.New("app-server disconnected; active work may be incomplete"), <-c.Done)
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
						u.applyObservedActivity()
						u.paneError(u.panes.save(u.shell, time.Now(), false))
						if u.expireNotice(time.Now()) {
							u.dirty = true
						}
						for _, view := range []*liveActivityView{u.view, u.agents} {
							if view.expireFlash(time.Now()) {
								u.dirty = true
							}
						}
						if u.sessionAnimating() || u.shell.activityOpen && u.agents.hasLiveReasoning() {
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
				err = c.Shutdown()
			} else {
				c.Close()
				<-c.Done
			}
		}
		if unsent := joinDrafts(slices.Concat(u.unsent, u.queued, []composerDraft{u.draftSnapshot()})...); unsent.text != "" {
			fmt.Fprintln(stdout, "Unsent draft:\n"+livediff.Safe(unsent.text, false))
		}
		unknown := slices.Clone(u.steers)
		if !u.submission.committed {
			unknown = append(unknown, u.submission)
		}
		if unknown := joinDrafts(u.steerParts(unknown)...); unknown.text != "" {
			fmt.Fprintln(stdout, "Submission outcome unknown; not automatically resent:\n"+livediff.Safe(unknown.text, false))
		}
		if err != nil {
			c.Diagnostics.Lock()
			defer c.Diagnostics.Unlock()
			if len(c.Diagnostics.Text) > 0 {
				fmt.Fprintln(stdout, livediff.Safe(string(c.Diagnostics.Text), false))
			}
		}
		return err
	}, nil
}

func (u *appServerUI) request(method string, params any) error {
	var id string
	var err error
	if method == "initialize" {
		id, err = u.client.Initialize()
	} else {
		id, err = u.client.Send(method, params, true)
	}
	if err == nil {
		u.requests[id] = method
	}
	return err
}

func (u *appServerUI) message(m appserver.Message) (err error) {
	defer u.refreshPicker()
	// Any event can make stacked input sendable: an acknowledgement, a turn
	// start or end, or settled settings.
	defer func() {
		if err == nil {
			err = u.flushInput()
		}
	}()
	if m.Method == "skills/changed" {
		u.picker.skillsLoaded = false
		u.picker.resolved = composerTarget{}
	}
	// Input observations precede the output items that answer them. Drain
	// before a completion, not just on the next paint tick, so links bind once.
	u.applyObservedActivity()
	u.applyPendingJournal()
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
		if u.pickerMessage(method, m) {
			return nil
		}
		if method == "thread/list" && u.resumeThread == "--last" {
			return u.resumeLastResponse(m)
		}
		if handled, err := u.settingsMessage(method, m); handled || err != nil {
			return err
		}
		if method == "thread/read" && u.applyThreadMetadata(m) {
			return nil
		}
		if method == "thread/list" || method == "thread/read" {
			return u.restoreActivityResponse(method, m)
		}
		if method == "turn/start" || method == "turn/steer" {
			u.submissionResponse(method, m.Error)
			return nil
		}
		if m.Error != nil {
			u.status, u.alert = method+": "+m.Error.Message, true
			if method == "initialize" || method == "thread/start" || method == "thread/resume" {
				return errors.New(u.status)
			}
			if method == "turn/interrupt" {
				u.interrupting, u.resendSteers = "", false
			}
			return nil
		}
		switch method {
		case "initialize":
			if _, err := u.client.Send("initialized", map[string]any{}, false); err != nil {
				return err
			}
			if u.resumeThread != "" {
				if u.resumeThread == "--last" {
					u.status = "Finding latest thread…"
					return u.request("thread/list", map[string]any{
						"cwd": u.resumeCwd, "limit": 1, "sortKey": "updated_at", "archived": false,
						"modelProviders": []string{}, "sourceKinds": []string{"cli", "vscode", "appServer"},
					})
				}
				return u.requestResume()
			}
			u.status = "Starting thread…"
			return u.request("thread/start", map[string]any{"approvalPolicy": "never", "sandbox": "danger-full-access"})
		case "thread/start", "thread/resume":
			var result struct {
				Model           string              `json:"model"`
				ReasoningEffort string              `json:"reasoningEffort"`
				ServiceTier     string              `json:"serviceTier"`
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
			u.model, u.reasoningEffort, u.serviceTier = result.Model, result.ReasoningEffort, result.ServiceTier
			u.modelsLoading = true
			if err := u.request("model/list", map[string]any{"includeHidden": true}); err != nil {
				return err
			}
			u.session.start(u.thread, result.Thread.Cwd)
			restoreContextUsage(u.session.agent("/root"), result.Thread)
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
	if handled, err := u.settingsMessage("", m); handled || err != nil {
		return err
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
			if p.Turn.Status == "completed" && !u.turnStarted.IsZero() {
				u.status += " in " + liveActivityAge(time.Since(u.turnStarted))
			}
			if p.Turn.Error != nil {
				u.status, u.alert = u.status+": "+p.Turn.Error.Message, true
			}
			u.settleInput(p.Turn.ID, p.Turn.Status == "interrupted")
		}
	case "item/started", "item/completed", "item/agentMessage/delta":
		if p.ItemID == "" {
			p.ItemID = p.Item.ID
		}
		// Only completion commits, so one message cannot match two identical steers.
		if p.ThreadID == u.thread && p.Item.Type == "userMessage" && m.Method == "item/completed" {
			u.commitSteer(p.Item)
		}
		if p.ThreadID == u.thread && (u.journal != nil && u.journal.hides(p.ItemID) || u.unscopedJournal != nil && u.unscopedJournal.hides(p.ItemID)) {
			return nil
		}
		u.view.applyAppServerItem(u.thread, p.ThreadID, p.TurnID, p.ItemID, m.Method, p.Delta, p.Item)
	}
	return nil
}

func (u *appServerUI) key(key byte) (bool, error) {
	defer u.refreshPicker()
	if !u.paste && u.escape == "" && key != 27 && u.pickerKey(string([]byte{key})) {
		return false, nil
	}
	if u.escape == "\x1b" && (key == 127 || key == 8) && !u.paste {
		u.escape = ""
		u.deleteWord(true)
		return false, nil
	}
	// macOS terminals commonly encode Option+Left/Right as Meta-b/f.
	if u.escape == "\x1b" && (key == 'b' || key == 'f') && !u.paste {
		sequence := u.escape + string(key)
		u.escape = ""
		u.moveDraft(sequence)
		return false, nil
	}
	// A bare Escape dismisses keybindings. Do not eat the next
	// ordinary key while waiting for a CSI sequence that never arrives.
	if u.escape == "\x1b" && key != '[' && key != 'O' {
		u.pickerKey("\x1b")
		u.escape = ""
	}
	if !u.paste && u.escape == "" {
		if key == '?' && u.draft == "" && (u.shell == nil || u.shell.focus == 0) {
			u.keybindings = !u.keybindings
			return false, nil
		}
		u.keybindings = false
	}
	if u.escape != "" || key == 27 {
		u.escape += string(key)
		if u.escape == "\x1b" || u.escape == "\x1b[" || u.escape == "\x1bO" {
			return false, nil
		}
		if key >= 0x40 && key <= 0x7e || len(u.escape) > 32 {
			if !u.paste && u.pickerKey(u.escape) {
				u.escape = ""
				return false, nil
			}
			switch u.escape {
			case "\x1b[1;2A", "\x1b[1;2B":
				sequence := u.escape
				u.escape = ""
				if !u.paste {
					return false, u.stepReasoning(strings.HasSuffix(sequence, "A"))
				}
			case "\x1b[1;3A", "\x1b[1;2D":
				if !u.paste {
					u.editQueued()
				}
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
				u.run = runNone
			case "\x1b[201~":
				u.paste = false
				if u.picker.modal == "manage" {
					u.picker.query += string(u.pasted)
					u.pasted = nil
				} else if u.picker.modal == "menu" {
					u.pasted = nil
				} else {
					u.finishPaste()
				}
				u.picker.dismissed, u.picker.open = u.completionTarget(), false
			case "\x1b[5~":
				if !u.paste {
					u.view.scrollKey('b')
				}
			case "\x1b[6~":
				if !u.paste {
					u.view.scrollKey(' ')
				}
			default:
				// Only caret movement ends an edit run; forward deletes still group.
				if !u.paste {
					u.moveDraft(u.escape)
				}
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
			u.pasted = append(u.pasted, key)
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
		// As in Codex, Ctrl-C first clears the draft (undoable with Ctrl+Z),
		// then interrupts the active turn, and otherwise quits like /quit.
		if u.draft != "" {
			u.deleteDraftRange(0, len(u.draft))
			u.setNotice("Draft cleared · Ctrl+Z restores · Ctrl-C again quits", false)
			if u.turn != "" || u.starting || u.submission.text != "" {
				u.setNotice("Draft cleared · Ctrl+Z restores · Ctrl-C again interrupts", false)
			}
			return false, nil
		}
		if u.turn != "" {
			// As in Codex, interrupting with pending steers sends them now as
			// the next turn instead of after the next tool call.
			u.interrupting = u.turn
			u.resendSteers = len(u.steers) > 0 || len(u.unsent) > 0 || u.submission.turn != ""
			u.status = "Interrupting…"
			if u.resendSteers {
				u.status = "Interrupting to send steer…"
			}
			return false, u.request("turn/interrupt", map[string]any{"threadId": u.thread, "turnId": u.turn})
		}
		if !u.starting && u.submission.text == "" {
			return true, nil
		}
		u.setNotice("Turn is starting · nothing to interrupt yet", false)
	case 127, 8:
		u.deleteDraft(true)
	case '\n':
		u.insertDraft("\n")
	case '\r':
		text := strings.TrimSpace(u.draft)
		if text == "/skills" {
			u.deleteDraftRange(0, len(u.draft))
			u.picker.modal, u.picker.open, u.picker.selected, u.picker.top = "menu", true, 0, 0
			return false, nil
		}
		if text == "/quit" {
			if u.turn == "" && !u.starting && u.submission.text == "" {
				u.draft = ""
				return true, nil
			}
			u.setNotice("Interrupt the active turn before quitting", false)
			return false, nil
		}
		if handled, err := u.settingsCommand(text); handled {
			return false, err
		}
		if strings.HasPrefix(text, "/") {
			u.setNotice("Unknown command "+strings.Fields(text)[0]+" · /skills, /model, /reasoning, /tier, /quit", true)
			return false, nil
		}
		if text == "" || u.thread == "" || u.restoring != nil {
			return false, nil
		}
		// A draft that cannot be sent yet stacks with earlier unsent steers.
		u.unsent = append(u.unsent, u.takeDraft())
		u.pruneDraftImages()
		u.setNotice("", false)
		u.view.follow()
		return false, u.flushInput()
	case '\t':
		// Tab queues busy input for the next turn; otherwise it sends like Enter.
		text := strings.TrimSpace(u.draft)
		if text == "" || strings.HasPrefix(text, "/") || u.turn == "" && !u.starting && u.submission.text == "" {
			return u.key('\r')
		}
		u.queued = append(u.queued, u.takeDraft())
		u.pruneDraftImages()
		u.setNotice("", false)
	default:
		if key >= 32 {
			u.insertDraft(string([]byte{key}))
		}
	}
	return false, nil
}

// applyActivity projects the collector once into the two audiences. Ordinary
// root activity stays in Main, but a directed message belongs at both ends.
func (u *appServerUI) applyActivity(entries []activityPaneEntry, agents []activityPaneAgent) {
	paneEntries := slices.Clone(entries)
	for i, entry := range paneEntries {
		if entry.Kind == "reply" {
			from, to, _, _, ok := activityui.ParseEnvelope(entry.Text)
			if ok {
				paneEntries[i].Agent = from
				if from == "/root" {
					paneEntries[i].Agent = to
				}
			}
		}
	}
	u.agents.apply(activityPaneEvent{Kind: "entries", Entries: paneEntries, Agents: agents})
	for _, entry := range paneEntries {
		if entry.Kind == "final" {
			u.agents.linkChildAnswers(u.agents.entrySeq(entry))
		}
	}
	for i, entry := range entries {
		main := entry.Agent == "/root" && (entry.Kind == "tool" || entry.Kind == "exit" || entry.Kind == "output_filter" || entry.Kind == "reasoning" || entry.Kind == "progress")
		if (entry.Kind == "assignment" || entry.Kind == "start") && entry.assignment != nil {
			main = true
			entry.Agent = entry.assignment.from
			if entry.Agent == "" {
				entry.Agent = "Main"
			}
		}
		if entry.Kind == "reply" {
			from, to, _, _, ok := activityui.ParseEnvelope(entry.Text)
			if ok {
				main = from == "/root" || to == "/root"
				entry.Agent = from
			}
		}
		main = main || (entry.Kind == "final" || entry.Kind == "start") && entry.Agent != "/root"
		if main {
			if entry.Kind == "final" || entry.Kind == "reply" {
				entry.activitySeq = u.agents.entrySeq(paneEntries[i])
			}
			entry.Seq = u.view.lastSeq + 1
			if entry.Agent == "/root" {
				entry.Agent = "Main"
			}
			u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{entry}})
			if entry.Kind == "final" {
				u.view.linkChildAnswers(u.view.entrySeq(entry))
			}
		}
	}
	u.applyCapturedEdits()
}

// mainFrame is the transcript above a boxed composer. The top border carries
// the session state; the bottom border the model. dock rows are left blank
// between the two, in the returned rectangle, for the live edit dock.
func (u *appServerUI) mainFrame(width, height, dock int) ([]string, terminalRect) {
	width, height = max(1, width), max(1, height)
	u.picker.rect = terminalRect{}
	if u.pickerVisible() && u.picker.modal != "" {
		return u.skillsModalFrame(width, height), terminalRect{}
	}
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
	var pending []string
	if u.pickerVisible() {
		dock = 0
	} else {
		// Waiting input yields to the transcript beyond half the room.
		pending = u.pendingInputPreview(width)
		pending = pending[:min(len(pending), room/2)]
		room -= len(pending)
	}
	dock = min(dock, max(0, room-1))
	room -= dock
	u.mainContentPainted = room > 1
	u.view.conversation, u.view.feedOnly, u.view.status = true, true, livediff.Safe(u.status, false)
	u.view.pinMainReply = u.turn != ""
	var frame []string
	u.view.feedRows = 0
	u.view.feedQuestions, u.view.feedSnippets = nil, nil
	if room > 0 {
		frame = u.view.render(width, room, time.Now())
		if u.keybindings && (u.shell == nil || u.shell.focus == 0) {
			u.mainContentPainted = false
			frame = renderNativeKeybindings(width, room)
		}
		if u.pickerVisible() {
			// The publication may occupy precisely the rows hidden by the picker.
			// Defer journal presentation receipts until the overlay closes.
			u.mainContentPainted = false
			popupHeight := min(room, u.pickerHeight(width))
			popup := u.renderPicker(width, popupHeight)
			u.picker.rect = terminalRect{0, len(frame) - popupHeight, width, popupHeight}
			copy(frame[len(frame)-popupHeight:], popup)
		}
	}
	dockAt := len(frame)
	frame = append(frame, make([]string, dock)...)
	frame = append(frame, pending...)
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
	textX := min(2, inset)
	if boxed {
		textX += 2
	}
	u.composerRect = terminalRect{textX, len(frame), textWidth, len(draft)}
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
			frame = append(frame, border+"│"+inputColor+" "+prefix+line+strings.Repeat(" ", textWidth-ansi.StringWidth(line))+border+"│"+activityui.Reset)
		} else {
			frame = append(frame, inputColor+prefix+line+activityui.Reset)
		}
	}
	if boxed {
		model := u.model
		if model != "" && u.reasoningEffort != "" {
			model += " (" + u.reasoningEffort + ")"
		}
		if u.serviceTier != "" {
			model += " · " + u.serviceTier
		}
		model = strings.ReplaceAll(livediff.Safe(model, false), "\n", " ")
		context := contextWindowLabel(activityPaneAgent{})
		if agent := u.session.agent("/root"); agent != nil {
			context = contextWindowLabel(*agent)
		}
		caption := context
		if room := width - 7 - ansi.StringWidth(context) - 3; model != "" && room > 0 {
			caption = ansi.Truncate(model, room, "…") + " • " + context
		}
		caption = activityui.Dim + ansi.Truncate(caption, max(0, width-7), "…") + activityui.Reset
		frame = append(frame, composerBorder("╰", "╯", "", caption, width, border))
	}
	return frame, terminalRect{0, dockAt, width, dock}
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
	return color + open + "─" + l + strings.Repeat("─", fill) + r + "─" + close + activityui.Reset
}

// activeReasoning returns only the current Main item's public summary heading.
func (u *appServerUI) activeReasoning() string {
	if u.turn == "" {
		return ""
	}
	i := u.view.latest("Main")
	if i < 0 {
		return ""
	}
	entry := u.view.entries[i]
	if entry.Kind != "reasoning" || entry.native == nil || entry.native.thread != u.thread || entry.native.turn != u.turn || entry.native.phase == "item/completed" {
		return ""
	}
	return activityui.ReasoningSummaryHeader(livediff.Safe(entry.Text, false))
}

// Successful feedback is transient; actionable errors remain until editing.
func (u *appServerUI) setNotice(text string, alert bool) {
	u.notice, u.noticeAlert = text, alert
	u.noticeUntil = time.Time{}
	if text != "" && !alert {
		u.noticeUntil = time.Now().Add(3 * time.Second)
	}
}

func (u *appServerUI) expireNotice(now time.Time) bool {
	if u.noticeUntil.IsZero() || now.Before(u.noticeUntil) {
		return false
	}
	u.setNotice("", false)
	return true
}

// stateLabel is the session state followed by any composer notice.
func (u *appServerUI) stateLabel(now time.Time) string {
	label := u.sessionLabel(now)
	notice := strings.ReplaceAll(livediff.Safe(u.notice, false), "\n", " ")
	switch {
	case notice == "":
		return label
	case u.noticeAlert:
		notice = activityui.Red + "✗ " + notice + activityui.Reset
	default:
		notice = "\x1b[39m" + notice + activityui.Reset
	}
	if label == "" {
		return notice
	}
	return label + activityui.Dim + " · " + activityui.Undim + notice
}

func (u *appServerUI) sessionAnimating() bool {
	return !u.alert && (u.turn != "" || u.starting || u.submission.text != "" || u.restoring != nil || u.thread == "")
}

func (u *appServerUI) sessionLabel(now time.Time) string {
	status := strings.ReplaceAll(livediff.Safe(u.status, false), "\n", " ")
	switch {
	case status == "":
		return ""
	case u.alert:
		return activityui.Red + "✗ " + status + activityui.Reset
	case u.turn != "":
		label := "\x1b[39m◐ " + activityui.StatusPulse(status, now, u.view.painter.Colors) + activityui.Reset
		if status == "Working" {
			if u.compacting != nil {
				status = "Compacting context"
			} else if u.polling != nil {
				status = "Still running"
			} else if summary := u.activeReasoning(); summary != "" {
				status = summary
			}
			label = "\x1b[39m◐ " + activityui.ReasoningShimmer(status, now.Sub(u.turnStarted), u.view.painter.Colors) + activityui.Reset
		}
		if !u.turnStarted.IsZero() {
			label += activityui.Dim + " " + liveActivityAge(now.Sub(u.turnStarted)) + activityui.Undim
		}
		return label
	case u.starting || u.submission.text != "" || u.restoring != nil || u.thread == "":
		return activityui.StatusPulse(status, now, u.view.painter.Colors) + activityui.Reset
	}
	// Idle states use default text; the border color would otherwise carry over.
	return "\x1b[39m" + status + activityui.Reset
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
		return activityui.Amber + label + activityui.Reset
	case v.offset > 0:
		return activityui.Dim + fmt.Sprintf("▲ %d", v.offset) + activityui.Undim
	}
	return ""
}

func (u *appServerUI) ensureShell() {
	u.view.conversation = true
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

// Router-owned annotations and authenticated directed inputs supplement the
// native event stream without becoming assistant speech or execution events.
func (u *appServerUI) applyObservedActivity() {
	if u.proxy == nil {
		return
	}
	entries := u.proxy.activity.takeNativeActivity(u.thread)
	for i := range entries {
		entries[i].Seq = u.session.next()
	}
	if len(entries) != 0 {
		u.applyActivity(entries, nil)
		u.dirty = true
	}
}
