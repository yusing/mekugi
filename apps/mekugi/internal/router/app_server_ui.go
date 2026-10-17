package router

import (
	"cmp"
	"context"
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

	"github.com/yusing/mekugi/libraries/shellinstrumentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/executionobservation"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/backends/codex/nativetrace"
	"github.com/yusing/mekugi/backends/codex/shellhook"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/libraries/diffrender"
	terminalui "github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/libraries/textsanitize"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/modules/commandsegments/reports"
	commandsegments "github.com/yusing/mekugi/modules/commandsegments/segments"
	"github.com/yusing/mekugi/modules/conversation/completion"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/notifications/notice"
	noticepresentation "github.com/yusing/mekugi/modules/notifications/presentation"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
	"github.com/yusing/mekugi/modules/usage/accounting"
	"golang.org/x/term"
)

type appServerUI struct {
	attached      attachedPresentation
	terminalViews terminalsession.Views
	preparePane   func(*scope.Store)
	reviewSource  *changecapture.Service
	*composer.Editor
	history  *history.Service
	segments *reports.Service
	sessioncontrol.Client[appServerRequest]
	conversationState     *conversationmodel.Service
	resumePending         []appserver.Message
	clock                 func() time.Time // Optional deterministic presentation clock.
	backendVersion        string
	title                 string // Host-confirmed thread name, independent of naming work.
	titleGenerator        *sessionNaming
	titleUpdates          <-chan sessionTitleUpdate
	side                  *sessioncontrol.Side
	notifications         *nativeNotifications
	questions             nativeQuestionDock
	approvals             nativeApprovalDock
	approvalMode          bool // Codex's configured policy applies, not --yolo's.
	guardHookCheck        approvalHookCheck
	statusPanel           *appServerStatusReport
	sessionCapture        *transportcapture.Recorder
	resumePicker          *appServerResumePicker
	statusConfig          appServerStatusConfig
	picker                composerPicker
	skillEnvironment      []string
	commits               *activitypresentation.CommitReader // Attached Activity enrichment owner.
	commandSegmentWrites  chan commandSegmentWrite           // Completed replay writes, consumed only by the UI.
	commandSegmentPending int
	restoredSegments      map[string]*reports.Report // Turn-local, validated and adopted history.
	controlObservations   *sessioncontrol.Observations
	usage                 *accounting.Service
	audience              *sessioncontrol.Audience
	review                *sessionreview.Review
	execWindows           *executionobservation.Windows
	nativeTrace           *nativetrace.Reader
	retained              changes.Scope
	changeSource          changes.Service
	snapshots             *changecapture.Workspaces
	recovered             *recovery.Service
	publishChanges        func([]changes.Update)
	captureNotice         func(string, string, string, string)
	guard                 *sessioncontrol.ShellApprovals
	execTrack             *commandsegments.Observer // Per-segment command reports; nil when not tracking.
	shellEdits            appServerShellEdits
	pendingEditReceipts   []*changepresentation.Receipt // Observed edits can precede their host Activity item.
	issues                *requestexecution.Failures
	shell                 *terminalsession.Attachment
	detailFiles
	streams                terminalsession.Streams
	activity               *activitymodel.Service
	observations           *binding.Observation
	retainedSession        history.Session
	ctx                    context.Context
	quitRequested          bool
	archived               bool
	exitUsage              binding.TokenUsage
	resumeArgv             []string
	replayDebugDirectory   string
	model, reasoningEffort string
	serviceTier            string
	serviceTiers           *inferencegateway.Tiers // Invocation defaults and confirmed thread/model choices.
	models                 []appServerModel
	modelsLoading          bool
	reasoningKey           *bool
	ownedImages            map[string]bool
	dirty                  bool
	panes                  *nativePreferences
	restoring              *appServerActivityRestore
	presentationState      *sessionpresentation.Service
	childHistoryState      scope.Key[*appServerChildHistory]
}

// StartAppServerUI starts the native terminal frontend without router observers.
// Codex app-server owns execution; the launcher
// still owns routing, environment, invocation-local configuration and cancellation.
func StartAppServerUI(terminalViews terminalsession.Views, reviewSource *changecapture.Service, views *sessionpresentation.Service, conversation *conversationmodel.Service, observations *binding.Observation, activity *activitymodel.Service, service *history.Service, prefs *preferences.Service, notices *notice.Service, ctx context.Context, cmd *exec.Cmd, stdin, stdout *os.File, resumeThread string, resumeArgv []string, approvals bool) (func() error, error) {
	faint, _ := terminalui.SupportsFaint(ctx, "auto")
	issues := requestexecution.NewFailures(notices)
	return startAppServerUI(terminalViews, reviewSource, views, conversation, activity, service, prefs, nil, ctx, cmd, stdin, stdout, appServerIntegration{}, issues, resumeThread, faint, nil, nil, resumeArgv, "", nil, observations, approvals)
}

