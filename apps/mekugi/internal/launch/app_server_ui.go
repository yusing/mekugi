package launch

import (
	"cmp"
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/libraries/shellinstrumentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/executionobservation"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
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
	"github.com/yusing/mekugi/libraries/textsanitize"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/modules/commandsegments/reports"
	commandsegments "github.com/yusing/mekugi/modules/commandsegments/segments"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	noticepresentation "github.com/yusing/mekugi/modules/notifications/presentation"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
	"github.com/yusing/mekugi/modules/usage/accounting"
	"github.com/yusing/mekugi/providers/openai"
)

// Codex approval policy and the invocation-local Shell guard are independent.
func startNativeSession(terminalViews terminalsession.Views, reviewSource *changecapture.Service, views *sessionpresentation.Service, conversation *conversationmodel.Service, activity *activitymodel.Service, service *history.Service, prefs *preferences.Service, segments *reports.Service, ctx context.Context, cmd *exec.Cmd, stdout *os.File, observationInputs *sessioncontrol.Observations, usage *accounting.Service, audience *sessioncontrol.Audience, reviewOwner *sessionreview.Review, windows *executionobservation.Windows, trace *nativetrace.Reader, retained changes.Scope, changeSource changes.Service, snapshots *changecapture.Workspaces, recovered *recovery.Service, publishChanges func([]changes.Update), captureNotice func(string, string, string, string), guard *sessioncontrol.ShellApprovals, guards []binding.ShellApproval, tracker *commandsegments.Observer, skillsManager bool, issues *requestexecution.Failures, resumeThread string, serviceTiers *inferencegateway.Tiers, capture *transportcapture.Recorder, resumeArgv []string, debugDirectory string, generator *sessionNaming, observations *binding.Observation, approvals bool, predictionProvider *openai.Client, api *sessionapi.Server, childSummary bool, threads *threadExecution) (func() error, error) {
	if api == nil {
		return nil, errors.New("server session requires a control API")
	}
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
	// Temporary migration scaffolding: semantic adapters still borrow native
	// presentation scratch state. Server execution never attaches or paints it.
	shell, preparePane := terminalViews.NewAttachment(diffrender.EnvironmentTheme(os.Getenv("COLORFGBG")))
	attached := newAttachedPresentation(views, conversation, observations)
	processCwd, _ := filepath.Abs(cmd.Dir)
	attached.cwd = processCwd
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

		ownedImages map[string]bool
		panes       *nativePreferences
		restoring   *appServerActivityRestore
	)

	ownedImages = make(map[string]bool)
	c := control.Host
	control.Startup.Thread = resumeThread
	control.Startup.History = retained.Store
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
	api.BindRequests(control.Requests)
	var threadChanges <-chan struct{}
	if threads != nil {
		api.BindChildAttachment(threads.AttachChild)
		policy := binding.ThreadAdmissionPolicy{}
		control.Requests.ThreadPolicy = &policy
		control.Startup.Prepared = threads.start
		if err := threads.driver.Connect(threads, audience); err != nil {
			c.Close()
			<-c.Done
			return nil, err
		}
		threadChanges = threads.driver.Changed()
	}
	notifications = new(nativeNotifications)
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

	settlePresentation(&attached, shell, &control, &picker, &editor, preferences.Applied{Model: model, Effort: effort, Tier: tier}, serviceTiers, title, version, restoring != nil, side, clock, &questions, &approvalDock, panel != nil || resumePicker != nil)
	predictions := composerPredictions{provider: predictionProvider, results: make(chan predictionConfiguration, 1)}
	native := newNativeViews(ctx, api.Native(), views, terminalViews, activity, control.Requests)
	attached.compaction = func(thread, item string) func(context.Context) (string, error) {
		return compactionSummaryReader(retainedSession, control.RolloutPath, thread, item)
	}
	native.diagnostics = &c.Diagnostics
	native.threads = threads
	syncPredictions := func() error {
		predictions.supported = sessioncontrol.PredictionForkSupported(version)
		focused, disabled, available := native.predictionEligibility(&attached)
		return predictions.sync(ctx, &attached, &control, restoring != nil || !available, focused, disabled)
	}
	adoptStartupChooser := func() {
		if control.Thread != "" || control.Startup.Thread != sessioncontrol.ResumePicker || resumePicker == nil {
			return
		}
		for id, view := range native.views {
			if !view.startup || view.ctx.Err() != nil {
				continue
			}
			attached.presentation.MoveResume(view.attached.presentation)
			view.resume, resumePicker = resumePicker, nil
			view.resume.view = id
			if response, ok := control.Requests.Lookup(view.resume.request); ok {
				response.Owner.view = id
				control.Requests.Attribute(view.resume.request, viewRequestScope(id), response.Owner)
			}
			view.projected = time.Time{}
			return
		}
	}
	var startupView string
	var latestResponse *appserver.Message
	var restoredCoordination string
	var dispatch func(appserver.Message) error
	dispatch = func(m appserver.Message) error {
		response, _ := control.Requests.Lookup(string(m.ID))
		if control.CompleteCall(m) {
			if m.Error == nil {
				if input := response.NativeDelivery(); input != nil {
					publishNativeDelivery(&attached, *input, presentationTime(clock))
				}
			}
			return nil
		}
		if threads != nil {
			syncLogicalWork(&attached, &control.Live, threads, presentationTime(clock))
			if err := observeLogicalHost(threads.driver, &control.Live, m); err != nil {
				return err
			}
		}
		if m.Method == "" {
			if response, ok := control.Requests.Lookup(string(m.ID)); ok && response.Method == "thread/list" && control.Startup.Thread == "--last" {
				// The host can finish lookup before the initial native attach is
				// admitted. Deliver through that actual view's retained effects.
				if startupView == "" {
					latestResponse = &m
					return nil
				}
				control.Requests.Take(string(m.ID))
				view := native.views[startupView]
				if view == nil || view.ctx.Err() != nil {
					quit = true
					return nil
				}
				err := resumeLastResponse(&view.attached, &control, &view.picker, clock, &view.resume, approvals, m)
				attached.presentation.BorrowExecutionStatus(view.attached.presentation)
				view.projected = time.Time{}
				return err
			}
			if response, ok := control.Requests.Lookup(string(m.ID)); ok && response.Owner.view != "" {
				control.Requests.Take(string(m.ID))
				view := native.views[response.Owner.view]
				if view == nil || view.ctx.Err() != nil || view.attached.executionPresentation != attached.executionPresentation {
					return nil
				}
				requestEditor := new(composer.Editor)
				closed := false
				replacing := control.Replacement.Pending()
				_, err := dispatchViewResponse(&view.attached, view.shell, &control, &view.picker, &requestEditor, &questions, &approvalDock, clock, preferences.Applied{Model: model, Effort: effort, Tier: tier}, serviceTiers, title, version, &restoring, &side, &view.panel, &view.resume, approvals, &closed, view.ctx, environment, response, m)
				if !replacing && control.Replacement.Pending() {
					native.replacing = response.Owner.view
				}
				view.projected = time.Time{}
				if closed {
					native.retire(response.Owner.view)
				}
				return err
			}
		}
		if m.Method == "skills/changed" {
			native.invalidateSkills(attached.executionPresentation)
		}
		err := dispatchHostMessage(&attached, shell, &control, &picker, &editor, &questions, &approvalDock, clock, &model, &effort, &tier, &serviceTiers, &title, &version, &restoring, &side, &panel, &resumePicker, &ownedImages, environment, &guardHook, observationInputs, ctx, &reviewOwner, &receipts, notifications, &shellEdits, retained, &retainedSession, changeSource, windows, audience, reviewSource, preparePane, approvals, generator, activity, &streams, tracker, trace, usage, &exitUsage, segments, &restoredSegments, views, &files, &statusConfig, issues, &quit, dispatch, &resumePending, &commits, terminalViews, conversation, observations, service, panes, &models, &modelsLoading, &archived, m)
		adoptStartupChooser()
		if err == nil && threads != nil && retainedSession.Thread != "" && retainedSession.Thread != restoredCoordination {
			restoredCoordination = retainedSession.Thread
			syncLogicalWork(&attached, &control.Live, threads, presentationTime(clock))
			attached.threadInput.restoration = threads.driver.Restore(retainedSession)
		}
		predictions.observe(m, control.Thread)
		return err
	}
	live := &apiLive{server: api, started: time.Now()}
	attached.restoreInput = native.restore
	attached.restoreShell = native.restoreShell
	attached.restoreSide = native.restoreSide
	attached.deliverActivity = native.deliverActivity
	notifications.relay = func(event, message string) { native.notify(attached.executionPresentation, event, message) }
	attached.activeThread = api.ActiveThread
	attached.reserveThread = api.ReserveThread
	attached.nativeImages = native.imageClaims
	attached.rewindArchive = native.rewindArchive
	nativeRequests, nativeClosed := api.Native().Requests(), native.closed
	intents := api.Intents()
	live.publish(&attached, shell, &control, reviewOwner, reviewSource, restoring != nil, childSummary, presentationTime(clock))
	native.retiring = func(view *nativeSessionView) error {
		defer pruneDraftImages(ownedImages, editor, attached.conversation, &questions, &approvalDock, shell.Prompts, side, attached.nativeImages)
		if view.startup && control.Thread == "" && (control.Startup.Thread == sessioncontrol.ResumePicker || control.Startup.Thread == "--last") {
			quit = true
		}

		return nil
	}
	applyNativeOperation := func(view *nativeSessionView, request sessionapi.NativeRequest) (refusal string, err error) {
		operation := request.Operation
		inputThread := view.attached.presentation.View().Input.ID
		if operation.Menu != nil && operation.Menu.Thread != "" {
			inputThread = operation.Menu.Thread
		}
		recovery := operation.Recovery
		if recovery != nil {
			effect := view.effects.recovery(recovery.Effect)
			if effect == nil {
				return "That retained submission is no longer available.", nil
			}
			if recovery.Action == sessionapi.EditRecovery {
				if effect.Recovery == nil {
					effect.Recovery = &sessionapi.NativeRecoveryView{}
				}
				effect.Recovery.Editing = true
				return "", nil
			}
			if recovery.Action == sessionapi.RetryRecovery {
				current := view.attached.presentation.View()
				request.Version = current.Version
				draft := effect.RecoveryDraft().Clone()
				if recovery.Text != nil {
					if effect.Recovery == nil || !effect.Recovery.Editing {
						return "Open the retained input editor before editing.", nil
					}
					draft, err = draft.WithTextParts(*recovery.Text)
					if err != nil {
						return err.Error(), nil
					}
				}
				if effect.ShellRestore != nil && !strings.HasPrefix(strings.TrimSpace(draft.Text), "!") {
					draft.Text = "!" + draft.Text
				}
				request.Prepared = sessionapi.NewPreparedInput(draft, nil)
				mode := sessionpresentation.SubmitComposer
				if effect.Recovery != nil && effect.Recovery.Queued {
					mode = sessionpresentation.QueueComposer
				}
				request.Operation = sessionapi.NativeOperation{Composer: &sessionapi.NativeComposerInput{Intent: sessionpresentation.ComposerIntent{Thread: effect.Thread, Version: current.Version, Operation: mode}}}
				operation = request.Operation
			} else if recovery.Action != sessionapi.DiscardRecovery {
				return "Choose retry or discard for retained input.", nil
			}
			defer func() {
				if refusal == "" && err == nil {
					view.effects.discardRecovery(recovery.Effect)
					pruneDraftImages(ownedImages, editor, attached.conversation, &questions, &approvalDock, shell.Prompts, side, attached.nativeImages)
				}
			}()
			if recovery.Action == sessionapi.DiscardRecovery {
				return "", nil
			}
		}
		// Reserve before admission can consume input or dispatch a host effect.
		// Interrupts and receipts use already-reserved restoration capacity.
		capacity := 0
		switch {
		case operation.Composer != nil && operation.Composer.Intent.Operation == sessionpresentation.RestoreWaiting:
			// Same-view inputs already own capacity; cross-view edits acquire it
			// before moving any input or releasing its previous owner's slot.
			for _, stack := range [][]conversationmodel.Input{attached.conversation.Unsent, attached.conversation.Queued} {
				for _, input := range stack {
					if input.Question == nil && input.Origin != view.attached.view {
						capacity++
					}
				}
			}
		case operation.Composer != nil && operation.Composer.Intent.Operation == sessionpresentation.SteerWaiting:
			// Promotion retains the existing input reservations.
		case operation.Command != nil || operation.Composer != nil:
			capacity = 3 // input provenance, presentation, command deletion
		case operation.Menu != nil:
			capacity = 2 // menu consequence and editor replacement
		case operation.Completion != nil || operation.Reference != nil || operation.Resume != nil:
			capacity = 1
		}
		if capacity > 0 {
			native.reconcileEffects(view)
			reservation := view.effects.reserve(capacity)
			if reservation == nil {
				return "Waiting for earlier editor consequences to be received; input is preserved.", nil
			}
			view.effects.current = reservation
			defer func() {
				view.effects.current = nil
				reservation.release()
			}()
		}
		replacing := control.Replacement.Pending()
		settings := preferences.Applied{Model: model, Effort: effort, Tier: tier}
		requestEditor := new(composer.Editor)
		if operation.Editor != nil {
			requestEditor.Replace(operation.Editor.Draft)
		}
		closed := false
		switch {
		case operation.SaveDraft != nil:
			attached.inputHistory.remember(*operation.SaveDraft)
		case operation.Completion != nil:
			view.complete(*operation.Completion, clock, control.Requests, environment)
		case operation.Preferences != nil:
			if err := panes.stageNative(operation.Preferences.Presentation, presentationTime(clock), operation.Preferences.Flush); err != nil {
				return err.Error(), nil
			}
			layout, _ := terminalsession.ReadPreferences(operation.Preferences.Presentation)
			view.shell.Main.InterruptLocked, view.shell.Main.PredictionsDisabled = layout.InterruptLocked, layout.PredictionsDisabled
		case operation.Command != nil || operation.Composer != nil:
			intent := sessionpresentation.ComposerIntent{Version: view.attached.presentation.View().Version, Thread: view.attached.presentation.View().Input.ID, Operation: sessionpresentation.SubmitComposer}
			if operation.Composer != nil {
				intent = operation.Composer.Intent
				requestEditor.Replace(operation.Composer.Draft)
				if request.Prepared != nil {
					requestEditor.Replace(request.Prepared.Draft)
					if intent.Version != request.Version {
						return sessionpresentation.ErrStaleIntent.Error(), nil
					}
				}
			} else {
				requestEditor.Text = *operation.Command
				if operation.Editor != nil {
					intent.Target = operation.Editor.Target
				}
			}
			before := requestEditor.Draft
			if intent.Thread != "" {
				inputThread = intent.Thread
			}
			if intent.Operation.ClientLocal() {
				return "This operation belongs to the client editor.", nil
			}
			commandText := strings.TrimSpace(requestEditor.Text)
			boundInput := (intent.Operation == sessionpresentation.SubmitComposer || intent.Operation == sessionpresentation.QueueComposer) && intent.Thread != "" && !strings.HasPrefix(commandText, "/") && !strings.HasPrefix(commandText, "!")
			boundMainInput := boundInput && intent.Thread == control.Thread
			if boundInput {
				if err := view.attached.presentation.AdmitBoundInput(intent); err != nil {
					return err.Error(), nil
				}
			} else {
				if err := view.attached.presentation.AdmitClientComposer(intent); err != nil {
					return err.Error(), nil
				}
			}
			if intent.Operation == sessionpresentation.RestoreWaiting || intent.Operation == sessionpresentation.SteerWaiting {
				if view.attached.presentation.DisplayingSide() || inputThread != control.Thread {
					return "Return to Main to use this control.", nil
				}
				active, err := view.attached.presentation.ResolveInterrupt(sessionpresentation.InterruptIntent{Version: intent.Version, Target: *intent.Work, Mode: sessioncontrol.RestoreWaiting}, &control.Live, &attached.session)
				if err != nil {
					return err.Error(), nil
				}
				if !active {
					setNotice(&view.attached.feedback, view.shell, presentationTime(clock), "Original work already ended.", false)
					return "", nil
				}
				if intent.Operation == sessionpresentation.SteerWaiting {
					admission := intent
					admission.Operation = sessionpresentation.SubmitComposer
					plan, failure := view.attached.presentation.ClientInput(admission, &control.Live, attached.conversation, restoring != nil)
					if failure != nil {
						return failure.Error(), nil
					}
					if !attached.conversation.SteerQueued() {
						return "No queued messages can be steered.", nil
					}
					if plan.Flush {
						return "", flushInput(&attached, shell, &control, &picker, &questions, &approvalDock, clock, model, serviceTiers, environment, ownedImages, &guardHook, &editor, ctx, restoring != nil, observationInputs, reviewOwner, &receipts)
					}
					return "", nil
				}
				native.claimWaiting(view)
			}
			if boundInput && !boundMainInput {
				if side == nil || intent.Thread != cmp.Or(side.Thread, "side:"+side.Key) {
					return sessionpresentation.ErrStaleIntent.Error(), nil
				}
				err = submitBTW(&view.attached, view.shell, &control, &view.picker, &requestEditor, &questions, &approvalDock, clock, settings, serviceTiers, title, version, restoring != nil, &side, view.panel != nil || view.resume != nil, &ownedImages, environment, &guardHook, observationInputs, &composerPreparation{ctx: request.Context, input: request.Prepared, side: true})
				if requestEditor.Text != "" {
					return view.attached.feedback.Text, err
				}
				return "", err
			}
			if intent.Operation == sessionpresentation.SubmitComposer || intent.Operation == sessionpresentation.QueueComposer {
				text := strings.TrimSpace(requestEditor.Text)
				if !strings.HasPrefix(text, "/") && (boundMainInput || !view.attached.presentation.DisplayingSide()) {
					if control.Replacement.Pending() && native.replacing != view.attached.view {
						return "This session is being replaced by another view; refresh before submitting.", nil
					}
					if request.Prepared == nil {
						return "The input has no prepared attachment snapshot.", nil
					}
					plan, admissionErr := view.attached.presentation.ClientInput(intent, &control.Live, attached.conversation, restoring != nil)
					if admissionErr != nil {
						return admissionErr.Error(), nil
					}
					requestEditor.Replace(request.Prepared.Draft)
					if strings.HasPrefix(text, "!") {
						err = submitShell(&view.attached, view.shell, &control, &view.picker, &requestEditor, clock, restoring != nil, environment, ctx, reviewOwner, &shellEdits, retained, retainedSession, changeSource, snapshots, recovered, publishChanges, windows, audience, captureNotice, reviewSource, preparePane)
						if requestEditor.Text != "" {
							return view.attached.feedback.Text, err
						}
					} else {
						if text == "" && len(request.Prepared.Draft.Images) == 0 && len(request.Prepared.Draft.Attachments) == 0 {
							return "Enter a message or attach content.", nil
						}
						if ownedImages == nil {
							ownedImages = make(map[string]bool)
						}
						if !request.Prepared.Admit(request.Context, ownedImages) {
							return "The prepared input was canceled before admission.", nil
						}
						input := conversationmodel.Input{Draft: request.Prepared.Draft, Origin: view.attached.view, Delivery: view.effects.input()}
						attached.conversation.QueueInput(input, plan.Queued)
						if input.AttachmentNotice != "" {
							setNotice(&view.attached.feedback, view.shell, presentationTime(clock), input.AttachmentNotice, true)
						}
						if plan.Flush {
							err = flushInput(&attached, shell, &control, &picker, &questions, &approvalDock, clock, model, serviceTiers, environment, ownedImages, &guardHook, &editor, ctx, restoring != nil, observationInputs, reviewOwner, &receipts)
						}
					}
					return "", err
				}
			}
			view.attached.presentation.BorrowExecutionStatus(attached.presentation)
			closed, err = applyComposerCommand(&view.attached, view.shell, &control, &view.picker, &requestEditor, &questions, &approvalDock, clock, &model, &effort, &tier, &serviceTiers, &title, &version, &restoring, &side, &view.panel, &view.resume, &ownedImages, environment, &guardHook, observationInputs, ctx, &reviewOwner, &receipts, &shellEdits, retained, &retainedSession, changeSource, snapshots, recovered, publishChanges, windows, audience, captureNotice, reviewSource, preparePane, approvals, generator, &view.files, &statusConfig, capture, debugDirectory, panes, &models, &modelsLoading, intent, &composerPreparation{ctx: request.Context, input: request.Prepared, side: side != nil && inputThread == cmp.Or(side.Thread, "side:"+side.Key)})
			if !strings.HasPrefix(strings.TrimSpace(before.Text), "/") && requestEditor.Text != "" {
				return view.attached.feedback.Text, err
			}
			attached.presentation.BorrowExecutionStatus(view.attached.presentation)
			if intent.Operation != sessionpresentation.RestoreWaiting {
				view.effects.add(sessionapi.NativeEffect{Thread: control.Thread, Presentation: &sessionapi.NativePresentationEffect{LiveHidden: view.shell.LiveHidden, InterruptLocked: view.shell.Main.InterruptLocked, PredictionsDisabled: view.shell.Main.PredictionsDisabled}})
			}
			if strings.HasPrefix(strings.TrimSpace(before.Text), "/") {
				if operation.Composer != nil && operation.Composer.Consumed {
					if requestEditor.Text != "" && err == nil {
						view.effects.add(sessionapi.NativeEffect{Thread: intent.Thread, Restore: new(requestEditor.Draft.Clone())})
					}
				} else if recovery == nil && requestEditor.Text == "" && (operation.Composer != nil || operation.Editor != nil) {
					// A raw command without editor metadata never occupied a draft.
					view.effects.add(sessionapi.NativeEffect{Thread: inputThread, Target: new(intent.Target), Action: &composer.Action{Kind: composer.DeleteAction, Start: 0, End: len(before.Text)}})
				}
			}
		case operation.Menu != nil:
			action, admissionErr := view.menuAction(request)
			if admissionErr != nil {
				return admissionErr.Error(), nil
			}
			before := requestEditor.Draft.Clone()
			applyMenuResult(&view.attached, view.shell, &control, &view.picker, &requestEditor, settings, &serviceTiers, title, version, restoring != nil, &side, clock, &questions, &approvalDock, view.panel != nil || view.resume != nil, ownedImages, panes, view.ctx, environment, terminalsession.MenuResult{Action: action, Handled: true})
			dispatchRewind(&view.attached, view.shell, &control, &view.picker, clock, ctx, reviewOwner, &shellEdits, retained, retainedSession, changeSource, snapshots, recovered, publishChanges, windows, audience, captureNotice, reviewSource, preparePane)
			if operation.Editor != nil && !reflect.DeepEqual(before, requestEditor.Draft) {
				draft := requestEditor.Draft.Clone()
				view.effects.add(sessionapi.NativeEffect{Thread: inputThread, Target: new(operation.Editor.Target), Action: &composer.Action{Kind: composer.DeleteAction, Start: 0, End: len(before.Text)}, Restore: &draft})
			}
		case operation.Resume != nil:
			err = applyResumeAction(&view.attached, view.shell, &control, &view.picker, &requestEditor, &questions, &approvalDock, clock, settings, serviceTiers, title, version, &restoring, &side, &view.panel, &view.resume, approvals, &closed, *operation.Resume)
		case operation.Notice != nil:
			notice := operation.Notice
			current := view.attached.presentation.View()
			if !notice.Version.SameAttachment(current.Version) {
				return sessionpresentation.ErrStaleIntent.Error(), nil
			}
			if !notice.Dismiss {
				return view.openNoticeDetails(notice.Text), nil
			}
			if notice.Feedback != current.Status.FeedbackRevision {
				return "", nil
			}
			setNotice(&view.attached.feedback, view.shell, presentationTime(clock), "", false)
		case operation.Reference != nil:
			referenceInput(&view.attached, view.shell, &control, &view.picker, &requestEditor, &questions, &approvalDock, clock, settings, serviceTiers, title, version, &restoring, &side, &view.panel, &view.resume, &ownedImages, environment, view.ctx, *operation.Reference)
		case operation.Side != nil:
			if admissionErr := view.attached.presentation.AdmitCloseSide(*operation.Side); admissionErr != nil {
				return admissionErr.Error(), nil
			}
			if side == nil || operation.Side.Key != side.Key || operation.Side.Exchange != side.ID {
				return sessionpresentation.ErrStaleIntent.Error(), nil
			}
			err = closeSide(&attached, shell, &control, &editor, &questions, &approvalDock, &side, &ownedImages)
		case operation.BTW != nil:
			current := view.attached.presentation.View()
			if !operation.BTW.Version.SameAttachment(current.Version) || current.BTW == nil || operation.BTW.Key == "" || operation.BTW.Key != current.BTW.Key || operation.BTW.Exchange != current.BTW.ID || control.BTW == nil || operation.BTW.Key != control.BTW.Key || operation.BTW.Exchange != control.BTW.ID {
				return sessionpresentation.ErrStaleIntent.Error(), nil
			}
			err = closeBTWQuestion(&attached, &control)
		case operation.Interrupt != nil:
			active, admissionErr := view.attached.presentation.ResolveInterrupt(*operation.Interrupt, &control.Live, &attached.session)
			if admissionErr != nil {
				return admissionErr.Error(), nil
			}
			if !active {
				setNotice(&view.attached.feedback, view.shell, presentationTime(clock), "Original work already ended.", false)
				return "", nil
			}
			if operation.Interrupt.Mode == sessioncontrol.RestoreWaiting && cancelQueuedCompact(&attached, shell, &control, &picker, &questions, &approvalDock, clock) {
				return "", nil
			}
			decision := control.RequestInterrupt(operation.Interrupt.Mode, len(attached.conversation.Unsent) > 0 && attached.conversation.Unsent[0].Text != "/compact")
			err = applyInterruption(&attached, shell, &control, &picker, &editor, &questions, &approvalDock, clock, reviewOwner, &receipts, decision)
		case operation.Panel != nil:
			action, admissionErr := view.panelAction(request)
			if admissionErr != nil {
				return admissionErr.Error(), nil
			}
			applyPanelAction(&view.attached, view.shell, clock, &view.panel, capture, action)
		default:
			return view.applyView(operation), nil
		}
		pruneDraftImages(ownedImages, editor, attached.conversation, &questions, &approvalDock, shell.Prompts, side, attached.nativeImages)
		if !replacing && control.Replacement.Pending() {
			native.replacing = view.attached.view
		}
		if closed {
			native.retire(view.attached.view)
		}
		return "", err
	}
	return func() error {
		defer control.Requests.Close()
		defer native.close()
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
		runLoop := func() error {
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
			}
			tick := time.NewTicker(33 * time.Millisecond)
			defer tick.Stop()
			for {
				if quit {
					return nil
				}
				if native.failure != nil {
					return native.failure
				}
				select {
				case retainedErr := <-control.Startup.ForkRetained():
					pending := control.Startup.ForkRetention
					pending.Err, pending.Complete = retainedErr, true
					if err := dispatch(pending.Message); err != nil {
						return err
					}
				case result := <-inputHistory.loads:
					inputHistory.loaded(attached.conversation, result)
				case historyErr := <-inputHistory.failures:
					setNotice(&attached.feedback, shell, presentationTime(clock), "Input history unavailable: "+historyErr.Error(), true)
				case <-ctx.Done():
					return ctx.Err()
				case result := <-predictions.results:
					if err := syncPredictions(); err != nil {
						return err
					}
					if err := predictions.configured(result, &control); err != nil {
						return err
					}
				case <-autoChanged:
					reviewOwner.Projection.Workspace = review.Workspace()
					publishReview(&attached, shell, reviewOwner, reviewSource, preparePane)
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
				case <-diffReady:
					if !admitReviewEvents(&attached, shell, reviewOwner, reviewSource, preparePane, &receipts, clock, ctx, sub, review.TakePreviews(sub)...) {
						resubscribe()
					}
					if err := finishRestoredContent(&attached, &control, &picker, &restoring, &reviewOwner, dispatch, &resumePending); err != nil {
						return err
					}
				case event := <-diffEvents:
					if !admitReviewEvents(&attached, shell, reviewOwner, reviewSource, preparePane, &receipts, clock, ctx, sub, event) {
						resubscribe()
					}
					if err := finishRestoredContent(&attached, &control, &picker, &restoring, &reviewOwner, dispatch, &resumePending); err != nil {
						return err
					}
				case result := <-files.links.results:
					files.links.finish(result)
				case result := <-files.fileReads:
					if _, path := files.loadedMarkdownFile(&attached.session, attached.presentation, attached.cwd, shell, result); path != "" {
						copyText(&attached.feedback, shell, clock, shell, path)
					}
				case result := <-picker.scanResults:
					picker.applyScan(attached.cwd, picker.completionTarget(editor.Draft, attached.conversation.ShellMode()), result)
				case <-commits.Updates():
					startCommitReads(&attached, commits)
				case err := <-writes:
					commandSegmentRetained(&pendingWrites, issues, err)
				case update := <-titleUpdates:
					persistSessionTitle(&control, generator, &attached.feedback, shell, clock, update)
				case request := <-guard.Requests():
					addGuardApproval(&attached, shell, &control, &picker, &editor, &questions, &approvalDock, clock, panel != nil || resumePicker != nil, notifications, reviewOwner, &receipts, request)
				case call := <-control.Calls():
					control.Dispatch(call)
				case <-threadChanges:
					syncLogicalWork(&attached, &control.Live, threads, presentationTime(clock))
					for _, failure := range threads.driver.Failures() {
						issues.AddThreadNotice(control.Thread, control.Turn, "threadcontrol", failure.Error())
					}
				case message, ok := <-c.Messages:
					if !ok {
						exited = true
						return errors.Join(errors.New("app-server disconnected; active work may be incomplete"), <-c.Done)
					}
					if err := dispatch(message); err != nil || quit {
						return err
					}
				case id := <-nativeClosed:
					native.retire(id)
				case request := <-nativeRequests:
					var refusal string
					var operationErr error
					if request.Attach {
						refusal = native.attach(request, &attached, control.Thread)
						if refusal == "" && request.Startup != "" && startupView == "" {
							startupView = request.Attachment
							if latestResponse != nil {
								response := *latestResponse
								latestResponse = nil
								operationErr = dispatch(response)
							}
						}
						adoptStartupChooser()
					} else {
						var view *nativeSessionView
						view, refusal = native.admit(request, &attached, control.Thread)
						if refusal == "" {
							switch operation := request.Operation; {
							case operation.Answers != nil:
								refusal, operationErr = applyNativeAnswers(&attached, shell, &control, &picker, &editor, preferences.Applied{Model: model, Effort: effort, Tier: tier}, serviceTiers, title, version, restoring != nil, side, clock, &questions, &approvalDock, panel != nil || resumePicker != nil, environment, ownedImages, &guardHook, ctx, observationInputs, reviewOwner, &receipts, *operation.Answers, request.Prepared, request.Context, request.RemoteHuman())
							case operation.Approval != nil:
								record, choice, err := view.attached.presentation.Approval(*operation.Approval, approvalDock.Approvals)
								if err != nil {
									refusal = err.Error()
								} else {
									operationErr = applyApproval(&attached, shell, &control, &picker, &editor, &questions, &approvalDock, clock, notifications, reviewOwner, &receipts, observationInputs, approvalDock.handles[record], choice, operation.Approval.Reason)
								}
							default:
								refusal, operationErr = applyNativeOperation(view, request)
							}
							view.projected = time.Time{}
						}
					}
					if request.Operation.Receipt != nil {
						pruneDraftImages(ownedImages, editor, attached.conversation, &questions, &approvalDock, shell.Prompts, side, attached.nativeImages)
					}
					if operationErr != nil {
						refusal = "The session failed: " + operationErr.Error()
					}
					// Mutation acknowledgement is a publication barrier for keyboard
					// continuations. Publish before releasing the requesting client.
					native.publish(&attached, &control, preferences.Applied{Model: model, Effort: effort, Tier: tier}, serviceTiers, title, version, restoring != nil, side, reviewOwner, reviewSource, nativePublicationFacts(&attached, panes, exitUsage, debugDirectory, archived, notifications, control.Thread, &predictions), presentationTime(clock))
					if request.Reply != nil {
						request.Reply <- refusal
					}
					if operationErr != nil {
						return operationErr
					}
				case intent := <-intents:
					refusal := apiIntentRefusal(&attached, &control, intent)
					var intentErr error
					if refusal == "" {
						if intent.Action == sessionapi.RunShell {
							refusal, intentErr = submitAPIShell(&attached, shell, &control, &picker, clock, restoring != nil, environment, ctx, reviewOwner, &shellEdits, retained, retainedSession, changeSource, snapshots, recovered, publishChanges, windows, audience, captureNotice, reviewSource, preparePane, intent.Text)
						} else {
							refusal, intentErr = applyAPIIntent(&attached, shell, &control, &picker, &editor, &questions, &approvalDock, clock, preferences.Applied{Model: model, Effort: effort, Tier: tier}, serviceTiers, title, version, restoring != nil, &side, panel != nil || resumePicker != nil, &ownedImages, environment, &guardHook, observationInputs, ctx, reviewOwner, &receipts, notifications, intent)
						}
					}
					if intentErr != nil {
						refusal = "The session failed: " + intentErr.Error()
					}
					if intent.Reply != nil {
						intent.Reply <- refusal
					}
					if intentErr != nil || quit {
						return intentErr
					}
				case <-tick.C:
					if threads != nil {
						syncLogicalWork(&attached, &control.Live, threads, presentationTime(clock))
						threads.driver.Tick(presentationTime(clock))
					}
					if side != nil && len(side.Drafts.Unsent) > 0 {
						if err := flushBTW(&attached, shell, &control, &picker, &questions, &approvalDock, clock, preferences.Applied{Model: model, Effort: effort, Tier: tier}, &side, &ownedImages, environment, &guardHook, observationInputs); err != nil {
							return err
						}
					}
					if control.Shell.Continuation() || attached.threadInput.preparing() || len(attached.conversation.Unsent) > 0 && attached.conversation.Unsent[0].Question != nil && (observationInputs == nil || !observationInputs.EmittingToolInput(attached.cwd, control.Thread)) {
						if err := flushInput(&attached, shell, &control, &picker, &questions, &approvalDock, clock, model, serviceTiers, environment, ownedImages, &guardHook, &editor, ctx, restoring != nil, observationInputs, reviewOwner, &receipts); err != nil {
							return err
						}
					}
					refreshSessionMetrics(panel, capture, presentationTime(clock), false)
					applyObservedActivity(&attached, shell, &control, &streams, usage, reviewOwner, &approvalDock, &receipts, clock, restoring, audience)
					publishPresentation(&attached, shell, &control, &picker, &editor, preferences.Applied{Model: model, Effort: effort, Tier: tier}, serviceTiers, title, version, restoring != nil, side, presentationTime(clock))
					if err := applyResumeAction(&attached, shell, &control, &picker, &editor, &questions, &approvalDock, clock, preferences.Applied{Model: model, Effort: effort, Tier: tier}, serviceTiers, title, version, &restoring, &side, &panel, &resumePicker, approvals, &quit, shell.Main.Resume.PageDemand()); err != nil {
						return err
					}
					flushStreamOutput(&attached, shell, &control, &streams, tracker, reviewOwner, &approvalDock, &receipts, clock, ctx, retained, segments, &writes, &pendingWrites, issues)
					startCommitReads(&attached, commits)
					paneError(attached.source, panes.savePrepared(presentationTime(clock), false))
					if !expireNotice(&attached.feedback, shell, presentationTime(clock)) {
						expireApprovals(&attached, shell, &control, &picker, &editor, &questions, &approvalDock, clock, notifications, reviewOwner, &receipts)
					}
					for _, view := range native.views {
						if view.ctx.Err() != nil || view.attached.executionPresentation != attached.executionPresentation {
							continue
						}
						expireNotice(&view.attached.feedback, view.shell, presentationTime(clock))
						refreshSessionMetrics(view.panel, capture, presentationTime(clock), false)
						if view.notices == nil {
							requestEditor := new(composer.Editor)
							view.notices = applyCriticalNotices(&view.attached, view.shell, &control, &view.picker, &requestEditor, clock, preferences.Applied{Model: model, Effort: effort, Tier: tier}, serviceTiers, title, version, &restoring, &side, audience, issues)
						}
					}
					notices := applyCriticalNotices(&attached, shell, &control, &picker, &editor, clock, preferences.Applied{Model: model, Effort: effort, Tier: tier}, serviceTiers, title, version, &restoring, &side, audience, issues)
					settlePresentation(&attached, shell, &control, &picker, &editor, preferences.Applied{Model: model, Effort: effort, Tier: tier}, serviceTiers, title, version, restoring != nil, side, clock, &questions, &approvalDock, panel != nil || resumePicker != nil)
					live.publish(&attached, shell, &control, reviewOwner, reviewSource, restoring != nil, childSummary, presentationTime(clock))
					native.publish(&attached, &control, preferences.Applied{Model: model, Effort: effort, Tier: tier}, serviceTiers, title, version, restoring != nil, side, reviewOwner, reviewSource, nativePublicationFacts(&attached, panes, exitUsage, debugDirectory, archived, notifications, control.Thread, &predictions), presentationTime(clock))
					// Publication is not a native paint receipt. Release claims without
					// acknowledging notices that no terminal has displayed.
					notices.finish(terminalsession.NoticeReceipt{})
					continue
				}
				if err := syncPredictions(); err != nil {
					return err
				}
				settlePresentation(&attached, shell, &control, &picker, &editor, preferences.Applied{Model: model, Effort: effort, Tier: tier}, serviceTiers, title, version, restoring != nil, side, clock, &questions, &approvalDock, panel != nil || resumePicker != nil)
				live.publish(&attached, shell, &control, reviewOwner, reviewSource, restoring != nil, childSummary, presentationTime(clock))
				native.publish(&attached, &control, preferences.Applied{Model: model, Effort: effort, Tier: tier}, serviceTiers, title, version, restoring != nil, side, reviewOwner, reviewSource, nativePublicationFacts(&attached, panes, exitUsage, debugDirectory, archived, notifications, control.Thread, &predictions), presentationTime(clock))
			}
		}
		err = runLoop()
		if saveErr := panes.savePrepared(presentationTime(clock), true); saveErr != nil {
			fmt.Fprintln(stdout, "Pane layout or settings could not be saved:", textsanitize.Safe(saveErr.Error(), false))
		}
		if panes != nil {
			err = errors.Join(err, panes.writer.close(panes.attachment, presentationTime(clock)))
		}
		if !exited {
			if ctx.Err() != nil {
				_ = c.Cancel()
			}
			err = errors.Join(err, c.Shutdown())
		}
		finishCommandSegments(&writes, &pendingWrites, issues)
		for _, view := range native.views {
			view.projected = time.Time{}
		}
		native.publish(&attached, &control, preferences.Applied{Model: model, Effort: effort, Tier: tier}, serviceTiers, title, version, restoring != nil, side, reviewOwner, reviewSource, nativePublicationFacts(&attached, panes, exitUsage, debugDirectory, archived, notifications, control.Thread, &predictions), presentationTime(clock))
		for key, run := range attached.session.Commands() {
			if run.Entry.Native.Invocation.ReleaseSegmentOutputs() {
				tracker.ReleaseOutput(key)
			}
		}
		reviewOwner.Close()
		editor = hideQuestionEditor(&questions, attached.conversation, shell, editor, control.Thread)
		hideApprovals(&approvalDock, shell, &attached.conversation.PromptEditor, &attached.conversation.Editor)
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
			if diagnostic := c.Diagnostics.Snapshot(); diagnostic != "" {
				fmt.Fprintln(stdout, diagnostic)
			}
		}
		return err
	}, nil
}

