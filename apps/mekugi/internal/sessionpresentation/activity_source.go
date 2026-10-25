package sessionpresentation

import (
	"slices"
	"strconv"
	"strings"

	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	"github.com/yusing/mekugi/libraries/pathdisplay"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

// ActivitySource borrows one of Activity's attached feeds. It admits application
// source facts and publishes detached values without a terminal dependency.
type ActivitySource struct {
	reloads map[string]reloadPlan
	*activitypresentation.Model[ActivityEntry, ActivityAgent]
	Audience    ActivityAudience
	Coverage    string
	publication *activityPublication
	Prepared    func(thread, id string) (activitypresentation.PreparedContent, bool)
	Repetition  func(thread, id string) string // Authenticated invocation-owned annotations.
}

type ActivityAudience uint8

const (
	MainAudience ActivityAudience = iota + 1
	ChildAudience
)

const unreturnedOutputNote = "output not returned to the model"

// Source: apps/mekugi/internal/router/live_activity_view.go:28:162@45fbe279
func (v *ActivitySource) Apply(event ActivityEvent) {
	if len(event.Entries) > 0 {
		defer v.applyReloads()
	}

	if event.Agents != nil {
		v.Agents = event.Agents
	}
	for _, entry := range event.Entries {
		if !v.Model.Accept(entry.Seq) {
			continue
		}
		if entry.Kind == "compact_reload" && entry.Native != nil {
			if v.reloads == nil {
				v.reloads = make(map[string]reloadPlan)
			}
			if len(entry.ReloadSources) == 0 {
				delete(v.reloads, entry.Native.Thread)
			} else {
				v.reloads[entry.Native.Thread] = reloadPlan{entry.Native.Turn, slices.Clone(entry.ReloadSources)}
			}
			continue
		}
		if entry.Native != nil && ContextBoundary(entry) {
			if plan, ok := v.reloads[entry.Native.Thread]; ok && plan.turn == "" {
				delete(v.reloads, entry.Native.Thread)
			}
		}
		if entry.Kind == "output_dedupe" {
			for i, previous := range v.Entries {
				if strings.HasPrefix(entry.CallID, attachments.ReceiptPrefix) {
					if previous.Source.Kind == "attachments" && previous.Source.Native != nil && entry.Native != nil && previous.Source.Native.Thread == entry.Native.Thread &&
						slices.ContainsFunc(previous.Values, func(value activitypresentation.Value) bool { return value.AttachmentID == entry.CallID }) {
						v.Model.Mutate(i, func(record *ActivityRecord) {
							for j := range record.Values {
								if record.Values[j].AttachmentID == entry.CallID {
									record.Values[j].Repeated = entry.Text
								}
							}
						})
					}
					continue
				}
				if previous.Source.Native == nil || previous.Source.Native.Item != entry.CallID || entry.Native == nil || previous.Source.Native.Thread != entry.Native.Thread {
					continue
				}
				v.Model.Mutate(i, func(record *ActivityRecord) {
					if len(record.Values) > 0 {
						record.Values[len(record.Values)-1].Repeated = entry.Text
					}
				})
			}
			continue
		}
		if entry.Agent == "Main" && entry.Kind == "text" && entry.Native != nil {
			for _, question := range slices.Backward(v.Entries) {
				if (question.Source.Agent == "You" || question.Source.Kind == "question_reply") && question.Source.Native != nil && question.Source.Native.Thread == entry.Native.Thread && question.Source.Native.Turn == entry.Native.Turn {
					entry.Native.Question = question.Source.Seq
					break
				}
			}
		}
		if entry.Kind != "reasoning" && v.MergeNative(entry) {
			continue
		}
		if entry.Kind == "reasoning" {
			if entry.Native != nil && entry.Native.Phase == "discarded" {
				v.RemoveThinking(entry.Native)
				continue
			}
			// These are provider-visible summaries from the collector, never
			// raw reasoning. Keep legacy panes and the other audience excluded.
			if !(v.Audience == ChildAudience && entry.Agent != "/root" || v.Audience == MainAudience && entry.Agent == "Main") {
				continue
			}
			updated := false
			if entry.CallID != "" {
				for i, previous := range v.Entries {
					if previous.Source.Kind != "reasoning" || previous.Source.Agent != entry.Agent {
						continue
					}
					// The request's pending block keeps its row and start.
					replaced := entry.Native != nil && entry.Native.Replaces != "" && previous.Source.Native != nil &&
						previous.Source.Native.Thread == entry.Native.Thread && previous.Source.Native.Item == entry.Native.Replaces
					if replaced || previous.Source.CallID == entry.CallID &&
						(previous.Source.Native == nil && entry.Native == nil || previous.Source.Native != nil && entry.Native != nil && previous.Source.Native.SameItem(entry.Native)) {
						if previous.Source.Native != nil && previous.Source.Native.Phase == "item/completed" {
							// Refresh authoritative text without replaying a completed
							// item's animation or replacing its observed timing.
							if entry.Native.Phase != "item/completed" {
								updated = true
								break
							}
							entry.Native.Thought, entry.Completed = previous.Source.Native.Thought, previous.Source.Completed
							entry.Observed = previous.Source.Observed
						}
						entry.Seq = previous.Source.Seq
						if replaced || activitypresentation.ReasoningSummaryHeader(previous.Source.Text) == activitypresentation.ReasoningSummaryHeader(entry.Text) {
							entry.Observed = previous.Source.Observed
						}
						v.Model.Replace(i, entry, ActivityContent(entry).Values())
						updated = true
						break
					}
				}
			}
			if updated {
				continue
			}
		}
		if entry.Kind == "exit" {
			for i, v0 := range slices.Backward(v.Entries) {
				if v0.Source.Agent == entry.Agent && v0.Source.CallID == entry.CallID && entry.CallID != "" {
					code, _ := strconv.Atoi(entry.Text)
					blocks := activitypresentation.CommandExit(v.SourceValues(i), code, entry.OutputTail, entry.OutputOmit)
					CommandPresentation(v0.Source).ApplyFacts(blocks)
					v.Model.Replace(i, v0.Source, blocks)
					break
				}
			}
			continue
		}
		blocks := ActivityContent(entry).Values()
		v.annotateRepetitions(entry, blocks)
		v.Model.Append(entry, blocks)
	}
	v.Model.Trim(activitypresentation.FeedLimit)
}

// Source: apps/mekugi/internal/router/live_activity_view.go:164:184@45fbe279
func (v *ActivitySource) MarkUnreturned(thread, call string) bool {
	for i, entry := range v.Entries {
		if entry.Source.Kind != "tool" || entry.Source.Native == nil || entry.Source.Native.Thread != thread || entry.Source.Native.Item != call {
			continue
		}
		output, noted := entry.Source.Native.Output != nil && entry.Source.Native.Output.HasContent(), false
		for _, segment := range entry.Source.Native.Segments {
			output = output || segment.Output != nil && segment.Output.HasContent()
		}
		for _, block := range v.Entries[i].Values {
			output = output || len(block.Tail)+block.TailOmitted > 0
			noted = noted || block.Kind == "filter" && block.Body == unreturnedOutputNote
		}
		if !output || noted {
			return false
		}
		v.Model.Replace(i, entry.Source, append(v.SourceValues(i), activitypresentation.Value{Kind: "filter", Body: unreturnedOutputNote}))
		return true
	}
	return false
}

// Source: apps/mekugi/internal/router/live_activity_view.go:186:193@45fbe279
func (v *ActivitySource) RemoveThinking(native *NativeItem) {
	for i, entry := range v.Entries {
		if entry.Source.Kind == "reasoning" && native.SameItem(entry.Source.Native) {
			v.Model.Remove(i, i+1)
			return
		}
	}
}

func (v *ActivitySource) annotateRepetitions(entry ActivityEntry, values []activitypresentation.Value) {
	if v.Repetition == nil || entry.Native == nil || len(values) == 0 {
		return
	}
	if entry.Kind == "attachments" {
		for i := range values {
			if values[i].AttachmentID != "" {
				values[i].Repeated = v.Repetition(entry.Native.Thread, values[i].AttachmentID)
			}
		}
		return
	}
	if label := v.Repetition(entry.Native.Thread, entry.Native.Item); label != "" {
		values[len(values)-1].Repeated = label
	}
}

// Reconcile explicit orchestration intents with the existing host rows. No shell
// classification, rendered text, or printed output establishes attachment identity.
func (v *ActivitySource) applyReloads() {
	for thread, plan := range v.reloads {
		start := 0
		for i, record := range v.Entries {
			if record.Source.Native != nil && record.Source.Native.Thread == thread && ContextBoundary(record.Source) {
				start = i + 1
			}
		}
		for i := start; i < len(v.Entries); i++ {
			entry := v.Entries[i].Source
			if entry.Kind != "tool" || entry.Native == nil || entry.Native.Thread != thread || plan.turn != "" && entry.Native.Turn != plan.turn {
				continue
			}
			command := CommandSource(entry.Native.Command, entry.Native.CommandCwd).Script
			if command != plan.sources[0].Command {
				continue
			}
			native := *entry.Native
			native.Reload = slices.Clone(plan.sources)
			for i := range native.Reload {
				native.Reload[i].Path = pathdisplay.ForWorkspace(native.CommandCwd, native.Reload[i].Path)
			}
			entry.Native = &native
			v.Model.Replace(i, entry, ActivityContent(entry).Values())
			delete(v.reloads, thread)
			break
		}
	}
}

type reloadPlan struct {
	turn    string
	sources []activitypresentation.ReloadSource
}