// Codex approval policy and the invocation-local Shell guard are independent.
func startAppServerUI(terminalViews terminalsession.Views, reviewSource *changecapture.Service, views *sessionpresentation.Service, conversation *conversationmodel.Service, activity *activitymodel.Service, service *history.Service, prefs *preferences.Service, segments *reports.Service, ctx context.Context, cmd *exec.Cmd, stdin, stdout *os.File, integration appServerIntegration, issues *requestexecution.Failures, resumeThread string, faint bool, serviceTiers *inferencegateway.Tiers, capture *transportcapture.Recorder, resumeArgv []string, debugDirectory string, generator *sessionNaming, observations *binding.Observation, approvals bool) (func() error, error) {
	guardHash := ""
	for _, entry := range cmd.Environ() {
		if hash, ok := strings.CutPrefix(entry, shellhook.Environment+"="); ok {
			guardHash = hash
		}
	}
	if guardHash != "" && integration.guard == nil {
		return nil, errors.New("Shell guard approval channel is unavailable")
	}
	var resumeCwd string
	if resumeThread == "--last" || resumeThread == sessioncontrol.ResumePicker {
		var err error
		resumeCwd, err = filepath.Abs(cmd.Dir)
		if err != nil {
			return nil, fmt.Errorf("resolve resume workspace: %w", err)
		}
	}
	// Live settings and trusted reasoning configuration_update items are Codex
	// opt-ins. Enable them only for this native app-server invocation.
	cmd.Args = append(cmd.Args, "-c", "features.step_model_switching=true", "-c", "features.reasoning_effort_override=true")
	tracker := integration.execTrack
	control, err := sessioncontrol.Start[appServerRequest](cmd, func(message appserver.Message) {
		if message.Method != "item/started" {
			return
		}
		if command, ok := binding.DecodeCommandObservation(message.Method, message.Params); ok {
			if script, ok := shellinstrumentation.CommandScript(command.Command); ok {
				tracker.Start(command.Key, script)
			}
		}
	})
	if err != nil {
		return nil, err
	}
	shell, preparePane := terminalViews.NewAttachment(diffrender.EnvironmentTheme(os.Getenv("COLORFGBG")))
	attached := newAttachedPresentation(views, conversation, observations)
	u := &appServerUI{attached: attached, terminalViews: terminalViews, reviewSource: reviewSource, presentationState: views, conversationState: conversation, activity: activity, observations: observations, history: service, segments: segments, Client: control, shell: shell, preparePane: preparePane, controlObservations: integration.controlObservations, usage: integration.usage, audience: integration.audience, review: integration.review, execWindows: integration.execWindows, nativeTrace: integration.nativeTrace, retained: integration.retained, changeSource: integration.changeSource, snapshots: integration.snapshots, recovered: integration.recovered, publishChanges: integration.publishChanges, captureNotice: integration.captureNotice, issues: issues, dirty: true, ctx: ctx, serviceTiers: serviceTiers, approvalMode: approvals}
	c := u.Host
	u.Startup.Thread = resumeThread
	attachPresentation(&u.attached, u.shell, &u.picker, &u.Editor, &u.questions, &u.approvals, u.Thread, &u.review, &u.commits, u.ctx, u.retained, u.changeSource, u.activity != nil)
	u.attached.presentation.Status.Set("Connecting…", false)
	u.guardHookCheck.hash = guardHash
	if generator != nil {
		u.titleGenerator = generator
		u.titleUpdates = generator.updates
	}
	u.execTrack = integration.execTrack
	u.guard = integration.guard
	if u.guard != nil {
		u.approvals.grants = sessioncontrol.NewApprovalGrants(integration.guards)
	}
	if integration.skillsManager {
		u.skillEnvironment = cmd.Environ()
	}
	u.resumeArgv = slices.Clone(resumeArgv)
	u.replayDebugDirectory = debugDirectory
	u.sessionCapture = capture
	u.Startup.Config = appServerResumeConfig(cmd.Args, resumeArgv)
	u.notifications = &nativeNotifications{out: stdout, focused: true}
	u.Startup.Workspace = resumeCwd
	if prefs != nil {
		u.panes = &nativePreferences{service: prefs}
	}
	if err := u.Request("initialize", nil); err != nil {
		c.Close()
		<-c.Done
		return nil, err
	}

	u.shell.Faint = faint
	settlePresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil)
	return func() error {
		u.review.Loader = sessionreview.NewLoader(ctx, u.review.Projection)
		defer u.review.Loader.Close()
		defer func() {
			u.retainedSession.Close()
		}()
		defer u.picker.cancelScan()
		defer u.shell.Diff.Close()
		defer func() { u.commits.Close() }()
		if u.audience != nil {
			defer u.audience.Detach()
		}
		defer c.Input.Close()
		defer func() { discardDraftImages(u.ownedImages) }()
		defer c.Output.Close()
		exited := false
		var err error
		for {
			err = terminalui.WithRawPane(ctx, stdin, stdout, "\x1b[?1049h\x1b[?25l\x1b[?1003;1004;1006;2004h\x1b]10;?\x1b\\\x1b]11;?\x1b\\", "\x1b[?2026l\x1b[?1003;1004;1006;2004l\x1b[0m\x1b[?25h\x1b[?1049l", func(keys <-chan byte) error {
				defer func() { u.notifications.clearTerminalTitle(&u.attached.feedback, u.shell, u.clock) }()
				review := u.review
				review.Preview().Enable()
				defer func() { review.Preview().Disable() }()
				u.review.Loader.AwaitResync()
				sub := review.Subscribe()
				defer func() { review.Unsubscribe(sub) }()
				diffEvents, diffGap, diffReady, previewGap := sub.Events(), sub.Gap(), sub.Ready(), sub.PreviewGap()
				autoChanged := review.Changed()
				resubscribe := func() {
					review.Unsubscribe(sub)
					u.review.Loader.AwaitResync()
					sub = review.Subscribe()
					diffEvents, diffGap, diffReady, previewGap = sub.Events(), sub.Gap(), sub.Ready(), sub.PreviewGap()
					u.dirty = true
				}
				tick := time.NewTicker(33 * time.Millisecond)
				defer tick.Stop()
				width, height := 0, 0
				agePaint := presentationTime(u.clock)
				for {
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-autoChanged:
						u.review.Projection.Workspace = review.Workspace()
						publishReview(&u.attached, u.shell, u.review, u.reviewSource, u.preparePane)
						u.dirty = true
					case <-diffGap:
						resubscribe()
					case <-previewGap:
						resubscribe()
					case result := <-u.review.Loader.Updates():
						if sub.Invalidated() {
							resubscribe()
						}
						loadedReview(&u.attached, u.shell, u.review, u.reviewSource, u.preparePane, &u.pendingEditReceipts, result)
						if err := u.finishRestoredContent(); err != nil {
							return err
						}
						u.dirty = true
					case <-diffReady:
						if !admitReviewEvents(&u.attached, u.shell, u.review, u.reviewSource, u.preparePane, &u.pendingEditReceipts, u.clock, ctx, sub, review.TakePreviews(sub)...) {
							resubscribe()
						}
						if err := u.finishRestoredContent(); err != nil {
							return err
						}
						u.dirty = true
					case event := <-diffEvents:
						if !admitReviewEvents(&u.attached, u.shell, u.review, u.reviewSource, u.preparePane, &u.pendingEditReceipts, u.clock, ctx, sub, event) {
							resubscribe()
						}
						if err := u.finishRestoredContent(); err != nil {
							return err
						}
						u.dirty = true
					case result := <-u.fileReads:
						u.dirty = u.detailFiles.loadedMarkdownFile(&u.attached.session, u.presentationState, u.attached.cwd, u.shell, result) || u.dirty
					case result := <-u.picker.scanResults:
						u.dirty = u.picker.applyScan(u.attached.cwd, u.picker.completionTarget(u.Draft, u.attached.conversation.ShellMode()), result) || u.dirty
					case <-u.commits.Updates():
						u.dirty = startCommitReads(&u.attached, u.commits) || u.dirty
					case err := <-u.commandSegmentWrites:
						commandSegmentRetained(&u.commandSegmentPending, u.issues, &u.dirty, err)
					case update := <-u.titleUpdates:
						persistSessionTitle(&u.Client, u.titleGenerator, &u.attached.feedback, u.shell, u.clock, &u.dirty, update)
					case request := <-u.guard.Requests():
						addGuardApproval(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, u.statusPanel != nil || u.resumePicker != nil, u.notifications, u.review, &u.pendingEditReceipts, request)
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
						if event := u.shell.Decoder.Feed(key, time.Now()); event.Kind != terminalsession.NoInput {
							if err := u.applyInput(u.shell, u.shell.FrameInput(presentationTime(u.clock), mekugiVersion()).Dispatch(&u.shell.Interaction, event)); err != nil || u.quitRequested {
								return err
							}
						}
						u.dirty = true
					case <-tick.C:
						if len(u.attached.conversation.Unsent) > 0 && u.attached.conversation.Unsent[0].Question != nil && (u.controlObservations == nil || !u.controlObservations.EmittingToolInput(u.attached.cwd, u.Thread)) {
							if err := flushInput(&u.attached, u.shell, &u.Client, &u.picker, &u.questions, &u.approvals, u.clock, u.model, u.serviceTiers, u.skillEnvironment, u.ownedImages, &u.guardHookCheck, &u.Editor, u.ctx, u.restoring != nil, u.controlObservations, u.review, &u.pendingEditReceipts); err != nil {
								return err
							}
						}
						u.dirty = refreshSessionMetrics(u.statusPanel, u.sessionCapture, presentationTime(u.clock), false) || u.dirty
						startSkillHistory(&u.attached, &u.Client, &u.childHistoryState, &u.restoring)
						applyObservedActivity(&u.attached, u.shell, &u.Client, &u.streams, u.usage, u.review, &u.approvals, &u.pendingEditReceipts, u.clock, &u.dirty, u.restoring, u.audience)
						publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, presentationTime(u.clock))
						if err := applyResumeAction(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, &u.restoring, &u.side, &u.statusPanel, &u.resumePicker, u.approvalMode, &u.quitRequested, u.shell.Main.Resume.PageDemand()); err != nil {
							return err
						}
						flushStreamOutput(&u.attached, u.shell, &u.Client, &u.streams, u.execTrack, u.review, &u.approvals, &u.pendingEditReceipts, u.clock, u.ctx, u.retained, u.segments, &u.commandSegmentWrites, &u.commandSegmentPending, u.issues, &u.dirty)
						u.dirty = startCommitReads(&u.attached, u.commits) || u.dirty
						u.dirty = paneError(u.attached.source, u.panes.save(u.shell.Shell.Preferences(u.shell.Main), presentationTime(u.clock), false)) || u.dirty
						if expireNotice(&u.attached.feedback, u.shell, presentationTime(u.clock)) || expireApprovals(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, u.notifications, u.review, &u.pendingEditReceipts) {
							u.dirty = true
						}
						now := presentationTime(u.clock)
						publishActivity(&u.attached, u.shell)
						if activityui.Settle(now, u.shell.Transcript, u.shell.Agents) {
							u.dirty = true
						}
						for _, view := range []*activityui.View{u.shell.Transcript, u.shell.Agents} {
							if view.ExpireFlash(now) {
								u.dirty = true
							}
							if view.Pace(now) {
								u.dirty = true
							}
						}
						settlePresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil)
						if u.attached.presentation.View().Status.Animating || u.shell.Agents.HasLiveStatus() {
							u.dirty = true
						}
						if time.Since(agePaint) >= time.Second {
							u.dirty = true
							agePaint = presentationTime(u.clock)
						}
						if err := u.drainKeys(keys); err != nil || u.quitRequested {
							return err
						}
						notices := applyCriticalNotices(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, u.clock, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, &u.restoring, &u.side, u.audience, &u.dirty, u.issues)
						settlePresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil)
						if u.shell.Live.Tick(presentationTime(u.clock)) || u.shell.Agents.RosterEasing {
							u.dirty = true
						}
						w, h, err := term.GetSize(int(stdout.Fd()))
						if err != nil {
							notices.finish(terminalsession.NoticeReceipt{})
							return err
						}
						if u.dirty || w != width || h != height {
							width, height = w, h
							if err := paintSession(&u.attached, u.shell, &u.detailFiles, u.ctx, presentationTime(u.clock), stdout, w, h); err != nil {
								notices.finish(terminalsession.NoticeReceipt{})
								return err
							}
							notices.finish(u.shell.Frame.Notices)
							u.dirty = false
						}
						u.notifications.writeTerminalTitle(&u.attached.feedback, u.shell, u.clock, u.attached.cwd, u.Thread, u.questions.Questions, u.approvals.Approvals, u.Turn != "" || u.Starting(), presentationTime(u.clock))
						continue
					}
					settlePresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil)
					u.notifications.writeTerminalTitle(&u.attached.feedback, u.shell, u.clock, u.attached.cwd, u.Thread, u.questions.Questions, u.approvals.Approvals, u.Turn != "" || u.Starting(), presentationTime(u.clock))
				}
			})
			var fileEditor *openFileEditor
			var composerEditor *composerEditorRequest
			if errors.As(err, &composerEditor) {
				openComposerEditor(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.side, &u.ownedImages, u.skillEnvironment, u.ctx, composerEditor.intent, stdin, stdout)
			} else if errors.As(err, &fileEditor) {
				openSelectedFileEditor(&u.attached, u.shell, u.clock, u.ctx, fileEditor.path, stdin, stdout)
			} else {
				break
			}
			u.dirty = true
		}
		if saveErr := u.panes.save(u.shell.Shell.Preferences(u.shell.Main), presentationTime(u.clock), true); saveErr != nil {
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
		finishCommandSegments(&u.commandSegmentWrites, &u.commandSegmentPending, u.issues, &u.dirty)
		for key, run := range u.attached.session.Commands() {
			if run.Entry.Native.Invocation.ReleaseSegmentOutputs() {
				u.execTrack.ReleaseOutput(key)
			}
		}
		u.review.Close()
		u.Editor = hideQuestionEditor(&u.questions, u.attached.conversation, u.shell, u.Editor, u.Thread)
		hideApprovals(&u.approvals, u.shell, &u.attached.conversation.PromptEditor, u.Editor)
		if unsent := conversationmodel.Join(slices.Concat(u.attached.conversation.Unsent, u.attached.conversation.Queued, []conversationmodel.Input{draftSnapshot(u.Editor)})...); unsent.Text != "" {
			fmt.Fprintln(stdout, "Unsent draft:\n"+textsanitize.Safe(unsent.Text, false))
		}
		unknown := slices.Clone(u.Steers())
		if !u.Submission().Committed {
			unknown = append(unknown, u.Submission().ID)
		}
		if unknown := conversationmodel.Join(u.attached.conversation.Parts(unknown)...); unknown.Text != "" {
			fmt.Fprintln(stdout, "Submission outcome unknown; not automatically resent:\n"+textsanitize.Safe(unknown.Text, false))
		}
		if u.attached.conversation.Shell.Text != "" {
			fmt.Fprintln(stdout, "Shell submission outcome unknown; not automatically rerun:\n"+textsanitize.Safe(u.attached.conversation.Shell.Text, false))
		}
		u.attached.session.Retire()
		if err != nil {
			c.Diagnostics.Lock()
			defer c.Diagnostics.Unlock()
			if len(c.Diagnostics.Text) > 0 {
				fmt.Fprintln(stdout, textsanitize.Safe(string(c.Diagnostics.Text), false))
			}
		}
		color := term.IsTerminal(int(stdout.Fd())) && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
		return errors.Join(err, writeExitSummary(u.exitUsage, u.archived, u.Thread, u.resumeArgv, u.replayDebugDirectory, stdout, color))
	}, nil
}

