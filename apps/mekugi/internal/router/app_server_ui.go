package router

import (
	"cmp"
	"context"

	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"

	"github.com/yusing/mekugi/libraries/diffrender"

	commandsegments "github.com/yusing/mekugi/modules/commandsegments/segments"

	"github.com/yusing/mekugi/modules/conversation/completion"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"

	"github.com/yusing/mekugi/backends/codex/binding"

	"github.com/yusing/mekugi/modules/conversation/prompt"

	"github.com/yusing/mekugi/modules/conversation/composer"

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

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/libraries/textsanitize"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/shellhook"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	"github.com/yusing/mekugi/modules/notifications/notice"

	noticepresentation "github.com/yusing/mekugi/modules/notifications/presentation"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

	terminalui "github.com/yusing/mekugi/libraries/terminalui"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"github.com/yusing/mekugi/modules/commandsegments/reports"
	"github.com/yusing/mekugi/modules/sessionhistory/history"

	"golang.org/x/term"

	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
)

type appServerUI struct {
	terminalViews terminalsession.Views
	reviewSource  *changecapture.Service
	mainView      terminalsession.Main
	*composer.Editor
	secretEditor composer.Editor
	feedback     noticepresentation.Model
	history      *history.Service
	segments     *reports.Service
	sessioncontrol.Live
	conversationState       *conversationmodel.Service
	resumeThread, resumeCwd string
	resumeConfig            map[string]any
	resumeEvidence          preferences.Evidence
	resumePending           []appserver.Message
	replay                  *uiReplayPlayback // Offline transport controls; nil for live sessions.
	clock                   func() time.Time  // Optional presentation clock for offline replay.
	backendVersion          string
	title                   string // Host-confirmed thread name, independent of naming work.
	names                   sessioncontrol.Names
	titleGenerator          *sessionNaming
	titleUpdates            <-chan sessionTitleUpdate
	btw                     *appServerBTW
	notifications           *nativeNotifications
	promptEditor            *prompt.Editor
	questions               nativeQuestionDock
	approvals               nativeApprovalDock
	approvalMode            bool // Codex's configured policy applies, not --yolo's.
	guardHookCheck          approvalHookCheck
	statusPanel             *appServerStatusReport
	sessionCapture          *transportcapture.Recorder
	resumePicker            *appServerResumePicker
	statusConfig            appServerStatusConfig
	picker                  composerPicker
	skillEnvironment        []string
	commits                 *activitypresentation.CommitReader // Attached Activity enrichment owner.
	commandSegmentWrites    chan commandSegmentWrite           // Completed replay writes, consumed only by the UI.
	commandSegmentPending   int
	restoredSegments        map[string]*reports.Report // Turn-local, validated and adopted history.
	client                  *appserver.Client
	view                    *activityAdapter
	agents                  *activityAdapter
	proxy                   *mekugiProxy
	guard                   *shellApprovalServer
	execTrack               *commandsegments.Observer // Per-segment command reports; nil when not tracking.
	shellEdits              appServerShellEdits
	pendingEditReceipts     []*changepresentation.Receipt // Observed edits can precede their host Activity item.
	issues                  *CriticalErrors
	shell                   *terminalUI
	summaryStream           activityui.ReasoningStream
	session                 appServerSession
	ctx                     context.Context
	quitRequested           bool
	archived                bool
	interruptLocked         bool
	exitUsage               binding.TokenUsage
	resumeArgv              []string
	replayDebugDirectory    string
	model, reasoningEffort  string
	serviceTier             string
	serviceTiers            *inferencegateway.Tiers // Invocation defaults and confirmed thread/model choices.
	models                  []appServerModel
	modelsLoading           bool
	reasoningKey            *bool
	settingsChoices         string
	requests                *sessioncontrol.Requests[appServerRequest]
	retiredThreads          map[string]string
	ownedImages             map[string]bool
	pasted                  []byte // Non-nil while collecting an admitted, already-framed paste.
	dirty                   bool
	keybindings             bool
	resumeNotice            string // Settings fallback survives the old session's presentation reset.
	panes                   *nativePreferences
	waitRelease             func()
	restoring               *appServerActivityRestore
	presentationState       *sessionpresentation.Service
	childHistoryState       scope.Key[*appServerChildHistory]
}

// StartAppServerUI starts the native terminal frontend without router observers.
// Codex app-server owns execution; the launcher
// still owns routing, environment, invocation-local configuration and cancellation.
func StartAppServerUI(terminalViews terminalsession.Views, reviewSource *changecapture.Service, views *sessionpresentation.Service, conversation *conversationmodel.Service, observations *binding.Observation, activity *activitymodel.Service, service *history.Service, prefs *preferences.Service, notices *notice.Service, ctx context.Context, cmd *exec.Cmd, stdin, stdout *os.File, resumeThread string, resumeArgv []string, approvals bool) (func() error, error) {
	faint, _ := terminalui.SupportsFaint(ctx, "auto")
	issues := NewCriticalErrors(notices)
	return startAppServerUI(terminalViews, reviewSource, views, conversation, activity, service, prefs, nil, ctx, cmd, stdin, stdout, nil, issues, resumeThread, faint, nil, nil, resumeArgv, "", nil, observations, approvals)
}

