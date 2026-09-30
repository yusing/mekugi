package router

import (
	"cmp"
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
	DurationMS       *int64                    `json:"durationMs"`
	Delivery         string                    `json:"delivery"`
	Questions        []nativeQuestion          `json:"questions"`
	ID               string                    `json:"id"`
	ClientID         string                    `json:"clientId"` // userMessage: the submission's clientUserMessageId.
	Type             string                    `json:"type"`
	Text             string                    `json:"text"`
	Summary          []string                  `json:"summary"`
	AgentThreadID    string                    `json:"agentThreadId"`
	AgentPath        string                    `json:"agentPath"`
	Command          string                    `json:"command"`
	Cwd              string                    `json:"cwd"` // commandExecution: the directory the host ran it in.
	Source           string                    `json:"source"`
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
	waitNames         map[string]string              // Presentation-only names for retained wait targets.
}

type appServerUI struct {
	backendVersion            string
	reset                     *journalResetDriver
	btw                       *appServerBTW
	btwRequests               map[string]btwRequest
	btwThreads                map[string]*appServerBTW
	notifications             *nativeNotifications
	questions                 nativeQuestionDock
	statusPanel               *appServerStatusReport
	resumePicker              *appServerResumePicker
	switching                 string // Saved thread a clearing request resumes.
	statusConfig              appServerStatusConfig
	statusReports             map[string]*appServerStatusReport
	picker                    composerPicker
	commitReads               chan gitCommitKey // Commit objects read off the UI goroutine.
	files                     []composerFile
	selections                []composerSelection
	skills                    []composerSkill
	client                    *appserver.Client
	view                      *liveActivityView
	agents                    *liveActivityView
	proxy                     *mekugiProxy
	execTrack                 *execTrackHub // Per-segment command reports; nil when not tracking.
	issues                    *CriticalErrors
	noticeEntries             map[string]bool
	journalView               nativeJournalView
	journal                   *nativeJournalSink
	unscopedJournal           *nativeJournalSink
	shell                     *terminalUI
	session                   appServerSession
	ctx                       context.Context
	quitRequested             bool
	mainContentPainted        bool
	thread, turn, status      string
	exitUsage                 appServerTokenUsage
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
	shellPending              composerDraft        // Awaiting thread/shellCommand acknowledgement, not completion.
	shellStandalone           bool                 // User shell turn; ordinary input waits until it completes.
	shellOrigin               *string              // Submitted turn, until Codex identifies the shell execution.
	submission                composerSubmission   // The unresolved turn/start or turn/steer request.
	steers                    []composerSubmission // Accepted steers the turn has not yet committed.
	unsent, queued            []composerDraft      // Stacked steers and next-turn input.
	interrupting              string               // The interrupted turn.
	pendingStart              composerSubmission   // Start awaiting a committed user message.
	interruptBeforeStart      bool
	compactRequest            bool // Compact RPC acknowledgement still pending.
	clearing                  bool // Fresh-thread request; keep old presentation until success.
	manualCompact             bool // Compact turn has not completed yet.
	retiredThreads            map[string]string
	composerRect              terminalRect // Visible draft text, relative to Main.
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
	waitRelease               func()
	restoring                 *appServerActivityRestore
	starting                  bool
}

// StartAppServerUI starts the native terminal frontend without router observers.
// Codex app-server owns execution; the launcher
// still owns routing, environment, invocation-local configuration and cancellation.
func StartAppServerUI(ctx context.Context, cmd *exec.Cmd, stdin, stdout *os.File, resumeThread string) (func() error, error) {
	faint, _ := terminalui.SupportsFaint(ctx, "auto")
	return startAppServerUI(ctx, cmd, stdin, stdout, nil, nil, resumeThread, faint)
}

