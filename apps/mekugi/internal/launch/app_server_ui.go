package launch

import (
	"cmp"
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/libraries/shellinstrumentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/backends/codex/shellhook"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	noticepresentation "github.com/yusing/mekugi/modules/notifications/presentation"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
	threadcontrol "github.com/yusing/mekugi/modules/threadcontrol/control"
	"github.com/yusing/mekugi/modules/usage/accounting"
	"github.com/yusing/mekugi/providers/openai"
)

// Codex approval policy and the invocation-local Shell guard are independent.
func (session *nativeSession) start(prefs *preferences.Service, cmd *exec.Cmd, guard *sessioncontrol.ShellApprovals, guards []binding.ShellApproval, skillsManager bool, resumeThread string, predictionProvider *openai.Client, api *sessionapi.Server, childSummary bool, threads *threadExecution) (func() error, error) {
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
	var err error
	session.Client, err = sessioncontrol.Start[appServerRequest](cmd, func(message appserver.Message) {
		if message.Method != "item/started" {
			return
		}
		if command, ok := binding.DecodeCommandObservation(message.Method, message.Params); ok {
			if script, ok := shellinstrumentation.CommandScript(command.Command); ok {
				session.execTrack.Start(command.Key, script)
			}
		}
	})
	if err != nil {
		return nil, err
	}
	hooks := new(executionHooks)
	hooks.toolInput = session.controlObservations.ToolInput
	session.attached = newAttachedPresentation(session.ctx, session.presentationState, session.conversationState, session.observations, hooks)
	processCwd, _ := filepath.Abs(cmd.Dir)
	session.attached.cwd = processCwd
	inputHistory := newInputHistory(cmd.Environ(), processCwd)
	session.attached.inputHistory = inputHistory

	session.ownedImages = make(map[string]bool)
	c := session.Client.Host
	session.Client.Startup.Thread = resumeThread
	session.Client.Startup.History = session.retained.Store
	attachPresentation(&session.attached, &session.picker, &session.Editor, &session.questions, &session.approvals, session.Client.Thread, &session.review, &session.commits, session.ctx, session.retained, session.changeSource, session.activity != nil)
	session.attached.presentation.Status.Set("Connecting…", false)
	session.guardHookCheck.hash = guardHash
	if session.titleGenerator != nil {
		session.titleUpdates = session.titleGenerator.updates
	}
	if guard != nil {
		session.approvals.guards = guards
	}
	if skillsManager {
		session.skillEnvironment = cmd.Environ()
	}
	session.resumeArgv = slices.Clone(session.resumeArgv)
	session.Client.Startup.Config = appServerResumeConfig(cmd.Args, session.resumeArgv)
	api.BindRequests(session.Client.Requests)
	api.BindCoordination(threads.shared, threads.driver.DeliverMessages)
	if session.preparer != nil {
		session.Client.Requests.InputContext = session.controlObservations.ContextIdentity
		session.Client.Requests.PrepareCompletion = session.preparer.PrepareCompletion
		session.Client.Requests.AdmitSideFork = session.preparer.AdmitSideFork
	}
	api.BindSideThreads(session.Client.Requests.HasSideThread)
	session.Client.Requests.Coordination, session.Client.Requests.InterruptWait = threads.shared, threads.driver.Interrupt
	session.Client.Requests.InheritedEffort = threads.driver.InheritedEffort
	session.Client.Requests.CaptureConfiguredCatalog = threads.driver.CaptureConfiguredCatalog
	api.BindChildAttachment(threads.AttachChild)
	policy := binding.ThreadAdmissionPolicy{}
	session.Client.Requests.ThreadPolicy = &policy
	session.Client.Startup.Prepared = threads.start
	if err := threads.driver.Connect(threads, session.audience); err != nil {
		c.Close()
		<-c.Done
		return nil, err
	}
	threadChanges := threads.driver.Changed()
	session.notifications = new(nativeNotifications)
	session.Client.Startup.Workspace = resumeCwd
	if prefs != nil {
		session.panes = &nativePreferences{service: prefs}
	}
	if err := session.Client.Request("initialize", nil); err != nil {
		_ = inputHistory.close()
		c.Close()
		<-c.Done
		return nil, err
	}

	settlePresentation(&session.attached, &session.Client, &session.picker, &session.Editor, session.settings, session.serviceTiers, session.title, session.backendVersion, session.restoring != nil, session.side, session.clock, &session.questions, &session.approvals, session.overlayOpen(session.statusPanel, session.resumePicker))
	predictions := composerPredictions{provider: predictionProvider, results: make(chan predictionConfiguration, 1)}
	native := newNativeViews(session.ctx, api.Native(), session.presentationState, session.activity, session.Client.Requests)
	native.guards = session.approvals.guards
	session.approvals.allows = native.allowsApproval
	hooks.bind(native, api, session.compactionReader)
	native.diagnostics = &c.Diagnostics
	native.hub = threads.hub
	syncPredictions := func() error {
		predictions.supported = sessioncontrol.PredictionForkSupported(session.backendVersion)
		focused, disabled, available := native.predictionEligibility(&session.attached)
		return predictions.sync(session.ctx, &session.attached, &session.Client, session.restoring != nil || !available, focused, disabled)
	}
	adoptStartupChooser := func() {
		if session.Client.Thread != "" || session.Client.Startup.Thread != sessioncontrol.ResumePicker || session.resumePicker == nil {
			return
		}
		for id, view := range native.views {
			if !view.startup || view.ctx.Err() != nil {
				continue
			}
			session.attached.presentation.MoveResume(view.attached.presentation)
			view.resume, session.resumePicker = session.resumePicker, nil
			view.resume.view = id
			if response, ok := session.Client.Requests.Lookup(view.resume.request); ok {
				response.Owner.view = id
				session.Client.Requests.Attribute(view.resume.request, viewRequestScope(id), response.Owner)
			}
			view.projected = time.Time{}
			return
		}
	}
	var startupView string
	var latestResponse *appserver.Message
	var restoredCoordination string

	var dispatchHost func(appserver.Message, bool) error
	flushCompletions := func() error {
		return session.attached.threadInput.flushCompletions(func(m appserver.Message) error { return dispatchHost(m, false) })
	}
	session.dispatch = func(m appserver.Message) error {
		if err := flushCompletions(); err != nil {
			return err
		}
		if err := dispatchHost(m, true); err != nil {
			return err
		}
		return flushCompletions()
	}
	dispatchHost = func(m appserver.Message, observe bool) error {
		response, correlated := session.Client.Requests.Lookup(string(m.ID))
		if session.Client.CompleteCall(m) {
			if m.Error == nil {
				if input := response.NativeDelivery(); input != nil {
					stageNativeDelivery(&session.attached, *input, response.Completion(), presentationTime(session.clock))
				}
			}
			return nil
		}
		syncLogicalWork(&session.attached, &session.Client.Live, threads, presentationTime(session.clock))
		if observe {
			reduced, err := observeLogicalHost(threads.driver, &session.Client.Live, m)
			if err != nil {
				return err
			}
			deferred, err := deferLogicalCompletion(&session.attached, &session.Client.Live, m, reduced, session.Client.Requests)
			if err != nil || deferred {
				return err
			}
		}
		if m.Method == "" {
			if response, ok := session.Client.Requests.Lookup(string(m.ID)); ok {
				if handled, warning := session.Client.Requests.ProcessResponse(response, m); handled {
					session.Client.Requests.Take(string(m.ID))
					if warning != "" {
						target := &session.attached
						if view := native.views[response.Owner.view]; view != nil && view.ctx.Err() == nil {
							target = &view.attached
							view.projected = time.Time{}
						}
						setNotice(&target.feedback, presentationTime(session.clock), warning, true)
					}
					return nil
				}
			}
			if response, ok := session.Client.Requests.Lookup(string(m.ID)); ok && response.Method == "thread/list" && session.Client.Startup.Thread == "--last" {
				// The host can finish lookup before the initial native attach is
				// admitted. Deliver through that actual view's retained effects.
				if startupView == "" {
					latestResponse = &m
					return nil
				}
				session.Client.Requests.Take(string(m.ID))
				view := native.views[startupView]
				if view == nil || view.ctx.Err() != nil {
					session.quitRequested = true
					return nil
				}
				err := resumeLastResponse(&view.attached, &session.Client, &view.picker, session.clock, &view.resume, session.approvalMode, m)
				session.attached.presentation.BorrowExecutionStatus(view.attached.presentation)
				view.projected = time.Time{}
				return err
			}
			if response, ok := session.Client.Requests.Lookup(string(m.ID)); ok && response.Owner.view != "" {
				session.Client.Requests.Take(string(m.ID))
				view := native.views[response.Owner.view]
				if view == nil || view.ctx.Err() != nil || view.attached.executionPresentation != session.attached.executionPresentation {
					return nil
				}
				requestEditor := new(composer.Editor)
				replacing := session.Client.Replacement.Pending()
				_, err := session.dispatchViewResponse(&view.attached, &view.picker, &requestEditor, session.clock, &view.panel, &view.resume, view.ctx, response, m)
				if !replacing && session.Client.Replacement.Pending() {
					native.replacing = response.Owner.view
				}
				view.projected = time.Time{}
				return err
			}
		}
		if m.Method == "skills/changed" {
			native.invalidateSkills(session.attached.executionPresentation)
		}
		err := session.dispatchHostMessage(&session.attached, &session.picker, &session.Editor, session.clock, &session.side, &session.statusPanel, &session.resumePicker, session.ctx, m)
		adoptStartupChooser()
		if err == nil && session.retainedSession.Thread != "" && session.retainedSession.Thread != restoredCoordination {
			restoredCoordination = session.retainedSession.Thread
			syncLogicalWork(&session.attached, &session.Client.Live, threads, presentationTime(session.clock))
			session.attached.threadInput.restoration = threads.driver.Restore(session.retainedSession)
		}
		predictions.observe(m, session.Client.Thread)
		if err == nil && threads.headless != nil && correlated && m.Method == "" && m.Error != nil && (response.Method == "turn/start" || response.Method == "turn/steer") {
			return fmt.Errorf("%s: %s", response.Method, m.Error.Message)
		}
		return err
	}
	live := &apiLive{server: api, coordination: threads.shared, started: time.Now()}
	session.notifications.relay = func(event, message string) { native.notify(session.attached.executionPresentation, event, message) }
	nativeRequests, nativeClosed := api.Native().Requests(), native.closed
	intents := api.Intents()
	live.publish(&session.attached, &session.Client, session.review, session.reviewSource, session.restoring != nil, childSummary, presentationTime(session.clock))
	native.retiring = func(view *nativeSessionView) error {
		defer pruneDraftImages(session.ownedImages, session.Editor, session.attached.conversation, &session.questions, &session.approvals, session.side, session.attached.hooks.nativeImages)
		if view.startup && session.Client.Thread == "" && (session.Client.Startup.Thread == sessioncontrol.ResumePicker || session.Client.Startup.Thread == "--last") {
			session.quitRequested = true
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
					pruneDraftImages(session.ownedImages, session.Editor, session.attached.conversation, &session.questions, &session.approvals, session.side, session.attached.hooks.nativeImages)
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
			for _, stack := range [][]conversationmodel.Input{session.attached.conversation.Unsent, session.attached.conversation.Queued} {
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
		case operation.Completion != nil || operation.Reference != nil || operation.Resume != nil || operation.Viewer != nil || operation.Navigate != nil:
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
		replacing := session.Client.Replacement.Pending()
		settings := session.settings
		requestEditor := new(composer.Editor)
		if operation.Editor != nil {
			requestEditor.Replace(operation.Editor.Draft)
		}
		closed := false
		switch {
		case operation.Viewer != nil:
			if operation.Viewer.Revision > view.viewerLease.Revision {
				view.viewerLease = *operation.Viewer
			}
			view.effects.add(sessionapi.NativeEffect{Thread: session.Client.Thread, Presentation: &sessionapi.NativePresentationEffect{Viewer: operation.Viewer}})
		case operation.Navigate != nil:
			publication := view.attached.childSource.Publication(view.attached.presentation, view.attached.source)
			if native.hub != nil {
				if family, _ := native.hub.Family(session.Client.Thread); family != nil && family.Agents != nil {
					publication = family.Agents
				}
			}
			return view.selectAgent(*operation.Navigate, publication), nil
		case operation.SaveDraft != nil:
			session.attached.inputHistory.remember(*operation.SaveDraft)
		case operation.Completion != nil:
			view.complete(*operation.Completion, session.clock, session.Client.Requests, session.skillEnvironment)
		case operation.Preferences != nil:
			if err := session.panes.stageNative(operation.Preferences.Presentation, presentationTime(session.clock), operation.Preferences.Flush); err != nil {
				return err.Error(), nil
			}
			layout, _ := terminalsession.ReadPreferences(operation.Preferences.Presentation)
			view.attached.interruptLocked, view.attached.predictionsDisabled = layout.InterruptLocked, layout.PredictionsDisabled
		case operation.Command != nil || operation.Composer != nil:
			intent := sessionpresentation.ComposerIntent{Version: view.attached.presentation.View().Version, Thread: view.attached.presentation.View().Input.ID, Operation: sessionpresentation.SubmitComposer}
			detached := operation.Composer != nil && operation.Composer.Detached
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
				if *operation.Command == "/presets" && operation.Editor != nil {
					if operation.Editor.Target.Call != "" || operation.Editor.Target.Request != "" {
						return "Presets are unavailable while answering a prompt.", nil
					}
					requestEditor.Replace(operation.Editor.Draft)
					openPresets(view.ctx, &view.attached, &session.Client, &view.picker, requestEditor, settings, session.restoring != nil)
					return "", nil
				}
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
			boundMainInput := boundInput && intent.Thread == session.Client.Thread
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
				if view.attached.presentation.DisplayingSide() || inputThread != session.Client.Thread {
					return "Return to Main to use this control.", nil
				}
				active, err := view.attached.presentation.ResolveInterrupt(sessionpresentation.InterruptIntent{Version: intent.Version, Target: *intent.Work, Mode: sessioncontrol.RestoreWaiting}, &session.Client.Live, &session.attached.session)
				if err != nil {
					return err.Error(), nil
				}
				if !active {
					setNotice(&view.attached.feedback, presentationTime(session.clock), "Original work already ended.", false)
					return "", nil
				}
				if intent.Operation == sessionpresentation.SteerWaiting {
					admission := intent
					admission.Operation = sessionpresentation.SubmitComposer
					plan, failure := view.attached.presentation.ClientInput(admission, &session.Client.Live, session.attached.conversation, session.restoring != nil)
					if failure != nil {
						return failure.Error(), nil
					}
					if !session.attached.conversation.SteerQueued(session.controlObservations.AssistantMessage(session.attached.cwd, session.Client.Thread)) {
						return "No queued messages can be steered.", nil
					}
					if plan.Flush {
						return "", session.flushInput(&session.attached, &session.picker, session.clock, &session.Editor, session.ctx)
					}
					return "", nil
				}
				native.claimWaiting(view)
			}
			if boundInput && !boundMainInput {
				if session.side == nil || intent.Thread != cmp.Or(session.side.Thread, "side:"+session.side.Key) {
					return sessionpresentation.ErrStaleIntent.Error(), nil
				}
				err = session.submitBTW(&view.attached, &view.picker, &requestEditor, session.clock, &session.side, session.overlayOpen(view.panel, view.resume), &composerPreparation{ctx: request.Context, input: request.Prepared, side: true})
				if requestEditor.Text != "" {
					return view.attached.feedback.Text, err
				}
				return "", err
			}
			if intent.Operation == sessionpresentation.SubmitComposer || intent.Operation == sessionpresentation.QueueComposer {
				text := strings.TrimSpace(requestEditor.Text)
				if !strings.HasPrefix(text, "/") && (boundMainInput || !view.attached.presentation.DisplayingSide()) {
					if session.Client.Replacement.Pending() && native.replacing != view.attached.view {
						return "This session is being replaced by another view; refresh before submitting.", nil
					}
					if request.Prepared == nil {
						return "The input has no prepared attachment snapshot.", nil
					}
					plan, admissionErr := view.attached.presentation.ClientInput(intent, &session.Client.Live, session.attached.conversation, session.restoring != nil)
					if admissionErr != nil {
						return admissionErr.Error(), nil
					}
					requestEditor.Replace(request.Prepared.Draft)
					if strings.HasPrefix(text, "!") {
						err = session.submitShell(&view.attached, &view.picker, &requestEditor, session.clock, session.ctx)
						if requestEditor.Text != "" {
							return view.attached.feedback.Text, err
						}
					} else {
						if text == "" && len(request.Prepared.Draft.Images) == 0 && len(request.Prepared.Draft.Attachments) == 0 {
							return "Enter a message or attach content.", nil
						}
						if session.ownedImages == nil {
							session.ownedImages = make(map[string]bool)
						}
						if !request.Prepared.Admit(request.Context, session.ownedImages) {
							return "The prepared input was canceled before admission.", nil
						}
						input := conversationmodel.Input{Draft: request.Prepared.Draft.Trimmed(), Origin: view.attached.view, Delivery: view.effects.input()}
						if !plan.Queued {
							input.MessageBoundary = session.controlObservations.AssistantMessage(session.attached.cwd, session.Client.Thread)
						}
						session.attached.conversation.QueueInput(input, plan.Queued)
						if input.AttachmentNotice != "" {
							setNotice(&view.attached.feedback, presentationTime(session.clock), input.AttachmentNotice, true)
						}
						if plan.Flush {
							err = session.flushInput(&session.attached, &session.picker, session.clock, &session.Editor, session.ctx)
						}
					}
					return "", err
				}
			}
			if view.remoteHuman && strings.TrimSpace(requestEditor.Text) == "/ps" {
				if !detached {
					view.effects.add(sessionapi.NativeEffect{Thread: inputThread, Target: new(intent.Target), Action: &composer.Action{Kind: composer.DeleteAction, Start: 0, End: len(requestEditor.Text)}})
				}
				setNotice(&view.attached.feedback, presentationTime(session.clock), "Active processes appear above the composer.", false)
				return "", nil
			}
			view.attached.presentation.BorrowExecutionStatus(session.attached.presentation)
			closed, err = session.applyComposerCommand(&view.attached, &view.picker, &requestEditor, session.clock, &session.side, &view.panel, &view.resume, session.ctx, &view.files, intent, &composerPreparation{ctx: request.Context, input: request.Prepared, side: session.side != nil && inputThread == cmp.Or(session.side.Thread, "side:"+session.side.Key)})
			if (intent.Operation == sessionpresentation.SubmitComposer || intent.Operation == sessionpresentation.QueueComposer) && !strings.HasPrefix(strings.TrimSpace(before.Text), "/") && requestEditor.Text != "" {
				return view.attached.feedback.Text, err
			}
			session.attached.presentation.BorrowExecutionStatus(view.attached.presentation)
			if intent.Operation != sessionpresentation.RestoreWaiting {
				view.effects.add(sessionapi.NativeEffect{Thread: session.Client.Thread, Presentation: &sessionapi.NativePresentationEffect{LiveHidden: view.attached.liveHidden, InterruptLocked: view.attached.interruptLocked, PredictionsDisabled: view.attached.predictionsDisabled}})
			}
			if strings.HasPrefix(strings.TrimSpace(before.Text), "/") {
				if operation.Composer != nil && operation.Composer.Consumed {
					if requestEditor.Text != "" && err == nil {
						view.effects.add(sessionapi.NativeEffect{Thread: intent.Thread, Restore: new(requestEditor.Draft.Clone())})
					}
				} else if !detached && recovery == nil && requestEditor.Text == "" && (operation.Composer != nil || operation.Editor != nil) {
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
			session.applyMenuResult(&view.attached, &view.picker, &requestEditor, &session.side, session.clock, session.overlayOpen(view.panel, view.resume), view.ctx, terminalsession.MenuResult{Action: action, Handled: true})
			session.dispatchRewind(&view.attached, &view.picker, session.clock, session.ctx)
			if operation.Editor != nil && !reflect.DeepEqual(before, requestEditor.Draft) {
				draft := requestEditor.Draft.Clone()
				view.effects.add(sessionapi.NativeEffect{Thread: inputThread, Target: new(operation.Editor.Target), Action: &composer.Action{Kind: composer.DeleteAction, Start: 0, End: len(before.Text)}, Restore: &draft})
			}
		case operation.Resume != nil:
			err = session.applyResumeAction(&view.attached, &view.picker, &requestEditor, session.clock, &session.side, &view.panel, &view.resume, &closed, *operation.Resume)
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
			setNotice(&view.attached.feedback, presentationTime(session.clock), "", false)
		case operation.Reference != nil:
			session.referenceInput(&view.attached, &view.picker, &requestEditor, session.clock, &session.side, &view.panel, &view.resume, view.ctx, *operation.Reference)
		case operation.Side != nil:
			if admissionErr := view.attached.presentation.AdmitCloseSide(*operation.Side); admissionErr != nil {
				return admissionErr.Error(), nil
			}
			if session.side == nil || operation.Side.Key != session.side.Key || operation.Side.Exchange != session.side.ID {
				return sessionpresentation.ErrStaleIntent.Error(), nil
			}
			err = closeSide(&session.attached, &session.Client, &session.Editor, &session.questions, &session.approvals, &session.side, &session.ownedImages)
		case operation.BTW != nil:
			current := view.attached.presentation.View()
			if !operation.BTW.Version.SameAttachment(current.Version) || current.BTW == nil || operation.BTW.Key == "" || operation.BTW.Key != current.BTW.Key || operation.BTW.Exchange != current.BTW.ID || session.Client.BTW == nil || operation.BTW.Key != session.Client.BTW.Key || operation.BTW.Exchange != session.Client.BTW.ID {
				return sessionpresentation.ErrStaleIntent.Error(), nil
			}
			err = closeBTWQuestion(&session.attached, &session.Client)
		case operation.StopProcess != nil:
			if admissionErr := session.attached.session.AdmitProcess(*operation.StopProcess); admissionErr != nil {
				return admissionErr.Error(), nil
			}
			err = session.Client.Requests.StopProcess(operation.StopProcess.Thread, operation.StopProcess.ID, appServerRequest{view: view.attached.view})
		case operation.Interrupt != nil:
			active, admissionErr := view.attached.presentation.ResolveInterrupt(*operation.Interrupt, &session.Client.Live, &session.attached.session)
			if admissionErr != nil {
				return admissionErr.Error(), nil
			}
			if !active {
				setNotice(&view.attached.feedback, presentationTime(session.clock), "Original work already ended.", false)
				return "", nil
			}
			if operation.Interrupt.Mode == sessioncontrol.RestoreWaiting && cancelQueuedCompact(&session.attached, &session.Client, &session.picker, &session.questions, &session.approvals, session.clock) {
				return "", nil
			}
			decision := session.Client.RequestInterrupt(operation.Interrupt.Mode, len(session.attached.conversation.Unsent) > 0 && session.attached.conversation.Unsent[0].Compact == nil)
			err = applyInterruption(&session.attached, &session.Client, &session.picker, &session.Editor, &session.questions, &session.approvals, session.clock, session.review, &session.pendingEditReceipts, decision)
		case operation.Panel != nil:
			action, admissionErr := view.panelAction(request)
			if admissionErr != nil {
				return admissionErr.Error(), nil
			}
			applyPanelAction(&view.attached, session.clock, &view.panel, session.sessionCapture, action)
		default:
			return view.applyView(operation), nil
		}
		pruneDraftImages(session.ownedImages, session.Editor, session.attached.conversation, &session.questions, &session.approvals, session.side, session.attached.hooks.nativeImages)
		if !replacing && session.Client.Replacement.Pending() {
			native.replacing = view.attached.view
		}
		if closed {
			native.retire(view.attached.view)
		}
		return "", err
	}
	delegateApprovals := sessioncontrol.NewDelegateApprovals(session.ctx)
	threads.approvals = delegateApprovals
	return func() (runErr error) {
		if threads.headless != nil {
			defer func() {
				if runErr != nil && !errors.Is(runErr, context.Canceled) {
					threads.headless.Fail(runErr)
				}
			}()
		}
		defer session.Client.Requests.Close()
		defer native.close()
		defer func() { _ = predictions.clear(&session.Client, session.attached.conversation) }()
		session.review.Loader = sessionreview.NewLoader(session.ctx, session.review.Projection)
		defer session.review.Loader.Close()
		defer func() {
			session.retainedSession.Close()
		}()
		defer session.picker.cancelScan()
		defer func() { session.commits.Close() }()
		if session.audience != nil {
			defer session.audience.Detach()
		}
		defer c.Input.Close()
		defer func() { discardDraftImages(session.ownedImages) }()
		defer c.Output.Close()
		defer delegateApprovals.Close()
		exited := false
		var err error
		runLoop := func() error {
			review := session.review
			review.Preview().Enable()
			defer func() { review.Preview().Disable() }()
			session.review.Loader.AwaitResync()
			sub := review.Subscribe()
			defer func() { review.Unsubscribe(sub) }()
			diffEvents, diffGap, diffReady, previewGap := sub.Events(), sub.Gap(), sub.Ready(), sub.PreviewGap()
			autoChanged := review.Changed()
			resubscribe := func() {
				review.Unsubscribe(sub)
				session.review.Loader.AwaitResync()
				sub = review.Subscribe()
				diffEvents, diffGap, diffReady, previewGap = sub.Events(), sub.Gap(), sub.Ready(), sub.PreviewGap()
			}
			tick := time.NewTicker(33 * time.Millisecond)
			defer tick.Stop()
			for {
				if threads.headless != nil {
					if session.questions.Count(session.Client.Thread) > 0 || len(session.approvals.Pending) > 0 {
						return errors.New("headless cannot answer pending questions or approvals; continue interactively")
					}
					if threads.start == nil {
						if !session.Client.Requests.Pending() && session.Client.Turn == "" && !session.attached.threadInput.preparing() {
							lifecycle := session.attached.session.Lifecycle(session.Client.Thread)
							member, ok := threads.shared.Member(session.Client.Thread)
							if lifecycle.Final && (!ok || member.Status == threadcontrol.Completed) {
								return nil
							}
							if ok {
								switch member.Status {
								case threadcontrol.Failed, threadcontrol.Interrupted, threadcontrol.Paused, threadcontrol.Uncertain:
									return fmt.Errorf("headless logical Main: %s", member.Status)
								}
							} else if lifecycle.Turns > 0 && !lifecycle.Responding && !lifecycle.Waiting && !session.Client.Compaction.Pending() {
								return errors.New("headless Main turn failed or was interrupted")
							}
						}
					}
				}
				if session.quitRequested {
					return nil
				}
				if native.failure != nil {
					return native.failure
				}
				select {
				case retainedErr := <-session.Client.Startup.ForkRetained():
					pending := session.Client.Startup.ForkRetention
					pending.Err, pending.Complete = retainedErr, true
					if err := session.dispatch(pending.Message); err != nil {
						return err
					}
				case result := <-inputHistory.loads:
					inputHistory.loaded(session.attached.conversation, result)
				case historyErr := <-inputHistory.failures:
					setNotice(&session.attached.feedback, presentationTime(session.clock), "Input history unavailable: "+historyErr.Error(), true)
				case <-session.ctx.Done():
					return session.ctx.Err()
				case result := <-predictions.results:
					if err := syncPredictions(); err != nil {
						return err
					}
					if err := predictions.configured(result, &session.Client); err != nil {
						return err
					}
				case <-autoChanged:
					session.review.Projection.Workspace = review.Workspace()
					publishReview(&session.attached, session.review, session.reviewSource)
				case <-diffGap:
					resubscribe()
				case <-previewGap:
					resubscribe()
				case result := <-session.review.Loader.Updates():
					if sub.Invalidated() {
						resubscribe()
					}
					loadedReview(&session.attached, session.review, session.reviewSource, &session.pendingEditReceipts, result)
					if err := finishRestoredContent(&session.attached, &session.Client, &session.picker, &session.restoring, &session.review, session.dispatch, &session.resumePending); err != nil {
						return err
					}
				case <-diffReady:
					if !admitReviewEvents(&session.attached, session.review, session.reviewSource, &session.pendingEditReceipts, session.ctx, sub, review.TakePreviews(sub)...) {
						resubscribe()
					}
					if err := finishRestoredContent(&session.attached, &session.Client, &session.picker, &session.restoring, &session.review, session.dispatch, &session.resumePending); err != nil {
						return err
					}
				case event := <-diffEvents:
					if !admitReviewEvents(&session.attached, session.review, session.reviewSource, &session.pendingEditReceipts, session.ctx, sub, event) {
						resubscribe()
					}
					if err := finishRestoredContent(&session.attached, &session.Client, &session.picker, &session.restoring, &session.review, session.dispatch, &session.resumePending); err != nil {
						return err
					}
				case result := <-session.picker.scanResults:
					session.picker.applyScan(session.attached.cwd, session.picker.completionTarget(session.Editor.Draft, session.attached.conversation.ShellMode()), result)
				case <-session.commits.Updates():
					startCommitReads(&session.attached, session.commits)
				case err := <-session.commandSegmentWrites:
					commandSegmentRetained(&session.commandSegmentPending, session.issues, err)
				case update := <-session.titleUpdates:
					persistSessionTitle(&session.Client, session.titleGenerator, &session.attached.feedback, session.clock, update)
				case request := <-guard.Requests():
					addGuardApproval(&session.attached, &session.Client, &session.picker, &session.Editor, &session.questions, &session.approvals, session.clock, session.overlayOpen(session.statusPanel, session.resumePicker), session.notifications, session.review, &session.pendingEditReceipts, request)
				case request := <-delegateApprovals.Requests():
					addDelegateApproval(&session.attached, &session.Client, &session.picker, &session.Editor, &session.questions, &session.approvals, session.clock, session.overlayOpen(session.statusPanel, session.resumePicker), session.notifications, session.review, &session.pendingEditReceipts, request)
				case call := <-session.Client.Calls():
					if err := flushCompletions(); err != nil {
						return err
					}
					session.Client.Dispatch(call)
				case inspection := <-session.Client.Requests.Inspections():
					target := inspection.Target()
					switch {
					case inspection.ContextWindow() != "" && (target == session.Client.Thread || session.Client.Requests.HasSideThread(target)):
						inspection.CompleteAttachments(target, session.conversationState.Attachments(session.attached.session.Thread(target)))
					case target == session.Client.Thread:
						inspection.Complete(target, threadInspection(&session.attached.session, target, session.settings, session.serviceTiers))
					case session.Client.Requests.HasSideThread(target):
						inspection.Complete(target, threadcontrol.SessionDetails{SessionMetrics: session.attached.session.InspectionMetrics(target)})
					default:
						inspection.Complete(session.Client.Thread, threadcontrol.SessionDetails{})
					}
				case question := <-session.Client.Requests.Questions():
					session.Client.Requests.DispatchQuestion(question)
				case <-threadChanges:
					if err := flushCompletions(); err != nil {
						return err
					}
					syncLogicalWork(&session.attached, &session.Client.Live, threads, presentationTime(session.clock))
					for _, failure := range threads.driver.Failures() {
						session.issues.AddThreadNotice(session.Client.Thread, session.Client.Turn, "threadcontrol", failure.Error())
					}
				case message, ok := <-c.Messages:
					if !ok {
						exited = true
						return errors.Join(errors.New("app-server disconnected; active work may be incomplete"), <-c.Done)
					}
					if threads.headless != nil {
						if err := threads.headless.Observe(message); err != nil {
							return err
						}
					}
					if err := session.dispatch(message); err != nil || session.quitRequested {
						return err
					}
				case id := <-nativeClosed:
					native.retire(id)
				case request := <-nativeRequests:
					var refusal string
					var operationErr error
					if request.Attach {
						refusal = native.attach(request, &session.attached, session.Client.Thread)
						if refusal == "" && request.Startup != "" && startupView == "" {
							startupView = request.Attachment
							if latestResponse != nil {
								response := *latestResponse
								latestResponse = nil
								operationErr = session.dispatch(response)
							}
						}
						adoptStartupChooser()
					} else {
						var view *nativeSessionView
						view, refusal = native.admit(request, &session.attached, session.Client.Thread)
						if refusal == "" {
							switch operation := request.Operation; {
							case operation.Answers != nil:
								refusal, operationErr = session.applyNativeAnswers(&session.attached, &session.picker, &session.Editor, session.side, session.clock, session.overlayOpen(session.statusPanel, session.resumePicker), session.ctx, *operation.Answers, request.Prepared, request.Context, request.RemoteHuman())
							case operation.Approval != nil:
								record, choice, err := view.attached.presentation.Approval(*operation.Approval, session.approvals.Approvals)
								if err != nil {
									refusal = err.Error()
								} else {
									operationErr = session.applyApproval(&session.attached, &session.picker, &session.Editor, session.clock, view.attached.grants, session.approvals.handles[record], choice, operation.Approval.Reason)
								}
							default:
								refusal, operationErr = applyNativeOperation(view, request)
							}
							view.projected = time.Time{}
						}
					}
					if request.Operation.Receipt != nil {
						pruneDraftImages(session.ownedImages, session.Editor, session.attached.conversation, &session.questions, &session.approvals, session.side, session.attached.hooks.nativeImages)
					}
					if operationErr != nil {
						refusal = "The session failed: " + operationErr.Error()
					}
					// Mutation acknowledgement is a publication barrier for keyboard
					// continuations. Publish before releasing the requesting client.
					native.publish(&session.attached, &session.Client, session.settings, session.serviceTiers, session.title, session.backendVersion, session.restoring != nil, session.side, session.review, session.reviewSource, nativePublicationFacts(&session.attached, session.panes, session.exitUsage, session.replayDebugDirectory, session.archived, session.notifications, session.Client.Thread, &predictions), presentationTime(session.clock))
					if request.Reply != nil {
						request.Reply <- refusal
					}
					if operationErr != nil {
						return operationErr
					}
				case intent := <-intents:
					refusal := apiIntentRefusal(&session.attached, &session.Client, intent)
					var intentErr error
					if refusal == "" {
						if intent.Action == sessionapi.RunShell {
							refusal, intentErr = session.submitAPIShell(&session.attached, &session.picker, session.clock, session.ctx, intent.Text)
						} else {
							refusal, intentErr = session.applyAPIIntent(&session.attached, &session.picker, &session.Editor, session.clock, &session.side, session.overlayOpen(session.statusPanel, session.resumePicker), session.ctx, intent)
						}
					}
					if intentErr != nil {
						refusal = "The session failed: " + intentErr.Error()
					}
					if intent.Reply != nil {
						intent.Reply <- refusal
					}
					if intentErr != nil || session.quitRequested {
						return intentErr
					}
				case <-tick.C:
					if err := flushCompletions(); err != nil {
						return err
					}
					syncLogicalWork(&session.attached, &session.Client.Live, threads, presentationTime(session.clock))
					threads.driver.Tick(presentationTime(session.clock), session.Client.Requests.RuntimeThreads(session.Client.Thread))
					if len(session.attached.sideInputs) > 0 || session.side != nil && len(session.side.Drafts.Unsent) > 0 {
						if err := flushBTW(&session.attached, &session.Client, &session.picker, &session.questions, &session.approvals, session.clock, session.settings, &session.side, &session.ownedImages, session.skillEnvironment, &session.guardHookCheck, session.controlObservations); err != nil {
							return err
						}
					}
					if session.Client.Shell.Continuation() || session.attached.threadInput.preparing() || len(session.attached.conversation.Unsent) > 0 {
						if err := session.flushInput(&session.attached, &session.picker, session.clock, &session.Editor, session.ctx); err != nil {
							return err
						}
					}
					refreshSessionMetrics(session.statusPanel, session.sessionCapture, presentationTime(session.clock), false)
					applyObservedActivity(&session.attached, &session.Client, session.usage, session.review, &session.approvals, &session.pendingEditReceipts, session.clock, session.restoring, session.audience)
					publishPresentation(&session.attached, &session.Client, &session.picker, &session.Editor, session.settings, session.serviceTiers, session.title, session.backendVersion, session.restoring != nil, session.side)
					session.flushStreamOutput(&session.attached, session.clock, session.ctx)
					startCommitReads(&session.attached, session.commits)
					paneError(session.attached.source, session.panes.savePrepared(presentationTime(session.clock), false))
					if !expireNotice(&session.attached.feedback, presentationTime(session.clock)) {
						expireApprovals(&session.attached, &session.Client, &session.picker, &session.Editor, &session.questions, &session.approvals, session.clock, session.notifications, session.review, &session.pendingEditReceipts)
					}
					for _, view := range native.views {
						if view.ctx.Err() != nil || view.attached.executionPresentation != session.attached.executionPresentation {
							continue
						}
						expireNotice(&view.attached.feedback, presentationTime(session.clock))
						refreshSessionMetrics(view.panel, session.sessionCapture, presentationTime(session.clock), false)
						if view.notices != nil && nativeErrorsWaiting(session.issues, session.Client.Thread, session.audience) {
							// Unpainted claims yield to newer composer errors; rows stay deduped.
							view.notices.finish(terminalsession.NoticeReceipt{})
							view.notices = nil
						}
						if view.notices == nil {
							requestEditor := new(composer.Editor)
							view.notices = applyCriticalNotices(&view.attached, &session.Client, &view.picker, &requestEditor, session.clock, session.settings, session.serviceTiers, session.title, session.backendVersion, &session.restoring, &session.side, session.audience, session.issues)
						}
					}
					notices := applyCriticalNotices(&session.attached, &session.Client, &session.picker, &session.Editor, session.clock, session.settings, session.serviceTiers, session.title, session.backendVersion, &session.restoring, &session.side, session.audience, session.issues)
					settlePresentation(&session.attached, &session.Client, &session.picker, &session.Editor, session.settings, session.serviceTiers, session.title, session.backendVersion, session.restoring != nil, session.side, session.clock, &session.questions, &session.approvals, session.overlayOpen(session.statusPanel, session.resumePicker))
					live.publish(&session.attached, &session.Client, session.review, session.reviewSource, session.restoring != nil, childSummary, presentationTime(session.clock))
					native.publish(&session.attached, &session.Client, session.settings, session.serviceTiers, session.title, session.backendVersion, session.restoring != nil, session.side, session.review, session.reviewSource, nativePublicationFacts(&session.attached, session.panes, session.exitUsage, session.replayDebugDirectory, session.archived, session.notifications, session.Client.Thread, &predictions), presentationTime(session.clock))
					// Publication is not a native paint receipt. Release claims without
					// acknowledging notices that no terminal has displayed.
					notices.finish(terminalsession.NoticeReceipt{})
					continue
				}
				if err := syncPredictions(); err != nil {
					return err
				}
				settlePresentation(&session.attached, &session.Client, &session.picker, &session.Editor, session.settings, session.serviceTiers, session.title, session.backendVersion, session.restoring != nil, session.side, session.clock, &session.questions, &session.approvals, session.overlayOpen(session.statusPanel, session.resumePicker))
				live.publish(&session.attached, &session.Client, session.review, session.reviewSource, session.restoring != nil, childSummary, presentationTime(session.clock))
				native.publish(&session.attached, &session.Client, session.settings, session.serviceTiers, session.title, session.backendVersion, session.restoring != nil, session.side, session.review, session.reviewSource, nativePublicationFacts(&session.attached, session.panes, session.exitUsage, session.replayDebugDirectory, session.archived, session.notifications, session.Client.Thread, &predictions), presentationTime(session.clock))
			}
		}
		err = runLoop()
		if !exited {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(session.ctx), 3*time.Second)
			err = errors.Join(err, session.Client.Requests.ArchiveSides(cleanup, c.Messages, session.side, session.Client.BTW))
			cancel()
		}
		_ = session.panes.savePrepared(presentationTime(session.clock), true)
		if session.panes != nil {
			err = errors.Join(err, session.panes.writer.close(session.panes.attachment, presentationTime(session.clock)))
		}
		if !exited {
			if session.ctx.Err() != nil {
				_ = c.Cancel()
			}
			err = errors.Join(err, c.Shutdown())
		}
		finishCommandSegments(&session.commandSegmentWrites, &session.commandSegmentPending, session.issues)
		for _, view := range native.views {
			view.projected = time.Time{}
		}
		native.publish(&session.attached, &session.Client, session.settings, session.serviceTiers, session.title, session.backendVersion, session.restoring != nil, session.side, session.review, session.reviewSource, nativePublicationFacts(&session.attached, session.panes, session.exitUsage, session.replayDebugDirectory, session.archived, session.notifications, session.Client.Thread, &predictions), presentationTime(session.clock))
		for key, run := range session.attached.session.Commands() {
			if run.Entry.Native.Invocation.ReleaseSegmentOutputs() {
				session.execTrack.ReleaseOutput(key)
			}
		}
		session.review.Close()
		session.Editor = hideQuestionEditor(&session.questions, session.attached.conversation, session.Editor, session.Client.Thread)
		hideApprovals(&session.approvals, &session.attached.conversation.PromptEditor, &session.attached.conversation.Editor)
		inputHistory.saveDraft(session.attached.conversation, session.Editor)
		_ = inputHistory.close()
		session.attached.releaseSideInputs()
		session.attached.session.Retire()
		if err != nil {
			if diagnostic := c.Diagnostics.Snapshot(); diagnostic != "" {
				if threads.headless != nil {
					err = fmt.Errorf("%w\nCodex: %s", err, diagnostic)
				}
			}
		}
		return err
	}, nil
}

// appServerRequest contains response routing data, not a second request ledger.
// Main's lifecycle scope retires on confirmed replacement. Side conversations
// and title saves retain their own completion and cleanup across that boundary.
type appServerRequest struct {
	history  *appServerHistoryRead
	view     string
	metadata string
	menu     uint64
	label    string
	title    *sessioncontrol.NameSave
	status   *appServerStatusReport
}

func (session *nativeSession) dispatchHostMessage(attached *attachedPresentation, picker *composerPicker, editor **composer.Editor, clock func() time.Time, side **sessioncontrol.Side, panel **appServerStatusReport, resumePicker **appServerResumePicker, parent context.Context, m appserver.Message) (err error) {
	if handled, complete, err := hydrateHistory(&session.Client, m); handled {
		if err != nil {
			return err
		}
		if complete != nil {
			return session.dispatch(*complete)
		}
		return nil
	}
	defer func() {
		if r := session.Client.Rewind; r != nil && r.Completed && r.Failed && r.Item == "" && session.shellEdits.pending != nil && session.shellEdits.pending.Thread() == r.Thread {
			session.shellEdits.pending.Discard()
			session.shellEdits.pending = nil
		}
		finishRewindRevert(attached, &session.Client, clock)
		settlePresentation(attached, &session.Client, picker, editor, session.settings, session.serviceTiers, session.title, session.backendVersion, session.restoring != nil, (*side), clock, &session.questions, &session.approvals, session.overlayOpen(*panel, *resumePicker))
	}()
	session.issues.ObserveTurnCompletion(m.Method, m.Params)
	observeShellMessage(&session.shellEdits, attached, clock, m)
	var response sessioncontrol.Request[appServerRequest]
	var correlated bool
	if m.Method == "" {
		if pending, ok := session.Client.Requests.Lookup(string(m.ID)); ok && pending.Method == "thread/fork" && pending.Side == nil && pending.Prediction == nil && session.Client.Replacement.Forking() && m.Error == nil && session.retained.Store != nil {
			if waiting := session.Client.Startup.ForkRetention; waiting != nil {
				if !waiting.Complete {
					return nil
				}
			} else if accepted, err := session.Client.Startup.Accept(&session.Client.Live, "thread/fork", m.Result); err == nil {
				session.Client.Startup.RetainFork(parent, session.retained, session.changeSource, session.Client.Thread, m, accepted)
				attached.presentation.Status.SetText("Retaining fork history…")
				return nil
			}
		}
		response, correlated = session.Client.Requests.Take(string(m.ID))
	}
	if handled, err := session.Client.ObservePrediction(m, response); handled {
		return err
	}
	if session.Client.Requests.ObserveAgentQuestion(m, response) {
		return nil
	}
	if sessionTitleMessage(&session.Client, session.titleGenerator, &attached.feedback, clock, &session.title, m, response.Owner.title) {
		return nil
	}
	defer func() {
		picker.refreshPicker(attached, clock, session.Client.Requests, (*editor), parent, session.skillEnvironment)
	}()
	// Side control settles first; work-capable forks share native event consumers.
	if update, err := session.Client.Requests.ObserveSide(m, response, session.Client.Thread); update.Handled {
		applySideUpdate(attached, &session.Client, clock, update)
		if err != nil {
			return err
		}

		if update.Flush {
			return flushBTW(attached, &session.Client, picker, &session.questions, &session.approvals, clock, session.settings, side, &session.ownedImages, session.skillEnvironment, &session.guardHookCheck, session.controlObservations)
		}
		if !update.Forward || update.Side == session.Client.BTW {
			return nil
		}
	}
	if session.Client.RetiredEvent(m) {
		return nil
	}
	// Any event can make stacked input sendable: an acknowledgement, a turn
	// start or end, or settled settings.
	defer func() {
		if err == nil {
			err = session.flushInput(attached, picker, clock, editor, parent)
		}
		if err == nil {
			err = flushBTW(attached, &session.Client, picker, &session.questions, &session.approvals, clock, session.settings, side, &session.ownedImages, session.skillEnvironment, &session.guardHookCheck, session.controlObservations)
		}
	}()
	if m.Method == "skills/changed" {
		picker.invalidateSkills()
	}
	// Input observations precede the output items that answer them. Drain
	// before a completion, not just on the next paint tick, so links bind once.
	applyObservedActivity(attached, &session.Client, session.usage, session.review, &session.approvals, &session.pendingEditReceipts, clock, session.restoring, session.audience)
	if handled, err := session.Client.Requests.ObserveForkSettings(&session.Client.Startup, &session.Client.Live, m, correlated && response.Owner.label == "fork/settings"); handled {
		if err != nil {
			return resumeSessionFailed(attached, &session.Client, picker, &session.questions, &session.approvals, clock, session.dispatch, &session.resumePending, err.Error())
		}
		return nil
	}
	resuming := session.Client.Startup.Thread != "" && session.Client.Startup.Thread != sessioncontrol.ResumePicker && (session.Client.Thread == "" || session.restoring != nil)
	if (resuming || session.Client.Replacement.Target != "" || session.Client.Replacement.Forking()) && m.Method != "" {
		if hold, keep := holdResumeEvent(&attached.session, &session.Client.Live, session.Client.Startup, m, resuming); hold {
			if !keep {
				return nil
			}
			if len(session.resumePending) == 256 {
				return errors.New("resume event capacity exceeded; session state is incomplete")
			}
			session.resumePending = append(session.resumePending, m)
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
		if handled, err := session.dispatchViewResponse(attached, picker, editor, clock, panel, resumePicker, parent, response, m); handled || err != nil {
			return err
		}
		if method == "thread/archive" {
			if m.Error != nil {
				session.Client.Replacement.Finish()
				setNotice(&attached.feedback, presentationTime(clock), "Failed to archive current thread: "+m.Error.Message, true)
			} else {
				session.archived, session.quitRequested = true, true
			}
			return nil
		}
		if method == "resume/settings" {
			return resumeSettingsResponse(attached, &session.Client, picker, &session.questions, &session.approvals, clock, session.approvalMode, session.dispatch, &session.resumePending, session.panes, m)
		}
		if method == "config/read" {
			session.notifications.notificationConfig(&attached.feedback, clock, m)
			attached.inputHistory.configure(m)
			if m.Error == nil && session.Client.Requests.CaptureConfiguredCatalog != nil {
				session.Client.Requests.CaptureConfiguredCatalog(m.Result)
			}
			session.Client.Requests.CatalogPending = false
			return flushBTW(attached, &session.Client, picker, &session.questions, &session.approvals, clock, session.settings, side, &session.ownedImages, session.skillEnvironment, &session.guardHookCheck, session.controlObservations)
		}
		if method == "thread/list" && session.Client.Startup.Thread == "--last" {
			return resumeLastResponse(attached, &session.Client, picker, clock, resumePicker, session.approvalMode, m)
		}
		if handled, err := session.settingsMessage(attached, picker, editor, (*side), clock, session.overlayOpen(*panel, *resumePicker), method, m); handled || err != nil {
			return err
		}
		if method == "turn/start" || method == "turn/steer" {
			submissionResponse(attached, &session.Client, picker, &session.questions, &session.approvals, clock, method, m.Error)
			return nil
		}
		if method == "thread/shellCommand" {
			shellResponse(attached, &session.Client, editor, clock, &session.shellEdits, m.Error)
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
					session.shellEdits.shell = info.Shell.Path
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
				applyInputEffects(attached, &session.questions, &session.approvals, picker, session.Client.Turn, clock, attached.presentation.AcknowledgeCompaction(&session.Client.Live, attached.conversation, failure, m.Error != nil))
			} else {
				applyInputEffects(attached, &session.questions, &session.approvals, picker, session.Client.Turn, clock, attached.presentation.AcknowledgeInterrupt(&session.Client.Live, attached.conversation, method, failure, m.Error != nil))
			}
			return nil
		}
		if method == "approval/hooks" {
			return session.guardHookResponse(attached, picker, editor, clock, parent, m)
		}
		if method == "thread/start" && session.Client.Replacement.Pending() && m.Error != nil {
			session.Client.Replacement.Finish()
			restoreDrafts(attached, &session.questions, &session.approvals, picker, session.Client.Turn, clock, slices.Concat(attached.conversation.Unsent, attached.conversation.Queued)...)
			attached.conversation.Unsent, attached.conversation.Queued = nil, nil
			attached.presentation.Status.SetText("Ready")
			setNotice(&attached.feedback, presentationTime(clock), "Could not clear session: "+m.Error.Message, true)
			return nil
		}
		if method == "thread/fork" && session.Client.Startup.ForkRetention != nil {
			pending := session.Client.Startup.ForkRetention
			session.Client.Startup.ForkRetention = nil
			if pending.Err != nil {
				if accepted, err := session.Client.Startup.Accept(&session.Client.Live, method, m.Result); err == nil {
					session.Client.Replacement.Target = accepted.Thread.ID
				}
				return resumeSessionFailed(attached, &session.Client, picker, &session.questions, &session.approvals, clock, session.dispatch, &session.resumePending, "retaining history: "+pending.Err.Error())
			}
		}
		if method == "thread/revert" && m.Error != nil {
			session.Client.Replacement.Finish()
			attached.presentation.Status.SetText("Ready")
			setNotice(&attached.feedback, presentationTime(clock), "Could not rewind conversation: "+m.Error.Message, true)
			return nil
		}
		if (method == "thread/resume" && session.Client.Replacement.Target != "" || method == "thread/fork" && session.Client.Replacement.Forking()) && m.Error != nil {
			return resumeSessionFailed(attached, &session.Client, picker, &session.questions, &session.approvals, clock, session.dispatch, &session.resumePending, m.Error.Message)
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
				session.backendVersion = backendVersion(initialized.UserAgent)
			}
			if err := session.Client.Requests.Initialized(session.execWindows != nil); err != nil {
				return err
			}
			return openSession(attached, &session.Client, picker, clock, resumePicker, session.approvalMode)
		case "thread/start", "thread/resume", "thread/fork", "thread/revert":
			result, err := session.Client.Startup.Accept(&session.Client.Live, method, m.Result)
			if err != nil {
				if method == "thread/revert" {
					session.Client.Replacement.Finish()
					attached.presentation.Status.SetText("Ready")
					setNotice(&attached.feedback, presentationTime(clock), "Could not rewind conversation: "+err.Error(), true)
					return nil
				}
				if session.Client.Replacement.Forking() {
					return resumeSessionFailed(attached, &session.Client, picker, &session.questions, &session.approvals, clock, session.dispatch, &session.resumePending, err.Error())
				}
				return err
			}
			if method == "thread/revert" {
				result.Model, result.ReasoningEffort, result.ServiceTier = session.settings.Model, session.settings.Effort, session.settings.Tier
				result.PermissionState = session.Client.Settings.Permissions
			}
			if held, err := session.Client.Requests.PrepareForkEffort(&session.Client.Startup, &session.Client.Live, method, &result, appServerRequest{label: "fork/settings"}); held {
				if err != nil {
					return resumeSessionFailed(attached, &session.Client, picker, &session.questions, &session.approvals, clock, session.dispatch, &session.resumePending, err.Error())
				}
				attached.presentation.Status.SetText("Restoring fork settings…")
				return nil
			}
			if session.Client.Replacement.Pending() {
				if method != "thread/revert" {
					if err := closeBTWQuestion(attached, &session.Client); err != nil {
						return err
					}
					// Detach only after host acceptance; process and operation owners remain live.
					closing := (*side)
					if closing != nil {
						(*side) = nil
						closing.Close()
						attached.conversation.Side = nil
						pruneDraftImages(session.ownedImages, (*editor), attached.conversation, &session.questions, &session.approvals, (*side), attached.hooks.nativeImages)
						if err := session.Client.Requests.ReleaseSide(closing); err != nil {
							return err
						}
					}
				}
				if session.panes != nil {
					paneError(attached.source, session.panes.savePrepared(time.Now(), true))
				}
				picker.cancelScan()
				roster := attached.session.Roster()
				retired := make([]string, 0, len(roster))
				for _, agent := range roster {
					retired = append(retired, agent.Thread)
				}
				if method != "thread/revert" {
					session.Client.RetireThreads(session.Client.Thread, retired...)
				}
				(*editor) = hideQuestionEditor(&session.questions, attached.conversation, (*editor), session.Client.Thread)
				retainedQuestions := session.questions.calls()
				if session.notifications != nil {
					clear(session.notifications.blocked)
				}
				(*panel) = nil
				attached.presentation.ClosePanel()
				if method != "thread/revert" {
					session.Client.Requests.Retire("main") // Retire old-thread response correlation.
					if err := session.Client.Request("thread/unsubscribe", map[string]any{"threadId": session.Client.Thread}); err != nil {
						return err
					}
				}
				(*picker) = composerPicker{}
				session.exitUsage = binding.TokenUsage{}
				attached.feedback = noticepresentation.Model{}
				releaseSegmentOutputs(attached, session.execTrack)
				waiting := attached.conversation
				sideInputs := attached.sideInputs
				attached.session.Retire()
				workspace := attached.cwd
				inputHistory, hooks := attached.inputHistory, attached.hooks
				(*attached) = newAttachedPresentation(parent, session.presentationState, session.conversationState, session.observations, hooks)
				attached.sideInputs = sideInputs
				attached.cwd = workspace
				attached.inputHistory = inputHistory
				attached.conversation.CarryInput(waiting)
				if method == "thread/revert" {
					attached.conversation.Side, attached.conversation.BTW = waiting.Side, waiting.BTW
				}
				(*editor) = &attached.conversation.Editor

				picker.State, picker.MenuState = &attached.conversation.Completion, attached.presentation.Menu()
				session.approvals.Approvals = &attached.conversation.Approvals
				session.questions.carryLocal(attached.conversation, retainedQuestions, session.Client.Thread)
				session.commits = attachCommitReader(&attached.session, session.commits, parent, session.activity != nil)
				attached.presentation.Attach(sessionpresentation.Thread{ID: cmp.Or(session.Client.Thread, "/root"), Caller: "/root"}, false)
				session.Client.LeaveThread()
				session.Client.Startup.Thread, session.Client.Startup.Workspace = "", ""
				session.Client.Readmit(result.Thread.ID)
				resetDiffScope(attached, session.review, session.reviewSource, parent)
				if method == "thread/resume" || method == "thread/fork" || method == "thread/revert" {
					// Restoration below replays events buffered during the switch.
					session.Client.Startup.Thread = result.Thread.ID
				} else {
					// Seed the Diff's lineage; after a switch, requests alone cannot.
					restoreDiffThread(attached, session.review, session.reviewSource, result.Thread)
				}
			}
			if method != "thread/revert" {
				session.statusConfig = appServerStatusConfig{}
				if err := json.Unmarshal(m.Result, &session.statusConfig); err != nil {
					return err
				}
			}
			session.Client.Thread, session.Client.RolloutPath = result.Thread.ID, result.Thread.Path
			session.Client.Settings.Permissions = result.PermissionState
			attached.inputHistory.selectThread(session.Client.Thread)
			for i := range attached.conversation.Editor.InputHistory {
				entry := &attached.conversation.Editor.InputHistory[i]
				if entry.Source == session.Client.Thread {
					entry.Restored = false
				}
			}
			attachPresentation(attached, picker, editor, &session.questions, &session.approvals, session.Client.Thread, &session.review, &session.commits, parent, session.retained, session.changeSource, session.activity != nil)
			attached.presentation.Status.SetText("Ready")
			session.title = result.Thread.Name
			session.titleGenerator.register(result.Thread)
			if name := session.Client.Names.Attach(session.Client.Thread); name != "" {
				session.titleGenerator.nameChanged(session.Client.Thread, name)
				requestSessionTitle(&session.Client, &attached.feedback, clock, session.Client.Names.Next(session.Client.Thread))
			}
			session.settings.Model, session.settings.Effort, session.settings.Tier = result.Model, result.ReasoningEffort, result.ServiceTier
			if method == "thread/resume" || method == "thread/fork" || method == "thread/revert" {
				session.Client.Settings.RestoreEffort = session.Client.TakeResumeEffort()
			}
			if method == "thread/start" {
				session.Client.Settings.RestoredEffort()
				if session.usage != nil {
					session.usage.MarkNew(result.Thread.ID)
				}
			}
			session.models, session.modelsLoading = nil, true // A cleared or switched session lists them again.
			if err := session.Client.Request("model/list", map[string]any{"includeHidden": true}); err != nil {
				return err
			}
			startAttachedSession(attached, session.observations, session.Client.Thread, result.Thread.Cwd)
			if method == "thread/start" {
				restoreDiffThread(attached, session.review, session.reviewSource, result.Thread)
			}
			session.retainedSession.Close()
			session.retainedSession = history.Session{}
			if session.notifications != nil {
				session.Client.Requests.CatalogPending = true
				if err := session.Client.Request("config/read", map[string]any{"cwd": result.Thread.Cwd, "includeLayers": false}); err != nil {
					return err
				}
			}
			if session.retained.Store != nil || session.panes != nil {
				err := openRetainedSession(parent, session.history, &session.retainedSession, session.Client.Thread, session.retained.Store, session.retained.Writes, session.retained.Limits)
				// Prepared children acquire their parent's namespace in logical admission.
				// Only an ordinary root can enroll itself before its first turn.
				if err == nil && session.Client.Startup.Prepared == nil {
					err = session.retainedSession.Store.Transaction(parent, session.retainedSession.Writes, func(tx *history.Transaction) error {
						return tx.InitializeScope(history.Scope{Thread: session.Client.Thread, Namespace: session.retainedSession.Namespace}, nil)
					})
					if err != nil {
						session.retainedSession.Close()
					}
				}
				if err != nil {
					setNotice(&attached.feedback, presentationTime(clock), "Session history could not be opened: "+err.Error(), true)
				}
			}
			restoreContextUsage(attached.session.Agent("/root"), result.Thread)
			session.controlObservations.RefreshContext(result.Thread.ID)
			if method == "thread/revert" && session.preparer != nil {
				session.preparer.AdmitRewind(result.Thread.ID, result.Thread.Cwd)
			}
			restoreCompactionMetrics(&attached.session, result.Thread)
			restoreUsage(session.usage, result.Thread)
			observeCost(session.usage, session.Client.Thread, attached.session.Agent("/root"))
			if attached.childSource != nil {
				attached.childSource.Apply(sessionpresentation.ActivityEvent{Agents: attached.session.Roster()})
			}
			if session.audience != nil {
				if session.Client.Startup.Prepared != nil {
					session.audience.AttachRuntime(session.Client.Thread)
				} else {
					session.audience.Attach(session.Client.Thread)
				}
				attached.bindRepetitions(session.audience, session.Client.Thread)
			}
			// Resumed history is applied once the roster is read, so Main can
			// follow child activity in order. An active snapshot keeps its
			// steer/interrupt target meanwhile.
			if method == "thread/resume" || method == "thread/fork" || method == "thread/revert" {
				for _, turn := range result.Thread.Turns {
					if turn.Status == "inProgress" {
						session.Client.Turn = turn.ID
						attached.presentation.Status.Started = presentationTime(clock)
						attached.presentation.Status.SetText("Working")
						attached.session.Lifecycle(session.Client.Thread).ResumeActive()
					}
				}
			}
			if session.panes != nil {
				paneError(attached.source, session.panes.open(result.Thread.Cwd, session.Client.Thread, method == "thread/resume"))
			}
			if method == "thread/resume" || method == "thread/fork" || method == "thread/revert" {
				repair, err := result.RepairDefaultEffort(&session.Client.Live)
				if err != nil {
					return err
				}
				session.settings.Effort = result.ReasoningEffort
				if repair != nil {
					// Preserve saved null intent before a corrective dispatch can fail.
					if session.panes != nil && session.panes.attachment != nil && session.Client.Startup.Evidence.Model != "" {
						session.panes.attachment.Recover(session.Client.Startup.Evidence, presentationTime(clock))
						paneError(attached.source, session.panes.savePrepared(presentationTime(clock), true))
					}
					if _, err := updateSettings(attached, &session.Client, session.settings, &session.serviceTiers, session.panes, session.restoring != nil, clock, repair); err != nil {
						return err
					}
				}
				if session.Client.Startup.MissingSettings {
					setNotice(&attached.feedback, presentationTime(clock), sessionpresentation.ResumeSettingsUnavailable, true)
					session.Client.Startup.MissingSettings = false
				}
				paneError(attached.source, session.panes.retainAppliedSettings(preferences.Scope{Workspace: attached.cwd, Thread: session.Client.Thread}, session.Client.Settings.RestoreEffort, preferences.Applied{Model: session.settings.Model, Effort: session.settings.Effort, Tier: session.settings.Tier, Observed: presentationTime(clock)}))
				err = session.restorePaneContent(attached, picker, editor, clock, panel, resumePicker, parent, result.Thread)
				if read := response.Owner.history; read != nil && read.failure != "" {
					message := "Conversation history incomplete: " + read.failure
					restoreHistoryNotice(attached, session.restoring, message, true)
					setNotice(&attached.feedback, presentationTime(clock), message, true)
				}
				return err
			}
			paneError(attached.source, session.panes.retainAppliedSettings(preferences.Scope{Workspace: attached.cwd, Thread: session.Client.Thread}, session.Client.Settings.RestoreEffort, preferences.Applied{Model: session.settings.Model, Effort: session.settings.Effort, Tier: session.settings.Tier, Observed: presentationTime(clock)}))
		}
		return nil
	}
	session.notifications.resolve(m)
	if handled, err := approvalMessage(attached, &session.Client, picker, editor, &session.questions, &session.approvals, clock, session.overlayOpen(*panel, *resumePicker), session.notifications, session.review, &session.pendingEditReceipts, session.controlObservations, m); handled {
		return err
	}
	if handled, err := questionMessage(attached, &session.Client, picker, editor, &session.questions, &session.approvals, clock, session.overlayOpen(*panel, *resumePicker), session.notifications, session.review, &session.pendingEditReceipts, m); handled {
		return err
	}
	if len(m.ID) != 0 {
		var target struct {
			Thread string `json:"threadId"`
		}
		if json.Unmarshal(m.Params, &target) == nil && session.Client.Requests.WorkSide(target.Thread) {
			side := session.Client.Requests.Side(target.Thread)
			side.Status, side.Alert = "Blocked on unsupported request "+m.Method, true
			return nil
		}
		// Never auto-approve an unexpected server request. Leave it pending,
		// visibly blocked, until interrupted.
		attached.presentation.Status.Set("Blocked on unsupported request "+m.Method+" · Ctrl-C interrupts", true)
		session.notifications.blockNotification(&attached.feedback, clock, m)
		attached.source.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{Seq: attached.source.LastSeq + 1, Agent: "Session", Kind: "text", Text: attached.presentation.Status.Text, Observed: presentationTime(clock)}}})
		return nil
	}
	if handled, err := session.settingsMessage(attached, picker, editor, (*side), clock, session.overlayOpen(*panel, *resumePicker), "", m); handled || err != nil {
		return err
	}
	switch m.Method {
	case "error", "thread/started", "thread/status/changed", "thread/tokenUsage/updated", "item/reasoning/summaryTextDelta", "item/reasoning/summaryPartAdded",
		"turn/started", "turn/completed", "item/started", "item/completed", "item/agentMessage/delta", "item/commandExecution/terminalInteraction",
		"item/commandExecution/outputDelta", "hook/started", "hook/completed":
	default:
		return nil
	}
	p, err := binding.DecodeNotification(m.Method, m.Params)
	if err != nil {
		return fmt.Errorf("%s: %w", m.Method, err)
	}
	return session.observedEvent(attached, picker, editor, clock, panel, resumePicker, m.Method, p)
}