// appServerRequest contains response routing data, not a second request ledger.
// Main's lifecycle scope retires on confirmed replacement. Side conversations
// and title saves retain their own completion and cleanup across that boundary.
type appServerRequest struct {
	metadata string
	label    string
	title    *sessioncontrol.NameSave
	status   *appServerStatusReport
}

func (u *appServerUI) message(m appserver.Message) (err error) {
	defer func() {
		settlePresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil)
	}()
	u.issues.ObserveTurnCompletion(m.Method, m.Params)
	observeShellMessage(&u.shellEdits, &u.attached, u.shell, u.clock, m)
	var response sessioncontrol.Request[appServerRequest]
	var correlated bool
	if m.Method == "" {
		response, correlated = u.Requests.Take(string(m.ID))
	}
	if sessionTitleMessage(&u.Client, u.titleGenerator, &u.attached.feedback, u.shell, u.clock, &u.dirty, &u.title, m, response.Owner.title) {
		return nil
	}
	defer func() {
		u.picker.refreshPicker(&u.attached, u.shell, u.clock, u.Requests, u.Editor, u.ctx, u.skillEnvironment)
	}()
	// Side-thread traffic never reaches Main's lifecycle, questions or roster.
	if update, err := u.Requests.ObserveSide(m, response, u.Thread, u.controlObservations); update.Handled {
		u.dirty = true
		applySideUpdate(&u.attached, u.shell, &u.Client, &u.picker, &u.questions, &u.approvals, u.clock, update)
		if err != nil {
			return err
		}
		if update.Flush {
			return flushBTW(&u.attached, u.shell, &u.Client, &u.picker, &u.questions, &u.approvals, u.clock, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, &u.side, &u.ownedImages, u.skillEnvironment)
		}
		return nil
	}
	if u.RetiredEvent(m) {
		return nil
	}
	// Any event can make stacked input sendable: an acknowledgement, a turn
	// start or end, or settled settings.
	defer func() {
		if err == nil {
			err = flushInput(&u.attached, u.shell, &u.Client, &u.picker, &u.questions, &u.approvals, u.clock, u.model, u.serviceTiers, u.skillEnvironment, u.ownedImages, &u.guardHookCheck, &u.Editor, u.ctx, u.restoring != nil, u.controlObservations, u.review, &u.pendingEditReceipts)
		}
		if err == nil {
			err = flushBTW(&u.attached, u.shell, &u.Client, &u.picker, &u.questions, &u.approvals, u.clock, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, &u.side, &u.ownedImages, u.skillEnvironment)
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
	applyObservedActivity(&u.attached, u.shell, &u.Client, &u.streams, u.usage, u.review, &u.approvals, &u.pendingEditReceipts, u.clock, &u.dirty, u.restoring, u.audience)
	if held, err := u.holdOlderHistoryEvent(m); held || err != nil {
		return err
	}
	resuming := u.Startup.Thread != "" && u.Startup.Thread != sessioncontrol.ResumePicker && (u.Thread == "" || u.restoring != nil)
	if (resuming || u.Replacement.Target != "") && m.Method != "" {
		if hold, keep := holdResumeEvent(&u.attached.session, &u.Live, u.Startup, m, resuming); hold {
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
				setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "Failed to archive current thread: "+m.Error.Message, true)
			} else {
				u.archived, u.quitRequested = true, true
			}
			return nil
		}
		if strings.HasPrefix(method, "activity-skills/") {
			return skillHistoryResponse(&u.attached, &u.Client, &u.childHistoryState, &u.restoring, u.ctx, u.retained, &u.dirty, u.activity, u.segments, &u.restoredSegments, strings.TrimPrefix(method, "activity-skills/"), m)
		}
		if strings.HasPrefix(method, "activity/") {
			return u.childHistoryResponse(method, m)
		}
		if method == "resume/settings" {
			return u.resumeSettingsResponse(m)
		}
		if method == "config/read" {
			u.notifications.notificationConfig(&u.attached.feedback, u.shell, u.clock, m)
			return nil
		}
		if method == resumePickerList {
			return resumePickerResponse(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, &u.restoring, &u.side, &u.statusPanel, &u.resumePicker, u.approvalMode, &u.quitRequested, m)
		}
		if handled, err := statusMessage(u.Requests, &u.dirty, u.statusPanel, method, m, response.Owner.status); handled {
			return err
		}
		if u.picker.pickerMessage(&u.attached, u.shell, u.clock, u.Requests, u.Editor, u.ctx, u.skillEnvironment, method, m) {
			return nil
		}
		if method == "thread/list" && u.Startup.Thread == "--last" {
			return resumeLastResponse(&u.attached, &u.Client, &u.picker, u.clock, &u.resumePicker, u.approvalMode, m)
		}
		if handled, err := settingsMessage(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, &u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil, u.ownedImages, u.panes, &u.dirty, &u.model, &u.reasoningEffort, &u.serviceTier, &u.models, &u.modelsLoading, &u.reasoningKey, &u.statusConfig, method, m); handled || err != nil {
			return err
		}
		if method == "thread/read" && applyThreadMetadata(&u.attached, u.shell, &u.Client, u.review, &u.approvals, &u.pendingEditReceipts, u.clock, m, response.Owner.metadata) {
			return nil
		}
		if method == "thread/list" || method == "thread/read" {
			return u.restoreActivityResponse(method, m)
		}
		if method == "turn/start" || method == "turn/steer" {
			submissionResponse(&u.attached, u.shell, &u.Client, &u.picker, &u.questions, &u.approvals, u.clock, method, m.Error)
			return nil
		}
		if method == "thread/shellCommand" {
			shellResponse(&u.attached, u.shell, &u.Client, &u.Editor, u.clock, &u.shellEdits, m.Error)
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
		if method == "thread/compact/start" || method == "compact/interrupt" || method == "steer/interrupt" || method == "turn/interrupt" {
			failure := ""
			if m.Error != nil {
				failure = m.Error.Message
			}
			if method == "thread/compact/start" {
				applyInputEffects(&u.attached, u.shell, &u.questions, &u.approvals, &u.picker, u.Turn, u.clock, u.attached.presentation.AcknowledgeCompaction(&u.Live, u.attached.conversation, failure, m.Error != nil))
			} else {
				applyInputEffects(&u.attached, u.shell, &u.questions, &u.approvals, &u.picker, u.Turn, u.clock, u.attached.presentation.AcknowledgeInterrupt(&u.Live, u.attached.conversation, method, failure, m.Error != nil))
			}
			return nil
		}
		if method == "approval/hooks" {
			return guardHookResponse(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.restoring != nil, &u.ownedImages, u.skillEnvironment, &u.guardHookCheck, u.controlObservations, u.ctx, u.review, &u.pendingEditReceipts, m)
		}
		if method == "thread/start" && u.Replacement.Pending() && m.Error != nil {
			u.Replacement.Finish()
			restoreDrafts(&u.attached, u.shell, &u.questions, &u.approvals, &u.picker, u.Turn, u.clock, slices.Concat(u.attached.conversation.Unsent, u.attached.conversation.Queued)...)
			u.attached.conversation.Unsent, u.attached.conversation.Queued = nil, nil
			u.attached.presentation.Status.SetText("Ready")
			setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "Could not clear session: "+m.Error.Message, true)
			return nil
		}
		if method == "thread/resume" && u.Replacement.Target != "" && m.Error != nil {
			return u.resumeSessionFailed(m.Error.Message)
		}
		if m.Error != nil {
			u.attached.presentation.Status.Set(method+": "+m.Error.Message, true)
			if method == "initialize" || method == "thread/start" || method == "thread/resume" {
				return errors.New(u.attached.presentation.Status.Text)
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
			if err := u.Requests.Initialized(u.execWindows != nil); err != nil {
				return err
			}
			return openSession(&u.attached, &u.Client, &u.picker, u.clock, &u.resumePicker, u.approvalMode)
		case "thread/start", "thread/resume":
			result, err := u.Startup.Accept(&u.Live, method, m.Result)
			if err != nil {
				return err
			}
			if u.Replacement.Pending() {
				// Detach only after host acceptance; process and operation owners remain live.
				closing := u.side
				if closing != nil {
					u.side = nil
					closing.Close()
					u.attached.conversation.Side = nil
					pruneDraftImages(u.ownedImages, u.Editor, u.attached.conversation, &u.questions, &u.approvals, u.shell.Prompts, u.side)
					if err := u.Requests.ReleaseSide(closing); err != nil {
						return err
					}
				}
				if u.panes != nil {
					u.dirty = paneError(u.attached.source, u.panes.save(u.shell.Shell.Preferences(u.shell.Main), time.Now(), true)) || u.dirty
				}
				u.picker.cancelScan()
				roster := u.attached.session.Roster()
				retired := make([]string, 0, len(roster))
				for _, agent := range roster {
					retired = append(retired, agent.Thread)
				}
				u.RetireThreads(u.Thread, retired...)
				u.Editor = hideQuestionEditor(&u.questions, u.attached.conversation, u.shell, u.Editor, u.Thread)
				retainedQuestions := u.questions.calls()
				if u.notifications != nil {
					clear(u.notifications.blocked)
				}
				u.statusPanel = nil
				u.attached.presentation.ClosePanel()
				u.Requests.Retire("main") // Retire old-thread response correlation.
				if err := u.Request("thread/unsubscribe", map[string]any{"threadId": u.Thread}); err != nil {
					return err
				}
				u.picker = composerPicker{}
				u.exitUsage = binding.TokenUsage{}
				u.attached.feedback = noticepresentation.Model{}
				u.shell.Main.NoticeDetails, u.shell.Main.NoticeDismiss = terminalsession.Rect{}, terminalsession.Rect{}
				releaseSegmentOutputs(&u.attached, u.execTrack)
				waiting := u.attached.conversation
				u.attached.session.Retire()
				workspace := u.attached.cwd
				u.attached = newAttachedPresentation(u.presentationState, u.conversationState, u.observations)
				u.attached.cwd = workspace
				u.attached.conversation.CarryInput(waiting)
				u.Editor = &u.attached.conversation.Editor

				u.picker.State, u.picker.MenuState = &u.attached.conversation.Completion, u.attached.presentation.Menu()
				u.approvals.Approvals = &u.attached.conversation.Approvals
				u.questions.carryLocal(u.attached.conversation, retainedQuestions, u.Thread)
				u.terminalViews.ReplaceConversation(u.shell)
				u.commits = attachCommitReader(&u.attached.session, u.commits, u.ctx, u.activity != nil)
				u.attached.presentation.Attach(sessionpresentation.Thread{ID: cmp.Or(u.Thread, "/root"), Caller: "/root"}, false)
				if u.shell != nil {
					u.shell.Selection = nil
					u.detailFiles.closeOutput(&u.attached.session, u.shell)
					u.shell.Focus = 0
					u.shell.StopDrag()
				}
				u.LeaveThread()
				u.Startup.Thread, u.Startup.Workspace = "", ""
				u.Readmit(result.Thread.ID)
				if method == "thread/resume" {
					// Restoration below replays events buffered during the switch.
					u.Startup.Thread = result.Thread.ID
					resetDiffScope(&u.attached, u.shell, u.review, u.reviewSource, u.preparePane, u.ctx)
				} else {
					// Seed the Diff's lineage; after a switch, requests alone cannot.
					restoreDiffThread(&u.attached, u.shell, u.review, u.reviewSource, u.preparePane, result.Thread, true)
				}
			}
			u.statusConfig = appServerStatusConfig{}
			if err := json.Unmarshal(m.Result, &u.statusConfig); err != nil {
				return err
			}
			u.Thread = result.Thread.ID
			attachPresentation(&u.attached, u.shell, &u.picker, &u.Editor, &u.questions, &u.approvals, u.Thread, &u.review, &u.commits, u.ctx, u.retained, u.changeSource, u.activity != nil)
			u.attached.presentation.Status.SetText("Ready")
			u.title = result.Thread.Name
			u.titleGenerator.register(result.Thread)
			if name := u.Names.Attach(u.Thread); name != "" {
				u.titleGenerator.nameChanged(u.Thread, name)
				requestSessionTitle(&u.Client, &u.attached.feedback, u.shell, u.clock, &u.dirty, u.Names.Next(u.Thread))
			}
			u.model, u.reasoningEffort, u.serviceTier = result.Model, result.ReasoningEffort, result.ServiceTier
			if method == "thread/resume" {
				u.Settings.RestoreEffort = u.TakeResumeEffort()
			}
			if method == "thread/start" {
				u.Settings.RestoredEffort()
				if u.usage != nil {
					u.usage.MarkNew(result.Thread.ID)
				}
			}
			u.models, u.modelsLoading = nil, true // A cleared or switched session lists them again.
			if err := u.Request("model/list", map[string]any{"includeHidden": true}); err != nil {
				return err
			}
			startAttachedSession(&u.attached, u.observations, u.Thread, result.Thread.Cwd)
			u.streams = terminalsession.Streams{}
			u.retainedSession.Close()
			u.retainedSession = history.Session{}
			if u.notifications != nil {
				if err := u.Request("config/read", map[string]any{"cwd": result.Thread.Cwd, "includeLayers": false}); err != nil {
					return err
				}
			}
			if u.retained.Store != nil || u.panes != nil {
				if err := openWaitStore(u.ctx, u.history, &u.retainedSession, u.Thread, u.retained.Store, u.retained.Writes, u.retained.Limits); err != nil {
					setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "Wait targets could not be retained: "+err.Error(), true)
				}
			}
			restoreContextUsage(u.attached.session.Agent("/root"), result.Thread)
			restoreUsage(u.usage, result.Thread)
			observeCost(u.usage, u.Thread, u.attached.session.Agent("/root"))
			if u.attached.childSource != nil {
				u.attached.childSource.Apply(sessionpresentation.ActivityEvent{Kind: "agents", Agents: u.attached.session.Roster()})
			}
			if u.audience != nil {
				u.audience.Attach(u.Thread)
			}
			// Resumed history is applied once the roster is read, so Main can
			// follow child activity in order. An active snapshot keeps its
			// steer/interrupt target meanwhile.
			if method == "thread/resume" {
				for _, turn := range result.Thread.Turns {
					if turn.Status == "inProgress" {
						u.Turn = turn.ID
						u.attached.presentation.Status.Started = presentationTime(u.clock)
						u.attached.presentation.Status.SetText("Working")
						u.attached.session.Lifecycle(u.Thread).ResumeActive()
					}
				}
			}
			if u.panes != nil {
				u.dirty = paneError(u.attached.source, u.panes.open(&u.shell.Shell, u.shell.Main, result.Thread.Cwd, u.Thread, method == "thread/resume")) || u.dirty
			}
			if method == "thread/resume" {
				repair, err := result.RepairDefaultEffort(&u.Live)
				if err != nil {
					return err
				}
				u.reasoningEffort = result.ReasoningEffort
				if repair != nil {
					// Preserve saved null intent before a corrective dispatch can fail.
					if u.panes != nil && u.panes.attachment != nil && u.Startup.Evidence.Model != "" {
						if err := u.panes.stage(u.shell.Shell.Preferences(u.shell.Main)); err != nil {
							u.dirty = paneError(u.attached.source, err) || u.dirty
						} else {
							u.panes.attachment.Recover(u.Startup.Evidence, presentationTime(u.clock))
							u.dirty = paneError(u.attached.source, u.panes.attachment.Save(presentationTime(u.clock), true)) || u.dirty
						}
					}
					if _, err := updateSettings(&u.attached, u.shell, &u.Client, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, &u.serviceTiers, u.panes, &u.dirty, u.restoring != nil, u.clock, repair); err != nil {
						return err
					}
				}
				if u.Startup.MissingSettings {
					setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), sessionpresentation.ResumeSettingsUnavailable, true)
					u.Startup.MissingSettings = false
				}
				u.dirty = paneError(u.attached.source, u.panes.retainAppliedSettings(u.shell, preferences.Scope{Workspace: u.attached.cwd, Thread: u.Thread}, u.Settings.RestoreEffort, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier, Observed: presentationTime(u.clock)})) || u.dirty
				return restorePaneContent(&u.attached, u.shell, &u.Client, &u.restoring, u.review, u.reviewSource, u.preparePane, result.Thread)
			}
			u.dirty = paneError(u.attached.source, u.panes.retainAppliedSettings(u.shell, preferences.Scope{Workspace: u.attached.cwd, Thread: u.Thread}, u.Settings.RestoreEffort, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier, Observed: presentationTime(u.clock)})) || u.dirty
		}
		return nil
	}
	u.notifications.resolve(m)
	if handled, err := approvalMessage(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, u.statusPanel != nil || u.resumePicker != nil, u.notifications, u.review, &u.pendingEditReceipts, u.controlObservations, m); handled {
		return err
	}
	if handled, err := questionMessage(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, u.statusPanel != nil || u.resumePicker != nil, u.notifications, u.review, &u.pendingEditReceipts, m); handled {
		return err
	}
	if len(m.ID) != 0 {
		// Never auto-approve an unexpected server request. Leave it pending,
		// visibly blocked, until interrupted.
		u.attached.presentation.Status.Set("Blocked on unsupported request "+m.Method+" · Ctrl-C interrupts", true)
		u.notifications.blockNotification(&u.attached.feedback, u.shell, u.clock, m)
		u.attached.source.Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{{Seq: u.attached.source.LastSeq + 1, Agent: "Session", Kind: "text", Text: u.attached.presentation.Status.Text, Observed: presentationTime(u.clock)}}})
		return nil
	}
	if handled, err := settingsMessage(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, &u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil, u.ownedImages, u.panes, &u.dirty, &u.model, &u.reasoningEffort, &u.serviceTier, &u.models, &u.modelsLoading, &u.reasoningKey, &u.statusConfig, "", m); handled || err != nil {
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
	return observedEvent(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.childHistoryState, &u.statusPanel, &u.resumePicker, u.review, &u.pendingEditReceipts, u.notifications, u.retainedSession, u.execWindows, u.activity, &u.streams, u.execTrack, u.nativeTrace, u.usage, &u.exitUsage, m.Method, p)
}

