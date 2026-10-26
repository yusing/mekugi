package sessionpresentation

import (
	"slices"
	"time"

	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
)

// Source: apps/mekugi/internal/router/live_activity_app_server.go:1:194@45fbe279

type NativeItem struct {
	SummaryPending bool     `json:"-"` // Resolve live compaction storage only when its dialog opens.
	ToolDetail     string   `json:"-"` // Original tool envelope behind a semantic row.
	Images         []string `json:"-"` // Bound user or viewed image sources, never inferred from labels.
	Reload         []activitypresentation.ReloadSource
	UserShell      bool                             `json:"-"` // Host command origin, independent of source classification.
	Invocation     *activitypresentation.Invocation `json:"-"` // Canonical host state, shared while terminal output settles.
	// Admitted native phase and timing. Terminal holds use detached projections
	// rather than delaying these host facts.
	CommandStarted     time.Time                         `json:"-"` // UI receipt of host start notification; not reconstructed on replay.
	CommandEnded       time.Time                         `json:"-"`
	Duration           time.Duration                     `json:"-"`
	Questions          []activitypresentation.Question   `json:"-"`
	ReplySource        string                            `json:"-"` // Stock async envelope or synchronous turn prompt.
	ProcessID          string                            `json:"-"`
	Thread, Turn, Item string                            `json:"-"`
	Phase              string                            `json:"-"`
	Wait               *activitypresentation.Value       `json:"-"` // Structured logical-wait progress.
	Command, Status    string                            `json:"-"`
	Exit               *int                              `json:"-"` // Observed exit presence is independent of retained output.
	CommandCwd         string                            `json:"-"` // Metadata-owned directory for classified paths and receipt refreshes.
	CommandTimeout     string                            `json:"-"` // Explicit timeout from available invocation input.
	Approval           string                            `json:"-"` // UI decision, independent of the host execution result.
	Workdir            string                            `json:"-"` // Display form of a command's directory outside the shown workspace.
	SearchResults      *int                              `json:"-"`
	Running            bool                              `json:"-"` // Started live and not yet completed; replay never sets it.
	Live               bool                              `json:"-"` // From a live notification; restored history never sets it.
	Changes            []activitypresentation.ChangeRow  `json:"-"` // Change rows read from a successful change history, VCS read, or commit.
	Commit             activitypresentation.CommitKey    `json:"-"` // The commit object whose files complete changes.
	Spans              []activitypresentation.TextSpan   `json:"-"` // Attachment spans, not text resembling image labels.
	Question           uint64                            `json:"-"` // Original user entry.
	Thought            time.Duration                     `json:"-"` // Reasoning time from its start to completion.
	Replaces           string                            `json:"-"` // Pending thinking block this reasoning item takes over.
	Attachments        []activitypresentation.Value      `json:"-"` // Submitted file snapshot outcomes, recovered from host history.
	CapturedEdit       *changepresentation.Receipt       `json:"-"` // Exact invocation receipt reconciled by the live diff owner.
	EditPages          []activitypresentation.Value      `json:"-"` // Completed host file changes, available before outer-cell capture.
	Segments           []ActivitySegment                 `json:"-"` // A tracked command's own segments, replacing its single row.
	SegmentsPending    bool                              `json:"-"` // Retained targets await independent report attribution.
	Output             *activitymodel.Output             `json:"-"` // A command's retained host output, which the output dialog reads.
	Hook               *activitypresentation.HookDetails `json:"-"`
}

func (n *NativeItem) SameItem(other *NativeItem) bool {
	if n.Hook != nil || other != nil && other.Hook != nil {
		return n.Hook != nil && other != nil && other.Hook != nil && n.Thread == other.Thread && n.Item == other.Item
	}
	return other != nil && n.Thread == other.Thread && n.Turn == other.Turn && n.Item == other.Item
}

// Unconfirmed reports a settled host item that did not complete, such as a
// declined or failed command: it can end without an exit code, so its rows
// alone read as successful.
func (n *NativeItem) Unconfirmed() bool {
	return n != nil && !n.Running && n.Status != "" && n.Status != "completed"
}

func (v *ActivitySource) EntrySeq(entry ActivityEntry) uint64 {
	for _, current := range v.Entries {
		if entry.Native != nil && entry.Native.SameItem(current.Source.Native) || entry.Native == nil && current.Source.Seq == entry.Seq {
			return current.Source.Seq
		}
	}
	return 0
}

// MergeNative updates the owning activity entry in place. Completed snapshots
// supersede fragments; duplicate starts and late deltas cannot reopen them.
func (v *ActivitySource) MergeNative(entry ActivityEntry) bool {
	if entry.Native == nil {
		return false
	}
	for i, previous := range v.Entries {
		if previous.Source.Native != nil && previous.Source.Native.Phase == "input/pending" && entry.Agent == "You" && entry.Native.Thread == previous.Source.Native.Thread && entry.Text == previous.Source.Text {
			// Reconcile the local echo with Codex's authoritative user item.
			entry.Seq, entry.Observed = previous.Source.Seq, previous.Source.Observed
			v.Model.Replace(i, entry, ActivityContent(entry).Values())
			return true
		}
		if !entry.Native.SameItem(previous.Source.Native) {
			continue
		}
		if entry.Native.Phase == "approval/updated" {
			native := *previous.Source.Native
			native.Approval = entry.Native.Approval
			current := previous.Source
			current.Native = &native
			blocks := v.SourceValues(i)
			if native.Command != "" && len(blocks) > 1 && !slices.ContainsFunc(blocks, func(b activitypresentation.Value) bool { return b.BatchExit }) {
				blocks = activitypresentation.RetainAnnotations(ActivityContent(current).Values(), blocks)
			} else {
				activitypresentation.ApplyApproval(blocks, native.Approval)
			}
			v.Model.Replace(i, current, blocks)
			return true
		}
		entry.Native.Approval = previous.Source.Native.Approval
		entry.Native.Reload = previous.Source.Native.Reload
		if previous.Source.Native.CapturedEdit != nil {
			entry.Native.CapturedEdit = previous.Source.Native.CapturedEdit
			entry.Text = previous.Source.Text
		}
		if previous.Source.Native.Phase == "item/completed" || entry.Native.Phase == "item/started" && previous.Source.Native.Phase != "approval/updated" && previous.Source.Native.Phase != "hook/restored" || previous.Source.Native.Phase == "turn/completed" && entry.Native.Phase != "item/completed" {
			return true
		}
		if entry.Native.Phase == "item/agentMessage/delta" {
			entry.Text = previous.Source.Text + entry.Text
		}
		entry.Seq, entry.Observed = previous.Source.Seq, previous.Source.Observed
		entry.Native.Question = previous.Source.Native.Question
		blocks := ActivityContent(entry).Values()
		blocks = activitypresentation.RetainAnnotations(blocks, v.Entries[i].Values)
		v.annotateRepetitions(entry, blocks)
		v.Model.Replace(i, entry, blocks)
		return true
	}
	return false
}

func (v *ActivitySource) RemovePendingInput(id string) {
	for i, entry := range v.Entries {
		if entry.Source.Native != nil && entry.Source.Native.Item == "input/"+id && entry.Source.Native.Phase == "input/pending" {
			v.Model.Remove(i, i+1)
			return
		}
	}
}
