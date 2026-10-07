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
	"github.com/yusing/mekugi/capturer"
	"github.com/yusing/mekugi/internal/appserver"
	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"github.com/yusing/mekugi/internal/ui/diffview"
	terminalui "github.com/yusing/mekugi/internal/ui/terminal"
	"github.com/yusing/mekugi/internal/vcsguard"
	"golang.org/x/term"
)

type appServerItem struct {
	replacesItems    []string                  // Retained presentation provenance, not a host field.
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
	Kind             string                    `json:"kind"` // subAgentActivity lifecycle.
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
	appServerLifecycle
	replay                    *uiReplayPlayback // Offline transport controls; nil for live sessions.
	clock                     func() time.Time  // Optional presentation clock for offline replay.
	backendVersion            string
	title                     string            // Host-confirmed thread name, independent of naming work.
	pendingTitle              string            // Manual name entered before the host thread exists.
	titleRenames              map[string]string // Latest manual name awaiting host confirmation, by thread.
	titleGenerator            *sessionTitleGenerator
	titleUpdates              <-chan sessionTitleUpdate
	titleRequests             map[string]sessionTitleRequest
	reset                     *journalResetDriver
	btw                       *appServerBTW
	btwRequests               map[string]btwRequest
	btwThreads                map[string]*appServerBTW
	notifications             *nativeNotifications
	questions                 nativeQuestionDock
	approvals                 nativeApprovalDock
	approvalMode              bool // Codex's configured policy applies, not --yolo's.
	guardHookCheck            approvalHookCheck
	statusPanel               *appServerStatusReport
	sessionCapture            *capturer.Recorder
	resumePicker              *appServerResumePicker
	statusConfig              appServerStatusConfig
	statusReports             map[string]*appServerStatusReport
	picker                    composerPicker
	skillEnvironment          []string
	commitReads               chan gitCommitKey        // Commit objects read off the UI goroutine.
	commandSegmentWrites      chan commandSegmentWrite // Completed replay writes, consumed only by the UI.
	commandSegmentPending     int
	restoredSegments          map[string]*retainedCommandSegments // Turn-local, validated and adopted history.
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
	journalReceipts           map[*nativeJournalSink]bool // Includes workers from replaced sessions.
	shell                     *terminalUI
	session                   appServerSession
	ctx                       context.Context
	quitRequested             bool
	interruptLocked           bool
	activeChildren            map[string]bool // Live host lifecycles only, never replayed processes.
	mainContentPainted        bool
	status                    string
	exitUsage                 appServerTokenUsage
	resumeArgv                []string
	replayDebugDirectory      string
	compacting                *[2]string // Main turn and compaction item.
	polling                   *[2]string // Main turn and polled process.
	alert                     bool       // The status reports a failure or blocked request.
	notice                    string     // Composer feedback; errors persist until the next draft edit.
	noticeAlert               bool
	noticeUntil               time.Time
	noticeDetails             terminalRect // Truncated composer error, relative to Main.
	noticeDismiss             terminalRect // Check button, relative to Main.
	turnStarted               time.Time    // Shown as elapsed time while a turn runs.
	model, reasoningEffort    string
	serviceTier               string
	serviceTiers              *serviceTierSettings // Invocation defaults and confirmed thread/model choices.
	models                    []appServerModel
	modelsLoading             bool
	reasoningKey              *bool
	settingsChoices           string
	requests                  map[string]string
	draft                     string
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
	resumeNotice              string // Settings fallback survives the old session's presentation reset.
	panes                     *nativePanePersistence
	waitRelease               func()
	restoring                 *appServerActivityRestore
	childHistory              map[string]*appServerChildHistory
	historyLoading            *appServerChildHistory
}

// StartAppServerUI starts the native terminal frontend without router observers.
// Codex app-server owns execution; the launcher
// still owns routing, environment, invocation-local configuration and cancellation.
func StartAppServerUI(ctx context.Context, cmd *exec.Cmd, stdin, stdout *os.File, resumeThread string, resumeArgv []string, approvals bool) (func() error, error) {
	faint, _ := terminalui.SupportsFaint(ctx, "auto")
	return startAppServerUI(ctx, cmd, stdin, stdout, nil, nil, resumeThread, faint, nil, nil, resumeArgv, "", nil, approvals)
}