// observedEvent retains application lifecycle and control handling for decoded
// host facts. Offline transcript observations bypass it through their reducer.
func (session *nativeSession) observedEvent(attached *attachedPresentation, picker *composerPicker, editor **composer.Editor, clock func() time.Time, panel **appServerStatusReport, resumePicker **appServerResumePicker, method string, p binding.Notification) error {
	if method == "turn/completed" && p.ThreadID == session.Client.Thread && p.Turn.Status != "completed" && !session.Client.Compaction.OwnsTurn(p.Turn.ID) && !session.Client.CompactionStopsWait(p.Turn.ID) {
		session.Client.Waiting, session.Client.LocalStop = "", ""
		attached.session.Lifecycle(p.ThreadID).SetWaiting(false, presentationTime(clock))
	}
	if r := session.Client.Rewind; r != nil && r.Sent && p.ThreadID == r.Thread && p.TurnID == r.Turn && p.Item.Type == "commandExecution" && p.Item.Source == "userShell" {
		if method == "item/started" && r.Item == "" {
			r.Item = p.Item.ID
		}
		if method == "item/completed" && r.Item != "" && p.Item.ID == r.Item {
			r.Completed, r.Failed = true, p.Item.ExitCode == nil || *p.Item.ExitCode != 0 || p.Item.Status != "completed"
		}
	}
	text := binding.RetainedTranscript(p.Observation(method, presentationTime(clock)))
	if target := session.Client.Requests.Side(p.ThreadID); target != nil && target.OneOff && !target.Closed {
		if method == "item/started" || method == "item/completed" {
			if observeMessageQuestions(attached, &session.Client, &session.approvals, clock, &session.questions, editor, picker, session.overlayOpen(*panel, *resumePicker), session.notifications, text, p.Item.Observation(), false) {
				return nil
			}
		}
		if method == "turn/completed" {
			for _, c := range session.questions.calls() {
				if c.Thread == p.ThreadID && c.Turn == p.Turn.ID && c.Sync {
					resolveQuestionCall(attached, &session.Client, editor, &session.questions, &session.approvals, clock, session.review, &session.pendingEditReceipts, c, "Turn ended")
				}
			}
		}
	}
	if method == "thread/started" {
		attached.session.Caller(p.Thread.ID)
		attached.session.Activity().Agent(p.Thread.ID).ParentThread = p.Thread.ParentThreadID
	}
	if method == "thread/started" && session.Client.Requests.WorkSide(p.Thread.ID) {
		p.Thread.Path = sessionpresentation.SideCaller + strings.TrimPrefix(p.Thread.Path, "/root")
		attached.session.Caller(p.Thread.ID)
		attached.session.Activity().Agent(p.Thread.ID).Isolated = true
	}
	if handled, err := session.sessionEvent(attached, clock, method, p); handled || err != nil {
		return err
	}

	switch method {
	case "turn/started":
		if p.ThreadID == session.Client.Thread {
			if r := session.Client.Rewind; r != nil && r.Sent && r.Turn == "" {
				r.Turn = p.Turn.ID
			}
			session.Client.Started(p.Turn.ID)
			if attached.presentation.Status.Text != "Waiting for agents" {
				attached.presentation.Status.Started = presentationTime(clock)
			}
			attached.presentation.Status.Set("Working", false)
			if session.Client.Interruption.BeforeStart || session.Client.Compaction.InterruptTarget() != "" {
				return applyInterruption(attached, &session.Client, picker, editor, &session.questions, &session.approvals, clock, session.review, &session.pendingEditReceipts, session.Client.Interrupt())
			}
		}
	case "turn/completed":
		if session.notifications != nil {
			for id, request := range session.notifications.blocked {
				if request.Thread == p.ThreadID && (request.Turn == "" || request.Turn == p.Turn.ID) {
					delete(session.notifications.blocked, id)
				}
			}
		}
		if p.ThreadID == session.Client.Thread && p.Turn.ID == session.Client.Turn {
			shellTurn := session.Client.Shell.OwnsTurn(p.Turn.ID)
			compactTurn := session.Client.Compaction.OwnsTurn(p.Turn.ID)
			compactWait := session.Client.CompactionStopsWait(p.Turn.ID)
			if r := session.Client.Rewind; r != nil && r.Turn == p.Turn.ID && p.Turn.Status != "completed" {
				r.Completed, r.Failed = true, true
			}
			session.Client.Completed(p.Turn.Status == "completed")
			endSyncQuestions(attached, &session.Client, editor, &session.questions, &session.approvals, clock, session.review, &session.pendingEditReceipts, p.Turn.ID)
			var failure *string
			if p.Turn.Error != nil {
				failure = &p.Turn.Error.Message
			}
			localStop := session.Client.LocalStop != "" && (compactTurn || compactWait || p.Turn.Status == "completed" && (session.Client.LocalStop == p.Turn.ID || shellTurn))
			continuing := compactTurn && session.Client.Compaction.ContinuationPending()
			if localStop {
				attached.presentation.Status.Set("Waiting for agents", false)
			} else if continuing {
				attached.presentation.Status.Set("Working", false)
			} else {
				attached.presentation.Status.Complete(p.Turn.Status, failure, presentationTime(clock))
			}
			settleInput(attached, &session.Client, picker, &session.questions, &session.approvals, clock, p.Turn.ID, p.Turn.Status == "interrupted")
			if !localStop && !continuing && !shellTurn && p.Turn.Status == "completed" && session.questions.Count(session.Client.Thread) == 0 {
				session.notifications.notify(&attached.feedback, clock, "agent-turn-complete", "Agent turn complete")
			}
			for _, c := range session.questions.calls() {
				if !c.Resolved {
					renderQuestionRecord(attached, session.Client.Turn, presentationTime(clock), c)
				}
			}
		}
	case "item/started", "item/completed", "item/agentMessage/delta":
		if method == "item/started" {
			observeShellItem(&session.Client.Shell, session.Client.Thread, p.ThreadID, p.Item)
		}
		if p.ItemID == "" {
			p.ItemID = p.Item.ID
		}
		// Only completion commits, so one message cannot match two identical steers.
		if p.ThreadID == session.Client.Thread && p.Item.Type == "userMessage" && method == "item/completed" {
			commitSteer(attached, &session.Client.Live, p.Item)
		}
		session.messageTranscript(attached, clock, editor, picker, session.overlayOpen(*panel, *resumePicker), text, p.Item.Observation(), true)
	}
	return nil
}

