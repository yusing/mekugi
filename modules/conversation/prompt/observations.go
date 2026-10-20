package prompt

import (
	"iter"

	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

type Reply struct {
	Item, Answer string
	Index        int
}

// Message contains decoded question facts, not host request or secret handles.
// HasReplies preserves reply-envelope identity even when no reply target decodes.
type Message struct {
	Ref                 observation.ItemRef
	Kind                observation.Kind
	Main, User, History bool
	Call                *Call
	ClientID            string
	HasReplies          bool
	Replies             []Reply
}

type CallChange struct {
	Call       *Call
	Discarded  []composer.Draft
	Superseded bool
}

type MessageChanges struct {
	Added    *Call
	Changed  []CallChange
	Suppress bool
}

type inputAction struct {
	call  *Call
	reply *Reply // nil supersedes the call.
}

// inputActions is shared by preflight and application, so parking selects exactly
// the same unresolved call that the subsequent serialized transition will change.
func (m *Questions) inputActions(in Message) iter.Seq[inputAction] {
	return func(yield func(inputAction) bool) {
		if !in.Main || !in.User || in.Kind != observation.ItemCompleted {
			return
		}
		if !in.HasReplies {
			if m.inputTurns[in.Ref.TurnID] {
				return
			}
			for _, c := range m.Calls {
				if c.Thread == in.Ref.ThreadID && !c.Sync && !c.Local && !c.Resolved && c.Turn != in.Ref.TurnID && (!c.Sent || c.SubmissionID == "") {
					if !yield(inputAction{call: c}) {
						return
					}
				}
			}
			return
		}
		for _, r := range in.Replies {
			for _, c := range m.Calls {
				if c.Thread == in.Ref.ThreadID && c.Item == r.Item && !c.Sync && !c.Local && r.Index >= 0 && r.Index < len(c.Questions) && c.Questions[r.Index].Outcome == "" {
					if !yield(inputAction{call: c, reply: &r}) {
						return
					}
				}
			}
		}
	}
}

// NeedsParking runs before ObserveMessage. The adapter parks an active answer,
// including renderer-local secrets, before commitment can clear its editor.
func (m *Questions) NeedsParking(in Message) bool {
	if m.Active != nil {
		for action := range m.inputActions(in) {
			if action.call == m.Active {
				return true
			}
		}
	}
	return false
}

// ObserveMessage shares question lifecycle and transcript admission across live,
// restored and playback messages. Returned calls identify existing owner state;
// detached views remain the publication boundary.
// Source: apps/mekugi/internal/router/app_server_questions.go:216:223,300:306,477:515
// and app_server_ui.go:935:943@[faf46626].
func (m *Questions) ObserveMessage(in Message) MessageChanges {
	var changes MessageChanges
	for action := range m.inputActions(in) {
		change := CallChange{Call: action.call, Superseded: action.reply == nil}
		if change.Superseded {
			change.Discarded = action.call.Resolve("superseded")
		} else if draft, changed := action.call.Commit(action.reply.Index, in.ClientID, action.reply.Answer); changed {
			change.Discarded = []composer.Draft{draft}
		}
		changes.Changed = append(changes.Changed, change)
	}
	if in.Main && in.User && in.Kind == observation.ItemCompleted {
		m.recordInputTurn(in.Ref.TurnID)
	}
	if in.Call != nil {
		changes.Suppress = true
		if in.Main && m.Add(in.Call, in.History) {
			changes.Added = in.Call
		}
	}
	if in.Kind == observation.AgentDelta {
		for _, c := range m.Calls {
			if c.Thread == in.Ref.ThreadID && c.Item == in.Ref.ItemID && !c.Sync {
				changes.Suppress = true
				break
			}
		}
	}
	return changes
}
