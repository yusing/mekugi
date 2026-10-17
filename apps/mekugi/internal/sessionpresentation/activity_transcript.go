package sessionpresentation

import (
	"encoding/json/jsontext"

	"github.com/yusing/mekugi/contracts/observation"
	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"
)

// Source: apps/mekugi/internal/router/app_server_transcript.go:36:43@45fbe279
func TranscriptEntry(update observation.TextUpdate, agent string) ActivityEntry {
	call := ""
	if update.Kind == "reasoning" {
		call = update.Ref.ItemID
	}
	return ActivityEntry{Seq: update.Sequence, Agent: agent, Kind: update.Kind, Text: update.Text, CallID: call, Observed: update.Observed, Completed: update.Completed,
		Native: &NativeItem{Thread: update.Ref.ThreadID, Turn: update.Ref.TurnID, Item: update.Ref.ItemID, Phase: update.Phase, Status: update.Status, Live: update.Live, Thought: update.Thought, Replaces: update.Replaces}}
}

// Source: apps/mekugi/internal/router/app_server_transcript.go:63:117@45fbe279
// ApplyTranscript enriches an existing feed with Conversation's user/span and
// attachment interpretation. It owns no host lifecycle or message accumulation.
func (v *ActivitySource) ApplyTranscript(update observation.TextUpdate, content jsontext.Value, cwd, main string, user bool) {
	ref := update.Ref
	if ref.ThreadID == "" || ref.TurnID == "" || ref.ItemID == "" {
		return
	}
	agent := "Main"
	if ref.ThreadID != main {
		agent = "Thread " + ref.ThreadID
	}
	entry := TranscriptEntry(update, agent)
	entry.Seq = v.LastSeq + 1
	if user {
		var ok bool
		if entry.Text, entry.Native.Spans, ok = UserText(content); !ok {
			return
		}
		if answer, replies, replied := conversationbinding.ReplyText(entry.Text); replied {
			entry.Kind, entry.Native.ReplySource = "question_reply", entry.Text
			for _, reply := range replies {
				questionID := reply.Item
				if entry.Native.Question == 0 {
					for _, question := range v.Entries {
						if question.Source.Kind == "question" && question.Source.Native != nil && question.Source.Native.Thread == ref.ThreadID && question.Source.Native.Item == questionID {
							entry.Native.Question = question.Source.Seq
							break
						}
					}
				}
			}
			entry.Text = answer
			v.Apply(ActivityEvent{Kind: "entries", Entries: []ActivityEntry{entry}})
			return
		}
		if ref.ThreadID == main {
			entry.Agent = "You"
		} else {
			entry.Agent += " · user"
		}
	}
	v.Apply(ActivityEvent{Kind: "entries", Entries: []ActivityEntry{entry}})
	if user {
		workspace := ""
		if ref.ThreadID == main {
			workspace = cwd
		}
		if blocks := AttachmentValues(workspace, content); len(blocks) > 0 {
			entry.Agent = agent
			entry.Seq, entry.Kind, entry.Text = v.LastSeq+1, "attachments", ""
			entry.Native = &NativeItem{Thread: ref.ThreadID, Turn: ref.TurnID, Item: ref.ItemID + "/attachments", Phase: update.Phase, Live: update.Live, Attachments: blocks}
			v.Apply(ActivityEvent{Kind: "entries", Entries: []ActivityEntry{entry}})
		}
	}
}
