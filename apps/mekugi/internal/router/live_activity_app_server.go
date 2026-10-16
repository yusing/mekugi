package router

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"slices"
	"strings"
	"time"

	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"

	"github.com/yusing/mekugi/backends/codex/binding"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	activitymodel "github.com/yusing/mekugi/modules/activity/feed"

	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func questionReplyText(replies []binding.QuestionReply) string {
	answers := make([]string, 0, len(replies))
	for _, reply := range replies {
		answers = append(answers, reply.Answer)
	}
	return strings.Join(answers, "\n")
}

type liveActivityNativeItem struct {
	commandStarted     time.Time // UI receipt of host start notification; not reconstructed on replay.
	commandEnded       time.Time
	duration           time.Duration
	questions          []activityui.Question
	replySource        string // Stock async envelope or synchronous turn prompt.
	thread, turn, item string
	phase              string
	wait               *activityui.Block // Structured wait progress is roster-only.
	command, status    string
	exit               *int   // Observed exit presence is independent of retained output.
	commandCwd         string // Metadata-owned directory for classified paths and receipt refreshes.
	commandTimeout     string // Explicit timeout from available invocation input.
	approval           string // UI decision, independent of the host execution result.
	workdir            string // Display form of a command's directory outside the shown workspace.
	searchResults      *int
	running            bool                             // Started live and not yet completed; replay never sets it.
	live               bool                             // From a live notification; restored history never sets it.
	settled            time.Time                        // Successful output stays open from then until its agent's next event.
	changes            []activitypresentation.ChangeRow // Change rows read from a successful change history, VCS read, or commit.
	commit             gitCommitKey                     // The commit object whose files complete changes.
	collapsed          bool                             // Settled output or a restored block.
	spans              []activityui.TextSpan            // Attachment spans, not text resembling image labels.
	question           uint64                           // Original user entry.
	thought            time.Duration                    // Reasoning time from its start to completion.
	replaces           string                           // Pending thinking block this reasoning item takes over.
	attachments        []activityui.Block               // Submitted file snapshot outcomes, recovered from host history.
	capturedEdit       *capturedActivityEdit            // Exact invocation receipt reconciled by the live diff owner.
	editPages          []activityui.Block               // Completed host file changes, available before outer-cell capture.
	segments           []commandSegment                 // A tracked command's own segments, replacing its single row.
	output             *activitymodel.Output            // A command's retained host output, which the output dialog reads.
	hook               *activityui.HookDetails
}

func (n *liveActivityNativeItem) sameItem(other *liveActivityNativeItem) bool {
	if n.hook != nil || other != nil && other.hook != nil {
		return n.hook != nil && other != nil && other.hook != nil && n.thread == other.thread && n.item == other.item
	}
	return other != nil && n.thread == other.thread && n.turn == other.turn && n.item == other.item
}

// unconfirmed reports a settled host item that did not complete, such as a
// declined or failed command: it can end without an exit code, so its rows
// alone read as successful.
func (n *liveActivityNativeItem) unconfirmed() bool {
	return n != nil && !n.running && n.status != "" && n.status != "completed"
}

func (v *liveActivityView) entrySeq(entry activityPaneEntry) uint64 {
	for _, current := range v.Entries {
		if entry.native != nil && entry.native.sameItem(current.Source.native) || entry.native == nil && current.Source.Seq == entry.Seq {
			return current.Source.Seq
		}
	}
	return 0
}

func (v *liveActivityView) applyAppServerItem(live bool, cwd, main, thread, turn, id, method, delta string, item appServerItem) {
	if item.Delivery == "async" && len(item.Questions) > 0 {
		return
	}
	if thread == "" || turn == "" || id == "" {
		return
	}
	entry := activityPaneEntry{Seq: v.LastSeq + 1, Agent: "Main", Kind: "text", Text: item.Text, Observed: v.now(),
		native: &liveActivityNativeItem{thread: thread, turn: turn, item: id, phase: method, live: live, command: item.Command, status: item.Status, exit: item.ExitCode, duration: appServerDuration(item)}}
	if thread != main {
		entry.Agent = "Thread " + thread
	}
	if text, wait, handled := appServerProgress(item, method); handled {
		if text != "" {
			entry.Kind, entry.Text = "progress", text
			entry.native.wait = wait
			v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{entry}})
		}
		return
	}
	if method == "item/agentMessage/delta" {
		entry.Text = delta
	} else {
		switch item.Type {
		case "reasoning":
			entry.Kind, entry.CallID, entry.Text = "reasoning", id, strings.Join(item.Summary, "\n\n")
			if strings.TrimSpace(entry.Text) == "" {
				return
			}
		case "agentMessage":
		case "userMessage":
			var ok bool
			if entry.Text, entry.native.spans, ok = appServerUserText(item.Content); !ok {
				return
			}
			if replies := binding.QuestionReplies(entry.Text); len(replies) > 0 {
				// Keep a chronological reply anchor without duplicating the
				// answer already displayed under Asked.
				entry.Kind = "question_reply"
				entry.native.replySource = entry.Text
				for _, reply := range replies {
					questionID, _, ok := binding.QuestionReplyIdentity(reply)
					if ok && entry.native.question == 0 {
						for _, question := range v.Entries {
							if question.Source.Kind == "question" && question.Source.native != nil && question.Source.native.thread == thread && question.Source.native.item == questionID {
								entry.native.question = question.Source.Seq
								break
							}
						}
					}
				}
				entry.Text = questionReplyText(replies)
				v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{entry}})
				return
			}
			if thread == main {
				entry.Agent = "You"
			} else {
				entry.Agent += " · user"
			}
		case "commandExecution":
			entry.Kind = "command"
		default:
			return // In particular, never render raw/encrypted reasoning.
		}
	}
	v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{entry}})
	if item.Type == "userMessage" {
		workspace := ""
		if thread == main {
			workspace = cwd
		}
		if blocks := appServerAttachmentBlocks(workspace, item.Content); len(blocks) > 0 {
			if thread == main {
				entry.Agent = "Main"
			} else {
				entry.Agent = "Thread " + thread
			}
			entry.Seq, entry.Kind, entry.Text = v.LastSeq+1, "attachments", ""
			entry.native = &liveActivityNativeItem{thread: thread, turn: turn, item: id + "/attachments", phase: method, live: live, attachments: blocks}
			v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{entry}})
		}
	}
}

