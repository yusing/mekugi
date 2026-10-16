package router

import (
	json "encoding/json/v2"
	"slices"

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
		var result struct {
			Thread appServerThreadInfo `json:"thread"`
		}
		if m.Error != nil || json.Unmarshal(m.Result, &result) != nil || result.Thread.ID != thread {
			return true // Keep the observed identity; metadata cannot block activity.
		}
		old := u.session.path(thread)
		u.session.registerThread(result.Thread)
		u.renameThreadActivity(old, u.session.paths[thread])
		u.applyActivity(nil, slices.Clone(u.session.agents))
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
	for _, view := range []*activityAdapter{u.view, u.agents} {
		for i := range view.Entries {
			view.mutateEntry(i, func(entry *liveActivityRecord) {
				if entry.Source.Agent == old {
					entry.Source.Agent = name
				}
				if entry.Source.assignment != nil {
					if entry.Source.assignment.from == old {
						entry.Source.assignment.from = name
					}
					if entry.Source.assignment.to == old {
						entry.Source.assignment.to = name
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
		view.RenameRetiredSkills(old, name)
	}
	for thread, entry := range u.session.messages {
		if entry.Agent == old {
			entry.Agent = name
			u.session.messages[thread] = entry
		}
	}
}