// appServerRequest contains response routing data, not a second request ledger.
// Main's lifecycle scope retires on confirmed replacement. Side conversations
// and title saves retain their own completion and cleanup across that boundary.
type appServerRequest struct {
	view     string
	metadata string
	menu     uint64
	label    string
	title    *sessioncontrol.NameSave
	status   *appServerStatusReport
}

func dispatchHostMessage(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, model *string, effort *string, tier *string, tiers **inferencegateway.Tiers, title *string, backend *string, restoring **appServerActivityRestore, side **sessioncontrol.Side, panel **appServerStatusReport, resumePicker **appServerResumePicker, owned *map[string]bool, environment []string, hook *approvalHookCheck, observations *sessioncontrol.Observations, parent context.Context, reviewOwner **sessionreview.Review, receipts *[]*changepresentation.Receipt, notifications *nativeNotifications, shellEdits *appServerShellEdits, retained changes.Scope, retainedSession *history.Session, changeSource changes.Service, windows *executionobservation.Windows, audience *sessioncontrol.Audience, reviewSource *changecapture.Service, preparePane func(*scope.Store), approvalMode bool, generator *sessionNaming, activityService *activitymodel.Service, streams *terminalsession.Streams, tracker *commandsegments.Observer, trace *nativetrace.Reader, usage *accounting.Service, exitUsage *binding.TokenUsage, segments *reports.Service, restoredSegments *map[string]*reports.Report, views *sessionpresentation.Service, files *detailFiles, statusConfig *appServerStatusConfig, issues *requestexecution.Failures, quit *bool, dispatch func(appserver.Message) error, resumePending *[]appserver.Message, commits **activitypresentation.CommitReader, terminalViews terminalsession.Views, conversationState *conversationmodel.Service, bindings *binding.Observation, historyService *history.Service, panes *nativePreferences, models *[]appServerModel, modelsLoading *bool, archived *bool, m appserver.Message) (err error) {
	defer func() {
		if r := control.Rewind; r != nil && r.Completed && r.Failed && r.Item == "" && shellEdits.pending != nil && shellEdits.pending.Thread() == r.Thread {
			shellEdits.pending.Discard()
			shellEdits.pending = nil
		}
		finishRewindRevert(attached, terminal, control, clock)
		settlePresentation(attached, terminal, control, picker, editor, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), (*restoring) != nil, (*side), clock, questions, approvals, (*panel) != nil || (*resumePicker) != nil)
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
		return err
	}
	if sessionTitleMessage(control, generator, &attached.feedback, terminal, clock, title, m, response.Owner.title) {
		return nil
	}
	defer func() {
		picker.refreshPicker(attached, terminal, clock, control.Requests, (*editor), parent, environment)
	}()
	// Side control settles first; work-capable forks share native event consumers.
	if update, err := control.Requests.ObserveSide(m, response, control.Thread, observations); update.Handled {
		applySideUpdate(attached, terminal, control, clock, update)
		if err != nil {
			return err
		}

		if update.Flush {
			return flushBTW(attached, terminal, control, picker, questions, approvals, clock, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, side, owned, environment, hook, observations)
		}
		if !update.Forward || update.Side == control.BTW {
			return nil
		}
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
			err = flushBTW(attached, terminal, control, picker, questions, approvals, clock, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, side, owned, environment, hook, observations)
		}
	}()
	if m.Method == "skills/changed" {
		picker.invalidateSkills()
	}
	// Input observations precede the output items that answer them. Drain
	// before a completion, not just on the next paint tick, so links bind once.
	applyObservedActivity(attached, terminal, control, streams, usage, (*reviewOwner), approvals, receipts, clock, (*restoring), audience)
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
		if handled, err := dispatchViewResponse(attached, terminal, control, picker, editor, questions, approvals, clock, preferences.Applied{Model: *model, Effort: *effort, Tier: *tier}, *tiers, *title, *backend, restoring, side, panel, resumePicker, approvalMode, quit, parent, environment, response, m); handled || err != nil {
			return err
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
		if method == "resume/settings" {
			return resumeSettingsResponse(attached, terminal, control, picker, questions, approvals, clock, approvalMode, dispatch, resumePending, panes, m)
		}
		if method == "config/read" {
			notifications.notificationConfig(&attached.feedback, terminal, clock, m)
			attached.inputHistory.configure(m)
			return nil
		}
		if method == "thread/list" && control.Startup.Thread == "--last" {
			return resumeLastResponse(attached, control, picker, clock, resumePicker, approvalMode, m)
		}
		if handled, err := settingsMessage(attached, terminal, control, picker, editor, tiers, (*title), (*backend), (*restoring) != nil, (*side), clock, questions, approvals, (*panel) != nil || (*resumePicker) != nil, (*owned), panes, model, effort, tier, models, modelsLoading, statusConfig, method, m); handled || err != nil {
			return err
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
				if err := closeBTWQuestion(attached, control); err != nil {
					return err
				}
				// Detach only after host acceptance; process and operation owners remain live.
				closing := (*side)
				if closing != nil {
					(*side) = nil
					closing.Close()
					attached.conversation.Side = nil
					pruneDraftImages((*owned), (*editor), attached.conversation, questions, approvals, terminal.Prompts, (*side), attached.nativeImages)
					if err := control.Requests.ReleaseSide(closing); err != nil {
						return err
					}
				}
				if panes != nil {
					paneError(attached.source, panes.savePrepared(time.Now(), true))
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
				restoreInput := attached.restoreInput
				restoreShell := attached.restoreShell
				restoreSide := attached.restoreSide
				deliverActivity := attached.deliverActivity
				activeThread := attached.activeThread
				reserveThread := attached.reserveThread
				nativeImages := attached.nativeImages
				rewindArchive := attached.rewindArchive
				(*attached) = newAttachedPresentation(views, conversationState, bindings)
				attached.restoreInput = restoreInput
				attached.restoreShell = restoreShell
				attached.restoreSide = restoreSide
				attached.deliverActivity = deliverActivity
				attached.activeThread = activeThread
				attached.reserveThread = reserveThread
				attached.nativeImages = nativeImages
				attached.rewindArchive = rewindArchive
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
					files.closeOutput(terminal)
					terminal.Focus = 0
					terminal.StopDrag()
				}
				control.LeaveThread()
				control.Startup.Thread, control.Startup.Workspace = "", ""
				control.Readmit(result.Thread.ID)
				resetDiffScope(attached, terminal, (*reviewOwner), reviewSource, preparePane, parent)
				if method == "thread/resume" || method == "thread/fork" {
					// Restoration below replays events buffered during the switch.
					control.Startup.Thread = result.Thread.ID
				} else {
					// Seed the Diff's lineage; after a switch, requests alone cannot.
					restoreDiffThread(attached, terminal, (*reviewOwner), reviewSource, preparePane, result.Thread)
				}
			}
			(*statusConfig) = appServerStatusConfig{}
			if err := json.Unmarshal(m.Result, statusConfig); err != nil {
				return err
			}
			control.Thread, control.RolloutPath = result.Thread.ID, result.Thread.Path
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
				requestSessionTitle(control, &attached.feedback, terminal, clock, control.Names.Next(control.Thread))
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
			if method == "thread/start" {
				restoreDiffThread(attached, terminal, (*reviewOwner), reviewSource, preparePane, result.Thread)
			}
			(*streams) = terminalsession.Streams{}
			retainedSession.Close()
			(*retainedSession) = history.Session{}
			if notifications != nil {
				if err := control.Request("config/read", map[string]any{"cwd": result.Thread.Cwd, "includeLayers": false}); err != nil {
					return err
				}
			}
			if retained.Store != nil || panes != nil {
				err := openRetainedSession(parent, historyService, retainedSession, control.Thread, retained.Store, retained.Writes, retained.Limits)
				// Prepared children acquire their parent's namespace in logical admission.
				// Only an ordinary root can enroll itself before its first turn.
				if err == nil && control.Startup.Prepared == nil {
					err = retainedSession.Store.Transaction(parent, retainedSession.Writes, func(tx *history.Transaction) error {
						return tx.InitializeScope(history.Scope{Thread: control.Thread, Namespace: retainedSession.Namespace}, nil)
					})
					if err != nil {
						retainedSession.Close()
					}
				}
				if err != nil {
					setNotice(&attached.feedback, terminal, presentationTime(clock), "Session history could not be opened: "+err.Error(), true)
				}
			}
			restoreContextUsage(attached.session.Agent("/root"), result.Thread)
			restoreUsage(usage, result.Thread)
			observeCost(usage, control.Thread, attached.session.Agent("/root"))
			if attached.childSource != nil {
				attached.childSource.Apply(sessionpresentation.ActivityEvent{Agents: attached.session.Roster()})
			}
			if audience != nil {
				if control.Startup.Prepared != nil {
					audience.AttachRuntime(control.Thread)
				} else {
					audience.Attach(control.Thread)
				}
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
				paneError(attached.source, panes.open(&terminal.Shell, terminal.Main, result.Thread.Cwd, control.Thread, method == "thread/resume"))
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
							paneError(attached.source, err)
						} else {
							panes.attachment.Recover(control.Startup.Evidence, presentationTime(clock))
							paneError(attached.source, panes.savePrepared(presentationTime(clock), true))
						}
					}
					if _, err := updateSettings(attached, terminal, control, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, tiers, panes, (*restoring) != nil, clock, repair); err != nil {
						return err
					}
				}
				if control.Startup.MissingSettings {
					setNotice(&attached.feedback, terminal, presentationTime(clock), sessionpresentation.ResumeSettingsUnavailable, true)
					control.Startup.MissingSettings = false
				}
				paneError(attached.source, panes.retainAppliedSettings(terminal, preferences.Scope{Workspace: attached.cwd, Thread: control.Thread}, control.Settings.RestoreEffort, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier), Observed: presentationTime(clock)}))
				return restorePaneContent(attached, terminal, control, picker, editor, questions, approvals, clock, restoring, panel, resumePicker, parent, reviewOwner, receipts, notifications, retained, retainedSession, activityService, segments, restoredSegments, reviewSource, preparePane, result.Thread, dispatch, resumePending)
			}
			paneError(attached.source, panes.retainAppliedSettings(terminal, preferences.Scope{Workspace: attached.cwd, Thread: control.Thread}, control.Settings.RestoreEffort, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier), Observed: presentationTime(clock)}))
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
		var target struct {
			Thread string `json:"threadId"`
		}
		if json.Unmarshal(m.Params, &target) == nil && control.Requests.WorkSide(target.Thread) {
			side := control.Requests.Side(target.Thread)
			side.Status, side.Alert = "Blocked on unsupported request "+m.Method, true
			return nil
		}
		// Never auto-approve an unexpected server request. Leave it pending,
		// visibly blocked, until interrupted.
		attached.presentation.Status.Set("Blocked on unsupported request "+m.Method+" · Ctrl-C interrupts", true)
		notifications.blockNotification(&attached.feedback, terminal, clock, m)
		attached.source.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{Seq: attached.source.LastSeq + 1, Agent: "Session", Kind: "text", Text: attached.presentation.Status.Text, Observed: presentationTime(clock)}}})
		return nil
	}
	if handled, err := settingsMessage(attached, terminal, control, picker, editor, tiers, (*title), (*backend), (*restoring) != nil, (*side), clock, questions, approvals, (*panel) != nil || (*resumePicker) != nil, (*owned), panes, model, effort, tier, models, modelsLoading, statusConfig, "", m); handled || err != nil {
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
	return observedEvent(attached, terminal, control, picker, editor, questions, approvals, clock, panel, resumePicker, (*reviewOwner), receipts, notifications, (*retainedSession), windows, activityService, streams, tracker, trace, usage, exitUsage, m.Method, p)
}