// appServerUserText renders userMessage content as the composer wrote it,
// with each image as its "[Image N]" label.
func appServerUserText(content jsontext.Value) (string, []activityui.TextSpan, bool) {
	var contents []composerUserContent
	if len(content) > 0 && json.Unmarshal(content, &contents) != nil {
		return "", nil, false
	}
	var text string
	var spans []activityui.TextSpan
	imageCount := 0
	for _, content := range contents {
		if content.Type == "text" {
			if _, attached := attachments.Decode(content.Text); attached {
				continue
			}
			for _, span := range composerElementSpans(content.Text, content.Elements) {
				span.Start += len(text)
				span.End += len(text)
				spans = append(spans, span)
			}
			text += content.Text
		} else if content.Type == "image" || content.Type == "localImage" {
			if len(binding.QuestionReplies(text)) > 0 {
				continue // Reply images belong to Asked, not another user-message band.
			}
			start := len(text)
			imageCount++
			text += fmt.Sprintf("[Image %d]", imageCount)
			spans = append(spans, activityui.TextSpan{Start: start, End: len(text), Kind: activityui.ImageToken})
		}
	}
	draft := conversationmodel.Input{Text: text}
	draft.Skills = unambiguousSkillBindings(text, composerContentSkills(contents), spans)
	for _, span := range draftDisplaySpans(draft.Draft) {
		if !slices.ContainsFunc(spans, func(existing activityui.TextSpan) bool {
			return span.Start < existing.End && span.End > existing.Start
		}) {
			spans = append(spans, span)
		}
	}
	slices.SortFunc(spans, func(a, b activityui.TextSpan) int { return a.Start - b.Start })
	return text, spans, true
}

// mergeNative updates the owning activity entry in place. Completed snapshots
// supersede fragments; duplicate starts and late deltas cannot reopen them.
func (v *liveActivityView) mergeNative(entry activityPaneEntry) bool {
	if entry.native == nil {
		return false
	}
	for i, previous := range v.Entries {
		if previous.Source.native != nil && previous.Source.native.phase == "input/pending" && entry.Agent == "You" && entry.native.thread == previous.Source.native.thread && entry.Text == previous.Source.Text {
			// Reconcile the local echo with Codex's authoritative user item.
			entry.Seq, entry.Observed = previous.Source.Seq, previous.Source.Observed
			v.replaceEntry(i, entry, parseLiveActivity(entry))
			return true
		}
		if !entry.native.sameItem(previous.Source.native) {
			continue
		}
		if entry.native.phase == "approval/updated" {
			native := *previous.Source.native
			native.approval = entry.native.approval
			current := previous.Source
			current.native = &native
			blocks := v.blocks(i)
			if native.command != "" && len(blocks) > 1 && !slices.ContainsFunc(blocks, func(b activityui.Block) bool { return b.BatchExit }) {
				blocks = parseLiveActivity(current)
			} else {
				setApprovalBlocks(blocks, native.approval)
			}
			v.replaceEntry(i, current, blocks)
			return true
		}
		entry.native.approval = previous.Source.native.approval
		if previous.Source.native.capturedEdit != nil {
			entry.native.capturedEdit = previous.Source.native.capturedEdit
			entry.Text = previous.Source.Text
		}
		if previous.Source.native.phase == "item/completed" || entry.native.phase == "item/started" && previous.Source.native.phase != "approval/updated" && previous.Source.native.phase != "hook/restored" || previous.Source.native.phase == "turn/completed" && entry.native.phase != "item/completed" {
			return true
		}
		if entry.native.phase == "item/agentMessage/delta" {
			entry.Text = previous.Source.Text + entry.Text
		}
		entry.Seq, entry.Observed = previous.Source.Seq, previous.Source.Observed
		entry.native.question = previous.Source.native.question
		blocks := parseLiveActivity(entry)
		for _, annotation := range v.blocks(i) {
			if annotation.Repeated != "" && len(blocks) > 0 {
				blocks[len(blocks)-1].Repeated = annotation.Repeated
			}
			if annotation.Kind == "filter" {
				blocks = append(blocks, annotation)
			}
		}
		v.replaceEntry(i, entry, blocks)
		return true
	}
	return false
}

func (v *liveActivityView) removePendingInput(seq uint64) {
	for i, entry := range v.Entries {
		if entry.Source.Seq == seq && entry.Source.native != nil && entry.Source.native.phase == "input/pending" {
			v.removeEntries(i, i+1)
			return
		}
	}
}
