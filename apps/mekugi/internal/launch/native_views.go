package launch

import (
	"context"
	"slices"
	"time"

	"github.com/yusing/mekugi/backends/codex/appserver"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/libraries/diffrender"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/conversation/completion"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversation "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

// nativeViews owns renderer attachments on the existing execution goroutine.
// It borrows execution owners and never constructs another host request ledger.
type nativeViews struct {
	diagnostics *appserver.Diagnostics
	threads     *threadExecution
	activity    *activitymodel.Service
	api         *sessionapi.Native
	requests    *sessioncontrol.Requests[appServerRequest]
	models      *sessionpresentation.Service
	terminals   terminalsession.Views
	replacing   string
	retiring    func(*nativeSessionView) error
	failure     error
	completions scope.Key[completion.State]
	views       map[string]*nativeSessionView
	closed      chan string
	ctx         context.Context
	cancel      context.CancelFunc
}

type nativeSessionView struct {
	childProjection       nativeChildProjection
	children              *sessionapi.Hub
	childDetails          *nativeChildDetails
	remoteHuman           bool
	focused               bool
	predictionDraftEmpty  bool
	files                 detailFiles
	activity              *activitymodel.Service
	detailDemand          *sessionpresentation.DetailOutputRequest
	scope                 sessionpresentation.Attachment
	attached              attachedPresentation
	picker                composerPicker
	thread                string
	startup               bool
	initial               bool
	ctx                   context.Context
	stop                  func() bool
	projected             time.Time
	lastExecutionFeedback uint64
	completionTarget      conversation.EditorTarget
	mainOutput            []activity.OutputResult
	agentOutput           []activity.OutputResult
	shell                 *terminalsession.Attachment
	panel                 *appServerStatusReport
	resume                *appServerResumePicker
	archiveOriginal       string
	effects               nativeViewEffects
	notification          *sessionapi.NativeEffect
	notices               *nativeCriticalNoticeDelivery
}

func newNativeViews(parent context.Context, api *sessionapi.Native, models *sessionpresentation.Service, terminals terminalsession.Views, activity *activitymodel.Service, requests *sessioncontrol.Requests[appServerRequest]) *nativeViews {
	ctx, cancel := context.WithCancel(parent)
	return &nativeViews{activity: activity, api: api, requests: requests, models: models, terminals: terminals, views: make(map[string]*nativeSessionView), closed: make(chan string, 16), ctx: ctx, cancel: cancel}
}

// View reads share the execution ledger, but their correlation ends with the
// requesting presentation. The local attachment retains its Main scope.
func viewRequestScope(id string) string {
	if id == "" {
		return "main"
	}
	return "view:" + id
}

func (n *nativeViews) close() {
	n.cancel()
	for id := range n.views {
		n.dispose(id)
	}
}

func (n *nativeViews) retire(id string) {
	n.dispose(id)
	n.api.Close(id)
}

// Shutdown disposes presentation resources before the hub publishes the final
// joined runtime outcome. Only an ordinary view retirement closes its stream.
func (n *nativeViews) dispose(id string) {
	if view := n.views[id]; view != nil {
		view.closeChildDetails()
		// Drop its image claims before the existing retirement cleanup prunes
		// unsubmitted files. The view's model remains alive for history cleanup.
		delete(n.views, id)
		n.requests.Retire(viewRequestScope(id))
		if n.retiring != nil && n.ctx.Err() == nil {
			if err := n.retiring(view); err != nil && n.failure == nil {
				n.failure = err
			}
		}
		view.stop()
		view.files.closeOutput(view.shell)
		view.picker.cancelScan()
		view.notices.finish(terminalsession.NoticeReceipt{})
		view.shell.Diff.Close()
		view.scope.Retire()
		view.attached = attachedPresentation{}
		view.picker = composerPicker{}
		view.mainOutput, view.agentOutput = nil, nil
	}
}

func (n *nativeViews) attach(request sessionapi.NativeRequest, execution *attachedPresentation, thread string) string {
	if request.Context == nil || request.Context.Err() != nil || request.ViewContext == nil || request.ViewContext.Err() != nil {
		return "The native attachment closed before admission."
	}
	if request.Attachment == "" || n.views[request.Attachment] != nil || request.Workspace != execution.cwd || request.Thread != thread && !(request.Thread == "" && request.Startup != "") {
		return "The requested session attachment is unavailable."
	}
	v := &nativeSessionView{remoteHuman: request.RemoteHuman(), thread: request.Thread, startup: request.Startup != "", initial: true, ctx: request.ViewContext}
	if n.threads != nil {
		v.children = n.threads.hub
	}
	v.attached = attachedPresentation{executionPresentation: execution.executionPresentation, presentation: n.models.Session(v.scope.Scope()), view: request.Attachment, effects: &v.effects}
	// Transitional semantic adapters borrow independent renderer scratch. No
	// physical terminal, editor, key decoder or painting lifetime is attached.
	v.shell, _ = n.terminals.NewAttachment(diffrender.DarkTheme)
	v.files.context, v.activity = v.ctx, n.activity
	v.attached.presentation.Attach(sessionpresentation.Thread{ID: request.Thread, Caller: "/root"}, false)
	v.picker.State, v.picker.MenuState = n.completions.Value(v.scope.Scope()), v.attached.presentation.Menu()
	v.picker.view = request.Attachment
	v.stop = context.AfterFunc(request.ViewContext, func() {
		select {
		case n.closed <- request.Attachment:
		case <-n.ctx.Done():
		}
	})
	n.views[request.Attachment] = v
	execution.modelsReady = n.refreshSettings
	return ""
}

func (n *nativeViews) refreshSettings(models []appServerModel, applied preferences.Applied, tiers *inferencegateway.Tiers, problem string) {
	for _, view := range n.views {
		if view.ctx.Err() != nil {
			continue
		}
		_, current := selectedSettings(&view.attached, n.requests, applied)
		refreshSettingsPicker(&view.picker, view.attached.presentation.View().Input.ID, models, current, tiers, problem)
		view.projected = time.Time{}
	}
}

func (n *nativeViews) admit(request sessionapi.NativeRequest, execution *attachedPresentation, thread string) (*nativeSessionView, string) {
	v := n.views[request.Attachment]
	if request.Context == nil || request.Context.Err() != nil || v == nil || v.ctx.Err() != nil {
		return nil, "The native attachment closed before admission."
	}
	current := v.attached.presentation.View()
	sameVersion := request.Version.SameAttachment(current.Version)
	// Raw commands carry no independent destination identity.
	if request.Operation.Command != nil {
		sameVersion = v.attached.presentation.AdmitDestination(request.Version) == nil
	}
	if details := request.Operation.Details; details != nil {
		if details.Displayed == (sessionpresentation.Thread{}) {
			// Public detail requests may omit display identity; the
			// observed destination lifetime identifies their destination.
			sameVersion = v.attached.presentation.AdmitDestination(request.Version) == nil
		} else if details.Displayed.ID != current.Main.ID && details.Displayed.ID != current.SideThread.ID && v.attached.session.KnownCaller(details.Displayed.ID) == "" {
			return nil, sessionpresentation.ErrStaleIntent.Error()
		}
	}
	if v.remoteHuman != request.RemoteHuman() || v.attached.executionPresentation != execution.executionPresentation || request.Thread != thread || request.Thread != v.thread || request.Workspace != execution.cwd || !sameVersion {
		return nil, "The requested session destination is unavailable."
	}
	return v, ""
}

// applyView accepts only presentation operations. Host operations retain their
// existing handlers and need explicit view attribution before they can enter here.
func (v *nativeSessionView) applyView(operation sessionapi.NativeOperation) string {
	switch {
	case operation.Focused != nil:
		v.focused = *operation.Focused
	case operation.Details != nil:
		return v.openDetails(*operation.Details)
	case operation.CloseDetails != nil:
		if details := v.childDetails; details != nil {
			if *operation.CloseDetails != details.id {
				return sessionpresentation.ErrStaleIntent.Error()
			}
			v.closeChildDetails()
			return ""
		}
		if v.shell.Dialog == nil || v.shell.Dialog.ID != *operation.CloseDetails {
			return sessionpresentation.ErrStaleIntent.Error()
		}
		v.files.closeOutput(v.shell)
		v.detailDemand = nil
	case operation.DetailOutput != nil:
		if details := v.childDetails; details != nil {
			event, err := details.observation.Snapshot()
			if err != nil || event.Frame.Details == nil || operation.DetailOutput.ID != details.id {
				return sessionpresentation.ErrStaleIntent.Error()
			}
			request := *operation.DetailOutput
			request.ID = event.Frame.Details.View.ID
			if err := details.observation.Read(sessionapi.NativeOperation{DetailOutput: &request}); err != nil {
				return err.Error()
			}
			return ""
		}
		if _, ok := v.files.details.DetailOutput(*operation.DetailOutput); !ok {
			return sessionpresentation.ErrStaleIntent.Error()
		}
		v.detailDemand = new(*operation.DetailOutput)
	case operation.Receipt != nil:
		receipt := operation.Receipt.Notice
		if v.notices != nil && receipt.Attachment != 0 && receipt.Attachment != v.notices.attachment {
			return sessionpresentation.ErrStaleIntent.Error()
		}
		if !v.effects.acknowledge(operation.Receipt.Effect, v.remoteHuman) {
			return sessionpresentation.ErrStaleIntent.Error()
		}
		if v.notification != nil && v.notification.ID <= operation.Receipt.Effect {
			v.notification = nil
		}
		if v.notices != nil && receipt.Attachment != 0 && receipt.Claim == v.notices.id {
			v.notices.finish(terminalsession.NoticeReceipt{Attachment: receipt.Attachment, Feedback: receipt.Feedback, Composer: receipt.Composer, Content: receipt.Content})
			v.notices = nil
		}
	case operation.Display != nil:
		intent := *operation.Display
		model := v.attached.presentation
		current := model.View()
		switch intent.Thread.ID {
		case current.Main.ID:
			intent.Thread = current.Main
		case current.SideThread.ID:
			intent.Thread = current.SideThread
		default:
			caller := v.attached.session.KnownCaller(intent.Thread.ID)
			if caller == "" {
				return sessionpresentation.ErrStaleIntent.Error()
			}
			intent.Thread.Caller = caller
		}
		if err := model.Display(intent); err != nil {
			return err.Error()
		}
	case operation.Output != nil:
		demand := operation.Output
		if demand.Agent {
			v.agentOutput = v.attached.childSource.ResolveOutputDemands(demand.Demands)
		} else {
			v.mainOutput = v.attached.source.ResolveOutputDemands(demand.Demands)
		}
	default:
		return "This native operation is unavailable."
	}
	return ""
}

func (v *nativeSessionView) complete(input sessionapi.NativeEditorInput, clock func() time.Time, requests *sessioncontrol.Requests[appServerRequest], environment []string) {
	v.completionTarget = input.Target
	if input.Explicit {
		v.picker.Dismissed = completion.Target{}
	}
	current := v.attached.presentation.View()
	v.predictionDraftEmpty = current.Input == current.Main && input.Target.Call == "" && input.Target.Request == "" && input.Draft.Text == ""
	editor := new(composer.Editor)
	editor.Replace(input.Draft)
	v.picker.refreshPicker(&v.attached, v.shell, clock, requests, editor, v.ctx, environment)
	if !slices.Equal(editor.Skills, input.Draft.Skills) {
		skills := slices.Clone(editor.Skills)
		v.effects.add(sessionapi.NativeEffect{Thread: current.Input.ID, Target: new(input.Target), Skills: &skills})
	}
}

// Prediction acquisition is shared by the execution owner, but requires an
// attached, focused Main editor that has opted in. Editors remain client-owned.
func (n *nativeViews) predictionEligibility(execution *attachedPresentation) (focused, disabled, available bool) {
	disabled = true
	for _, v := range n.views {
		if v.ctx.Err() != nil || v.attached.executionPresentation != execution.executionPresentation || v.shell.Main.PredictionsDisabled {
			continue
		}
		disabled = false
		view := v.attached.presentation.View()
		if v.predictionDraftEmpty && view.ReadOnly == sessionpresentation.Editable && view.Displayed == view.Main && view.Input == view.Main {
			available = true
			focused = focused || v.focused
		}
	}
	return focused, disabled, available
}

func (n *nativeViews) publish(execution *attachedPresentation, control *sessioncontrol.Client[appServerRequest], settings preferences.Applied, tiers *inferencegateway.Tiers, title, backend string, restoring bool, side *sessioncontrol.Side, review *sessionreview.Review, reviewSource *changecapture.Service, facts sessionapi.NativeSessionFacts, now time.Time) {
	children := n.descendants(execution, control.Thread)
	for id, v := range n.views {
		if v.ctx.Err() != nil || v.attached.executionPresentation != execution.executionPresentation && id != n.replacing {
			n.retire(id)
			continue
		}
		if !v.initial && now.Sub(v.projected) < apiFrameInterval {
			continue
		}
		if !v.remoteHuman && facts.Diagnostics == "" {
			facts.Diagnostics = n.diagnostics.Snapshot()
		}
		var switched *sessionapi.NativeSwitch
		if !v.initial && v.thread != control.Thread {
			if (!v.startup || v.thread != "") && id != n.replacing || control.Thread == "" {
				n.retire(id)
				continue
			}
			switched = &sessionapi.NativeSwitch{From: v.thread, To: control.Thread, Origin: id}
			if v.thread != "" {
				n.replacePresentation(v, execution)
			}
			v.attached.executionPresentation = execution.executionPresentation
			v.attached.presentation.Attach(sessionpresentation.Thread{ID: control.Thread, Caller: "/root"}, false)
		}
		n.reconcileEffects(v)
		if v.archiveOriginal != "" {
			showRewindArchive(&v.picker, v.archiveOriginal)
			v.archiveOriginal = ""
		}
		for scanning := true; scanning; {
			select {
			case result := <-v.picker.scanResults:
				v.picker.applyScan(v.attached.cwd, v.picker.Target, result)
			default:
				scanning = false
			}
		}
		if v.lastExecutionFeedback != execution.feedback.Revision {
			v.lastExecutionFeedback = execution.feedback.Revision
			v.attached.feedback.Set(execution.feedback.Text, execution.feedback.Alert, now)
		}
		v.attached.feedback.Expire(now)
		if v.initial || switched != nil {
			if layout, err := terminalsession.ReadPreferences(facts.Preferences); err == nil {
				v.shell.Main.InterruptLocked, v.shell.Main.PredictionsDisabled = layout.InterruptLocked, layout.PredictionsDisabled
			}
		}
		v.attached.presentation.BorrowExecutionStatus(execution.presentation)
		view := publishSessionView(&v.attached, control, &v.picker, "", settings, tiers, title, backend, restoring, side, now, true)
		facts.Busy = view.Status.Session.Running || control.Turn != "" || control.Starting()
		frame := sessionapi.NativeFrame{View: view, Facts: facts, CompletionTarget: v.completionTarget, CompletionCatalog: v.picker.skillsRevision,
			Effects:  v.effects.publication(),
			Activity: v.effects.activityPublication(), Notification: v.notification,
			PendingEffects: v.effects.reserved > 0,
			Questions:      nativePendingQuestions(execution.conversation),
			Approvals:      nativePendingApprovals(execution.conversation),
			Main:           v.attached.source.Publication(v.attached.presentation, nil),
			Agents:         v.attached.childSource.Publication(v.attached.presentation, v.attached.source),
			Review:         new(reviewSource.ReviewView(execution.session.Scope())),
			MainOutput:     v.mainOutput, AgentOutput: v.agentOutput, Switch: switched,
		}
		if v.notices != nil {
			frame.Notice = v.notices.id
		}
		if review != nil {
			frame.LiveScope = review.Scope()
			if previews := review.Preview(); previews != nil {
				frame.Live = previews.Current()
			}
		}
		v.publishDetails(&frame)
		n.publishChild(v, &frame, children)
		if err := n.api.Publish(id, frame); err != nil {
			n.retire(id)
			continue
		}
		if switched != nil {
			v.thread, v.startup = switched.To, false
		}
		if id == n.replacing && !restoring && !control.Replacement.Pending() {
			n.replacing = ""
		}
		v.initial, v.projected = false, now
		v.mainOutput, v.agentOutput = nil, nil
	}
}

func (n *nativeViews) rewindArchive(original string) {
	if view := n.views[n.replacing]; view != nil && view.ctx.Err() == nil {
		// Move the completed workflow's chooser request into its originating
		// view. The next publication first binds the confirmed replacement.
		view.archiveOriginal = original
		view.projected = time.Time{}
	}
}

// Confirmed replacement retires only this view's old acquisitions. Effects stay
// with the same client editor lifetime; execution owners are borrowed afresh.
func (n *nativeViews) replacePresentation(v *nativeSessionView, execution *attachedPresentation) {
	v.closeChildDetails()
	n.requests.Retire(viewRequestScope(v.attached.view))
	v.files.closeOutput(v.shell)
	v.picker.cancelScan()
	if v.picker.sideTask != nil {
		v.effects.restore([]conversation.Input{{Delivery: v.picker.sideTaskDelivery}}, sessionapi.NativeEffect{Thread: v.thread, Restore: v.picker.sideTask})
	}
	v.notices.finish(terminalsession.NoticeReceipt{})
	v.notices = nil
	v.scope.Retire()
	v.scope = sessionpresentation.Attachment{}
	v.attached.presentation = n.models.Session(v.scope.Scope())
	v.attached.executionPresentation = execution.executionPresentation
	v.picker = composerPicker{view: v.attached.view, State: n.completions.Value(v.scope.Scope()), MenuState: v.attached.presentation.Menu()}
	v.panel, v.resume, v.detailDemand = nil, nil, nil
	v.mainOutput, v.agentOutput = nil, nil
	v.completionTarget = conversation.EditorTarget{}
	v.lastExecutionFeedback = 0
	n.terminals.ReplaceConversation(v.shell)
}

func (n *nativeViews) invalidateSkills(execution *executionPresentation) {
	for _, view := range n.views {
		if view.ctx.Err() == nil && view.attached.executionPresentation == execution {
			view.picker.invalidateSkills()
			view.projected = time.Time{}
		}
	}
}
