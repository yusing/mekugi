package sessionpresentation

import (
	"fmt"
	"slices"
	"time"

	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
)

type SideToken struct {
	ID    uint64
	Label string
}

type CloseSideIntent struct {
	Version  Version
	Exchange uint64
}

func (m *Model) AdmitCloseSide(in CloseSideIntent) error {
	if in.Version != m.view.Version || m.view.Side == nil || in.Exchange != m.view.Side.ID {
		return ErrStaleIntent
	}
	if m.replay {
		return ErrReplayIntent
	}
	return nil
}

// SetSide updates the one invocation-local editable side destination.
func (m *Model) SetSide(thread Thread) {
	previous := m.view.SideThread
	if previous == thread {
		return
	}
	selected := m.DisplayingSide()
	m.view.SideThread = thread
	if selected {
		m.view.Displayed, m.view.Input = thread, thread
		if thread.ID == "" {
			m.view.Displayed, m.view.Input = m.view.Main, m.view.Main
		}
	}
	m.view.Version.Revision++
}
func (m *Model) DisplayingSide() bool {
	return m.view.SideThread.ID != "" && m.view.Displayed == m.view.SideThread
}

// InputConversation selects a present editor owner, independently of Main control.
func (m *Model) InputConversation(main *conversationmodel.Session) *conversationmodel.Session {
	if m.DisplayingSide() && main.Side != nil {
		return &main.Side.Drafts
	}
	return main
}

// ProjectSide joins the bounded side conversation into the existing transcript
// source. It carries no independent history or host lifecycle.
func (a *Attachment) ProjectSide(source *ActivitySource, target Thread, side *conversationmodel.SideAnswer) {
	var entries []ActivityEntry
	if side != nil || slices.ContainsFunc(source.Agents, func(agent ActivityAgent) bool { return agent.Isolated }) {
		source.Agents = a.Roster()
	}
	if side != nil {
		appendText := func(exchange uint64, item, kind, text string, at time.Time) {
			if text == "" {
				return
			}
			entries = append(entries, ActivityEntry{Side: true, Agent: target.Caller, Kind: kind, Text: text, Observed: at, Native: &NativeItem{Thread: target.ID, Turn: fmt.Sprint(exchange), Item: item, Phase: "item/completed", Live: true}})
		}
		exchange := func(id uint64, question, notice, failure string, answers []conversationmodel.Answer, at time.Time) {
			appendText(id, "question", "user", question, at)
			appendText(id, "attachments", "text", notice, at)
			if side.OneOff {
				answers = nil
			}
			for i, answer := range answers {
				appendText(id, fmt.Sprintf("answer:%d:%s", i, answer.ID), "text", answer.Text, at)
			}
			appendText(id, "failure", "text", failure, at)
		}
		for _, h := range side.History {
			exchange(h.ID, h.Question, h.AttachmentNotice, h.Failure, h.Answer, time.Time{})
		}
		exchange(side.ID, side.Question, side.AttachmentNotice, "", side.Answer, side.Started)
		if side.Truncated {
			appendText(0, "truncation", "text", "Earlier side content omitted (display limit).", time.Time{})
		}
	}
	desired := make(map[[3]string]int, len(entries))
	for i, entry := range entries {
		desired[[3]string{entry.Native.Thread, entry.Native.Turn, entry.Native.Item}] = i
	}
	for i := len(source.Entries) - 1; i >= 0; i-- {
		old := source.Entries[i].Source
		if !old.Side {
			continue
		}
		index, keep := desired[[3]string{old.Native.Thread, old.Native.Turn, old.Native.Item}]
		if !keep {
			source.Model.Remove(i, i+1)
			continue
		}
		entry := &entries[index]
		entry.Seq, entry.Observed = old.Seq, old.Observed
		if old.Text != entry.Text {
			source.Model.Replace(i, *entry, ActivityContent(*entry).Values())
		}
	}
	for _, entry := range entries {
		if entry.Seq != 0 {
			continue
		}
		entry.Seq = a.Activity().NextSequence()
		source.Apply(ActivityEvent{Entries: []ActivityEntry{entry}})
	}
}
