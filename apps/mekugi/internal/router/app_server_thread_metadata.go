package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/backends/codex/binding"

	"github.com/yusing/mekugi/backends/codex/appserver"
)

// Child event subscriptions do not imply a thread/started notification. Read
// metadata once, without loading history or resuming/executing the child.
func (u *appServerUI) requestThreadMetadata(thread string) error {
	path := u.session.KnownCaller(thread)
	known := path != "" && path != sessionpresentation.Placeholder(thread) && u.session.Agent(path).Role != ""
	return u.Requests.ReadThreadMetadata("main", thread, known, appServerRequest{metadata: thread})
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
	old := u.session.Caller(thread)
	u.registerThread(info)
	u.renameThreadActivity(old, u.session.KnownCaller(thread))
	u.applyActivity(nil, u.session.Roster())
	return true
}

// Late metadata updates canonical labels before detached terminal delivery.
func (u *appServerUI) renameThreadActivity(old, name string) {
	u.sessionPresentation().RenameCaller(old, name)
	for _, source := range []*sessionpresentation.ActivitySource{u.source, u.childSource} {
		source.RenameCaller(old, name)
	}
	terminalsession.RenameSelection(old, name, u.view, u.agents)
}