func startAppServerUI(ctx context.Context, cmd *exec.Cmd, stdin, stdout *os.File, proxy *mekugiProxy, issues *CriticalErrors, resumeThread string, faint bool) (func() error, error) {
	var resumeCwd string
	if resumeThread == "--last" || resumeThread == resumePickerStartup {
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
	u := &appServerUI{client: c, view: newLiveActivityView(), agents: newLiveActivityView(), proxy: proxy, issues: issues, requests: make(map[string]string), status: "Connecting…", dirty: true, ctx: ctx, resumeThread: resumeThread}
	if proxy != nil {
		u.execTrack = proxy.execTrack
	}
	u.resumeConfig = appServerResumeConfig(cmd.Args)
	u.notifications = &nativeNotifications{out: stdout, focused: true}
	u.resumeCwd = resumeCwd
	u.panes = new(nativePanePersistence)
	if err := u.request("initialize", nil); err != nil {
		c.Close()
		<-c.Done
		return nil, err
	}
	u.ensureShell()
	u.shell.faint = faint
	return func() error {
		defer func() {
			if u.waitRelease != nil {
				u.waitRelease()
			}
		}()
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
			err = terminalui.WithRawPane(ctx, stdin, stdout, "\x1b[?1049h\x1b[?25l\x1b[?1003;1004;1006;2004h\x1b]10;?\x1b\\\x1b]11;?\x1b\\", "\x1b[?2026l\x1b[?1003;1004;1006;2004l\x1b[0m\x1b[?25h\x1b[?1049l", func(keys <-chan byte) error {
				defer u.clearTerminalTitle()
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
					case key := <-u.commitReads:
						if u.commitRead(key) {
							u.dirty = true
						}
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
						if err := u.tickJournalReset(time.Now()); err != nil {
							u.setNotice(err.Error(), true)
						}
						u.applyObservedActivity()
						if err := u.fillResumePicker(); err != nil {
							return err
						}
						u.flushCommandOutput()
						u.startCommitReads()
						u.paneError(u.panes.save(u.shell, time.Now(), false))
						if u.expireNotice(time.Now()) {
							u.dirty = true
						}
						now := time.Now()
						if settleActivity(now, u.view, u.agents) {
							u.dirty = true
						}
						for _, view := range []*liveActivityView{u.view, u.agents} {
							if view.expireFlash(now) {
								u.dirty = true
							}
							if view.pace(now) {
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
						notices := u.applyCriticalNotices()
						journalPending := make(map[*nativeJournalSink][]nativeJournalPublication)
						for _, sink := range []*nativeJournalSink{u.journal, u.unscopedJournal} {
							if sink == nil {
								continue
							}
							items := sink.snapshot()
							journalPending[sink] = items
							for _, publication := range items {
								u.applyJournalPublication(sink, publication)
							}
							u.dirty = u.dirty || len(items) > 0
						}
						if u.shell.animating(time.Now()) || u.agents.rosterEasing {
							u.dirty = true
						}
						w, h, err := term.GetSize(int(stdout.Fd()))
						if err != nil {
							notices.finish(false)
							return err
						}
						if u.dirty || w != width || h != height {
							width, height = w, h
							if err := u.paint(stdout, w, h); err != nil {
								notices.finish(false)
								return err
							}
							notices.finish(u.mainContentPainted)
							for sink, items := range journalPending {
								if !u.mainContentPainted {
									if !u.journalPanePresents(sink) {
										continue
									}
									items = slices.DeleteFunc(slices.Clone(items), func(p nativeJournalPublication) bool { return p.card != nil || p.event == nil })
								}
								if err := sink.acknowledge(ctx, proxy, items); err != nil {
									return fmt.Errorf("journal presentation receipt: %w", err)
								}
							}
							u.dirty = false
						}
					}
					u.writeTerminalTitle(time.Now())
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
		u.hideQuestions()
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
		if u.shellPending.text != "" {
			fmt.Fprintln(stdout, "Shell submission outcome unknown; not automatically rerun:\n"+livediff.Safe(u.shellPending.text, false))
		}
		if err != nil {
			c.Diagnostics.Lock()
			defer c.Diagnostics.Unlock()
			if len(c.Diagnostics.Text) > 0 {
				fmt.Fprintln(stdout, livediff.Safe(string(c.Diagnostics.Text), false))
			}
		}
		color := term.IsTerminal(int(stdout.Fd())) && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
		return errors.Join(err, u.writeExitSummary(stdout, color))
	}, nil
}

func (u *appServerUI) request(method string, params any) error {
	_, err := u.requestAs(method, method, params)
	return err
}

// requestAs correlates the response by label, for owners that share a method.
func (u *appServerUI) requestAs(method, label string, params any) (string, error) {
	var id string
	var err error
	if method == "initialize" {
		id, err = u.client.Initialize()
	} else {
		id, err = u.client.Send(method, params, true)
	}
	if err == nil {
		u.requests[id] = label
	}
	return id, err
}

func (u *appServerUI) message(m appserver.Message) (err error) {
	defer u.refreshPicker()
	u.ensureJournalReset()
	if u.reset != nil {
		handled, resetErr := u.reset.message(m)
		if resetErr != nil {
			u.setNotice("Journal slice: "+resetErr.Error(), true)
		}
		u.showResetNotice()
		if handled {
			return u.flushInput()
		}
	}
	// Side-thread traffic never reaches Main's lifecycle, questions or roster.
	if handled, err := u.btwMessage(m); handled {
		return err
	}
	if u.retiredSessionEvent(m) {
		return nil
	}
	// Any event can make stacked input sendable: an acknowledgement, a turn
	// start or end, or settled settings.
	defer func() {
		if err == nil {
			err = u.flushInput()
		}
		if err == nil {
			err = u.flushBTW()
		}
	}()
	if m.Method == "skills/changed" {
		u.picker.skillsLoaded = false
		u.picker.skillsProblem = ""
		u.picker.resolved = composerTarget{}
	}
	// Input observations precede the output items that answer them. Drain
	// before a completion, not just on the next paint tick, so links bind once.
	u.applyObservedActivity()
	u.applyPendingJournal()
	resuming := u.resumeThread != "" && u.resumeThread != resumePickerStartup && (u.thread == "" || u.restoring != nil)
	if (resuming || u.switching != "") && m.Method != "" {
		if hold, keep := u.holdResumeEvent(m, resuming); hold {
			if !keep {
				return nil
			}
			if len(u.resumePending) == 256 {
				return errors.New("resume event capacity exceeded; session state is incomplete")
			}
			u.resumePending = append(u.resumePending, m)
			return nil
		}
	}
	if m.Method == "" {
		method := u.requests[string(m.ID)]
		if method == "" {
			return nil
		}
		delete(u.requests, string(m.ID))
		if method == "config/read" {
			u.notificationConfig(m)
			return nil
		}
		if method == resumePickerList {
			return u.resumePickerResponse(m)
		}
		if handled, err := u.statusMessage(method, m); handled {
			return err
		}
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
		if method == "thread/shellCommand" {
			u.shellResponse(m.Error)
			return nil
		}
		if method == "thread/compact/start" {
			u.compactRequest = false
			if m.Error != nil {
				u.starting, u.interruptBeforeStart, u.manualCompact = false, false, false
				u.restoreDrafts(append([]composerDraft{{text: "/compact"}}, slices.Concat(u.unsent, u.queued)...)...)
				u.unsent, u.queued = nil, nil
				u.setNotice("Compaction failed: "+m.Error.Message, true)
				u.status = "Ready"
			}
			return nil
		}
		if method == "thread/start" && u.clearing && m.Error != nil {
			u.clearing = false
			u.restoreDrafts(slices.Concat(u.unsent, u.queued)...)
			u.unsent, u.queued = nil, nil
			u.status = "Ready"
			u.setNotice("Could not clear session: "+m.Error.Message, true)
			return nil
		}
		if method == "thread/resume" && u.switching != "" && m.Error != nil {
			return u.resumeSessionFailed(m.Error.Message)
		}
		if m.Error != nil {
			u.status, u.alert = method+": "+m.Error.Message, true
			if method == "initialize" || method == "thread/start" || method == "thread/resume" {
				return errors.New(u.status)
			}
			if method == "turn/interrupt" {
				u.interrupting, u.interruptBeforeStart = "", false
			}
			return nil
		}
		switch method {
		case "initialize":
			var initialized struct {
				UserAgent string `json:"userAgent"`
			}
			if json.Unmarshal(m.Result, &initialized) == nil {
				u.backendVersion = backendVersion(initialized.UserAgent)
			}
			if _, err := u.client.Send("initialized", map[string]any{}, false); err != nil {
				return err
			}
			if u.resumeThread == resumePickerStartup {
				u.status = "Choose a session"
				return u.openResumePicker(true)
			}
			if u.resumeThread != "" {
				if u.resumeThread == "--last" {
					u.status = "Finding latest thread…"
					params := resumableThreads(1)
					params["cwd"] = u.resumeCwd
					return u.request("thread/list", params)
				}
				return u.requestResume(u.resumeThread)
			}
			return u.startThread()
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
			if method == "thread/resume" && result.Thread.ID != cmp.Or(u.switching, u.resumeThread) {
				return errors.New("thread/resume returned a different thread identity")
			}
			if u.clearing {
				if err := u.clearSessionPresentation(); err != nil {
					return err
				}
				u.releaseRetired(result.Thread.ID)
				if method == "thread/resume" {
					// Restoration below replays events buffered during the switch.
					u.resumeThread, u.switching = result.Thread.ID, ""
					u.resetDiffScope()
				} else {
					// Seed the Diff's lineage; after a switch, requests alone cannot.
					u.restoreDiffThread(result.Thread, true)
				}
			}
			u.statusConfig = appServerStatusConfig{}
			if err := json.Unmarshal(m.Result, &u.statusConfig); err != nil {
				return err
			}
			u.thread, u.status = result.Thread.ID, "Ready"
			u.model, u.reasoningEffort, u.serviceTier = result.Model, result.ReasoningEffort, result.ServiceTier
			u.models, u.modelsLoading = nil, true // A cleared or switched session lists them again.
			if err := u.request("model/list", map[string]any{"includeHidden": true}); err != nil {
				return err
			}
			u.session.start(u.thread, result.Thread.Cwd)
			if u.notifications != nil {
				if err := u.request("config/read", map[string]any{"cwd": result.Thread.Cwd, "includeLayers": false}); err != nil {
					return err
				}
			}
			if u.proxy != nil || u.panes != nil {
				var waitStore *mekugiReplayStore
				if u.proxy != nil {
					waitStore = u.proxy.replayStore
				}
				if err := u.openWaitStore(waitStore); err != nil {
					u.setNotice("Wait targets could not be retained: "+err.Error(), true)
				}
			}
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
				for _, sink := range []*nativeJournalSink{u.journal, u.unscopedJournal} {
					if err := u.proxy.journals.restoreNative(u.ctx, u.proxy.replayStore, sink); err != nil {
						u.setNotice("Journal restore: "+err.Error(), true)
					}
				}
				u.proxy.activity.attachNativePane(u.thread)
			}
			// Resumed history is applied once the roster is read, so Main can
			// follow child activity in order. An active snapshot keeps its
			// steer/interrupt target meanwhile.
			if method == "thread/resume" {
				for _, turn := range result.Thread.Turns {
					if turn.Status == "inProgress" {
						u.turn, u.status, u.turnStarted = turn.ID, "Working", time.Now()
						u.session.agent("/root").Responding = true
					}
				}
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
	u.resolveNotification(m)
	if handled, err := u.questionMessage(m); handled {
		return err
	}
	if len(m.ID) != 0 {
		// Never auto-approve an unexpected server request. Leave it pending,
		// visibly blocked, until interrupted.
		u.status, u.alert = "Blocked on unsupported request "+m.Method+" · Ctrl-C interrupts", true
		u.blockNotification(m)
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
			if u.shellOrigin != nil && p.Turn.ID != *u.shellOrigin {
				u.shellStandalone, u.shellOrigin = true, nil
			}
			u.turn, u.status, u.starting, u.alert, u.turnStarted = p.Turn.ID, "Working", false, false, time.Now()
			if u.interruptBeforeStart {
				return u.interruptTurn()
			}
		}
	case "turn/completed":
		if u.notifications != nil {
			for id, request := range u.notifications.blocked {
				if request.Thread == p.ThreadID && (request.Turn == "" || request.Turn == p.Turn.ID) {
					delete(u.notifications.blocked, id)
				}
			}
		}
		if p.ThreadID == u.thread && p.Turn.ID == u.turn {
			u.shellStandalone, u.manualCompact = false, false
			u.endSyncQuestions(p.Turn.ID)
			u.turn, u.starting = "", false
			u.status, u.alert = strings.ToUpper(p.Turn.Status[:min(1, len(p.Turn.Status))])+p.Turn.Status[min(1, len(p.Turn.Status)):], p.Turn.Status == "failed"
			if p.Turn.Status == "completed" && !u.turnStarted.IsZero() {
				u.status += " in " + liveActivityAge(time.Since(u.turnStarted))
			}
			if p.Turn.Error != nil {
				u.status, u.alert = u.status+": "+p.Turn.Error.Message, true
			}
			u.settleInput(p.Turn.ID, p.Turn.Status == "interrupted")
			if p.Turn.Status == "completed" && u.questionCount() == 0 {
				u.notify("agent-turn-complete", "Agent turn complete")
				if u.reset != nil && len(u.unsent) == 0 && len(u.queued) == 0 {
					if err := u.reset.completed(p.Turn.ID); err != nil {
						u.setNotice("Slice continuation: "+err.Error(), true)
					}
				}
			}
			for _, c := range u.questions.calls {
				if !c.resolved {
					u.renderQuestionRecord(c)
				}
			}
		}
	case "item/started", "item/completed", "item/agentMessage/delta":
		if m.Method == "item/started" {
			u.observeShellItem(p.ThreadID, p.Item)
		}
		if p.ItemID == "" {
			p.ItemID = p.Item.ID
		}
		// Only completion commits, so one message cannot match two identical steers.
		if p.ThreadID == u.thread && p.Item.Type == "userMessage" && m.Method == "item/completed" {
			u.commitSteer(p.Item)
			u.commitQuestionReplies(p.Item, p.TurnID)
		}
		if p.ThreadID == u.thread && (u.journal != nil && u.journal.hides(p.ItemID) || u.unscopedJournal != nil && u.unscopedJournal.hides(p.ItemID)) {
			return nil
		}
		if u.observeQuestionItem(p.ThreadID, p.TurnID, p.Item, false) {
			return nil
		}
		for _, c := range u.questions.calls {
			if c.thread == p.ThreadID && c.item == p.ItemID && len(c.request) == 0 && m.Method == "item/agentMessage/delta" {
				return nil
			}
		}
		u.view.applyAppServerItem(u.session.cwd, u.thread, p.ThreadID, p.TurnID, p.ItemID, m.Method, p.Delta, p.Item)
	}
	return nil
}

func (u *appServerUI) key(key byte) (bool, error) {
	if u.questions.active != nil && !u.questions.painted {
		u.hideQuestions()
	}
	if !u.paste && u.escape == "" && key != 27 {
		if handled, err := u.questionKey(string([]byte{key})); handled {
			return false, err
		}
	}
	defer u.refreshPicker()
	if u.statusPanelKey(string([]byte{key})) {
		return false, nil
	}
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
		if u.currentQuestion() == nil && key == '?' && u.draft == "" && (u.shell == nil || u.shell.focus == 0) {
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
			if !u.paste {
				if handled, err := u.questionKey(u.escape); handled {
					u.escape = ""
					return false, err
				}
			}
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
				} else if u.picker.modal == "menu" || u.picker.modal == "copy" || u.picker.modal == "settings" {
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
	case 23: // macOS terminals may encode Option+Backspace as Ctrl+W.
		u.deleteWord(true)
	case 11: // Ctrl+K kills to the logical line end, or joins at its newline.
		at := u.cursor()
		end := len(u.draft)
		if next := strings.IndexByte(u.draft[at:], '\n'); next >= 0 {
			end = at + next
			if next == 0 {
				end++
			}
		}
		for start, tokenEnd := range u.draftTokenSpans() {
			if end > start && end < tokenEnd {
				end = tokenEnd
			}
		}
		u.deleteDraftRange(at, end)
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
		if u.turn != "" || u.starting || u.submission.text != "" || len(u.unsent)+len(u.queued) > 0 {
			return false, u.interruptTurn()
		}
		return true, nil
	case 127, 8:
		u.deleteDraft(true)
	case '\n':
		u.insertDraft("\n")
	case '\r':
		text := strings.TrimSpace(u.draft)
		if text == "/btw" || strings.HasPrefix(text, "/btw ") || strings.HasPrefix(text, "/btw\n") || strings.HasPrefix(text, "/btw\t") {
			return false, u.submitBTW()
		}
		if u.shellMode() {
			return false, u.submitShell()
		}
		if text == "/compact" || text == "/clear" {
			return false, u.sessionCommand(text)
		}
		if text == "/resume" || strings.HasPrefix(text, "/resume ") {
			return false, u.resumeCommand(text)
		}
		if text == "/status" {
			return false, u.showStatus()
		}
		if text == "/copy" {
			u.showCopyPicker()
			return false, nil
		}
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
			u.setNotice("Unknown command "+strings.Fields(text)[0]+" · /compact, /clear, /resume, /btw, /status, /copy, /skills, /model, /effort, /reasoning, /tier, /live, /quit", true)
			return false, nil
		}
		if text == "" || u.thread == "" || u.restoring != nil {
			return false, nil
		}
		// A draft that cannot be sent yet stacks with earlier unsent steers.
		if u.waitingQuestion() {
			u.queued = append(u.queued, u.takeDraft())
		} else {
			u.unsent = append(u.unsent, u.takeDraft())
		}
		u.pruneDraftImages()
		u.setNotice("", false)
		u.view.follow()
		return false, u.flushInput()
	case '\t':
		// Tab queues busy input for the next turn; otherwise it sends like Enter.
		text := strings.TrimSpace(u.draft)
		if u.shellMode() || text == "" || strings.HasPrefix(text, "/") || u.turn == "" && !u.starting && u.submission.text == "" {
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

// paneActivityAgent names the Activity agent an entry belongs to. A directed
// message shows under its sender, or under its recipient when Main sent it.
func paneActivityAgent(entry activityPaneEntry) string {
	if entry.Kind == "reply" && entry.message != nil {
		if entry.message.from == "/root" {
			return entry.message.to
		}
		return entry.message.from
	}
	return entry.Agent
}

// historyPending reports resumed history that Main has yet to show. Router
// observations wait in their queues so they follow it, as live.
func (u *appServerUI) historyPending() bool {
	return u.restoring != nil && !u.restoring.rendered
}

// mainActivityEntry projects one collector entry into Main's audience.
// Ordinary root activity stays in Main, but a directed message belongs at
// both ends, and a child's start and answer show where Main follows them.
func mainActivityEntry(entry activityPaneEntry) (activityPaneEntry, bool) {
	main := entry.Agent == "/root" && (entry.Kind == "tool" || entry.Kind == "exit" || entry.Kind == "error" || entry.Kind == "reasoning" || entry.Kind == "progress")
	if (entry.Kind == "assignment" || entry.Kind == "start") && entry.assignment != nil {
		main = true
		entry.Agent = entry.assignment.from
		if entry.Agent == "" {
			entry.Agent = "Main"
		}
	}
	if entry.Kind == "reply" && entry.message != nil {
		main = entry.message.from == "/root" || entry.message.to == "/root"
		entry.Agent = entry.message.from
	}
	main = main || (entry.Kind == "final" || entry.Kind == "start") && entry.Agent != "/root"
	if entry.Agent == "/root" {
		entry.Agent = "Main"
	}
	return entry, main
}

// mainActivityLinked reports a Main entry that excerpts its Activity entry.
func mainActivityLinked(entry activityPaneEntry) bool {
	return entry.Kind == "final" || entry.Kind == "reply" || entry.assignment != nil
}

func (u *appServerUI) applyMainActivity(entry activityPaneEntry) {
	entry.Seq = u.view.lastSeq + 1
	u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{entry}})
	if entry.Kind == "final" {
		u.view.linkChildAnswers(u.view.entrySeq(entry))
	}
}

// applyActivity projects the collector once into the two audiences. Ordinary
// root activity stays in Main, but a directed message belongs at both ends.
func (u *appServerUI) applyActivity(entries []activityPaneEntry, agents []activityPaneAgent) {
	paneEntries := slices.Clone(entries)
	for i := range paneEntries {
		paneEntries[i].Agent = paneActivityAgent(paneEntries[i])
	}
	u.agents.apply(activityPaneEvent{Kind: "entries", Entries: paneEntries, Agents: agents})
	for _, entry := range paneEntries {
		if entry.Kind == "final" {
			u.agents.linkChildAnswers(u.agents.entrySeq(entry))
		}
	}
	for i, entry := range entries {
		if entry, main := mainActivityEntry(entry); main {
			if mainActivityLinked(entry) {
				entry.activitySeq = u.agents.entrySeq(paneEntries[i])
			}
			u.applyMainActivity(entry)
		}
	}
	u.applyCapturedEdits()
}

// mainFrame is the transcript above a boxed composer. The top border carries
// the session state; the bottom border the model. dock rows are left blank
// between the two, in the returned rectangle, for the live edit dock.
func (u *appServerUI) mainFrame(width, height, dock int) (frameRows []string, dockRect terminalRect) {
	if len(u.view.entries) == 0 && u.draft == "" && height > 10 && u.statusPanel == nil && u.resumePicker == nil && !u.pickerVisible() {
		height--
		defer func() {
			frameRows = append([]string{ansi.Truncate(u.welcome(), max(1, width), "…")}, frameRows...)
			dockRect.y++
			u.composerRect.y++
			u.view.feedTop++
		}()
	}
	u.autoOpenQuestions()
	width, height = max(1, width), max(1, height)
	u.picker.rect = terminalRect{}
	u.questions.rect = terminalRect{}
	if u.statusPanel != nil {
		return u.statusPanelFrame(width, height), terminalRect{}
	}
	if u.resumePicker != nil {
		return u.resumePickerFrame(width, height), terminalRect{}
	}
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
	if q := u.currentQuestion(); q != nil && q.IsSecret {
		for i, line := range draft {
			draft[i] = strings.Repeat("•", ansi.StringWidth(line))
		}
	}
	caret := points[len(points)-1]
	for _, point := range points {
		if point.Offset == u.cursor() {
			caret = point
			break
		}
	}
	visible := min(6, height-borderRows)
	firstRow := max(0, caret.Row-visible+1)
	draft = draft[firstRow:min(len(draft), firstRow+visible)]

	room := max(0, height-len(draft)-borderRows)
	strip := u.journalPlanStrip(width)
	journalPinned := strip != ""
	if reset := u.journalResetStrip(width); reset != "" {
		strip, journalPinned = reset, false
	}
	if strip != "" && room > 0 {
		room--
	} else {
		strip, journalPinned = "", false
	}
	var pending []string
	questionRows := u.questionRows(width, max(1, min(height/2, room)))
	if len(questionRows) > 0 {
		dock = 0
		room -= len(questionRows)
	}
	btwRows := u.btwRows(width, max(0, min(height/2, room)))
	if len(btwRows) > 0 {
		dock = 0
		room -= len(btwRows)
	}
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
	// Reserve a blank row immediately above the composer, including below
	// any journal strip, pending input, or live dock.
	gap := 0
	if !u.pickerVisible() && room > 2 {
		gap = 1
		room--
	}
	u.mainContentPainted = room > 1
	u.view.conversation, u.view.feedOnly, u.view.status = true, true, livediff.Safe(u.status, false)
	// One pin: a journal state newer than Main's latest reply replaces it.
	u.view.pinMainReply = u.turn != "" && !(journalPinned && u.journalNewerThanReply())
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
	frame = append(frame, btwRows...)
	u.questions.rect = terminalRect{0, len(frame), width, len(questionRows)}
	frame = append(frame, questionRows...)
	if strip != "" {
		frame = append(frame, strip)
	}
	frame = append(frame, make([]string, gap)...)
	if u.questions.active != nil {
		u.questions.painted = true
	}
	// Visual reference: Grok CLI PromptStyle / PromptWidget::draw, source
	// crates/codegen/xai-grok-pager/src/views/prompt_widget/mod.rs:217:240,3007:3078
	// @be7ce6e8cffe46d20bef9834b211616082ee866b. Keep continuation rows aligned.
	// Colors: xai-grok-pager-render/src/theme/oscura.rs, same revision.
	border := "\x1b[38;2;52;48;72m"
	if u.currentQuestion() != nil {
		border = u.view.painter.Theme.Accent()
	} else if u.shellMode() {
		border = activityui.Red
	}
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
		if focused && textWidth > 1 && i+firstRow == caret.Row {
			before := ansi.Cut(line, 0, caret.Column)
			cluster, _, _, _ := uniseg.FirstGraphemeClusterInString(u.draft[u.cursor():], -1)
			cellWidth := max(1, ansi.StringWidth(livediff.Safe(cluster, false)))
			cell := ansi.Cut(line, caret.Column, caret.Column+cellWidth)
			if ansi.StringWidth(cell) == 0 {
				cell = " "
			}
			rest := ansi.Cut(line, caret.Column+cellWidth, textWidth)
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
	if u.questions.active != nil {
		label = "answering"
		if u.currentQuestion().note {
			label += " · note"
		}
		if len(u.questions.active.request) > 0 {
			label += " · turn waiting"
		}
		if u.questions.parked.snapshot.text != "" {
			label += " · draft kept"
		}
	}
	if u.shellMode() {
		label = activityui.Red + "Shell Mode" + activityui.Reset + " · " + label
	}
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
			if u.shellStandalone {
				status = "Running shell"
			} else if u.compacting != nil {
				status = "Compacting context"
			} else if u.polling != nil {
				status = "Still running"
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
	if u.proxy == nil || u.historyPending() {
		return
	}
	entries := u.proxy.activity.takeNativeActivity(u.thread)
	for _, start := range u.proxy.activity.takeRequestStarts(u.thread) {
		entries = append(entries, u.session.beginThinking(start.thread, start.at)...)
	}
	for i := range entries {
		entries[i].Seq = u.session.next()
	}
	if len(entries) != 0 {
		u.applyActivity(entries, nil)
		u.dirty = true
	}
	for _, ref := range u.proxy.activity.takeUnreturned(u.thread) {
		if main, activity := u.view.markUnreturned(ref.thread, ref.call), u.agents.markUnreturned(ref.thread, ref.call); main || activity {
			u.dirty = true
		}
	}
}

// interruptTurn preserves the composer while interrupting the active turn.
func (u *appServerUI) interruptTurn() error {
	if u.turn == "" {
		if u.starting || u.submission.text != "" {
			u.interruptBeforeStart = true
			u.status = "Interrupting…"
			return nil
		}
		u.restoreDrafts(slices.Concat(u.unsent, u.queued)...)
		u.unsent, u.queued = nil, nil
		return nil
	}
	if u.interrupting == u.turn {
		return nil
	}
	u.endSyncQuestions(u.turn)
	u.interrupting = u.turn
	u.status = "Interrupting…"
	return u.request("turn/interrupt", map[string]any{"threadId": u.thread, "turnId": u.turn})
}

// applyCriticalNotices presents router diagnostics without creating assistant
// messages. The caller acknowledges the reserved batch only after terminal paint.
func (u *appServerUI) applyCriticalNotices() *criticalNoticeDelivery {
	var activity *subagentActivity
	if u.proxy != nil {
		activity = u.proxy.activity
	}
	delivery := u.issues.takeNative(u.thread, activity)
	if delivery == nil {
		return nil
	}
	if u.noticeEntries == nil {
		u.noticeEntries = make(map[string]bool)
	}
	for _, notice := range delivery.snapshots {
		if u.noticeEntries[notice.id] {
			continue
		}
		text := noticeText(&notice)
		if notice.thread != "" && notice.thread != u.thread && activity != nil {
			activity.mu.Lock()
			if node := activity.threads[notice.thread]; node != nil {
				text = node.name + ": " + text
			}
			activity.mu.Unlock()
		}
		u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{
			Seq: u.view.lastSeq + 1, Agent: "Main", Kind: "error", Text: text, Observed: time.Now(),
		}}})
		u.noticeEntries[notice.id] = true
	}
	u.dirty = true
	return delivery
}
