package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/backends/codex/binding"

	"github.com/yusing/mekugi/backends/codex/appserver"
)

// Child event subscriptions do not imply a thread/started notification. Read
// metadata once, without loading history or resuming/executing the child.
func requestThreadMetadata(requests *sessioncontrol.Requests[appServerRequest], session *sessionpresentation.Attachment, thread string) error {
	path := session.KnownCaller(thread)
	known := path != "" && path != sessionpresentation.Placeholder(thread) && session.Agent(path).Role != ""
	return requests.ReadThreadMetadata("main", thread, known, appServerRequest{metadata: thread})
}

func (u *appServerUI) applyThreadMetadata(m appserver.Message, thread string) bool {
	if thread == "" {
		return false
	}
	if m.Error != nil {
		return true
	}
	info, err := binding.DecodeHistoryThread(m.Result, thread)
	if err != nil {
		return true
	} // Metadata cannot block observed activity.
	old := u.attached.session.Caller(thread)
	registerThread(&u.attached.session, info)
	renameThreadActivity(&u.attached, u.shell, old, u.attached.session.KnownCaller(thread))
	u.applyActivity(nil, u.attached.session.Roster())
	return true
}

// Late metadata updates canonical labels before detached terminal delivery.
func renameThreadActivity(attached *attachedPresentation, terminal *terminalsession.Attachment, old, name string) {
	attached.presentation.RenameCaller(old, name)
	for _, source := range []*sessionpresentation.ActivitySource{attached.source, attached.childSource} {
		source.RenameCaller(old, name)
	}
	terminalsession.RenameSelection(old, name, terminal.Transcript, terminal.Agents)
}