// Codex approval policy and the invocation-local Shell guard are independent.
func startAppServerUI(terminalViews terminalsession.Views, reviewSource *changecapture.Service, views *sessionpresentation.Service, conversation *conversationmodel.Service, activity *activitymodel.Service, service *history.Service, prefs *preferences.Service, segments *reports.Service, ctx context.Context, cmd *exec.Cmd, stdin, stdout *os.File, proxy *mekugiProxy, issues *CriticalErrors, resumeThread string, faint bool, serviceTiers *inferencegateway.Tiers, capture *transportcapture.Recorder, resumeArgv []string, debugDirectory string, generator *sessionNaming, observations *binding.Observation, approvals bool) (func() error, error) {
	guardHash := ""
	for _, entry := range cmd.Environ() {
		if hash, ok := strings.CutPrefix(entry, shellhook.Environment+"="); ok {
			guardHash = hash
		}
	}
	if guardHash != "" && (proxy == nil || proxy.guard == nil) {
		return nil, errors.New("Shell guard approval channel is unavailable")
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
	var tracker *commandsegments.Observer
	if proxy != nil {
		tracker = proxy.execTrack
	}
	c, err := appserver.Start(cmd, func(message appserver.Message) {
		if message.Method != "item/started" {
			return
		}
		if command, ok := binding.DecodeCommandObservation(message.Method, message.Params); ok {
			if script, ok := binding.CommandScript(command.Command); ok {
				tracker.Start(command.Key, script)
			}
		}
	})
	if err != nil {
		return nil, err
	}
	u := &appServerUI{terminalViews: terminalViews, reviewSource: reviewSource, presentationState: views, conversationState: conversation, session: appServerSession{activity: activity, observations: observations}, history: service, segments: segments, client: c, view: newTranscriptAdapter(activityui.NewView()), agents: newTranscriptAdapter(terminalViews.NewThreadView()), proxy: proxy, issues: issues, requests: sessioncontrol.NewRequests[appServerRequest](c), dirty: true, ctx: ctx, resumeThread: resumeThread, serviceTiers: serviceTiers, approvalMode: approvals}
	u.sessionPresentation().Status.Set("Connecting…", false)
	u.attachActivity()
	u.guardHookCheck.hash = guardHash
	if generator != nil {
		u.titleGenerator = generator
		u.titleUpdates = generator.updates
	}
	if proxy != nil {
		u.execTrack = proxy.execTrack
		u.guard = proxy.guard
		if u.guard != nil {
			u.approvals.initGuards(u.guard.guards)
		}
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
	if prefs != nil {
		u.panes = &nativePreferences{service: prefs}
	}
	if err := u.request("initialize", nil); err != nil {
		c.Close()
		<-c.Done
		return nil, err
	}
	u.ensureShell()
	u.shell.faint = faint
	return func() error {
		u.shell.reviewLoader = newSavedReviewLoader(ctx, u.shell.saved)
		defer u.shell.reviewLoader.close()
		defer func() {
			if u.waitRelease != nil {
				u.waitRelease()
			}
		}()
		defer u.cancelPickerScan()
		defer u.shell.diffScreen.Close()
		defer func() { u.commits.Close() }()
		if proxy != nil {
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
				review := u.shell.review
				review.Preview().Enable()
				defer func() { review.Preview().Disable() }()
				u.shell.reviewLoader.awaitResync()
				sub := review.Subscribe()
				defer func() { review.Unsubscribe(sub) }()
				diffEvents, diffGap, diffReady, previewGap := sub.Events(), sub.Gap(), sub.Ready(), sub.PreviewGap()
				autoChanged := review.Changed()
				resubscribe := func() {
					review.Unsubscribe(sub)
					u.shell.reviewLoader.awaitResync()
					sub = review.Subscribe()
					diffEvents, diffGap, diffReady, previewGap = sub.Events(), sub.Gap(), sub.Ready(), sub.PreviewGap()
					u.dirty = true
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
						u.shell.saved.workspace = review.Workspace()
						u.shell.publishReview()
						u.dirty = true
					case <-diffGap:
						resubscribe()
					case <-previewGap:
						resubscribe()
					case result := <-u.shell.reviewLoader.updates():
						if sub.Invalidated() {
							resubscribe()
						}
						u.shell.loadedReview(result)
						if err := u.finishRestoredContent(); err != nil {
							return err
						}
						u.dirty = true
					case <-diffReady:
						if !u.shell.admitReviewEvents(ctx, sub, review.TakePreviews(sub)...) {
							resubscribe()
						}
						if err := u.finishRestoredContent(); err != nil {
							return err
						}
						u.dirty = true
					case event := <-diffEvents:
						if !u.shell.admitReviewEvents(ctx, sub, event) {
							resubscribe()
						}
						if err := u.finishRestoredContent(); err != nil {
							return err
						}
						u.dirty = true
					case result := <-u.shell.markdownFileUpdates():
						u.dirty = u.shell.loadedMarkdownFile(result) || u.dirty
					case result := <-u.picker.scanResults:
						u.applyPickerScan(result)
					case <-u.commits.Updates():
						u.startCommitReads()
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
						if err := u.message(message); err != nil || u.quitRequested {
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
						if len(u.conversation().Unsent) > 0 && u.conversation().Unsent[0].Question != nil && (u.proxy == nil || !u.proxy.control.EmittingToolInput(u.session.cwd, u.Thread)) {
							if err := u.flushInput(); err != nil {
								return err
							}
						}
						u.refreshSessionMetrics(false)
						u.startSkillHistory()
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
						if settleActivity(now, u.view, u.agents) {
							u.dirty = true
						}
						for _, view := range []*activityAdapter{u.view, u.agents} {
							if view.ExpireFlash(now) {
								u.dirty = true
							}
							if view.Pace(now) {
								u.dirty = true
							}
						}
						if u.mainPresentation(u.now()).Status.Animating || u.agents.HasLiveStatus() {
							u.dirty = true
						}
						if time.Since(agePaint) >= time.Second {
							u.dirty = true
							agePaint = u.now()
						}
						if err := u.drainKeys(keys); err != nil || u.quitRequested {
							return err
						}
						notices := u.applyCriticalNotices()
						if u.shell.animating(u.now()) || u.agents.RosterEasing {
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
							u.dirty = false
						}
					}
					u.writeTerminalTitle(u.now())
				}
			})
			var fileEditor *openFileEditor
			if errors.Is(err, errOpenComposerEditor) {
				u.openComposerEditor(stdin, stdout)
			} else if errors.As(err, &fileEditor) {
				u.openSelectedFileEditor(fileEditor.path, stdin, stdout)
			} else {
				break
			}
			u.dirty = true
		}
		if saveErr := u.panes.save(u.shell, u.now(), true); saveErr != nil {
			fmt.Fprintln(stdout, "Pane layout or settings could not be saved:", textsanitize.Safe(saveErr.Error(), false))
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
		for key, run := range u.session.commands {
			if run.entry.native.invocation.ReleaseSegmentOutputs() {
				u.execTrack.ReleaseOutput(key)
			}
		}
		u.shell.review.Close()
		u.hideQuestions()
		u.hideApprovals()
		if unsent := conversationmodel.Join(slices.Concat(u.conversation().Unsent, u.conversation().Queued, []conversationmodel.Input{u.draftSnapshot()})...); unsent.Text != "" {
			fmt.Fprintln(stdout, "Unsent draft:\n"+textsanitize.Safe(unsent.Text, false))
		}
		unknown := slices.Clone(u.Steers())
		if !u.Submission().Committed {
			unknown = append(unknown, u.Submission().ID)
		}
		if unknown := conversationmodel.Join(u.conversation().Parts(unknown)...); unknown.Text != "" {
			fmt.Fprintln(stdout, "Submission outcome unknown; not automatically resent:\n"+textsanitize.Safe(unknown.Text, false))
		}
		if u.conversation().Shell.Text != "" {
			fmt.Fprintln(stdout, "Shell submission outcome unknown; not automatically rerun:\n"+textsanitize.Safe(u.conversation().Shell.Text, false))
		}
		u.session.Retire()
		if err != nil {
			c.Diagnostics.Lock()
			defer c.Diagnostics.Unlock()
			if len(c.Diagnostics.Text) > 0 {
				fmt.Fprintln(stdout, textsanitize.Safe(string(c.Diagnostics.Text), false))
			}
		}
		color := term.IsTerminal(int(stdout.Fd())) && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
		return errors.Join(err, u.writeExitSummary(stdout, color))
	}, nil
}

// appServerRequest contains response routing data, not a second request ledger.
// Main's lifecycle scope retires on confirmed replacement. Side conversations
// and title saves retain their own completion and cleanup across that boundary.
type appServerRequest struct {
	label  string
	side   *appServerBTW
	title  *sessioncontrol.NameSave
	status *appServerStatusReport
}

func (u *appServerUI) request(method string, params any) error {
	if method == "initialize" {
		_, err := u.requests.Initialize(appServerRequest{})
		return err
	}
	_, err := u.requests.Send(method, params, "main", appServerRequest{})
	return err
}

func (u *appServerUI) message(m appserver.Message) (err error) {
	u.issues.observeTurnCompletion(m.Method, m.Params)
	u.observeShellMessage(m)
	var response sessioncontrol.Request[appServerRequest]
	var correlated bool
	if m.Method == "" {
		response, correlated = u.requests.Take(string(m.ID))
	}
	if u.sessionTitleMessage(m, response.Owner.title) {
		return nil
	}
	defer u.refreshPicker()
	// Side-thread traffic never reaches Main's lifecycle, questions or roster.
	if handled, err := u.btwMessage(m, response.Method, response.Owner.side); handled {
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
		u.picker.toggleOwnsManager = false
		u.picker.SkillsLoaded = false
		u.picker.SkillsProblem = ""
		u.picker.Resolved = completion.Target{}
	}
	// Input observations precede the output items that answer them. Drain
	// before a completion, not just on the next paint tick, so links bind once.
	u.applyObservedActivity()
	if held, err := u.holdOlderHistoryEvent(m); held || err != nil {
		return err
	}
	resuming := u.resumeThread != "" && u.resumeThread != resumePickerStartup && (u.Thread == "" || u.restoring != nil)
	if (resuming || u.Replacement.Target != "") && m.Method != "" {
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
		if !correlated {
			return nil
		}
		method := response.Method
		if response.Owner.label != "" {
			method = response.Owner.label
		}
		if method == "thread/archive" {
			if m.Error != nil {
				u.Replacement.Finish()
				u.setNotice("Failed to archive current thread: "+m.Error.Message, true)
			} else {
				u.archived, u.quitRequested = true, true
			}
			return nil
		}
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
		if handled, err := u.statusMessage(method, m, response.Owner.status); handled {
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
		if method == "environment/info" {
			if m.Error == nil {
				var info struct {
					Shell struct {
						Path string `json:"path"`
					} `json:"shell"`
				}
				if json.Unmarshal(m.Result, &info) == nil {
					u.shellEdits.shell = info.Shell.Path
				}
			}
			return nil
		}
		if method == "thread/compact/start" {
			u.CompactResponse(m.Error != nil)
			if m.Error != nil {
				u.restoreDrafts(append([]conversationmodel.Input{{Text: "/compact"}}, slices.Concat(u.conversation().Unsent, u.conversation().Queued)...)...)
				u.conversation().Unsent, u.conversation().Queued = nil, nil
				u.setNotice("Compaction failed: "+m.Error.Message, true)
				u.sessionPresentation().Status.SetText("Ready")
			}
			return nil
		}
		if method == "compact/interrupt" {
			u.InterruptResponse(method, m.Error != nil)
			if m.Error != nil {
				u.restoreDrafts(slices.Concat(u.conversation().Unsent, u.conversation().Queued)...)
				u.conversation().Unsent, u.conversation().Queued = nil, nil
				u.setNotice("Could not interrupt for compaction: "+m.Error.Message, true)
				u.sessionPresentation().Status.SetText("Ready")
				if u.Turn != "" {
					u.sessionPresentation().Status.SetText("Working")
				}
			}
			return nil
		}
		if method == "steer/interrupt" {
			restore := u.InterruptResponse(method, m.Error != nil)
			if m.Error != nil {
				// The completion can precede this rejection. Do not deliver
				// steers that were moved to the local stack in that interval.
				if restore {
					u.restoreDrafts(slices.Concat(u.conversation().Unsent, u.conversation().Queued)...)
					u.conversation().Unsent, u.conversation().Queued = nil, nil
				}
				u.setNotice("Could not interrupt to send steer: "+m.Error.Message, true)
			}
			return nil
		}
		if method == "approval/hooks" {
			return u.guardHookResponse(m)
		}
		if method == "thread/start" && u.Replacement.Pending() && m.Error != nil {
			u.Replacement.Finish()
			u.restoreDrafts(slices.Concat(u.conversation().Unsent, u.conversation().Queued)...)
			u.conversation().Unsent, u.conversation().Queued = nil, nil
			u.sessionPresentation().Status.SetText("Ready")
			u.setNotice("Could not clear session: "+m.Error.Message, true)
			return nil
		}
		if method == "thread/resume" && u.Replacement.Target != "" && m.Error != nil {
			return u.resumeSessionFailed(m.Error.Message)
		}
		if m.Error != nil {
			u.sessionPresentation().Status.Set(method+": "+m.Error.Message, true)
			if method == "initialize" || method == "thread/start" || method == "thread/resume" {
				return errors.New(u.sessionPresentation().Status.Text)
			}
			if method == "turn/interrupt" {
				u.InterruptResponse(method, true)
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
			if err := u.requests.Notify("initialized", map[string]any{}); err != nil {
				return err
			}
			if u.proxy != nil {
				if err := u.request("environment/info", map[string]any{"environmentId": "local"}); err != nil {
					return err
				}
			}
			if u.resumeThread == resumePickerStartup {
				u.sessionPresentation().Status.SetText("Choose a session")
				return u.openResumePicker(true)
			}
			if u.resumeThread != "" {
				if u.resumeThread == "--last" {
					u.sessionPresentation().Status.SetText("Finding latest thread…")
					params := resumableThreads(1)
					params["cwd"] = u.resumeCwd
					return u.request("thread/list", params)
				}
				return u.requestResume(u.resumeThread)
			}
			return u.startThread()
		case "thread/start", "thread/resume":
			var result struct {
				Model             string             `json:"model"`
				ReasoningEffort   string             `json:"reasoningEffort"`
				ServiceTier       string             `json:"serviceTier"`
				CollaborationMode map[string]any     `json:"collaborationMode"`
				Thread            binding.ThreadInfo `json:"thread"`
			}
			if err := json.Unmarshal(m.Result, &result); err != nil {
				return err
			}
			if result.Thread.ID == "" {
				return fmt.Errorf("%s returned no thread identity", method)
			}
			if method == "thread/resume" && result.Thread.ID != cmp.Or(u.Replacement.Target, u.resumeThread) {
				return errors.New("thread/resume returned a different thread identity")
			}
			if u.Replacement.Pending() {
				if err := u.clearSessionPresentation(); err != nil {
					return err
				}
				u.LeaveThread()
				u.resumeThread, u.resumeCwd = "", ""
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
			u.Thread = result.Thread.ID
			u.sessionPresentation().Status.SetText("Ready")
			u.title = result.Thread.Name
			u.titleGenerator.register(result.Thread)
			if name := u.names.Attach(u.Thread); name != "" {
				u.titleGenerator.nameChanged(u.Thread, name)
				u.requestSessionTitle(u.names.Next(u.Thread))
			}
			u.model, u.reasoningEffort, u.serviceTier = result.Model, result.ReasoningEffort, result.ServiceTier
			if method == "thread/resume" {
				u.Settings.RestoreEffort = u.TakeResumeEffort()
			}
			if method == "thread/start" {
				u.Settings.RestoredEffort()
				if u.proxy != nil {
					u.proxy.usage.MarkNew(result.Thread.ID)
				}
			}
			u.models, u.modelsLoading = nil, true // A cleared or switched session lists them again.
			if err := u.request("model/list", map[string]any{"includeHidden": true}); err != nil {
				return err
			}
			u.session.start(u.Thread, result.Thread.Cwd)
			u.summaryStream = activityui.ReasoningStream{}
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
			u.observeCost(u.Thread, u.session.agent("/root"))
			if u.agents != nil {
				u.agents.apply(activityPaneEvent{Kind: "agents", Agents: u.session.roster()})
			}
			if u.proxy != nil {
				u.proxy.activity.attachNativePane(u.Thread)
			}
			// Resumed history is applied once the roster is read, so Main can
			// follow child activity in order. An active snapshot keeps its
			// steer/interrupt target meanwhile.
			if method == "thread/resume" {
				for _, turn := range result.Thread.Turns {
					if turn.Status == "inProgress" {
						u.Turn = turn.ID
						u.sessionPresentation().Status.Started = u.now()
						u.sessionPresentation().Status.SetText("Working")
						u.session.lifecycle(u.Thread).ResumeActive()
					}
				}
			}
			if u.panes != nil {
				u.ensureShell()
				u.paneError(u.panes.open(u.shell, result.Thread.Cwd, u.Thread, method == "thread/resume"))
			}
			if method == "thread/resume" {
				if u.Settings.RestoreEffort {
					// effort:null means no change in the host RPC. Its complete
					// collaboration-mode snapshot can express a nullable effort,
					// without replacing the host's mode or developer instructions.
					settings, ok := result.CollaborationMode["settings"].(map[string]any)
					if !ok {
						if u.reasoningEffort != "" {
							return errors.New("Codex did not return the settings needed to restore default reasoning")
						}
						u.Settings.RestoredEffort()
					} else if settings["reasoning_effort"] != nil {
						// Codex checkpoints resume defaults before this response.
						// Do not let that transient effort replace saved null intent
						// on a fresh-process retry if the corrective update fails.
						if u.panes != nil && u.panes.attachment != nil && u.resumeEvidence.Model != "" {
							if err := u.panes.stage(u.shell.paneState()); err != nil {
								u.paneError(err)
							} else {
								u.panes.attachment.Recover(u.resumeEvidence, u.now())
								u.paneError(u.panes.attachment.Save(u.now(), true))
							}
						}
						settings["reasoning_effort"] = nil
						if _, err := u.updateSettings(map[string]any{"collaborationMode": result.CollaborationMode}); err != nil {
							return err
						}
					} else {
						u.Settings.RestoredEffort()
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
		u.sessionPresentation().Status.Set("Blocked on unsupported request "+m.Method+" · Ctrl-C interrupts", true)
		u.blockNotification(m)
		u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: u.view.LastSeq + 1, Agent: "Session", Kind: "text", Text: u.sessionPresentation().Status.Text, Observed: u.now()}}})
		return nil
	}
	if handled, err := u.settingsMessage("", m); handled || err != nil {
		return err
	}
	switch m.Method {
	case "thread/started", "thread/status/changed", "thread/tokenUsage/updated", "item/reasoning/summaryTextDelta", "item/reasoning/summaryPartAdded",
		"turn/started", "turn/completed", "item/started", "item/completed", "item/agentMessage/delta", "item/commandExecution/terminalInteraction",
		"item/commandExecution/outputDelta", "hook/started", "hook/completed":
	default:
		return nil
	}
	p, err := binding.DecodeNotification(m.Method, m.Params)
	if err != nil {
		return fmt.Errorf("%s: %w", m.Method, err)
	}
	return u.observedEvent(m.Method, p)
}

// observedEvent retains application lifecycle and control handling for decoded
// host facts. Offline transcript observations bypass it through their reducer.
func (u *appServerUI) observedEvent(method string, p binding.Notification) error {
	text := binding.RetainedTranscript(p.Observation(method, u.now()))
	if handled, err := u.sessionEvent(method, p); handled || err != nil {
		return err
	}

	switch method {
	case "turn/started":
		if p.ThreadID == u.Thread {
			u.Started(p.Turn.ID)
			u.sessionPresentation().Status.Started = u.now()
			u.sessionPresentation().Status.Set("Working", false)
			if u.Interruption.BeforeStart || u.Compaction.InterruptTarget() != "" {
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
		if p.ThreadID == u.Thread && p.Turn.ID == u.Turn {
			u.Completed(p.Turn.Status == "completed")
			u.endSyncQuestions(p.Turn.ID)
			var failure *string
			if p.Turn.Error != nil {
				failure = &p.Turn.Error.Message
			}
			u.sessionPresentation().Status.Complete(p.Turn.Status, failure, u.now())
			u.settleInput(p.Turn.ID, p.Turn.Status == "interrupted")
			if p.Turn.Status == "completed" && u.questionCount() == 0 {
				u.notify("agent-turn-complete", "Agent turn complete")
			}
			for _, c := range u.questionCalls() {
				if !c.Resolved {
					u.renderQuestionRecord(c)
				}
			}
		}
	case "item/started", "item/completed", "item/agentMessage/delta":
		if method == "item/started" {
			u.observeShellItem(p.ThreadID, p.Item)
		}
		if p.ItemID == "" {
			p.ItemID = p.Item.ID
		}
		// Only completion commits, so one message cannot match two identical steers.
		if p.ThreadID == u.Thread && p.Item.Type == "userMessage" && method == "item/completed" {
			u.commitSteer(p.Item)
		}
		u.messageTranscript(text, p.Item.Observation(), true)
	}
	return nil
}

// key interprets one complete terminal key. Framing belongs to terminalsession.Decoder.
func (u *appServerUI) key(key string) (bool, error) {
	if !u.prepareComposerInput() {
		return false, nil
	}
	if handled, err := u.approvalKey(key); handled {
		return false, err
	}
	if handled, err := u.questionKey(key); handled {
		return false, err
	}
	defer u.refreshPicker()
	if u.statusPanelKey(key) {
		return false, nil
	}
	if strings.HasPrefix(key, "\x1b") {
		u.keybindings = false
	}
	if key == "\x1b\x7f" || key == "\x1b\x08" {
		u.deleteWord(true)
		return false, nil
	}
	// macOS terminals commonly encode Option+Left/Right as Meta-b/f.
	if key == "\x1bb" || key == "\x1bf" {
		u.moveDraft(key)
		return false, nil
	}
	// Unbound Meta input retains the ordinary key after dismissing help.
	if len(key) == 2 && key[0] == 27 && key[1] != '[' && key[1] != 'O' {
		u.pickerKey("\x1b")
		key = key[1:]
	}
	// An exact /compact has a submission meaning for Tab, not just completion.
	compactTab := key == "\t" && strings.TrimSpace(u.Text) == "/compact" && u.picker.modal == ""
	if !compactTab && u.pickerKey(key) {
		return false, nil
	}
	if u.currentQuestion() == nil && !u.approvals.Open && key == "?" && u.Text == "" && (u.shell == nil || u.shell.Focus == 0) {
		u.keybindings = !u.keybindings
		return false, nil
	}
	u.keybindings = false
	if strings.HasPrefix(key, "\x1b") {
		switch key {
		case "\x1b[1;2A", "\x1b[1;2B":
			return false, u.stepReasoning(strings.HasSuffix(key, "A"))
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
		case "\x1b[5~":
			u.transcriptView().ScrollKey('b')
		case "\x1b[6~":
			u.transcriptView().ScrollKey(' ')
		default:
			// Only caret movement ends an edit run; forward deletes still group.
			u.moveDraft(key)
		}
		return false, nil
	}
	switch key {
	case "\x05": // Ctrl+E discloses the displayed transcript, leaving the draft intact.
		u.transcriptView().ToggleExpansion()
	case "\x17": // macOS terminals may encode Option+Backspace as Ctrl+W.
		u.deleteWord(true)
	case "\x0b": // Ctrl+K kills to the logical line end, or joins at its newline.
		at := u.Cursor()
		end := len(u.Text)
		if next := strings.IndexByte(u.Text[at:], '\n'); next >= 0 {
			end = at + next
			if next == 0 {
				end++
			}
		}
		for _, span := range u.Spans() {
			start, tokenEnd := span.Start, span.End
			if end > start && end < tokenEnd {
				end = tokenEnd
			}
		}
		u.deleteDraftRange(at, end)
	case "\x1a":
		u.undoDraft(false)
	case "\x19":
		u.undoDraft(true)
	case "\x07":
		if q := u.currentQuestion(); q != nil && q.IsSecret {
			u.setNotice("External editor is unavailable for secret answers", false)
			return false, nil
		}
		return false, errOpenComposerEditor
	case "\x16":
		u.pasteImage()
	case "\x03":
		if u.Replacement.Archiving() {
			u.setNotice("Archiving session…", false)
			return false, nil
		}
		// As in Codex, Ctrl-C first clears the draft (undoable with Ctrl+Z),
		// then cancels queued compaction, interrupts the active turn, or quits.
		if u.Text != "" {
			u.deleteDraftRange(0, len(u.Text))
			u.setNotice("Draft cleared · Ctrl+Z restores · Ctrl-C again quits", false)
			if u.Turn != "" || u.Starting() || u.InputPending() {
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
		if u.Turn != "" || u.Starting() || u.InputPending() || u.Compaction.Pending() || u.Compaction.ContinuationPending() || len(u.conversation().Unsent)+len(u.conversation().Queued) > 0 {
			return false, u.keyboardInterrupt()
		}
		if u.interruptLocked {
			u.lockNotice()
			return false, nil
		}
		return true, nil
	case "\x7f", "\x08":
		u.deleteDraft(true)
	case "\n":
		u.insertDraft("\n")
	case "\r":
		if u.Replacement.Archiving() {
			u.setNotice("Archiving session…", false)
			return false, nil
		}
		text := strings.TrimSpace(u.Text)
		if text == "/archive" {
			u.showArchivePicker()
			return false, nil
		}
		if handled, err := u.controlsCommand(text); handled {
			return false, err
		}
		if u.titleCommand(text) {
			return false, nil
		}
		if text == "/btw" || strings.HasPrefix(text, "/btw ") || strings.HasPrefix(text, "/btw\n") || strings.HasPrefix(text, "/btw\t") {
			return false, u.submitBTW()
		}
		if u.conversation().ShellMode() {
			return false, u.submitShell()
		}
		if text == "/compact" || text == "/clear" {
			return false, u.sessionCommand(text)
		}
		if text == "/resume" || strings.HasPrefix(text, "/resume ") {
			return false, u.resumeCommand(text)
		}
		if text == "/ps" {
			u.showRunningProcesses()
			return false, nil
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
			u.deleteDraftRange(0, len(u.Text))
			u.picker.modal, u.picker.Open, u.picker.Selected, u.picker.Top = "menu", true, 0, 0
			return false, nil
		}
		if text == "/quit" {
			if u.Turn == "" && !u.Starting() && !u.InputPending() {
				u.Text = ""
				u.Editor.Changed()
				return true, nil
			}
			u.setNotice("Interrupt the active turn before quitting", false)
			return false, nil
		}
		if handled, err := u.settingsCommand(text); handled {
			return false, err
		}
		if strings.HasPrefix(text, "/") {
			u.setNotice("Unknown command "+strings.Fields(text)[0]+" · /title, /compact, /clear, /resume, /archive, /btw, /status, /session, /ps, /copy, /skills, /model, /effort, /reasoning, /tier, /live, /quit", true)
			return false, nil
		}
		if text == "" || u.Thread == "" || u.restoring != nil {
			return false, nil
		}
		view := u.mainPresentation(u.now())
		return false, u.submitInput(sessionpresentation.SubmitIntent{Version: view.Version, Revision: view.Draft.Revision, Mode: sessioncontrol.SendInput})
	case "\t":
		// Tab queues busy input for the next turn; otherwise it sends like Enter.
		text := strings.TrimSpace(u.Text)
		if text == "/compact" {
			return false, u.submitCompact(true)
		}
		if u.conversation().ShellMode() || text == "" || strings.HasPrefix(text, "/") || u.Turn == "" && !u.Starting() && !u.InputPending() {
			return u.key("\r")
		}
		view := u.mainPresentation(u.now())
		return false, u.submitInput(sessionpresentation.SubmitIntent{Version: view.Version, Revision: view.Draft.Revision, Mode: sessioncontrol.QueueNextTurn})
	default:
		if len(key) > 0 && key[0] >= 32 {
			u.insertDraft(key)
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
	return u.restoring != nil && !u.restoring.state.View().Rendered
}

// mainActivityEntry projects one collector entry into Main's audience.
// Ordinary root activity stays in Main, but a directed message belongs at
// both ends, and a child's start and answer show where Main follows them.
func mainActivityEntry(entry activityPaneEntry) (activityPaneEntry, bool) {
	main := entry.Agent == "/root" && (entry.Kind == "output_dedupe" || entry.Kind == "tool" || entry.Kind == "hook" || entry.Kind == "exit" || entry.Kind == "error" || entry.Kind == "reasoning" || entry.Kind == "progress")
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
	entry.Seq = u.view.LastSeq + 1
	u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{entry}})
}

// applyActivity projects the collector once into the two audiences. Ordinary
// root activity stays in Main, but a directed message belongs at both ends.
func (u *appServerUI) applyActivity(entries []activityPaneEntry, agents []activityPaneAgent) {
	paneEntries := slices.Clone(entries)
	for i := range paneEntries {
		paneEntries[i].Agent = paneActivityAgent(paneEntries[i])
	}
	u.agents.apply(activityPaneEvent{Kind: "entries", Entries: paneEntries, Agents: agents})
	for i, entry := range entries {
		if entry, main := mainActivityEntry(entry); main {
			if mainActivityLinked(entry) {
				entry.activitySeq = u.agents.entrySeq(paneEntries[i])
			}
			u.applyMainActivity(entry)
		}
	}
	u.applyCapturedEdits()
	u.flushApprovalUpdates()
}

// mainFrame is the transcript above a boxed composer. The top border carries
// the session state; the bottom border the model. dock rows are left blank
// between the two, in the returned rectangle, for the live edit dock.
func (u *appServerUI) mainFrame(width, height, dock int) ([]string, terminalsession.Rect) {
	transcript := u.transcriptView()
	readOnly := u.agents != nil && u.agents.displayingChild()
	u.autoOpenApprovals()
	u.autoOpenQuestions()
	presentation := u.mainPresentation(u.now())
	width, height = max(1, width), max(1, height)
	u.picker.rect = terminalsession.Rect{}
	u.mainView.NoticeDetails, u.mainView.NoticeDismiss = terminalsession.Rect{}, terminalsession.Rect{}
	u.questions.rect = terminalsession.Rect{}
	if !readOnly && u.statusPanel != nil {
		return u.statusPanelFrame(width, height), terminalsession.Rect{}
	}
	if !readOnly && u.resumePicker != nil {
		return u.resumePickerFrame(width, height), terminalsession.Rect{}
	}
	if u.pickerVisible() && u.picker.modal != "" {
		return u.skillsModalFrame(width, height), terminalsession.Rect{}
	}
	in := terminalsession.MainInput{
		Session: presentation, Draft: u.Draft, Now: u.now(), ReadOnly: readOnly,
		Focused: u.shell == nil || u.shell.Focus == 0, Empty: len(transcript.Entries) == 0,
		Picker: u.pickerVisible(), Welcome: u.welcome(), Selection: u.pickerSelection(), TurnIdle: u.Turn == "",
	}
	if q := u.currentQuestion(); q != nil {
		in.Question, in.Secret, in.QuestionLayout = true, q.IsSecret, u.questionLayout(q)
	}
	if presentation.Approvals.Count > 0 {
		in.ApprovalLayout = &u.approvals.handles[u.approvals.Pending[0]].layout
		in.ApprovalAgent = u.approvalAgent(presentation.Approvals.Approval.Thread)
	}
	if b := u.btw; b != nil {
		in.Side = &terminalsession.SideInput{View: b.View(), Running: b.Busy || b.Starting, Scroll: b.scroll}
	}
	frame := u.mainView.Layout(in, &u.view.Painter, width, height, dock)
	transcript.Conversation, transcript.FeedOnly, transcript.StatusLabel = true, true, textsanitize.Safe(terminalsession.StatusText(presentation.Status.State), false)
	transcript.FeedRows = 0
	transcript.FeedQuestions, transcript.FeedSnippets = nil, nil
	if room := frame.Feed.H; room > 0 {
		rows := transcript.Render(width, room, in.Now)
		for row := range rows {
			rows[row] = terminalui.AttachCopy(rows[row], transcript.CopyRows[row])
		}
		if !readOnly && u.keybindings && in.Focused {
			u.mainView.ContentPainted = false
			rows = terminalsession.Keybindings(width, room)
		}
		if in.Picker {
			u.mainView.ContentPainted = false
			popupHeight := min(room, u.pickerHeight(width))
			popup := u.renderPicker(width, popupHeight)
			u.picker.rect = terminalsession.Rect{0, frame.Feed.Y + len(rows) - popupHeight, width, popupHeight}
			copy(rows[len(rows)-popupHeight:], popup)
		}
		copy(frame.Rows[frame.Feed.Y:], rows)
	}
	transcript.FeedTop += frame.Feed.Y
	u.questions.rect = frame.Questions
	if b := u.btw; b != nil {
		b.rect = frame.SideRect
		if !readOnly && frame.Side.Rows != nil {
			b.copyAnswer, b.rows, b.scroll = frame.Side.CopyAnswer, frame.Side.AnswerRows, frame.Side.Scroll
		}
	}
	return frame.Rows, frame.Dock
}

// scrollLabel shows how much of the feed lies above the viewport and, once
// the reader scrolls back, how much lies below and what arrived since.
func scrollLabel(v *activityAdapter) string {
	below := v.FeedLines - v.Offset - v.FeedRows
	switch {
	case !v.Following && below > 0:
		label := fmt.Sprintf("▼ %d", below)
		if v.Unseen > 0 {
			label += fmt.Sprintf(" · %d new", v.Unseen)
		}
		return terminalui.Amber + label + terminalui.Reset
	case v.Offset > 0:
		return terminalui.Dim + fmt.Sprintf("▲ %d", v.Offset) + terminalui.Undim
	}
	return ""
}

func (u *appServerUI) ensureShell() {
	u.sessionPresentation()
	u.attachActivity()
	u.view.Conversation = true
	u.view.Painter.FileLink = u.markdownFileAllowed
	if u.shell != nil {
		return
	}
	if u.agents == nil {
		u.agents = newTranscriptAdapter(u.terminalViews.NewThreadView())
		u.sessionPresentation()
		u.attachActivity()
	}
	u.agents.Painter.FileLink = u.markdownFileAllowed
	u.agents.ChildrenOnly = true
	u.agents.StatusLabel = "" // Fed by app-server directly, never by a collector connection.
	u.agents.mainView = u.view
	var store *mekugiReplayStore
	var review *savedReview
	if u.proxy != nil {
		store = u.proxy.replayStore
		review = u.proxy.review
	}
	if review == nil {
		review = newSavedReview(u.ctx, nil)
	}
	u.agents.Bare = true
	shell := &terminalUI{main: u, agents: u.agents, review: review, diffScreen: new(terminalsession.DiffScreen)}
	shell.RosterLabel = u.terminalViews.RosterLabel()
	shell.Theme = diffrender.EnvironmentTheme(os.Getenv("COLORFGBG"))
	label, attachment := u.terminalViews.Attach(shell.Theme)
	shell.AuxiliaryLabel, shell.Auxiliary, shell.preparePane = label, attachment.Pane, attachment.Prepare
	shell.saved = savedDiff{store: store, data: new(changepresentation.Snapshot), coverage: "CONNECTING"}
	u.shell = shell
}

func (u *appServerUI) paint(out io.Writer, width, height int) error {
	u.ensureShell()
	u.refreshRosterRoles()
	u.agents.mainView = u.view
	u.view.StatusLabel = textsanitize.Safe(terminalsession.StatusText(u.mainPresentation(u.now()).Status.State), false)
	u.shell.width, u.shell.height = max(1, width), max(1, height)
	u.mainView.ContentPainted = false
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
		u.agents.apply(activityPaneEvent{Kind: "agents", Agents: u.session.roster()})
		u.dirty = true
	}
	entries := u.proxy.activity.takeNativeActivity(u.Thread)
	for _, start := range u.proxy.activity.takeRequestStarts(u.Thread) {
		entries = append(entries, u.transcriptUpdates(u.session.providerStarted(start.thread, start.at), false)...)
	}
	for i := range entries {
		entries[i].Seq = u.session.next()
	}
	if len(entries) != 0 {
		u.applyActivity(entries, nil)
		u.dirty = true
	}
	for _, ref := range u.proxy.activity.takeUnreturned(u.Thread) {
		if main, activity := u.view.markUnreturned(ref.thread, ref.call), u.agents.markUnreturned(ref.thread, ref.call); main || activity {
			u.dirty = true
		}
	}
}
