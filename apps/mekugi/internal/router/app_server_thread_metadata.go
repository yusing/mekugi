package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/binding"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"

	"github.com/yusing/mekugi/backends/codex/appserver"
)

// Child event subscriptions do not imply a thread/started notification. Read
// metadata once, without loading history or resuming/executing the child.
func (u *appServerUI) requestThreadMetadata(thread string) error {
	if _, requested := u.session.metadata[thread]; requested {
		return nil
	}
	if path := u.session.paths[thread]; path != "" && path != appServerPlaceholder(thread) && u.session.agent(path).Role != "" {
		u.session.metadata[thread] = ""
		return nil // thread/started or restored history already supplied it.
	}
	id, err := u.requests.Send("thread/read", map[string]any{"threadId": thread, "includeTurns": false}, "main", appServerRequest{})
	if err != nil {
		return err
	}
	u.session.metadata[thread] = id
	return nil
}

func (u *appServerUI) applyThreadMetadata(m appserver.Message) bool {
	for thread, id := range u.session.metadata {
		if id == "" || id != string(m.ID) {
			continue
		}
		u.session.metadata[thread] = ""
		if m.Error != nil {
			return true
		}
		info, err := binding.DecodeHistoryThread(m.Result, thread)
		if err != nil {
			return true
		} // Metadata cannot block observed activity.

		old := u.session.path(thread)
		u.session.registerThread(info)
		u.renameThreadActivity(old, u.session.paths[thread])
		u.applyActivity(nil, u.session.roster())
		return true
	}
	return false
}

// Late metadata changes the display name, not the identity of retained items.
func (u *appServerUI) renameThreadActivity(old, name string) {
	u.sessionPresentation().RenameCaller(old, name)
	if old == name {
		return
	}
	for i, source := range []*sessionpresentation.ActivitySource{u.source, u.childSource} {
		view := []*activityui.View{u.view, u.agents}[i]
		for i := range source.Entries {
			source.Model.Mutate(i, func(entry *sessionpresentation.ActivityRecord) {
				if entry.Source.Agent == old {
					entry.Source.Agent = name
				}
				if entry.Source.Assignment != nil {
					if entry.Source.Assignment.From == old {
						entry.Source.Assignment.From = name
					}
					if entry.Source.Assignment.To == old {
						entry.Source.Assignment.To = name
					}
				}
				// Preserve merged exits, filters and answer links while renaming
				// only the display identities in already-parsed blocks.
				for j := range entry.Values {
					block := &entry.Values[j]
					if block.From == old {
						block.From = name
					}
					if block.To == old {
						block.To = name
					}
					if block.Owner == old {
						block.Owner = name
					}
				}
			})
		}
		if view.Selected == old {
			view.Selected = name
		}
		if view.RosterSelected == old {
			view.RosterSelected = name
		}
		view.Runs = nil
		source.RenameRetiredSkills(old, name)
	}
}
