package sessionapi

import (
	"slices"
	"strings"

	activity "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/conversation/prompt"
)

const secretPromptText = "Secret prompt (answer in the terminal)."

// publicValues is shared by both public projections. It copies only modified
// slices and strips repeated secret content at every original/group boundary.
func publicValues(values []activity.Value) ([]activity.Value, bool) {
	var out []activity.Value
	for i, v := range values {
		var members, requested bool
		v.Members, members = publicValues(v.Members)
		v.RequestedOperations, requested = publicValues(v.RequestedOperations)
		own := slices.ContainsFunc(v.Questions, func(q activity.Question) bool { return q.Secret })
		if own {
			v.Questions = slices.Clone(v.Questions)
			for j, q := range v.Questions {
				if q.Secret {
					v.Questions[j] = activity.Question{Text: secretPromptText, Secret: true}
				}
			}
		}
		if own || members || requested {
			v.Body, v.Code, v.Repeated, v.ReplySummary = "", "", "", ""
			v.Tail = nil
			if out == nil {
				out = slices.Clone(values)
			}
			out[i] = v
		}
	}
	if out == nil {
		return values, false
	}
	return out, true
}

func publicEntry(e activity.Entry) activity.Entry {
	var secret bool
	e.Values, secret = publicValues(e.Values)
	if secret {
		// Question records repeat prompt bodies after their owner-built label.
		e.Text, _, _ = strings.Cut(e.Text, "\n")
		e.Command = nil
	}
	return e
}

func publicEntries(entries []activity.Entry) []activity.Entry {
	out := slices.Clone(entries)
	for i := range out {
		out[i] = publicEntry(out[i])
		out[i].Images = nil // Raw sources stay on the local native transport.
	}
	return out
}

func publicActivity(p *activity.Publication) *activity.Publication {
	if p == nil {
		return nil
	}
	out := *p
	out.Entries, out.MainSummary = publicEntries(p.Entries), publicEntries(p.MainSummary)
	out.Roster = slices.Clone(p.Roster)
	for i := range out.Roster {
		out.Roster[i].Agent.Summary = publicEntries(out.Roster[i].Agent.Summary)
	}
	return &out
}

func publicCall(call prompt.CallView) prompt.CallView {
	call.Questions = slices.Clone(call.Questions)
	for i, q := range call.Questions {
		if q.Secret {
			call.Questions[i] = prompt.QuestionView{ID: q.ID, Pending: q.Pending, Secret: true}
		}
	}
	return call
}

func (f NativeFrame) remote() NativeFrame {
	f.Facts = NativeSessionFacts{Usage: f.Facts.Usage, Archived: f.Facts.Archived, ActionRequired: f.Facts.ActionRequired, Busy: f.Facts.Busy}
	f.Effects = slices.Clone(f.Effects)
	for i := range f.Effects {
		effect := &f.Effects[i]
		if draft := effect.RecoveryDraft(); draft != nil {
			preview := strings.ToValidUTF8(draft.Text[:min(len(draft.Text), 512)], "")
			if len(draft.Text) > 512 {
				preview += "…"
			}
			editing := effect.Recovery != nil && effect.Recovery.Editing
			queued := effect.Recovery != nil && effect.Recovery.Queued
			effect.Recovery = &NativeRecoveryView{Editing: editing, Queued: queued, Preview: preview, Shell: effect.ShellRestore != nil,
				Attachments: len(draft.Files)+len(draft.Images)+len(draft.Skills)+len(draft.Selections)+len(draft.Attachments)+len(draft.AnswerImages) > 0}
			if editing {
				effect.Recovery.Parts = draft.TextParts()
			}
			effect.Restore, effect.ShellRestore = nil, nil
		}
	}
	f.Questions = slices.Clone(f.Questions)
	for i := range f.Questions {
		f.Questions[i] = publicCall(f.Questions[i])
	}
	if call := f.View.Questions.Call; call != nil {
		f.View.Questions.Call = new(publicCall(*call))
	}
	// Every revision repeats the complete frame. Remote views take transcripts
	// from session/subscribe, so terminal renderer state stays native: only a
	// displayed side conversation, which has no shared publication, keeps Main.
	var side *activity.Publication
	if f.View.SideThread.ID != "" && f.View.Displayed.ID == f.View.SideThread.ID {
		side = publicActivity(f.Main)
	}
	f.Main, f.Agents, f.Review, f.Live, f.FileLinks, f.Activity = side, nil, nil, nil, nil, nil
	if f.Family != nil {
		f.Family = new(*f.Family)
		f.Family.Agents = nil
	}
	f.MainOutput, f.AgentOutput, f.ReacquireOutput = nil, nil, false
	if f.Details != nil {
		details := *f.Details
		details.View.Pages = slices.Clone(details.View.Pages)
		for i := range details.View.Pages {
			page := &details.View.Pages[i]
			values, secret := publicValues([]activity.Value{page.Value})
			page.Value = values[0]
			if secret {
				page.Prepared = nil
				if details.Output != nil && details.Output.Page == page.ID {
					details.Output = nil
				}
			}
		}
		f.Details = &details
	}
	return f
}
