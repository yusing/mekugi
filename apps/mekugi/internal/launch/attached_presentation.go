package launch

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/observation"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	noticepresentation "github.com/yusing/mekugi/modules/notifications/presentation"
)

// attachedPresentation borrows scoped feature models independently of the live
// connection, terminal, invocation services and asynchronous resource lifetimes.
type attachedPresentation struct {
	session             sessionpresentation.Attachment
	source, childSource *sessionpresentation.ActivitySource
	transcript          observation.Transcript
	waits               activitypresentation.Waits
	feedback            noticepresentation.Model
	cwd                 string
	presentation        *sessionpresentation.Model
	conversation        *conversationmodel.Session
}

func newAttachedPresentation(views *sessionpresentation.Service, conversation *conversationmodel.Service, observations *binding.Observation) attachedPresentation {
	var a attachedPresentation
	store := a.session.Scope()
	a.presentation = views.Session(store)
	a.conversation = conversation.Session(store)
	a.transcript = observations.NewTranscript(store)
	a.bindSources()
	return a
}

// bindSources adopts any admitted initial feed into the attachment's one model.
// Source: apps/mekugi/internal/router/live_activity_record.go:13:42@bfd2668c.
func (a *attachedPresentation) bindSources() {
	if a.source == nil {
		a.source = sessionpresentation.NewActivitySource(sessionpresentation.MainAudience)
	}
	if a.childSource == nil {
		a.childSource = sessionpresentation.NewActivitySource(sessionpresentation.ChildAudience)
		a.childSource.Coverage = ""
	}
	state := a.session.Activity()
	if a.source.Model != &state.Main {
		state.Main = *a.source.Model
		a.source.Model = &state.Main
	}
	if a.childSource.Model != &state.Children {
		state.Children = *a.childSource.Model
		a.childSource.Model = &state.Children
	}
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