// observedEvent retains application lifecycle and control handling for decoded
// host facts. Offline transcript observations bypass it through their reducer.
func observedEvent(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, panel **appServerStatusReport, resumePicker **appServerResumePicker, reviewOwner *sessionreview.Review, receipts *[]*changepresentation.Receipt, notifications *nativeNotifications, retainedSession history.Session, windows *executionobservation.Windows, activityService *activitymodel.Service, streams *terminalsession.Streams, tracker *commandsegments.Observer, trace *nativetrace.Reader, usage *accounting.Service, exitUsage *binding.TokenUsage, method string, p binding.Notification) error {
	if method == "turn/completed" && p.ThreadID == control.Thread && p.Turn.Status != "completed" {
		control.Waiting, control.LocalStop = "", ""
		attached.session.Lifecycle(p.ThreadID).SetWaiting(false, presentationTime(clock))
	}
	if r := control.Rewind; r != nil && r.Sent && p.ThreadID == r.Thread && p.TurnID == r.Turn && p.Item.Type == "commandExecution" && p.Item.Source == "userShell" {
		if method == "item/started" && r.Item == "" {
			r.Item = p.Item.ID
		}
		if method == "item/completed" && r.Item != "" && p.Item.ID == r.Item {
			r.Completed, r.Failed = true, p.Item.ExitCode == nil || *p.Item.ExitCode != 0 || p.Item.Status != "completed"
		}
	}
	text := binding.RetainedTranscript(p.Observation(method, presentationTime(clock)))
	if target := control.Requests.Side(p.ThreadID); target != nil && target.OneOff && !target.Closed {
		if method == "item/started" || method == "item/completed" {
			if observeMessageQuestions(attached, terminal, control, approvals, clock, questions, editor, picker, *panel != nil || *resumePicker != nil, notifications, text, p.Item.Observation(), false) {
				return nil
			}
		}
		if method == "turn/completed" {
			for _, c := range questions.calls() {
				if c.Thread == p.ThreadID && c.Turn == p.Turn.ID && c.Sync {
					resolveQuestionCall(attached, terminal, control, editor, questions, approvals, clock, reviewOwner, receipts, c, "Turn ended")
				}
			}
		}
	}
	if method == "thread/started" {
		attached.session.Caller(p.Thread.ID)
		attached.session.Activity().Agent(p.Thread.ID).ParentThread = p.Thread.ParentThreadID
	}
	if method == "thread/started" && control.Requests.WorkSide(p.Thread.ID) {
		side := control.Requests.Side(p.Thread.ID)
		p.Thread.Path = side.Caller() + strings.TrimPrefix(p.Thread.Path, "/root")
		attached.session.Caller(p.Thread.ID)
		attached.session.Activity().Agent(p.Thread.ID).Isolated = true
	}
	if handled, err := sessionEvent(attached, terminal, control, streams, tracker, activityService, usage, trace, windows, reviewOwner, approvals, receipts, retainedSession, exitUsage, clock, method, p); handled || err != nil {
		return err
	}

	switch method {
	case "turn/started":
		if p.ThreadID == control.Thread {
			if r := control.Rewind; r != nil && r.Sent && r.Turn == "" {
				r.Turn = p.Turn.ID
			}
			control.Started(p.Turn.ID)
			if attached.presentation.Status.Text != "Waiting for agents" {
				attached.presentation.Status.Started = presentationTime(clock)
			}
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
			shellTurn := control.Shell.OwnsTurn(p.Turn.ID)
			if r := control.Rewind; r != nil && r.Turn == p.Turn.ID && p.Turn.Status != "completed" {
				r.Completed, r.Failed = true, true
			}
			control.Completed(p.Turn.Status == "completed")
			endSyncQuestions(attached, terminal, control, editor, questions, approvals, clock, reviewOwner, receipts, p.Turn.ID)
			var failure *string
			if p.Turn.Error != nil {
				failure = &p.Turn.Error.Message
			}
			localStop := p.Turn.Status == "completed" && (control.LocalStop == p.Turn.ID || shellTurn && control.LocalStop != "")
			if localStop {
				attached.presentation.Status.Set("Waiting for agents", false)
			} else {
				attached.presentation.Status.Complete(p.Turn.Status, failure, presentationTime(clock))
			}
			settleInput(attached, terminal, control, picker, questions, approvals, clock, p.Turn.ID, p.Turn.Status == "interrupted")
			if !localStop && !shellTurn && p.Turn.Status == "completed" && questions.Count(control.Thread) == 0 {
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

// Router-owned annotations and authenticated directed inputs supplement the
// native event stream without becoming assistant speech or execution events.
func applyObservedActivity(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], streams *terminalsession.Streams, usage *accounting.Service, reviewOwner *sessionreview.Review, approvals *nativeApprovalDock, receipts *[]*changepresentation.Receipt, clock func() time.Time, restoring *appServerActivityRestore, audience *sessioncontrol.Audience) {
	if restoring != nil && !restoring.rendered {
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
	}
	if audience == nil {
		return
	}
	roots := []string{control.Thread}
	target := attached.presentation.View().SideThread
	borrowed := ""
	if control.Requests.WorkSide(target.ID) {
		borrowed = target.ID
		roots = append(roots, borrowed)
	}
	audience.BorrowSide(borrowed)
	var entries []sessionpresentation.ActivityEntry
	admitted := false
	if borrowed != "" {
		snapshot, _ := audience.Snapshot(borrowed)
		for thread, name := range snapshot.Threads {
			if control.Requests.Side(thread) != nil {
				continue
			}
			control.Requests.TrackSide(thread, control.Requests.Side(borrowed))
			attached.session.Caller(thread)
			agent := attached.session.Activity().Agent(thread)
			agent.Isolated, agent.AudiencePending = true, false
			previous := agent.Name
			agent.Name = target.Caller + strings.TrimPrefix(name, "/root")
			renameThreadActivity(attached, terminal, previous, agent.Name)
			admitted = true
		}
	}
	for _, root := range roots {
		extra := takeNativeActivity(audience, root)
		for i := range extra {
			if root == borrowed {
				extra[i].Agent = strings.Replace(extra[i].Agent, "/root", target.Caller, 1)
			}
		}
		entries = append(entries, extra...)
	}
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
	for _, root := range roots {
		for _, start := range audience.TakeStarts(root) {
			entries = append(entries, transcriptUpdates(attached, terminal, streams, control.Thread, providerStarted(attached, start.Thread, start.At), false)...)
		}
	}
	for i := range entries {
		entries[i].Seq = attached.session.Activity().NextSequence()
	}
	if len(entries) != 0 || receivedReceipt || admitted {
		applyActivity(attached, approvals, terminal, reviewOwner, receipts, control.Thread, clock, entries, attached.session.Roster())
	}
	for _, root := range roots {
		for _, ref := range audience.TakeUnreturned(root) {
			attached.source.MarkUnreturned(ref.Thread, ref.Call)
			attached.childSource.MarkUnreturned(ref.Thread, ref.Call)
		}
	}
}