// observedEvent retains application lifecycle and control handling for decoded
// host facts. Offline transcript observations bypass it through their reducer.
func observedEvent(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, childKey *scope.Key[*appServerChildHistory], panel **appServerStatusReport, resumePicker **appServerResumePicker, reviewOwner *sessionreview.Review, receipts *[]*changepresentation.Receipt, notifications *nativeNotifications, retainedSession history.Session, windows *executionobservation.Windows, activityService *activitymodel.Service, streams *terminalsession.Streams, tracker *commandsegments.Observer, trace *nativetrace.Reader, usage *accounting.Service, exitUsage *binding.TokenUsage, method string, p binding.Notification) error {
	text := binding.RetainedTranscript(p.Observation(method, presentationTime(clock)))
	if handled, err := sessionEvent(attached, terminal, control, streams, childKey, tracker, activityService, usage, trace, windows, reviewOwner, approvals, receipts, retainedSession, exitUsage, clock, method, p); handled || err != nil {
		return err
	}

	switch method {
	case "turn/started":
		if p.ThreadID == control.Thread {
			control.Started(p.Turn.ID)
			attached.presentation.Status.Started = presentationTime(clock)
			attached.presentation.Status.Set("Working", false)
			if control.Interruption.BeforeStart || control.Compaction.InterruptTarget() != "" {
				return applyInterruption(attached, terminal, control, picker, editor, questions, approvals, clock, reviewOwner, receipts, control.Interrupt())
			}
		}
	case "turn/completed":
		if notifications != nil {
			for id, request := range notifications.blocked {
				if request.Thread == p.ThreadID && (request.Turn == "" || request.Turn == p.Turn.ID) {
					delete(notifications.blocked, id)
				}
			}
		}
		if p.ThreadID == control.Thread && p.Turn.ID == control.Turn {
			control.Completed(p.Turn.Status == "completed")
			endSyncQuestions(attached, terminal, control, editor, questions, approvals, clock, reviewOwner, receipts, p.Turn.ID)
			var failure *string
			if p.Turn.Error != nil {
				failure = &p.Turn.Error.Message
			}
			attached.presentation.Status.Complete(p.Turn.Status, failure, presentationTime(clock))
			settleInput(attached, terminal, control, picker, questions, approvals, clock, p.Turn.ID, p.Turn.Status == "interrupted")
			if p.Turn.Status == "completed" && questions.Count(control.Thread) == 0 {
				notifications.notify(&attached.feedback, terminal, clock, "agent-turn-complete", "Agent turn complete")
			}
			for _, c := range questions.calls() {
				if !c.Resolved {
					renderQuestionRecord(attached, control.Turn, presentationTime(clock), c)
				}
			}
		}
	case "item/started", "item/completed", "item/agentMessage/delta":
		if method == "item/started" {
			observeShellItem(&control.Shell, control.Thread, p.ThreadID, p.Item)
		}
		if p.ItemID == "" {
			p.ItemID = p.Item.ID
		}
		// Only completion commits, so one message cannot match two identical steers.
		if p.ThreadID == control.Thread && p.Item.Type == "userMessage" && method == "item/completed" {
			commitSteer(attached, &control.Live, p.Item)
		}
		messageTranscript(attached, terminal, control, streams, reviewOwner, approvals, receipts, clock, questions, editor, picker, (*panel) != nil || (*resumePicker) != nil, notifications, text, p.Item.Observation(), true)
	}
	return nil
}

