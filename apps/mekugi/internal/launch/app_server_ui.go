package launch

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
	noticepresentation "github.com/yusing/mekugi/modules/notifications/presentation"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
	"github.com/yusing/mekugi/modules/usage/accounting"
	"github.com/yusing/mekugi/providers/openai"
	"golang.org/x/term"
)

// Codex approval policy and the invocation-local Shell guard are independent.
func startNativeSession(terminalViews terminalsession.Views, reviewSource *changecapture.Service, views *sessionpresentation.Service, conversation *conversationmodel.Service, activity *activitymodel.Service, service *history.Service, prefs *preferences.Service, segments *reports.Service, ctx context.Context, cmd *exec.Cmd, stdin, stdout *os.File, observationInputs *sessioncontrol.Observations, usage *accounting.Service, audience *sessioncontrol.Audience, reviewOwner *sessionreview.Review, windows *executionobservation.Windows, trace *nativetrace.Reader, retained changes.Scope, changeSource changes.Service, snapshots *changecapture.Workspaces, recovered *recovery.Service, publishChanges func([]changes.Update), captureNotice func(string, string, string, string), guard *sessioncontrol.ShellApprovals, guards []binding.ShellApproval, tracker *commandsegments.Observer, skillsManager bool, issues *requestexecution.Failures, resumeThread string, faint bool, serviceTiers *inferencegateway.Tiers, capture *transportcapture.Recorder, resumeArgv []string, debugDirectory string, generator *sessionNaming, observations *binding.Observation, approvals bool, predictionProvider *openai.Client) (func() error, error) {
	guardHash := ""
	for _, entry := range cmd.Environ() {
		if hash, ok := strings.CutPrefix(entry, shellhook.Environment+"="); ok {
			guardHash = hash
		}
	}
	if guardHash != "" && guard == nil {
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
	processCwd, _ := filepath.Abs(cmd.Dir)
	inputHistory := newInputHistory(cmd.Environ(), processCwd)
	attached.inputHistory = inputHistory
	var (
		editor              *composer.Editor
		resumePending       []appserver.Message
		clock               func() time.Time
		version, title      string
		titleUpdates        <-chan sessionTitleUpdate
		side                *sessioncontrol.Side
		notifications       *nativeNotifications
		questions           nativeQuestionDock
		approvalDock        nativeApprovalDock
		guardHook           approvalHookCheck
		panel               *appServerStatusReport
		resumePicker        *appServerResumePicker
		statusConfig        appServerStatusConfig
		picker              composerPicker
		environment         []string
		commits             *activitypresentation.CommitReader
		writes              chan commandSegmentWrite
		pendingWrites       int
		restoredSegments    map[string]*reports.Report
		shellEdits          appServerShellEdits
		receipts            []*changepresentation.Receipt
		files               detailFiles
		streams             terminalsession.Streams
		retainedSession     history.Session
		quit, archived      bool
		exitUsage           binding.TokenUsage
		model, effort, tier string
		models              []appServerModel
		modelsLoading       bool
		reasoningKey        *bool
		ownedImages         map[string]bool
		panes               *nativePreferences
		restoring           *appServerActivityRestore
		childKey            scope.Key[*appServerChildHistory]
	)
	dirty := true
	c := control.Host
	control.Startup.Thread = resumeThread
	attachPresentation(&attached, shell, &picker, &editor, &questions, &approvalDock, control.Thread, &reviewOwner, &commits, ctx, retained, changeSource, activity != nil)
	attached.presentation.Status.Set("Connecting…", false)
	guardHook.hash = guardHash
	if generator != nil {
		titleUpdates = generator.updates
	}
	if guard != nil {
		approvalDock.grants = sessioncontrol.NewApprovalGrants(guards)
	}
	if skillsManager {
		environment = cmd.Environ()
	}
	resumeArgv = slices.Clone(resumeArgv)
	control.Startup.Config = appServerResumeConfig(cmd.Args, resumeArgv)
	notifications = &nativeNotifications{out: stdout, focused: true}
	control.Startup.Workspace = resumeCwd
	if prefs != nil {
		panes = &nativePreferences{service: prefs}
	}
	if err := control.Request("initialize", nil); err != nil {
		_ = inputHistory.close()
		c.Close()
		<-c.Done
		return nil, err
	}

	shell.Faint = faint
	settlePresentation(&attached, shell, &control, &picker, &editor, &childKey, preferences.Applied{Model: model, Effort: effort, Tier: tier}, serviceTiers, title, version, restoring != nil, side, clock, &questions, &approvalDock, panel != nil || resumePicker != nil)
	predictions := composerPredictions{provider: predictionProvider, results: make(chan predictionConfiguration, 1)}
	syncPredictions := func() error {
		predictions.supported = sessioncontrol.PredictionForkSupported(version)
		before := attached.conversation.Prediction
		err := predictions.sync(ctx, &attached, shell, &control, restoring != nil, notifications.focused)
		dirty = dirty || before != attached.conversation.Prediction
		return err
	}
	var dispatch func(appserver.Message) error
	dispatch = func(m appserver.Message) error {
		err := dispatchHostMessage(&attached, shell, &control, &picker, &editor, &questions, &approvalDock, clock, &childKey, &model, &effort, &tier, &serviceTiers, &title, &version, &restoring, &side, &panel, &resumePicker, &ownedImages, environment, &guardHook, observationInputs, ctx, &reviewOwner, &receipts, notifications, &shellEdits, retained, &retainedSession, changeSource, windows, audience, reviewSource, preparePane, approvals, generator, &dirty, activity, &streams, tracker, trace, usage, &exitUsage, segments, &restoredSegments, views, &files, &statusConfig, issues, &quit, dispatch, &resumePending, &commits, terminalViews, conversation, observations, service, panes, &models, &modelsLoading, &reasoningKey, &archived, m)
		predictions.observe(m, control.Thread)
		return err
	}
	return func() error {
		defer func() { _ = predictions.clear(&control, attached.conversation) }()
		reviewOwner.Loader = sessionreview.NewLoader(ctx, reviewOwner.Projection)
		defer reviewOwner.Loader.Close()
		defer func() {
			retainedSession.Close()
		}()
		defer picker.cancelScan()
		defer shell.Diff.Close()
		defer func() { commits.Close() }()
		if audience != nil {
			defer audience.Detach()
		}
		defer c.Input.Close()
		defer func() { discardDraftImages(ownedImages) }()
		defer c.Output.Close()
		exited := false
		var err error
		for {
			err = terminalui.WithRawPane(ctx, stdin, stdout, "\x1b[?1049h\x1b[?25l\x1b[?1003;1004;1006;2004h\x1b]10;?\x1b\\\x1b]11;?\x1b\\", "\x1b[?2026l\x1b[?1003;1004;1006;2004l\x1b[0m\x1b[?25h\x1b[?1049l", func(keys <-chan byte) error {
				defer func() { notifications.clearTerminalTitle(&attached.feedback, shell, clock) }()
				review := reviewOwner
				review.Preview().Enable()
				defer func() { review.Preview().Disable() }()
				reviewOwner.Loader.AwaitResync()
				sub := review.Subscribe()
				defer func() { review.Unsubscribe(sub) }()
				diffEvents, diffGap, diffReady, previewGap := sub.Events(), sub.Gap(), sub.Ready(), sub.PreviewGap()
				autoChanged := review.Changed()
				resubscribe := func() {
					review.Unsubscribe(sub)
					reviewOwner.Loader.AwaitResync()
					sub = review.Subscribe()
					diffEvents, diffGap, diffReady, previewGap = sub.Events(), sub.Gap(), sub.Ready(), sub.PreviewGap()
					dirty = true
				}
				tick := time.NewTicker(33 * time.Millisecond)
				defer tick.Stop()
				width, height := 0, 0
				agePaint := presentationTime(clock)
				for {
					select {
					case retainedErr := <-control.Startup.ForkRetained():
						pending := control.Startup.ForkRetention
						pending.Err, pending.Complete = retainedErr, true
						if err := dispatch(pending.Message); err != nil {
							return err
						}
						dirty = true
					case result := <-inputHistory.loads:
						inputHistory.loaded(attached.conversation, result)
						dirty = true
					case historyErr := <-inputHistory.failures:
						setNotice(&attached.feedback, shell, presentationTime(clock), "Input history unavailable: "+historyErr.Error(), true)
						dirty = true
					case <-ctx.Done():
						return ctx.Err()
					case result := <-predictions.results:
						if err := syncPredictions(); err != nil {
							return err
						}
						if err := predictions.configured(result, &control); err != nil {
							return err
						}
						dirty = true
					case <-autoChanged:
						reviewOwner.Projection.Workspace = review.Workspace()
						publishReview(&attached, shell, reviewOwner, reviewSource, preparePane)
						dirty = true
					case <-diffGap:
						resubscribe()
					case <-previewGap:
						resubscribe()
					case result := <-reviewOwner.Loader.Updates():
						if sub.Invalidated() {
							resubscribe()
						}
						loadedReview(&attached, shell, reviewOwner, reviewSource, preparePane, &receipts, result)
						if err := finishRestoredContent(&attached, &control, &picker, &restoring, &reviewOwner, dispatch, &resumePending); err != nil {
							return err
						}
						dirty = true
					case <-diffReady:
						if !admitReviewEvents(&attached, shell, reviewOwner, reviewSource, preparePane, &receipts, clock, ctx, sub, review.TakePreviews(sub)...) {
							resubscribe()
						}
						if err := finishRestoredContent(&attached, &control, &picker, &restoring, &reviewOwner, dispatch, &resumePending); err != nil {
							return err
						}
						dirty = true
					case event := <-diffEvents:
						if !admitReviewEvents(&attached, shell, reviewOwner, reviewSource, preparePane, &receipts, clock, ctx, sub, event) {
							resubscribe()
						}
						if err := finishRestoredContent(&attached, &control, &picker, &restoring, &reviewOwner, dispatch, &resumePending); err != nil {
							return err
						}
						dirty = true
					case result := <-files.fileReads:
						dirty = files.loadedMarkdownFile(&attached.session, views, attached.cwd, shell, result) || dirty
					case result := <-picker.scanResults:
						dirty = picker.applyScan(attached.cwd, picker.completionTarget(editor.Draft, attached.conversation.ShellMode()), result) || dirty
					case <-commits.Updates():
						dirty = startCommitReads(&attached, commits) || dirty
					case err := <-writes:
						commandSegmentRetained(&pendingWrites, issues, &dirty, err)
					case update := <-titleUpdates:
						persistSessionTitle(&control, generator, &attached.feedback, shell, clock, &dirty, update)
					case request := <-guard.Requests():
						addGuardApproval(&attached, shell, &control, &picker, &editor, &questions, &approvalDock, clock, panel != nil || resumePicker != nil, notifications, reviewOwner, &receipts, request)
						dirty = true
					case message, ok := <-c.Messages:
						if !ok {
							exited = true
							return errors.Join(errors.New("app-server disconnected; active work may be incomplete"), <-c.Done)
						}
						if err := dispatch(message); err != nil || quit {
							return err
						}
						dirty = true
					case key, ok := <-keys:
						if !ok {
							return io.EOF
						}
						if event := shell.Decoder.Feed(key, time.Now()); event.Kind != terminalsession.NoInput {
							if err := applyInput(&attached, shell, &control, &picker, &editor, &questions, &approvalDock, clock, &childKey, &model, &effort, &tier, &serviceTiers, &title, &version, &restoring, &side, &panel, &resumePicker, &ownedImages, environment, &guardHook, observationInputs, ctx, &reviewOwner, &receipts, notifications, &shellEdits, retained, &retainedSession, changeSource, snapshots, recovered, publishChanges, windows, audience, captureNotice, reviewSource, preparePane, approvals, generator, &dirty, activity, segments, &restoredSegments, &files, &statusConfig, capture, debugDirectory, &quit, dispatch, &resumePending, panes, &models, &modelsLoading, &reasoningKey, shell, shell.FrameInput(presentationTime(clock), mekugiVersion()).Dispatch(&shell.Interaction, event)); err != nil || quit {
								return err
							}
						}
						dirty = true
					case <-tick.C:
						if len(attached.conversation.Unsent) > 0 && attached.conversation.Unsent[0].Question != nil && (observationInputs == nil || !observationInputs.EmittingToolInput(attached.cwd, control.Thread)) {
							if err := flushInput(&attached, shell, &control, &picker, &questions, &approvalDock, clock, model, serviceTiers, environment, ownedImages, &guardHook, &editor, ctx, restoring != nil, observationInputs, reviewOwner, &receipts); err != nil {
								return err
							}
						}
						dirty = refreshSessionMetrics(panel, capture, presentationTime(clock), false) || dirty
						startSkillHistory(&attached, &control, &childKey, &restoring)
						applyObservedActivity(&attached, shell, &control, &streams, usage, reviewOwner, &approvalDock, &receipts, clock, &dirty, restoring, audience)
						publishPresentation(&attached, shell, &control, &picker, &editor, &childKey, preferences.Applied{Model: model, Effort: effort, Tier: tier}, serviceTiers, title, version, restoring != nil, side, presentationTime(clock))
						if err := applyResumeAction(&attached, shell, &control, &picker, &editor, &questions, &approvalDock, clock, &childKey, preferences.Applied{Model: model, Effort: effort, Tier: tier}, serviceTiers, title, version, &restoring, &side, &panel, &resumePicker, approvals, &quit, shell.Main.Resume.PageDemand()); err != nil {
							return err
						}
						flushStreamOutput(&attached, shell, &control, &streams, tracker, reviewOwner, &approvalDock, &receipts, clock, ctx, retained, segments, &writes, &pendingWrites, issues, &dirty)
						dirty = startCommitReads(&attached, commits) || dirty
						dirty = paneError(attached.source, panes.save(shell.Shell.Preferences(shell.Main), presentationTime(clock), false)) || dirty
						if expireNotice(&attached.feedback, shell, presentationTime(clock)) || expireApprovals(&attached, shell, &control, &picker, &editor, &questions, &approvalDock, clock, notifications, reviewOwner, &receipts) {
							dirty = true
						}
						now := presentationTime(clock)
						publishActivity(&attached, shell)
						if activityui.Settle(now, shell.Transcript, shell.Agents) {
							dirty = true
						}
						for _, view := range []*activityui.View{shell.Transcript, shell.Agents} {
							if view.ExpireFlash(now) {
								dirty = true
							}
							if view.Pace(now) {
								dirty = true
							}
						}
						settlePresentation(&attached, shell, &control, &picker, &editor, &childKey, preferences.Applied{Model: model, Effort: effort, Tier: tier}, serviceTiers, title, version, restoring != nil, side, clock, &questions, &approvalDock, panel != nil || resumePicker != nil)
						if attached.presentation.View().Status.Animating || shell.Agents.HasLiveStatus() {
							dirty = true
						}
						if time.Since(agePaint) >= time.Second {
							dirty = true
							agePaint = presentationTime(clock)
						}
						if err := drainKeys(&attached, shell, &control, &picker, &editor, &questions, &approvalDock, clock, &childKey, &model, &effort, &tier, &serviceTiers, &title, &version, &restoring, &side, &panel, &resumePicker, &ownedImages, environment, &guardHook, observationInputs, ctx, &reviewOwner, &receipts, notifications, &shellEdits, retained, &retainedSession, changeSource, snapshots, recovered, publishChanges, windows, audience, captureNotice, reviewSource, preparePane, approvals, generator, &dirty, activity, segments, &restoredSegments, &files, &statusConfig, capture, debugDirectory, &quit, dispatch, &resumePending, panes, &models, &modelsLoading, &reasoningKey, keys); err != nil || quit {
							return err
						}
						if err := syncPredictions(); err != nil {
							return err
						}
						notices := applyCriticalNotices(&attached, shell, &control, &picker, &editor, clock, &childKey, preferences.Applied{Model: model, Effort: effort, Tier: tier}, serviceTiers, title, version, &restoring, &side, audience, &dirty, issues)
						settlePresentation(&attached, shell, &control, &picker, &editor, &childKey, preferences.Applied{Model: model, Effort: effort, Tier: tier}, serviceTiers, title, version, restoring != nil, side, clock, &questions, &approvalDock, panel != nil || resumePicker != nil)
						if shell.Live.Tick(presentationTime(clock)) || shell.Agents.RosterEasing {
							dirty = true
						}
						w, h, err := term.GetSize(int(stdout.Fd()))
						if err != nil {
							notices.finish(terminalsession.NoticeReceipt{})
							return err
						}
						if dirty || w != width || h != height {
							width, height = w, h
							if err := paintSession(&attached, shell, &files, ctx, presentationTime(clock), stdout, w, h); err != nil {
								notices.finish(terminalsession.NoticeReceipt{})
								return err
							}
							notices.finish(shell.Frame.Notices)
							dirty = false
						}
						notifications.writeTerminalTitle(&attached.feedback, shell, clock, attached.cwd, control.Thread, questions.Questions, approvalDock.Approvals, control.Turn != "" || control.Starting(), presentationTime(clock))
						continue
					}
					if err := syncPredictions(); err != nil {
						return err
					}
					settlePresentation(&attached, shell, &control, &picker, &editor, &childKey, preferences.Applied{Model: model, Effort: effort, Tier: tier}, serviceTiers, title, version, restoring != nil, side, clock, &questions, &approvalDock, panel != nil || resumePicker != nil)
					notifications.writeTerminalTitle(&attached.feedback, shell, clock, attached.cwd, control.Thread, questions.Questions, approvalDock.Approvals, control.Turn != "" || control.Starting(), presentationTime(clock))
				}
			})
			var fileEditor *openFileEditor
			var composerEditor *composerEditorRequest
			if errors.As(err, &composerEditor) {
				openComposerEditor(&attached, shell, &control, &picker, &editor, &questions, &approvalDock, clock, &side, &ownedImages, environment, ctx, composerEditor.intent, stdin, stdout)
			} else if errors.As(err, &fileEditor) {
				openSelectedFileEditor(&attached, shell, clock, ctx, fileEditor.path, stdin, stdout)
			} else {
				break
			}
			dirty = true
		}
		if saveErr := panes.save(shell.Shell.Preferences(shell.Main), presentationTime(clock), true); saveErr != nil {
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
		finishCommandSegments(&writes, &pendingWrites, issues, &dirty)
		for key, run := range attached.session.Commands() {
			if run.Entry.Native.Invocation.ReleaseSegmentOutputs() {
				tracker.ReleaseOutput(key)
			}
		}
		reviewOwner.Close()
		editor = hideQuestionEditor(&questions, attached.conversation, shell, editor, control.Thread)
		hideApprovals(&approvalDock, shell, &attached.conversation.PromptEditor, editor)
		inputHistory.saveDraft(attached.conversation, editor)
		if historyErr := inputHistory.close(); historyErr != nil {
			fmt.Fprintln(stdout, "Input history could not be saved:", textsanitize.Safe(historyErr.Error(), false))
		}
		if unsent := conversationmodel.Join(slices.Concat(attached.conversation.Unsent, attached.conversation.Queued, []conversationmodel.Input{draftSnapshot(editor)})...); unsent.Text != "" {
			fmt.Fprintln(stdout, "Unsent draft:\n"+textsanitize.Safe(unsent.Text, false))
		}
		unknown := slices.Clone(control.Steers())
		if !control.Submission().Committed {
			unknown = append(unknown, control.Submission().ID)
		}
		if unknown := conversationmodel.Join(attached.conversation.Parts(unknown)...); unknown.Text != "" {
			fmt.Fprintln(stdout, "Submission outcome unknown; not automatically resent:\n"+textsanitize.Safe(unknown.Text, false))
		}
		if attached.conversation.Shell.Text != "" {
			fmt.Fprintln(stdout, "Shell submission outcome unknown; not automatically rerun:\n"+textsanitize.Safe(attached.conversation.Shell.Text, false))
		}
		attached.session.Retire()
		if err != nil {
			c.Diagnostics.Lock()
			defer c.Diagnostics.Unlock()
			if len(c.Diagnostics.Text) > 0 {
				fmt.Fprintln(stdout, textsanitize.Safe(string(c.Diagnostics.Text), false))
			}
		}
		color := term.IsTerminal(int(stdout.Fd())) && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
		return errors.Join(err, writeExitSummary(exitUsage, archived, control.Thread, resumeArgv, debugDirectory, stdout, color))
	}, nil
}

// appServerRequest contains response routing data, not a second request ledger.
// Main's lifecycle scope retires on confirmed replacement. Side conversations
// and title saves retain their own completion and cleanup across that boundary.
type appServerRequest struct {
	metadata string
	menu     uint64
	label    string
	title    *sessioncontrol.NameSave
	status   *appServerStatusReport
}

func dispatchHostMessage(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, childKey *scope.Key[*appServerChildHistory], model *string, effort *string, tier *string, tiers **inferencegateway.Tiers, title *string, backend *string, restoring **appServerActivityRestore, side **sessioncontrol.Side, panel **appServerStatusReport, resumePicker **appServerResumePicker, owned *map[string]bool, environment []string, hook *approvalHookCheck, observations *sessioncontrol.Observations, parent context.Context, reviewOwner **sessionreview.Review, receipts *[]*changepresentation.Receipt, notifications *nativeNotifications, shellEdits *appServerShellEdits, retained changes.Scope, retainedSession *history.Session, changeSource changes.Service, windows *executionobservation.Windows, audience *sessioncontrol.Audience, reviewSource *changecapture.Service, preparePane func(*scope.Store), approvalMode bool, generator *sessionNaming, dirty *bool, activityService *activitymodel.Service, streams *terminalsession.Streams, tracker *commandsegments.Observer, trace *nativetrace.Reader, usage *accounting.Service, exitUsage *binding.TokenUsage, segments *reports.Service, restoredSegments *map[string]*reports.Report, views *sessionpresentation.Service, files *detailFiles, statusConfig *appServerStatusConfig, issues *requestexecution.Failures, quit *bool, dispatch func(appserver.Message) error, resumePending *[]appserver.Message, commits **activitypresentation.CommitReader, terminalViews terminalsession.Views, conversationState *conversationmodel.Service, bindings *binding.Observation, historyService *history.Service, panes *nativePreferences, models *[]appServerModel, modelsLoading *bool, reasoningKey **bool, archived *bool, m appserver.Message) (err error) {
	defer func() {
		settlePresentation(attached, terminal, control, picker, editor, childKey, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), (*restoring) != nil, (*side), clock, questions, approvals, (*panel) != nil || (*resumePicker) != nil)
	}()
	issues.ObserveTurnCompletion(m.Method, m.Params)
	observeShellMessage(shellEdits, attached, terminal, clock, m)
	var response sessioncontrol.Request[appServerRequest]
	var correlated bool
	if m.Method == "" {
		if pending, ok := control.Requests.Lookup(string(m.ID)); ok && pending.Method == "thread/fork" && pending.Side == nil && pending.Prediction == nil && control.Replacement.Forking() && m.Error == nil && retained.Store != nil {
			if waiting := control.Startup.ForkRetention; waiting != nil {
				if !waiting.Complete {
					return nil
				}
			} else if accepted, err := control.Startup.Accept(&control.Live, "thread/fork", m.Result); err == nil {
				control.Startup.RetainFork(parent, retained, changeSource, control.Thread, m, accepted)
				attached.presentation.Status.SetText("Retaining fork history…")
				return nil
			}
		}
		response, correlated = control.Requests.Take(string(m.ID))
	}
	if handled, err := control.ObservePrediction(m, response, observations); handled {
		*dirty = true
		return err
	}
	if sessionTitleMessage(control, generator, &attached.feedback, terminal, clock, dirty, title, m, response.Owner.title) {
		return nil
	}
	defer func() {
		picker.refreshPicker(attached, terminal, clock, control.Requests, (*editor), parent, environment)
	}()
	// Side-thread traffic never reaches Main's lifecycle, questions or roster.
	if update, err := control.Requests.ObserveSide(m, response, control.Thread, observations); update.Handled {
		(*dirty) = true
		applySideUpdate(attached, terminal, control, picker, questions, approvals, clock, update)
		if err != nil {
			return err
		}
		if update.Flush {
			return flushBTW(attached, terminal, control, picker, questions, approvals, clock, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, side, owned, environment)
		}
		return nil
	}
	if control.RetiredEvent(m) {
		return nil
	}
	// Any event can make stacked input sendable: an acknowledgement, a turn
	// start or end, or settled settings.
	defer func() {
		if err == nil {
			err = flushInput(attached, terminal, control, picker, questions, approvals, clock, (*model), (*tiers), environment, (*owned), hook, editor, parent, (*restoring) != nil, observations, (*reviewOwner), receipts)
		}
		if err == nil {
			err = flushBTW(attached, terminal, control, picker, questions, approvals, clock, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, side, owned, environment)
		}
	}()
	if m.Method == "skills/changed" {
		picker.toggleOwnsManager = false
		picker.SkillsLoaded = false
		picker.SkillsProblem = ""
		picker.Resolved = completion.Target{}
	}
	// Input observations precede the output items that answer them. Drain
	// before a completion, not just on the next paint tick, so links bind once.
	applyObservedActivity(attached, terminal, control, streams, usage, (*reviewOwner), approvals, receipts, clock, dirty, (*restoring), audience)
	if held, err := holdOlderHistoryEvent(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, model, effort, tier, tiers, title, backend, restoring, side, panel, resumePicker, parent, reviewOwner, receipts, notifications, retained, retainedSession, activityService, segments, restoredSegments, dispatch, resumePending, m); held || err != nil {
		return err
	}
	if handled, err := control.Requests.ObserveForkSettings(&control.Startup, &control.Live, m, correlated && response.Owner.label == "fork/settings"); handled {
		if err != nil {
			return resumeSessionFailed(attached, terminal, control, picker, questions, approvals, clock, dispatch, resumePending, err.Error())
		}
		return nil
	}
	resuming := control.Startup.Thread != "" && control.Startup.Thread != sessioncontrol.ResumePicker && (control.Thread == "" || (*restoring) != nil)
	if (resuming || control.Replacement.Target != "" || control.Replacement.Forking()) && m.Method != "" {
		if hold, keep := holdResumeEvent(&attached.session, &control.Live, control.Startup, m, resuming); hold {
			if !keep {
				return nil
			}
			if len((*resumePending)) == 256 {
				return errors.New("resume event capacity exceeded; session state is incomplete")
			}
			(*resumePending) = append((*resumePending), m)
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
		if method == "rewind/turns" {
			rewindTurnsResponse(control, picker, response.Owner, m)
			return nil
		}
		if method == "rewind/archive" {
			message := "Original thread " + response.Owner.metadata + " archived"
			if m.Error != nil {
				message = "Original thread " + response.Owner.metadata + " still exists: " + m.Error.Message
			}
			setNotice(&attached.feedback, terminal, presentationTime(clock), message, m.Error != nil)
			return nil
		}
		if method == "thread/archive" {
			if m.Error != nil {
				control.Replacement.Finish()
				setNotice(&attached.feedback, terminal, presentationTime(clock), "Failed to archive current thread: "+m.Error.Message, true)
			} else {
				(*archived), (*quit) = true, true
			}
			return nil
		}
		if strings.HasPrefix(method, "activity-skills/") {
			return skillHistoryResponse(attached, control, childKey, restoring, parent, retained, dirty, activityService, segments, restoredSegments, strings.TrimPrefix(method, "activity-skills/"), m)
		}
		if strings.HasPrefix(method, "activity/") {
			return childHistoryResponse(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, restoring, panel, resumePicker, parent, reviewOwner, receipts, notifications, retained, retainedSession, activityService, usage, segments, restoredSegments, dispatch, resumePending, method, m)
		}
		if method == "resume/settings" {
			return resumeSettingsResponse(attached, terminal, control, picker, questions, approvals, clock, approvalMode, dirty, dispatch, resumePending, panes, m)
		}
		if method == "config/read" {
			notifications.notificationConfig(&attached.feedback, terminal, clock, m)
			attached.inputHistory.configure(m)
			return nil
		}
		if method == resumePickerList {
			return resumePickerResponse(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), restoring, side, panel, resumePicker, approvalMode, quit, m)
		}
		if handled, err := statusMessage(control.Requests, dirty, (*panel), method, m, response.Owner.status); handled {
			return err
		}
		if picker.pickerMessage(attached, terminal, clock, control.Requests, (*editor), parent, environment, method, m) {
			return nil
		}
		if method == "thread/list" && control.Startup.Thread == "--last" {
			return resumeLastResponse(attached, control, picker, clock, resumePicker, approvalMode, m)
		}
		if handled, err := settingsMessage(attached, terminal, control, picker, editor, childKey, tiers, (*title), (*backend), (*restoring) != nil, (*side), clock, questions, approvals, (*panel) != nil || (*resumePicker) != nil, (*owned), panes, dirty, model, effort, tier, models, modelsLoading, reasoningKey, statusConfig, method, m); handled || err != nil {
			return err
		}
		if method == "thread/read" && applyThreadMetadata(attached, terminal, control, (*reviewOwner), approvals, receipts, clock, m, response.Owner.metadata) {
			return nil
		}
		if method == "thread/list" || method == "thread/read" {
			return restoreActivityResponse(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, restoring, panel, resumePicker, parent, reviewOwner, receipts, notifications, retained, retainedSession, audience, reviewSource, preparePane, activityService, usage, segments, restoredSegments, views, dispatch, resumePending, method, m)
		}
		if method == "turn/start" || method == "turn/steer" {
			submissionResponse(attached, terminal, control, picker, questions, approvals, clock, method, m.Error)
			return nil
		}
		if method == "thread/shellCommand" {
			shellResponse(attached, terminal, control, editor, clock, shellEdits, m.Error)
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
					shellEdits.shell = info.Shell.Path
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
				applyInputEffects(attached, terminal, questions, approvals, picker, control.Turn, clock, attached.presentation.AcknowledgeCompaction(&control.Live, attached.conversation, failure, m.Error != nil))
			} else {
				applyInputEffects(attached, terminal, questions, approvals, picker, control.Turn, clock, attached.presentation.AcknowledgeInterrupt(&control.Live, attached.conversation, method, failure, m.Error != nil))
			}
			return nil
		}
		if method == "approval/hooks" {
			return guardHookResponse(attached, terminal, control, picker, editor, questions, approvals, clock, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*restoring) != nil, owned, environment, hook, observations, parent, (*reviewOwner), receipts, m)
		}
		if method == "thread/start" && control.Replacement.Pending() && m.Error != nil {
			control.Replacement.Finish()
			restoreDrafts(attached, terminal, questions, approvals, picker, control.Turn, clock, slices.Concat(attached.conversation.Unsent, attached.conversation.Queued)...)
			attached.conversation.Unsent, attached.conversation.Queued = nil, nil
			attached.presentation.Status.SetText("Ready")
			setNotice(&attached.feedback, terminal, presentationTime(clock), "Could not clear session: "+m.Error.Message, true)
			return nil
		}
		if method == "thread/fork" && control.Startup.ForkRetention != nil {
			pending := control.Startup.ForkRetention
			control.Startup.ForkRetention = nil
			if pending.Err != nil {
				if accepted, err := control.Startup.Accept(&control.Live, method, m.Result); err == nil {
					control.Replacement.Target = accepted.Thread.ID
				}
				return resumeSessionFailed(attached, terminal, control, picker, questions, approvals, clock, dispatch, resumePending, "retaining history: "+pending.Err.Error())
			}
		}
		if (method == "thread/resume" && control.Replacement.Target != "" || method == "thread/fork" && control.Replacement.Forking()) && m.Error != nil {
			return resumeSessionFailed(attached, terminal, control, picker, questions, approvals, clock, dispatch, resumePending, m.Error.Message)
		}
		if m.Error != nil {
			attached.presentation.Status.Set(method+": "+m.Error.Message, true)
			if method == "initialize" || method == "thread/start" || method == "thread/resume" {
				return errors.New(attached.presentation.Status.Text)
			}
			return nil
		}
		switch method {
		case "initialize":
			var initialized struct {
				UserAgent string `json:"userAgent"`
			}
			if json.Unmarshal(m.Result, &initialized) == nil {
				(*backend) = backendVersion(initialized.UserAgent)
			}
			if err := control.Requests.Initialized(windows != nil); err != nil {
				return err
			}
			return openSession(attached, control, picker, clock, resumePicker, approvalMode)
		case "thread/start", "thread/resume", "thread/fork":
			result, err := control.Startup.Accept(&control.Live, method, m.Result)
			if err != nil {
				if control.Replacement.Forking() {
					return resumeSessionFailed(attached, terminal, control, picker, questions, approvals, clock, dispatch, resumePending, err.Error())
				}
				return err
			}
			if held, err := control.Requests.PrepareForkEffort(&control.Startup, &control.Live, method, &result, appServerRequest{label: "fork/settings"}); held {
				if err != nil {
					return resumeSessionFailed(attached, terminal, control, picker, questions, approvals, clock, dispatch, resumePending, err.Error())
				}
				attached.presentation.Status.SetText("Restoring fork settings…")
				return nil
			}
			if control.Replacement.Pending() {
				// Detach only after host acceptance; process and operation owners remain live.
				closing := (*side)
				if closing != nil {
					(*side) = nil
					closing.Close()
					attached.conversation.Side = nil
					pruneDraftImages((*owned), (*editor), attached.conversation, questions, approvals, terminal.Prompts, (*side))
					if err := control.Requests.ReleaseSide(closing); err != nil {
						return err
					}
				}
				if panes != nil {
					(*dirty) = paneError(attached.source, panes.save(terminal.Shell.Preferences(terminal.Main), time.Now(), true)) || (*dirty)
				}
				picker.cancelScan()
				roster := attached.session.Roster()
				retired := make([]string, 0, len(roster))
				for _, agent := range roster {
					retired = append(retired, agent.Thread)
				}
				control.RetireThreads(control.Thread, retired...)
				(*editor) = hideQuestionEditor(questions, attached.conversation, terminal, (*editor), control.Thread)
				retainedQuestions := questions.calls()
				if notifications != nil {
					clear(notifications.blocked)
				}
				(*panel) = nil
				attached.presentation.ClosePanel()
				control.Requests.Retire("main") // Retire old-thread response correlation.
				if err := control.Request("thread/unsubscribe", map[string]any{"threadId": control.Thread}); err != nil {
					return err
				}
				(*picker) = composerPicker{}
				(*exitUsage) = binding.TokenUsage{}
				attached.feedback = noticepresentation.Model{}
				terminal.Main.NoticeDetails, terminal.Main.NoticeDismiss = terminalsession.Rect{}, terminalsession.Rect{}
				releaseSegmentOutputs(attached, tracker)
				waiting := attached.conversation
				attached.session.Retire()
				workspace := attached.cwd
				inputHistory := attached.inputHistory
				(*attached) = newAttachedPresentation(views, conversationState, bindings)
				attached.cwd = workspace
				attached.inputHistory = inputHistory
				attached.conversation.CarryInput(waiting)
				(*editor) = &attached.conversation.Editor

				picker.State, picker.MenuState = &attached.conversation.Completion, attached.presentation.Menu()
				approvals.Approvals = &attached.conversation.Approvals
				questions.carryLocal(attached.conversation, retainedQuestions, control.Thread)
				terminalViews.ReplaceConversation(terminal)
				(*commits) = attachCommitReader(&attached.session, (*commits), parent, activityService != nil)
				attached.presentation.Attach(sessionpresentation.Thread{ID: cmp.Or(control.Thread, "/root"), Caller: "/root"}, false)
				if terminal != nil {
					terminal.Selection = nil
					files.closeOutput(&attached.session, terminal)
					terminal.Focus = 0
					terminal.StopDrag()
				}
				control.LeaveThread()
				control.Startup.Thread, control.Startup.Workspace = "", ""
				control.Readmit(result.Thread.ID)
				if method == "thread/resume" || method == "thread/fork" {
					// Restoration below replays events buffered during the switch.
					control.Startup.Thread = result.Thread.ID
					resetDiffScope(attached, terminal, (*reviewOwner), reviewSource, preparePane, parent)
				} else {
					// Seed the Diff's lineage; after a switch, requests alone cannot.
					restoreDiffThread(attached, terminal, (*reviewOwner), reviewSource, preparePane, result.Thread, true)
				}
			}
			(*statusConfig) = appServerStatusConfig{}
			if err := json.Unmarshal(m.Result, statusConfig); err != nil {
				return err
			}
			control.Thread = result.Thread.ID
			attached.inputHistory.selectThread(control.Thread)
			for i := range attached.conversation.Editor.InputHistory {
				entry := &attached.conversation.Editor.InputHistory[i]
				if entry.Source == control.Thread {
					entry.Restored = false
				}
			}
			attachPresentation(attached, terminal, picker, editor, questions, approvals, control.Thread, reviewOwner, commits, parent, retained, changeSource, activityService != nil)
			attached.presentation.Status.SetText("Ready")
			(*title) = result.Thread.Name
			generator.register(result.Thread)
			if name := control.Names.Attach(control.Thread); name != "" {
				generator.nameChanged(control.Thread, name)
				requestSessionTitle(control, &attached.feedback, terminal, clock, dirty, control.Names.Next(control.Thread))
			}
			(*model), (*effort), (*tier) = result.Model, result.ReasoningEffort, result.ServiceTier
			if method == "thread/resume" || method == "thread/fork" {
				control.Settings.RestoreEffort = control.TakeResumeEffort()
			}
			if method == "thread/start" {
				control.Settings.RestoredEffort()
				if usage != nil {
					usage.MarkNew(result.Thread.ID)
				}
			}
			(*models), (*modelsLoading) = nil, true // A cleared or switched session lists them again.
			if err := control.Request("model/list", map[string]any{"includeHidden": true}); err != nil {
				return err
			}
			startAttachedSession(attached, bindings, control.Thread, result.Thread.Cwd)
			(*streams) = terminalsession.Streams{}
			retainedSession.Close()
			(*retainedSession) = history.Session{}
			if notifications != nil {
				if err := control.Request("config/read", map[string]any{"cwd": result.Thread.Cwd, "includeLayers": false}); err != nil {
					return err
				}
			}
			if retained.Store != nil || panes != nil {
				if err := openWaitStore(parent, historyService, retainedSession, control.Thread, retained.Store, retained.Writes, retained.Limits); err != nil {
					setNotice(&attached.feedback, terminal, presentationTime(clock), "Wait targets could not be retained: "+err.Error(), true)
				}
			}
			restoreContextUsage(attached.session.Agent("/root"), result.Thread)
			restoreUsage(usage, result.Thread)
			observeCost(usage, control.Thread, attached.session.Agent("/root"))
			if attached.childSource != nil {
				attached.childSource.Apply(sessionpresentation.ActivityEvent{Agents: attached.session.Roster()})
			}
			if audience != nil {
				audience.Attach(control.Thread)
				attached.bindRepetitions(audience, control.Thread)
			}
			// Resumed history is applied once the roster is read, so Main can
			// follow child activity in order. An active snapshot keeps its
			// steer/interrupt target meanwhile.
			if method == "thread/resume" || method == "thread/fork" {
				for _, turn := range result.Thread.Turns {
					if turn.Status == "inProgress" {
						control.Turn = turn.ID
						attached.presentation.Status.Started = presentationTime(clock)
						attached.presentation.Status.SetText("Working")
						attached.session.Lifecycle(control.Thread).ResumeActive()
					}
				}
			}
			if panes != nil {
				(*dirty) = paneError(attached.source, panes.open(&terminal.Shell, terminal.Main, result.Thread.Cwd, control.Thread, method == "thread/resume")) || (*dirty)
			}
			if method == "thread/resume" || method == "thread/fork" {
				repair, err := result.RepairDefaultEffort(&control.Live)
				if err != nil {
					return err
				}
				(*effort) = result.ReasoningEffort
				if repair != nil {
					// Preserve saved null intent before a corrective dispatch can fail.
					if panes != nil && panes.attachment != nil && control.Startup.Evidence.Model != "" {
						if err := panes.stage(terminal.Shell.Preferences(terminal.Main)); err != nil {
							(*dirty) = paneError(attached.source, err) || (*dirty)
						} else {
							panes.attachment.Recover(control.Startup.Evidence, presentationTime(clock))
							(*dirty) = paneError(attached.source, panes.attachment.Save(presentationTime(clock), true)) || (*dirty)
						}
					}
					if _, err := updateSettings(attached, terminal, control, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, tiers, panes, dirty, (*restoring) != nil, clock, repair); err != nil {
						return err
					}
				}
				if control.Startup.MissingSettings {
					setNotice(&attached.feedback, terminal, presentationTime(clock), sessionpresentation.ResumeSettingsUnavailable, true)
					control.Startup.MissingSettings = false
				}
				(*dirty) = paneError(attached.source, panes.retainAppliedSettings(terminal, preferences.Scope{Workspace: attached.cwd, Thread: control.Thread}, control.Settings.RestoreEffort, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier), Observed: presentationTime(clock)})) || (*dirty)
				return restorePaneContent(attached, terminal, control, restoring, (*reviewOwner), reviewSource, preparePane, result.Thread)
			}
			(*dirty) = paneError(attached.source, panes.retainAppliedSettings(terminal, preferences.Scope{Workspace: attached.cwd, Thread: control.Thread}, control.Settings.RestoreEffort, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier), Observed: presentationTime(clock)})) || (*dirty)
		}
		return nil
	}
	notifications.resolve(m)
	if handled, err := approvalMessage(attached, terminal, control, picker, editor, questions, approvals, clock, (*panel) != nil || (*resumePicker) != nil, notifications, (*reviewOwner), receipts, observations, m); handled {
		return err
	}
	if handled, err := questionMessage(attached, terminal, control, picker, editor, questions, approvals, clock, (*panel) != nil || (*resumePicker) != nil, notifications, (*reviewOwner), receipts, m); handled {
		return err
	}
	if len(m.ID) != 0 {
		// Never auto-approve an unexpected server request. Leave it pending,
		// visibly blocked, until interrupted.
		attached.presentation.Status.Set("Blocked on unsupported request "+m.Method+" · Ctrl-C interrupts", true)
		notifications.blockNotification(&attached.feedback, terminal, clock, m)
		attached.source.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{Seq: attached.source.LastSeq + 1, Agent: "Session", Kind: "text", Text: attached.presentation.Status.Text, Observed: presentationTime(clock)}}})
		return nil
	}
	if handled, err := settingsMessage(attached, terminal, control, picker, editor, childKey, tiers, (*title), (*backend), (*restoring) != nil, (*side), clock, questions, approvals, (*panel) != nil || (*resumePicker) != nil, (*owned), panes, dirty, model, effort, tier, models, modelsLoading, reasoningKey, statusConfig, "", m); handled || err != nil {
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
	return observedEvent(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, panel, resumePicker, (*reviewOwner), receipts, notifications, (*retainedSession), windows, activityService, streams, tracker, trace, usage, exitUsage, m.Method, p)
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
func applyMainKey(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, childKey *scope.Key[*appServerChildHistory], model *string, effort *string, tier *string, tiers **inferencegateway.Tiers, title *string, backend *string, restoring **appServerActivityRestore, side **sessioncontrol.Side, panel **appServerStatusReport, resumePicker **appServerResumePicker, owned *map[string]bool, environment []string, hook *approvalHookCheck, observations *sessioncontrol.Observations, parent context.Context, reviewOwner **sessionreview.Review, receipts *[]*changepresentation.Receipt, notifications *nativeNotifications, shellEdits *appServerShellEdits, retained changes.Scope, retainedSession *history.Session, changeSource changes.Service, snapshots *changecapture.Workspaces, recovered *recovery.Service, publishChanges func([]changes.Update), windows *executionobservation.Windows, audience *sessioncontrol.Audience, captureNotice func(string, string, string, string), reviewSource *changecapture.Service, preparePane func(*scope.Store), approvalMode bool, generator *sessionNaming, dirty *bool, files *detailFiles, statusConfig *appServerStatusConfig, capture *transportcapture.Recorder, debugDirectory string, panes *nativePreferences, models *[]appServerModel, modelsLoading *bool, reasoningKey **bool, next terminalsession.KeyContinuation) (bool, error) {
	defer func() {
		settlePresentation(attached, terminal, control, picker, editor, childKey, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), (*restoring) != nil, (*side), clock, questions, approvals, (*panel) != nil || (*resumePicker) != nil)
	}()
	if !prepareComposerInput(attached, terminal, control, picker, editor, childKey, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), (*restoring) != nil, (*side), clock, questions, approvals) {
		return false, nil
	}
	// Acquisition stays application-owned; interpretation consumes its publication.
	if picker.Open && picker.Kind == "" && picker.completionTarget((*editor).Draft, attached.conversation.ShellMode()) != picker.Target {
		picker.refreshPicker(attached, terminal, clock, control.Requests, (*editor), parent, environment)
	}
	publishPresentation(attached, terminal, control, picker, editor, childKey, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), (*restoring) != nil, (*side), presentationTime(clock))
	refresh := false
	defer func() {
		if refresh {
			picker.refreshPicker(attached, terminal, clock, control.Requests, (*editor), parent, environment)
		}
	}()
	focused := terminal == nil || terminal.Focus == 0
	input := terminal.Main.ContinueKey(next, terminal.Prompts, transcriptView(attached.presentation, terminal), focused)
	for {
		refresh = refresh || input.Regular
		switch {
		case input.Prompt != nil:
			if handled, err := applyPromptInput(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), (*restoring) != nil, side, (*panel) != nil || (*resumePicker) != nil, owned, environment, hook, observations, parent, (*reviewOwner), receipts, notifications, *input.Prompt); handled || err != nil {
				return false, err
			}
		case input.Panel != nil:
			if input.ClearSelection && terminal != nil {
				terminal.Selection = nil
			}
			applyPanelAction(attached, terminal, clock, panel, dirty, capture, *input.Panel)
			(*dirty) = true
		case input.Menu != nil:
			if applyMenuResult(attached, terminal, control, picker, editor, childKey, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, tiers, (*title), (*backend), (*restoring) != nil, (*side), clock, questions, approvals, (*panel) != nil || (*resumePicker) != nil, (*owned), panes, dirty, parent, environment, *input.Menu) {
				return false, nil
			}
		case input.Composer != nil:
			return applyComposerInput(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, model, effort, tier, tiers, title, backend, restoring, side, panel, resumePicker, owned, environment, hook, observations, parent, reviewOwner, receipts, shellEdits, retained, retainedSession, changeSource, snapshots, recovered, publishChanges, windows, audience, captureNotice, reviewSource, preparePane, approvalMode, generator, dirty, files, statusConfig, capture, debugDirectory, panes, models, modelsLoading, reasoningKey, *input.Composer)
		}
		if input.Next == nil {
			return false, nil
		}
		publishPresentation(attached, terminal, control, picker, editor, childKey, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), (*restoring) != nil, (*side), presentationTime(clock))
		input = terminal.Main.ContinueKey(*input.Next, terminal.Prompts, transcriptView(attached.presentation, terminal), focused)
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
			attached.source.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{entry}})
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
		attached.childSource.Apply(sessionpresentation.ActivityEvent{Agents: attached.session.Roster()})
		(*dirty) = true
	}
	if audience == nil {
		return
	}
	entries := takeNativeActivity(audience, control.Thread)
	receivedReceipt := false
	// Receipts enrich the existing host item through the same reconciliation as
	// retained review publications. Keep early receipts pending until it arrives.
	entries = slices.DeleteFunc(entries, func(entry sessionpresentation.ActivityEntry) bool {
		if entry.Kind != activitypresentation.SupplementEditReceipt {
			return false
		}
		*receipts = append(*receipts, entry.Native.CapturedEdit)
		receivedReceipt = true
		return true
	})
	for _, start := range audience.TakeStarts(control.Thread) {
		entries = append(entries, transcriptUpdates(attached, terminal, streams, control.Thread, providerStarted(attached, start.Thread, start.At), false)...)
	}
	for i := range entries {
		entries[i].Seq = attached.session.Activity().NextSequence()
	}
	if len(entries) != 0 || receivedReceipt {
		applyActivity(attached, approvals, terminal, reviewOwner, receipts, control.Thread, clock, entries, nil)
		(*dirty) = true
	}
	for _, ref := range audience.TakeUnreturned(control.Thread) {
		if main, activity := attached.source.MarkUnreturned(ref.Thread, ref.Call), attached.childSource.MarkUnreturned(ref.Thread, ref.Call); main || activity {
			(*dirty) = true
		}
	}
}