func applyActivity(attached *attachedPresentation, approvals *nativeApprovalDock, review *sessionreview.Review, pending *[]*changepresentation.Receipt, main string, clock func() time.Time, entries []sessionpresentation.ActivityEntry, agents []sessionpresentation.ActivityAgent) {
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
	attached.session.ProjectSide(attached.childSource, attached.presentation.View().SideThread, attached.conversation.Side)
	refreshRosterRoles(&attached.session, attached.childSource)
}

// Router-owned annotations and authenticated directed inputs supplement the
// native event stream without becoming assistant speech or execution events.
func applyObservedActivity(attached *attachedPresentation, control *sessioncontrol.Client[appServerRequest], usage *accounting.Service, reviewOwner *sessionreview.Review, approvals *nativeApprovalDock, receipts *[]*changepresentation.Receipt, clock func() time.Time, restoring *appServerActivityRestore, audience *sessioncontrol.Audience) {
	if restoring != nil && !restoring.rendered {
		return
	}
	// Provider rounds can start within one host turn, before another usage
	// notification. Refresh from the owner so a prior round's TPS is not stale.
	usageChanged := false
	for _, observed := range attached.session.Roster() {
		if agent := attached.session.Agent(observed.Name); agent != nil {
			before := *agent
			agent.Generating = observed.Generating
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
			renameThreadActivity(attached, previous, agent.Name)
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
			entries = append(entries, attached.session.AdmitText(providerStarted(attached, start.Thread, start.At), control.Thread)...)
		}
	}
	for i := range entries {
		entries[i].Seq = attached.session.Activity().NextSequence()
	}
	if len(entries) != 0 || receivedReceipt || admitted {
		applyActivity(attached, approvals, reviewOwner, receipts, control.Thread, clock, entries, attached.session.Roster())
	}
	for _, root := range roots {
		for _, ref := range audience.TakeUnreturned(root) {
			attached.source.MarkUnreturned(ref.Thread, ref.Call)
			attached.childSource.MarkUnreturned(ref.Thread, ref.Call)
		}
	}
}