// applyMainKey settles typed Main effects before resuming terminal interpretation.
func (u *appServerUI) applyMainKey(next terminalsession.KeyContinuation) (bool, error) {
	defer func() {
		settlePresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil)
	}()
	if !prepareComposerInput(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals) {
		return false, nil
	}
	// Acquisition stays application-owned; interpretation consumes its publication.
	if u.picker.Open && u.picker.Kind == "" && u.picker.completionTarget(u.Draft, u.attached.conversation.ShellMode()) != u.picker.Target {
		u.picker.refreshPicker(&u.attached, u.shell, u.clock, u.Requests, u.Editor, u.ctx, u.skillEnvironment)
	}
	publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, presentationTime(u.clock))
	refresh := false
	defer func() {
		if refresh {
			u.picker.refreshPicker(&u.attached, u.shell, u.clock, u.Requests, u.Editor, u.ctx, u.skillEnvironment)
		}
	}()
	focused := u.shell == nil || u.shell.Focus == 0
	input := u.shell.Main.ContinueKey(next, u.shell.Prompts, transcriptView(u.attached.presentation, u.shell), focused)
	for {
		refresh = refresh || input.Regular
		switch {
		case input.Prompt != nil:
			if handled, err := applyPromptInput(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, &u.side, u.statusPanel != nil || u.resumePicker != nil, &u.ownedImages, u.skillEnvironment, &u.guardHookCheck, u.controlObservations, u.ctx, u.review, &u.pendingEditReceipts, u.notifications, *input.Prompt); handled || err != nil {
				return false, err
			}
		case input.Panel != nil:
			if input.ClearSelection && u.shell != nil {
				u.shell.Selection = nil
			}
			applyPanelAction(&u.attached, u.shell, u.clock, &u.statusPanel, &u.dirty, u.sessionCapture, *input.Panel)
			u.dirty = true
		case input.Menu != nil:
			if applyMenuResult(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, &u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil, u.ownedImages, u.panes, &u.dirty, u.ctx, u.skillEnvironment, *input.Menu) {
				return false, nil
			}
		case input.Composer != nil:
			return u.applyComposerInput(*input.Composer)
		}
		if input.Next == nil {
			return false, nil
		}
		publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, presentationTime(u.clock))
		input = u.shell.Main.ContinueKey(*input.Next, u.shell.Prompts, transcriptView(u.attached.presentation, u.shell), focused)
	}
}