// Codex approval policy and the invocation-local VCS guard are independent.
func startAppServerUI(ctx context.Context, cmd *exec.Cmd, stdin, stdout *os.File, proxy *mekugiProxy, issues *CriticalErrors, resumeThread string, faint bool, serviceTiers *serviceTierSettings, capture *capturer.Recorder, resumeArgv []string, debugDirectory string, generator *sessionTitleGenerator, approvals bool) (func() error, error) {
	guardCommand := ""
	for _, entry := range cmd.Environ() {
		if command, ok := strings.CutPrefix(entry, vcsguard.HookEnvironment+"="); ok {
			guardCommand = command
		}
	}
	if guardCommand != "" && (proxy == nil || proxy.execTrack == nil || proxy.execTrack.guardClose == nil) {
		return nil, errors.New("VCS guard approval channel is unavailable")
	}
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
	u := &appServerUI{client: c, view: newLiveActivityView(), agents: newLiveActivityView(), proxy: proxy, issues: issues, requests: make(map[string]string), status: "Connecting…", dirty: true, ctx: ctx, resumeThread: resumeThread, serviceTiers: serviceTiers, approvalMode: approvals}
	u.guardHookCheck.command = guardCommand
	if generator != nil {
		u.titleGenerator = generator
		u.titleUpdates = generator.updates
	}
	if proxy != nil {
		u.execTrack = proxy.execTrack
		if proxy.skillsManager {
			u.skillEnvironment = cmd.Environ()
		}
	}
	u.resumeArgv = slices.Clone(resumeArgv)
	u.replayDebugDirectory = debugDirectory
	u.sessionCapture = capture
	u.resumeConfig = appServerResumeConfig(cmd.Args, resumeArgv)
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
				agePaint := u.now()
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
						clear(u.shell.livePending)
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
					case err := <-u.commandSegmentWrites:
						u.commandSegmentRetained(err)
					case update := <-u.titleUpdates:
						u.persistSessionTitle(update)
					case request := <-u.guardRequests():
						u.addGuardApproval(request)
						u.dirty = true
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
						if len(u.unsent) > 0 && u.unsent[0].questionCall != nil && !u.proxy.emittingToolInput(u.session.cwd, u.thread) {
							if err := u.flushInput(); err != nil {
								return err
							}
						}
						u.refreshSessionMetrics(false)
						u.startSkillHistory()
						if err := u.tickJournalReset(u.now()); err != nil {
							u.setNotice(err.Error(), true)
						}
						u.applyObservedActivity()
						if err := u.fillResumePicker(); err != nil {
							return err
						}
						u.flushStreamOutput()
						u.startCommitReads()
						u.paneError(u.panes.save(u.shell, u.now(), false))
						if u.expireNotice(u.now()) || u.expireApprovals() {
							u.dirty = true
						}
						now := u.now()
						if u.shell.output != nil && u.shell.output.expireFlash(now) {
							u.dirty = true
						}
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
							agePaint = u.now()
						}
						if err := u.drainKeys(keys); err != nil || u.quitRequested {
							return err
						}
						notices := u.applyCriticalNotices()
						journalPending := make(map[*nativeJournalSink][]nativeJournalPublication)
						if err := u.finishJournalAcknowledgements(false); err != nil {
							return err
						}
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
						if u.shell.animating(u.now()) || u.agents.rosterEasing {
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
							notices.finish(true)
							for sink, items := range journalPending {
								if !u.mainContentPainted {
									if !u.journalPanePresents(sink) {
										continue
									}
									items = slices.DeleteFunc(slices.Clone(items), func(p nativeJournalPublication) bool { return p.card != nil || p.event == nil })
								}
								sink.startAcknowledgement(ctx, proxy, items)
								if len(items) > 0 {
									if u.journalReceipts == nil {
										u.journalReceipts = make(map[*nativeJournalSink]bool)
									}
									u.journalReceipts[sink] = true
								}
							}
							u.dirty = false
						}
					}
					u.writeTerminalTitle(u.now())
				}
			})
			if !errors.Is(err, errOpenComposerEditor) {
				break
			}
			u.openComposerEditor(stdin, stdout)
			u.dirty = true
		}
		if saveErr := u.panes.save(u.shell, u.now(), true); saveErr != nil {
			fmt.Fprintln(stdout, "Pane layout or settings could not be saved:", livediff.Safe(saveErr.Error(), false))
		}
		if !exited {
			if err == nil {
				err = c.Shutdown()
			} else {
				c.Close()
				<-c.Done
			}
		}
		u.finishCommandSegments()
		err = errors.Join(err, u.finishJournalAcknowledgements(true))
		u.hideQuestions()
		u.hideApprovals()
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
		if u.shellCommand.pending.text != "" {
			fmt.Fprintln(stdout, "Shell submission outcome unknown; not automatically rerun:\n"+livediff.Safe(u.shellCommand.pending.text, false))
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
	if u.sessionTitleMessage(m) {
		return nil
	}
	defer u.refreshPicker()
	u.ensureJournalReset()
	if u.reset != nil {
		pendingCompletion := u.reset.pendingCompleted
		handled, resetErr := u.reset.message(m)
		if resetErr != nil {
			u.setNotice("Journal slice: "+resetErr.Error(), true)
		}
		u.showResetNotice()
		if handled {
			if pendingCompletion != "" && pendingCompletion == u.reset.continuationTurn && !u.reset.active() && u.questionCount() == 0 {
				u.notify("agent-turn-complete", "Agent turn complete")
			}
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
	if held, err := u.holdOlderHistoryEvent(m); held || err != nil {
		return err
	}
	resuming := u.resumeThread != "" && u.resumeThread != resumePickerStartup && (u.thread == "" || u.restoring != nil)
	if (resuming || u.replacement.target != "") && m.Method != "" {
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
		if strings.HasPrefix(method, "activity-skills/") {
			return u.skillHistoryResponse(strings.TrimPrefix(method, "activity-skills/"), m)
		}
		if strings.HasPrefix(method, "activity/") {
			return u.childHistoryResponse(method, m)
		}
		if method == "resume/settings" {
			return u.resumeSettingsResponse(m)
		}
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
			u.compactResponse(m.Error != nil)
			if m.Error != nil {
				u.restoreDrafts(append([]composerDraft{{text: "/compact"}}, slices.Concat(u.unsent, u.queued)...)...)
				u.unsent, u.queued = nil, nil
				u.setNotice(u.compactionText("Compaction failed: ")+m.Error.Message, true)
				u.status = "Ready"
			}
			return nil
		}
		if method == "compact/interrupt" {
			u.compaction.interruptAckPending = false
			if m.Error != nil {
				u.compaction.interrupting = false
				u.interruption.finish()
				u.restoreDrafts(slices.Concat(u.unsent, u.queued)...)
				u.unsent, u.queued = nil, nil
				u.setNotice("Could not interrupt for compaction: "+m.Error.Message, true)
				u.status = "Ready"
				if u.turn != "" {
					u.status = "Working"
				}
			}
			return nil
		}
		if method == "steer/interrupt" {
			u.steerInterruptAckPending = false
			if m.Error != nil {
				u.sendSteersAfterInterrupt = false
				u.interruption.finish()
				// The completion can precede this rejection. Do not deliver
				// steers that were moved to the local stack in that interval.
				if u.turn == "" {
					u.restoreDrafts(slices.Concat(u.unsent, u.queued)...)
					u.unsent, u.queued = nil, nil
				}
				u.setNotice("Could not interrupt to send steer: "+m.Error.Message, true)
			}
			return nil
		}
		if method == "approval/hooks" {
			return u.guardHookResponse(m)
		}
		if method == "thread/start" && u.replacement.pending() && m.Error != nil {
			u.replacement.finish()
			u.restoreDrafts(slices.Concat(u.unsent, u.queued)...)
			u.unsent, u.queued = nil, nil
			u.status = "Ready"
			u.setNotice("Could not clear session: "+m.Error.Message, true)
			return nil
		}
		if method == "thread/resume" && u.replacement.target != "" && m.Error != nil {
			return u.resumeSessionFailed(m.Error.Message)
		}
		if m.Error != nil {
			u.status, u.alert = method+": "+m.Error.Message, true
			if method == "initialize" || method == "thread/start" || method == "thread/resume" {
				return errors.New(u.status)
			}
			if method == "turn/interrupt" {
				u.interruption.finish()
				u.sendSteersAfterInterrupt = false
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
				Model             string              `json:"model"`
				ReasoningEffort   string              `json:"reasoningEffort"`
				ServiceTier       string              `json:"serviceTier"`
				CollaborationMode map[string]any      `json:"collaborationMode"`
				Thread            appServerThreadInfo `json:"thread"`
			}
			if err := json.Unmarshal(m.Result, &result); err != nil {
				return err
			}
			if result.Thread.ID == "" {
				return fmt.Errorf("%s returned no thread identity", method)
			}
			if method == "thread/resume" && result.Thread.ID != cmp.Or(u.replacement.target, u.resumeThread) {
				return errors.New("thread/resume returned a different thread identity")
			}
			if u.replacement.pending() {
				if err := u.clearSessionPresentation(); err != nil {
					return err
				}
				u.leaveThread()
				u.releaseRetired(result.Thread.ID)
				if method == "thread/resume" {
					// Restoration below replays events buffered during the switch.
					u.resumeThread = result.Thread.ID
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
			u.title = result.Thread.Name
			u.titleGenerator.register(result.Thread)
			if u.pendingTitle != "" {
				name := u.pendingTitle
				u.pendingTitle = ""
				u.renameSessionTitle(name)
			}
			u.model, u.reasoningEffort, u.serviceTier = result.Model, result.ReasoningEffort, result.ServiceTier
			if method == "thread/resume" {
				u.settings.restoreEffort = u.takeDefaultEffort()
			}
			if method == "thread/start" {
				u.settings.restoredEffort()
				if u.proxy != nil {
					u.proxy.usage.markNew(result.Thread.ID)
				}
			}
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
			u.restoreContextUsage(u.session.agent("/root"), result.Thread)
			u.restoreUsage(result.Thread)
			u.observeCost(u.thread, u.session.agent("/root"))
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
						u.turn, u.status, u.turnStarted = turn.ID, "Working", u.now()
						u.session.agent("/root").Responding = true
					}
				}
			}
			if u.panes != nil {
				u.ensureShell()
				u.paneError(u.panes.open(u.shell, result.Thread.Cwd, u.thread, method == "thread/resume"))
			}
			if method == "thread/resume" {
				if u.settings.restoreEffort {
					// effort:null means no change in the host RPC. Its complete
					// collaboration-mode snapshot can express a nullable effort,
					// without replacing the host's mode or developer instructions.
					settings, ok := result.CollaborationMode["settings"].(map[string]any)
					if !ok {
						if u.reasoningEffort != "" {
							return errors.New("Codex did not return the settings needed to restore default reasoning")
						}
						u.settings.restoredEffort()
					} else if settings["reasoning_effort"] != nil {
						// Codex checkpoints resume defaults before this response.
						// Do not let that transient effort replace saved null intent
						// on a fresh-process retry if the corrective update fails.
						if u.panes != nil && u.resumeEvidence.Model != "" {
							u.panes.resumeObserved = u.now()
							u.panes.resumeEvidence = u.resumeEvidence
							u.paneError(u.panes.save(u.shell, u.now(), true))
						}
						settings["reasoning_effort"] = nil
						if _, err := u.updateSettings(map[string]any{"collaborationMode": result.CollaborationMode}); err != nil {
							return err
						}
					} else {
						u.settings.restoredEffort()
						u.reasoningEffort = ""
					}
				}
				if u.resumeNotice != "" {
					u.setNotice(u.resumeNotice, true)
					u.resumeNotice = ""
				}
				u.retainAppliedSettings()
				return u.restorePaneContent(result.Thread)
			}
			u.retainAppliedSettings()
		}
		return nil
	}
	u.resolveNotification(m)
	if handled, err := u.approvalMessage(m); handled {
		return err
	}
	if handled, err := u.questionMessage(m); handled {
		return err
	}
	if len(m.ID) != 0 {
		// Never auto-approve an unexpected server request. Leave it pending,
		// visibly blocked, until interrupted.
		u.status, u.alert = "Blocked on unsupported request "+m.Method+" · Ctrl-C interrupts", true
		u.blockNotification(m)
		u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: u.view.lastSeq + 1, Agent: "Session", Kind: "text", Text: u.status, Observed: u.now()}}})
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
		Turn     appServerTurn `json:"turn"`
	}
	if err := json.Unmarshal(m.Params, &p); err != nil {
		return fmt.Errorf("%s: %w", m.Method, err)
	}
	switch m.Method {
	case "turn/started":
		if p.ThreadID == u.thread {
			u.started(p.Turn.ID)
			u.status, u.alert, u.turnStarted = "Working", false, u.now()
			if u.interruption.beforeStart {
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
			if p.Turn.Status == "interrupted" {
				if err := u.reset.stop(p.Turn.ID); err != nil {
					u.setNotice("Journal stop: "+err.Error(), true)
				}
			}
			u.completed(p.Turn.Status == "completed")
			u.endSyncQuestions(p.Turn.ID)
			u.status, u.alert = strings.ToUpper(p.Turn.Status[:min(1, len(p.Turn.Status))])+p.Turn.Status[min(1, len(p.Turn.Status)):], p.Turn.Status == "failed"
			if p.Turn.Status == "completed" && !u.turnStarted.IsZero() {
				u.status += " in " + liveActivityAge(u.now().Sub(u.turnStarted))
			}
			if p.Turn.Error != nil {
				u.status, u.alert = u.status+": "+p.Turn.Error.Message, true
			}
			u.settleInput(p.Turn.ID, p.Turn.Status == "interrupted")
			if p.Turn.Status == "completed" && u.questionCount() == 0 {
				if u.reset != nil && len(u.unsent) == 0 && len(u.queued) == 0 {
					if err := u.reset.completed(p.Turn.ID); err != nil {
						u.setNotice("Slice continuation: "+err.Error(), true)
					}
				}
				if !u.reset.active() {
					u.notify("agent-turn-complete", "Agent turn complete")
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
		if p.Item.Type == "agentMessage" && m.Method == "item/completed" {
			p.Item.replacesItems = u.proxy.commentaryReplacementItems(u.ctx, u.session.cwd, p.ThreadID, p.TurnID, p.ItemID)
		}
		u.view.applyAppServerItem(true, u.session.cwd, u.thread, p.ThreadID, p.TurnID, p.ItemID, m.Method, p.Delta, p.Item)
	}
	return nil
}

func (u *appServerUI) key(key byte) (bool, error) {
	if u.paste {
		u.pasteByte(key)
		return false, nil
	}
	if u.questions.active != nil && !u.questions.painted {
		u.hideQuestions()
	}
	if u.approvals.open && !u.approvals.painted {
		u.hideApprovals()
	}
	if u.escape == "" && key != 27 {
		if handled, err := u.approvalKey(string([]byte{key})); handled {
			return false, err
		}
		if handled, err := u.questionKey(string([]byte{key})); handled {
			return false, err
		}
	}
	defer u.refreshPicker()
	if u.statusPanelKey(string([]byte{key})) {
		return false, nil
	}
	// An exact /compact has a submission meaning for Tab, not just completion.
	compactTab := key == '\t' && strings.TrimSpace(u.draft) == "/compact" && u.picker.modal == ""
	if u.escape == "" && key != 27 && !compactTab && u.pickerKey(string([]byte{key})) {
		return false, nil
	}
	if u.escape == "\x1b" && (key == 127 || key == 8) {
		u.escape = ""
		u.deleteWord(true)
		return false, nil
	}
	// macOS terminals commonly encode Option+Left/Right as Meta-b/f.
	if u.escape == "\x1b" && (key == 'b' || key == 'f') {
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
	if u.escape == "" {
		if u.currentQuestion() == nil && !u.approvals.open && key == '?' && u.draft == "" && (u.shell == nil || u.shell.focus == 0) {
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
			if handled, err := u.approvalKey(u.escape); handled {
				u.escape = ""
				return false, err
			}
			if handled, err := u.questionKey(u.escape); handled {
				u.escape = ""
				return false, err
			}
			if u.pickerKey(u.escape) {
				u.escape = ""
				return false, nil
			}
			switch u.escape {
			case "\x1b[1;2A", "\x1b[1;2B":
				sequence := u.escape
				u.escape = ""
				return false, u.stepReasoning(strings.HasSuffix(sequence, "A"))
			case "\x1b[1;3A", "\x1b[1;2D":
				u.editQueued()
			case "\x1b[122;6u":
				u.undoDraft(true)
			case "\x1b[3;3~":
				u.deleteWord(false)
			case "\x1b[127;3u", "\x1b[8;3u":
				u.deleteWord(true)
			case "\x1b[3~":
				u.deleteDraft(false)
			case "\x1b[200~":
				u.paste = true
				u.pasted = nil
				u.run = runNone
			case "\x1b[201~": // A stray terminator is not composer input.

			case "\x1b[5~":
				u.view.scrollKey('b')
			case "\x1b[6~":
				u.view.scrollKey(' ')
			default:
				// Only caret movement ends an edit run; forward deletes still group.
				u.moveDraft(u.escape)
			}
			u.escape = ""
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
		// then cancels queued compaction, interrupts the active turn, or quits.
		if u.draft != "" {
			u.deleteDraftRange(0, len(u.draft))
			u.setNotice("Draft cleared · Ctrl+Z restores · Ctrl-C again quits", false)
			if u.turn != "" || u.starting() || u.submission.text != "" {
				u.setNotice("Draft cleared · Ctrl+Z restores · Ctrl-C again interrupts", false)
			}
			if u.interruptLocked {
				u.setNotice("Draft cleared · Ctrl+Z restores · Locked · /unlock", false)
			}
			return false, nil
		}
		if u.cancelQueuedCompact() {
			return false, nil
		}
		if u.turn != "" || u.starting() || u.submission.text != "" || u.compaction.pending() || u.compaction.continueTask || len(u.unsent)+len(u.queued) > 0 {
			return false, u.keyboardInterrupt()
		}
		if u.interruptLocked {
			u.lockNotice()
			return false, nil
		}
		return true, nil
	case 127, 8:
		u.deleteDraft(true)
	case '\n':
		u.insertDraft("\n")
	case '\r':
		text := strings.TrimSpace(u.draft)
		if handled, err := u.controlsCommand(text); handled {
			return false, err
		}
		if u.titleCommand(text) {
			return false, nil
		}
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
		if text == "/session" {
			u.showSessionMetrics()
			return false, nil
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
			if u.turn == "" && !u.starting() && u.submission.text == "" {
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
			u.setNotice("Unknown command "+strings.Fields(text)[0]+" · /title, /compact, /clear, /resume, /btw, /status, /session, /copy, /skills, /model, /effort, /reasoning, /tier, /live, /quit", true)
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
		if text == "/compact" {
			return false, u.submitCompact(true)
		}
		if u.shellMode() || text == "" || strings.HasPrefix(text, "/") || u.turn == "" && !u.starting() && u.submission.text == "" {
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
	main := entry.Agent == "/root" && (entry.Kind == "tool" || entry.Kind == "approval" || entry.Kind == "exit" || entry.Kind == "error" || entry.Kind == "reasoning" || entry.Kind == "progress" || entry.Kind == "journal_event")
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
	u.annotateChildCompletion(&entry)
	entry.Seq = u.view.lastSeq + 1
	u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{entry}})
	if entry.Kind == "final" && (u.restoring == nil || !u.restoring.paging) {
		u.view.linkChildAnswers(u.view.entrySeq(entry))
	}
}

func (u *appServerUI) annotateChildCompletion(entry *activityPaneEntry) {
	if entry.Kind != "final" || entry.native == nil || entry.Agent == "/root" || entry.Agent == "Main" || len(entry.native.replacesItems) != 0 {
		return
	}
	items := u.proxy.commentaryReplacementItems(u.ctx, u.session.cwd, entry.native.thread, entry.native.turn, entry.native.item)
	if len(items) != 0 {
		native := *entry.native
		native.replacesItems = items
		entry.native = &native
	}
}

// applyActivity projects the collector once into the two audiences. Ordinary
// root activity stays in Main, but a directed message belongs at both ends.
func (u *appServerUI) applyActivity(entries []activityPaneEntry, agents []activityPaneAgent) {
	for i := range entries {
		u.annotateChildCompletion(&entries[i])
	}
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
	u.autoOpenApprovals()
	u.autoOpenQuestions()
	width, height = max(1, width), max(1, height)
	u.picker.rect = terminalRect{}
	u.noticeDetails, u.noticeDismiss = terminalRect{}, terminalRect{}
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
	if dock == 0 && len(u.view.entries) == 0 && height >= 6 && height > min(6, len(draft))+borderRows && !u.pickerVisible() {
		height--
		defer func() {
			frameRows = append([]string{ansi.Truncate(u.welcome(), max(1, width), "…")}, frameRows...)
			dockRect.y++
			u.composerRect.y++
			u.noticeDetails.y++
			u.noticeDismiss.y++
			if u.btw != nil && u.btw.rect.h > 0 {
				u.btw.rect.y++
			}
			u.view.feedTop++
		}()
	}
	visible := min(6, height-borderRows)
	firstRow := max(0, caret.Row-visible+1)
	draft = draft[firstRow:min(len(draft), firstRow+visible)]

	room := max(0, height-len(draft)-borderRows)
	strip := u.journalPlanStrip(width)
	if reset := u.journalResetStrip(width); reset != "" {
		strip = reset
	}
	if strip != "" && room > 0 {
		room--
	} else {
		strip = ""
	}
	var pending []string
	approvalRows := u.approvalRows(width, max(1, min(height/2, room)))
	if len(approvalRows) > 0 {
		dock = 0
		room -= len(approvalRows)
	}
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
	// Reserve a blank row immediately above the composer, including below
	// any journal strip, pending input, or live dock.
	gap := 0
	if !u.pickerVisible() && room > 2 {
		gap = 1
		room--
	}
	dock = min(dock, room)
	room -= dock
	u.mainContentPainted = room > 1
	u.view.conversation, u.view.feedOnly, u.view.status = true, true, livediff.Safe(u.status, false)
	var frame []string
	u.view.feedRows = 0
	u.view.feedQuestions, u.view.feedSnippets = nil, nil
	if room > 0 {
		frame = u.view.render(width, room, u.now())
		for row := range frame {
			frame[row] = activityui.AttachCopy(frame[row], u.view.copyRows[row])
		}
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
	if u.btw != nil {
		u.btw.rect = terminalRect{2, len(frame) + 1, max(0, width-2), max(0, len(btwRows)-2)}
	}
	frame = append(frame, btwRows...)
	u.questions.rect = terminalRect{0, len(frame), width, len(questionRows)}
	frame = append(frame, questionRows...)
	frame = append(frame, approvalRows...)
	if strip != "" {
		frame = append(frame, strip)
	}
	frame = append(frame, make([]string, gap)...)
	if u.questions.active != nil {
		u.questions.painted = true
	}
	if u.approvals.open {
		u.approvals.painted = true
	}
	// Visual reference: Grok CLI PromptStyle / PromptWidget::draw, source
	// crates/codegen/xai-grok-pager/src/views/prompt_widget/mod.rs:217:240,3007:3078
	// @be7ce6e8cffe46d20bef9834b211616082ee866b. Keep continuation rows aligned.
	// Colors: xai-grok-pager-render/src/theme/oscura.rs, same revision.
	border := "\x1b[38;2;52;48;72m"
	if u.currentQuestion() != nil || u.approvals.open {
		border = u.view.painter.Theme.Accent()
	} else if u.shellMode() {
		border = activityui.Red
	}
	const inputColor = "\x1b[39m"
	focused := u.shell == nil || u.shell.focus == 0
	if boxed {
		frame = append(frame, u.composerNoticeBorder(width, len(frame), border))
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
		if tier := u.displayServiceTier(); tier != "" {
			model += " · " + tier
		}
		model = strings.ReplaceAll(livediff.Safe(model, false), "\n", " ")
		context := contextWindowLabel(activityPaneAgent{})
		if agent := u.session.agent("/root"); agent != nil {
			context = contextWindowLabel(*agent)
			if throughput := outputThroughputLabel(agent.OutputThroughput); throughput != "" {
				context += " • " + throughput
			}
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
	if inner-ansi.StringWidth(l)-ansi.StringWidth(r)-2 < 0 {
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

func (u *appServerUI) sessionAnimating() bool {
	return u.btw != nil && (u.btw.busy || u.btw.starting) || !u.alert && (u.turn != "" || u.starting() || u.submission.text != "" || u.restoring != nil || u.thread == "")
}

func (u *appServerUI) sessionLabel(now time.Time) string {
	status := strings.ReplaceAll(livediff.Safe(u.status, false), "\n", " ")
	switch {
	case status == "":
		return ""
	case u.alert:
		return activityui.Red + "✗ " + activityui.ErrorPreview(u.status) + activityui.Reset
	case u.turn != "":
		label := "\x1b[39m◐ " + activityui.StatusPulse(status, now, u.view.painter.Colors) + activityui.Reset
		if status == "Working" {
			if u.shellCommand.standalone() {
				status = "Running shell"
			} else if u.compacting != nil {
				status = u.compactionProgressText()
				if u.journalResetTurn(u.thread, u.compacting[0]) {
					status = "Resetting context from journal"
				}
			} else if u.polling != nil {
				status = "Still running"
			}
			label = "\x1b[39m◐ " + activityui.ReasoningShimmer(status, now.Sub(u.turnStarted), u.view.painter.Colors) + activityui.Reset
		}
		if !u.turnStarted.IsZero() {
			label += activityui.Dim + " " + liveActivityAge(now.Sub(u.turnStarted)) + activityui.Undim
		}
		return label
	case u.starting() || u.submission.text != "" || u.restoring != nil || u.thread == "":
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
	shell := &terminalUI{main: u, agents: u.agents, side: true, activityOpen: true, journalOpen: true, auto: auto, diffScreen: vt.NewEmulator(1, 3)}
	shell.diff = newLiveDiffTerminalController(store, "", os.Stdout)
	shell.diff.native, shell.diff.diffMode = true, true
	shell.diff.stdout = shell.diffScreen
	shell.diff.size = func() (int, int, error) { return max(1, shell.layout.diff.w), max(3, shell.layout.diff.h), nil }
	u.shell = shell
}

func (u *appServerUI) paint(out io.Writer, width, height int) error {
	u.ensureShell()
	u.refreshRosterRoles()
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
	// Provider rounds can start within one host turn, before another usage
	// notification. Refresh from the owner so a prior round's TPS is not stale.
	usageChanged := false
	for thread, path := range u.session.paths {
		if agent := u.session.agent(path); agent != nil {
			before := *agent
			u.observeCost(thread, agent)
			usageChanged = usageChanged || before != *agent
		}
	}
	if usageChanged {
		u.agents.apply(activityPaneEvent{Kind: "agents", Agents: slices.Clone(u.session.agents)})
		u.dirty = true
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
	if u.compaction.running() || u.compaction.ackPending || u.compaction.continueTask {
		// Restore waiting input when cancellation is admitted, even if Codex
		// completes compaction successfully before acknowledging the interrupt.
		u.restoreDrafts(slices.Concat(u.unsent, u.queued)...)
		u.unsent, u.queued = nil, nil
	}
	u.compaction.cancelContinuation()
	if u.turn == "" {
		if u.starting() || u.submission.text != "" {
			u.interruption.deferUntilStart()
			u.status = "Interrupting…"
			return nil
		}
		u.restoreDrafts(slices.Concat(u.unsent, u.queued)...)
		u.unsent, u.queued = nil, nil
		return nil
	}
	if !u.interruption.begin(u.turn) {
		return nil
	}
	if err := u.reset.stop(u.turn); err != nil {
		u.setNotice("Journal stop: "+err.Error(), true)
	}
	u.endSyncQuestions(u.turn)
	u.status = "Interrupting…"
	if u.compaction.interrupting {
		_, err := u.requestAs("turn/interrupt", "compact/interrupt", map[string]any{"threadId": u.thread, "turnId": u.turn})
		return err
	}
	if u.sendSteersAfterInterrupt {
		u.steerInterruptAckPending = true
		_, err := u.requestAs("turn/interrupt", "steer/interrupt", map[string]any{"threadId": u.thread, "turnId": u.turn})
		if err != nil {
			u.steerInterruptAckPending = false
		}
		return err
	}
	return u.request("turn/interrupt", map[string]any{"threadId": u.thread, "turnId": u.turn})
}
