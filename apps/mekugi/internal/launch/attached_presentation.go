package launch

import (
	"context"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/observation"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	noticepresentation "github.com/yusing/mekugi/modules/notifications/presentation"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

// executionPresentation owns one admitted execution attachment. Views borrow this
// actual owner; its feature scopes, transcript and input queues are never copied.
type executionPresentation struct {
	ctx                 context.Context
	hooks               *executionHooks
	inputHistory        *appServerInputHistory
	session             sessionpresentation.Attachment
	source, childSource *sessionpresentation.ActivitySource
	transcript          observation.Transcript
	cwd                 string
	conversation        *conversationmodel.Session
	interruptWait       func(string, string)
	threadInput         *threadInput
	sideInputs          map[*sessioncontrol.Side]*threadInput
	reasoning           *pendingReasoning

	// Progress notice rows belong to the shared Main transcript, not to a view.
	noticeProgress noticepresentation.Progress
}

// executionHooks are bound once before the owner loop starts and survive presentation replacement.
type executionHooks struct {
	compaction      func(thread, item string) func(context.Context) (string, error)
	restoreInput    func(*executionPresentation, []conversationmodel.Input)
	restoreShell    func(*executionPresentation, conversationmodel.Input)
	restoreSide     func(*executionPresentation, conversationmodel.Input, string)
	deliverActivity func(*executionPresentation, []sessionpresentation.ActivityDelivery)
	activeThread    func(string) bool
	reserveThread   func(string) error
	nativeImages    func(func(string))
	modelsReady     func([]appServerModel, preferences.Applied, *inferencegateway.Tiers, string)
}

func (hooks *executionHooks) bind(native *nativeViews, api *sessionapi.Server, compaction func(string, string) func(context.Context) (string, error)) {
	hooks.compaction = compaction
	hooks.restoreInput, hooks.restoreShell, hooks.restoreSide = native.restore, native.restoreShell, native.restoreSide
	hooks.deliverActivity = native.deliverActivity
	hooks.activeThread, hooks.reserveThread = api.ActiveThread, api.ReserveThread
	hooks.nativeImages, hooks.modelsReady = native.imageClaims, native.refreshSettings
}

// attachedPresentation combines a borrowed execution attachment with one view's
// scoped presentation and feedback. Retiring a view does not retire execution.
type attachedPresentation struct {
	*executionPresentation
	feedback     noticepresentation.Model
	presentation *sessionpresentation.Model
	view         string
	effects      *nativeViewEffects
	grants       sessioncontrol.ApprovalGrants
}

func newAttachedPresentation(ctx context.Context, views *sessionpresentation.Service, conversation *conversationmodel.Service, observations *binding.Observation, hooks *executionHooks) attachedPresentation {
	a := attachedPresentation{executionPresentation: new(executionPresentation)}
	a.ctx, a.hooks = ctx, hooks
	store := a.session.Scope()
	a.presentation = views.Session(store)
	a.conversation = conversation.Session(store)
	a.transcript = observations.NewTranscript(store)
	state := a.session.Activity()
	a.source = &sessionpresentation.ActivitySource{Model: &state.Main, Audience: sessionpresentation.MainAudience, Coverage: "CONNECTING"}
	a.childSource = &sessionpresentation.ActivitySource{Model: &state.Children, Audience: sessionpresentation.ChildAudience}
	return a
}

// startAttachedSession admits the host's root identity without replacing the
// session scope, its parked input or its borrowed models.
// Source: apps/mekugi/internal/router/app_server_events.go:23:34@bfd2668c.
func startAttachedSession(a *attachedPresentation, observations *binding.Observation, thread, workspace string) {
	a.session.Start(thread)
	a.session.Activity().StartRoster(sessionpresentation.ActivityAgent{Thread: thread, Name: "/root", Role: "main"})
	a.transcript = observations.NewTranscript(a.session.Scope())
	clear(a.session.Commands())
	a.cwd = workspace
}

func (a *attachedPresentation) bindRepetitions(audience *sessioncontrol.Audience, root string) {
	lookup := func(thread, id string) string { return audience.Repetition(root, thread, id) }
	a.source.Repetition, a.childSource.Repetition = lookup, lookup
	prepared := func(thread, id string) (activitypresentation.PreparedContent, bool) {
		return audience.Prepared(root, thread, id)
	}
	a.source.Prepared, a.childSource.Prepared = prepared, prepared
}