func applyActivity(attached *attachedPresentation, approvals *nativeApprovalDock, terminal *terminalsession.Attachment, review *sessionreview.Review, pending *[]*changepresentation.Receipt, main string, clock func() time.Time, entries []sessionpresentation.ActivityEntry, agents []sessionpresentation.ActivityAgent) {
	sessionpresentation.ApplyActivity(attached.source, attached.childSource, entries, agents)
	applyCapturedEdits(attached, review, pending)
	// Reconcile the independent guard and host channels without re-entering
	// Activity publication. Remove each ready request before applying its update.
	for i := 0; i < len(approvals.unbound); {
		a := approvals.unbound[i]
		if approvalItem(attached, a.Thread, a.Turn, a.Item) == nil {
			i++
			continue
		}
		approvals.unbound = slices.Delete(approvals.unbound, i, i+1)
		entry, ok := approvalRecord(attached, approvals, main, presentationTime(clock), a, a.Outcome)
		if !ok {
			continue
		}
		if attached.session.KnownCaller(main) == "" {
			attached.source.Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{entry}})
		} else {
			sessionpresentation.ApplyActivity(attached.source, attached.childSource, []sessionpresentation.ActivityEntry{entry}, nil)
			applyCapturedEdits(attached, review, pending)
		}
	}
	publishActivity(attached, terminal)
}

