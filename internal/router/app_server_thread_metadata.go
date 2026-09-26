package router

import (
	json "encoding/json/v2"
	"slices"
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
	id, err := u.client.send("thread/read", map[string]any{"threadId": thread, "includeTurns": false}, true)
	if err != nil {
		return err
	}
	u.requests[id] = "thread/read"
	u.session.metadata[thread] = id
	return nil
}

func (u *appServerUI) applyThreadMetadata(m appServerMessage) bool {
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
	if old == name {
		return
	}
	for _, view := range []*liveActivityView{u.view, u.agents} {
		for i := range view.entries {
			entry := &view.entries[i]
			if entry.Agent == old {
				entry.Agent = name
			}
			if entry.assignment != nil {
				if entry.assignment.from == old {
					entry.assignment.from = name
				}
				if entry.assignment.to == old {
					entry.assignment.to = name
				}
			}
			// Preserve merged exits, filters and answer links while renaming
			// only the display identities in already-parsed blocks.
			for j := range view.blocks[i] {
				block := &view.blocks[i][j]
				if block.from == old {
					block.from = name
				}
				if block.to == old {
					block.to = name
				}
				if block.owner == old {
					block.owner = name
				}
			}
		}
		if view.selected == old {
			view.selected = name
		}
		if view.rosterSelected == old {
			view.rosterSelected = name
		}
		view.runs = nil
	}
	for thread, entry := range u.session.messages {
		if entry.Agent == old {
			entry.Agent = name
			u.session.messages[thread] = entry
		}
	}
}
