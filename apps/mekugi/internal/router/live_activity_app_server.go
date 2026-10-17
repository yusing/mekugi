package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"encoding/json/jsontext"


	json "encoding/json/v2"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/backends/codex/binding"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"

	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"

	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
)

func questionReplyText(replies []binding.QuestionReply) string {
	answers := make([]string, 0, len(replies))
	for _, reply := range replies {
		answers = append(answers, reply.Answer)
	}
	return strings.Join(answers, "\n")
}

type liveActivityNativeItem struct {
	invocation *activitypresentation.Invocation // Canonical host state, shared while terminal output settles.
	// Admitted native phase and timing. Terminal holds use detached projections
	// rather than delaying these host facts.
	commandStarted     time.Time // UI receipt of host start notification; not reconstructed on replay.
	commandEnded       time.Time
	duration           time.Duration
	questions          []activitypresentation.Question
	replySource        string // Stock async envelope or synchronous turn prompt.
	thread, turn, item string
	phase              string
	wait               *activitypresentation.Value // Structured wait progress is roster-only.
	command, status    string
	exit               *int   // Observed exit presence is independent of retained output.
	commandCwd         string // Metadata-owned directory for classified paths and receipt refreshes.
	commandTimeout     string // Explicit timeout from available invocation input.
	approval           string // UI decision, independent of the host execution result.
	workdir            string // Display form of a command's directory outside the shown workspace.
	searchResults      *int
	running            bool                             // Started live and not yet completed; replay never sets it.
	live               bool                             // From a live notification; restored history never sets it.
	changes            []activitypresentation.ChangeRow // Change rows read from a successful change history, VCS read, or commit.
	commit             activitypresentation.CommitKey   // The commit object whose files complete changes.
	spans              []activitypresentation.TextSpan  // Attachment spans, not text resembling image labels.
	question           uint64                           // Original user entry.
	thought            time.Duration                    // Reasoning time from its start to completion.
	replaces           string                           // Pending thinking block this reasoning item takes over.
	attachments        []activitypresentation.Value     // Submitted file snapshot outcomes, recovered from host history.
	capturedEdit       *changepresentation.Receipt      // Exact invocation receipt reconciled by the live diff owner.
	editPages          []activitypresentation.Value     // Completed host file changes, available before outer-cell capture.
	segments           []commandSegment                 // A tracked command's own segments, replacing its single row.
	segmentsPending    bool                             // Retained targets await independent report attribution.
	output             *activitymodel.Output            // A command's retained host output, which the output dialog reads.
	hook               *activitypresentation.HookDetails
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

func (v *activityAdapter) entrySeq(entry activityPaneEntry) uint64 {
	for _, current := range v.Entries {
		if entry.native != nil && entry.native.sameItem(current.Source.native) || entry.native == nil && current.Source.Seq == entry.Seq {
			return current.Source.Seq
		}
	}
	return 0
}

// appServerUserText renders userMessage content as the composer wrote it,
// with each image as its "[Image N]" label.
func appServerUserText(content jsontext.Value) (string, []activitypresentation.TextSpan, bool) {
	var contents []composerUserContent
	if len(content) > 0 && json.Unmarshal(content, &contents) != nil {
		return "", nil, false
	}
	var text string
	var spans []activitypresentation.TextSpan
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
			spans = append(spans, activitypresentation.TextSpan{Start: start, End: len(text), Kind: activitypresentation.ImageToken})
		}
	}
	draft := conversationmodel.Input{Text: text}
	draft.Skills = unambiguousSkillBindings(text, composerContentSkills(contents), spans)
	for _, span := range sessionpresentation.DraftSpans(draft.Draft) {
		if !slices.ContainsFunc(spans, func(existing activitypresentation.TextSpan) bool {
			return span.Start < existing.End && span.End > existing.Start
		}) {
			spans = append(spans, span)
		}
	}
	slices.SortFunc(spans, func(a, b activitypresentation.TextSpan) int { return a.Start - b.Start })
	return text, spans, true
}

// mergeNative updates the owning activity entry in place. Completed snapshots
// supersede fragments; duplicate starts and late deltas cannot reopen them.
func (v *activityAdapter) mergeNative(entry activityPaneEntry) bool {
	if entry.native == nil {
		return false
	}
	for i, previous := range v.Entries {
		if previous.Source.native != nil && previous.Source.native.phase == "input/pending" && entry.Agent == "You" && entry.native.thread == previous.Source.native.thread && entry.Text == previous.Source.Text {
			// Reconcile the local echo with Codex's authoritative user item.
			entry.Seq, entry.Observed = previous.Source.Seq, previous.Source.Observed
			v.Model.Replace(i, entry, activityContent(entry).Values())
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
			blocks := v.sourceValues(i)
			if native.command != "" && len(blocks) > 1 && !slices.ContainsFunc(blocks, func(b activitypresentation.Value) bool { return b.BatchExit }) {
				blocks = activitypresentation.RetainAnnotations(activityContent(current).Values(), blocks)
			} else {
				activitypresentation.ApplyApproval(blocks, native.approval)
			}
			v.Model.Replace(i, current, blocks)
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
		blocks := activityContent(entry).Values()
		blocks = activitypresentation.RetainAnnotations(blocks, v.Entries[i].Values)
		v.Model.Replace(i, entry, blocks)
		return true
	}
	return false
}

func (v *activityAdapter) removePendingInput(id string) {
	for i, entry := range v.Entries {
		if entry.Source.native != nil && entry.Source.native.item == "input/"+id && entry.Source.native.phase == "input/pending" {
			v.Model.Remove(i, i+1)
			return
		}
	}
}