func paintSession(attached *attachedPresentation, terminal *terminalsession.Attachment, files *detailFiles, ctx context.Context, at time.Time, out io.Writer, width, height int) error {
	terminal.Width, terminal.Height = max(1, width), max(1, height)
	terminal.Main.ContentPainted = false
	files.prepareOutput(&attached.session, terminal)
	if ctx == nil {
		ctx = context.Background()
	}
	view := transcriptView(attached.presentation, terminal)
	return paintNative(terminal, terminal.FrameInput(at, mekugiVersion()), view, activitySource(attached, terminal, view), ctx, out)
}

// Router-owned annotations and authenticated directed inputs supplement the
// native event stream without becoming assistant speech or execution events.
func applyObservedActivity(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], streams *terminalsession.Streams, usage *accounting.Service, reviewOwner *sessionreview.Review, approvals *nativeApprovalDock, receipts *[]*changepresentation.Receipt, clock func() time.Time, dirty *bool, restoring *appServerActivityRestore, audience *sessioncontrol.Audience) {
	if restoring != nil && !restoring.state.View().Rendered {
		return
	}
	// Provider rounds can start within one host turn, before another usage
	// notification. Refresh from the owner so a prior round's TPS is not stale.
	usageChanged := false
	for _, observed := range attached.session.Roster() {
		if agent := attached.session.Agent(observed.Name); agent != nil {
			before := *agent
			observeCost(usage, observed.Thread, agent)
			usageChanged = usageChanged || before != *agent
		}
	}
	if usageChanged {
		attached.childSource.Apply(sessionpresentation.ActivityEvent{Kind: "agents", Agents: attached.session.Roster()})
		(*dirty) = true
	}
	if audience == nil {
		return
	}
	entries := takeNativeActivity(audience, control.Thread)
	for _, start := range audience.TakeStarts(control.Thread) {
		entries = append(entries, transcriptUpdates(attached, terminal, streams, control.Thread, providerStarted(attached, start.Thread, start.At), false)...)
	}
	for i := range entries {
		entries[i].Seq = attached.session.Activity().NextSequence()
	}
	if len(entries) != 0 {
		applyActivity(attached, approvals, terminal, reviewOwner, receipts, control.Thread, clock, entries, nil)
		(*dirty) = true
	}
	for _, ref := range audience.TakeUnreturned(control.Thread) {
		if main, activity := attached.source.MarkUnreturned(ref.Thread, ref.Call), attached.childSource.MarkUnreturned(ref.Thread, ref.Call); main || activity {
			(*dirty) = true
		}
	}
}
