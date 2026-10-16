package router

import (
	"slices"
	"strconv"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// activityAdapter joins original host evidence to the Activity owners. It never paints.
type activityAdapter struct {
	*activitypresentation.Model[activityPaneEntry, activityPaneAgent]
	*activityui.View
	display  *sessionpresentation.Model
	mainView *activityAdapter
}

const liveActivityFeedLimit = activitypresentation.FeedLimit
const unreturnedOutputNote = "output not returned to the model"

func newActivityAdapter() *activityAdapter {
	v := &activityAdapter{Model: new(activitypresentation.Model[activityPaneEntry, activityPaneAgent]), View: activityui.NewView(), display: new(sessionpresentation.Model)}
	v.display.Attach(sessionpresentation.Thread{ID: "/root", Caller: "/root"}, false)
	return v
}
func (v *activityAdapter) apply(event activityPaneEvent) bool {
	switch event.Kind {
	case "end":
		return true
	case "coverage":
		v.StatusLabel = "RECONNECTING"
		return false
	case "snapshot":
		v.StatusLabel = ""
	case "entries", "agents", "heartbeat":
	default:
		return false
	}
	if event.Agents != nil || event.Kind == "snapshot" {
		if !slices.EqualFunc(v.Agents, event.Agents, func(a, b activityPaneAgent) bool { return a.Name == b.Name }) {
			v.Hovered = ""
		}

		v.Agents = event.Agents
	}
	for _, entry := range event.Entries {
		if !v.Model.Accept(entry.Seq) {
			continue
		}
		if entry.Kind == "output_dedupe" {
			for i, previous := range v.Entries {
				if previous.Source.native == nil || previous.Source.native.item != entry.CallID || entry.native == nil || previous.Source.native.thread != entry.native.thread {
					continue
				}
				v.mutateEntry(i, func(record *liveActivityRecord) {
					if len(record.Values) > 0 {
						record.Values[len(record.Values)-1].Repeated = entry.Text
					}
				})
			}
			continue
		}
		if entry.Agent == "Main" && entry.Kind == "text" && entry.native != nil {
			for _, question := range slices.Backward(v.Entries) {
				if (question.Source.Agent == "You" || question.Source.Kind == "question_reply") && question.Source.native != nil && question.Source.native.thread == entry.native.thread && question.Source.native.turn == entry.native.turn {
					entry.native.question = question.Source.Seq
					break
				}
			}
		}
		if entry.Kind != "reasoning" && v.mergeNative(entry) {
			continue
		}
		if entry.Kind == "reasoning" {
			if entry.native != nil && entry.native.phase == "discarded" {
				v.removeThinking(entry.native)
				continue
			}
			// These are provider-visible summaries from the collector, never
			// raw reasoning. Keep legacy panes and the other audience excluded.
			if !(v.ChildrenOnly && entry.Agent != "/root" || v.Conversation && entry.Agent == "Main") {
				continue
			}
			updated := false
			if entry.CallID != "" {
				for i, previous := range v.Entries {
					if previous.Source.Kind != "reasoning" || previous.Source.Agent != entry.Agent {
						continue
					}
					// The request's pending block keeps its row and start.
					replaced := entry.native != nil && entry.native.replaces != "" && previous.Source.native != nil &&
						previous.Source.native.thread == entry.native.thread && previous.Source.native.item == entry.native.replaces
					if replaced || previous.Source.CallID == entry.CallID &&
						(previous.Source.native == nil && entry.native == nil || previous.Source.native != nil && entry.native != nil && previous.Source.native.sameItem(entry.native)) {
						if previous.Source.native != nil && previous.Source.native.phase == "item/completed" {
							// Refresh authoritative text without replaying a completed
							// item's animation or replacing its observed timing.
							if entry.native.phase != "item/completed" {
								updated = true
								break
							}
							entry.native.thought, entry.completed = previous.Source.native.thought, previous.Source.completed
							entry.Observed = previous.Source.Observed
						}
						entry.Seq = previous.Source.Seq
						if replaced || activityui.ReasoningSummaryHeader(previous.Source.Text) == activityui.ReasoningSummaryHeader(entry.Text) {
							entry.Observed = previous.Source.Observed
						}
						v.replaceEntry(i, entry, parseLiveActivity(entry))
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
					blocks := commandExitBlocks(v.blocks(i), code, entry.outputTail, entry.outputOmit)
					setCommandFacts(blocks, v0.Source)
					v.replaceEntry(i, v0.Source, blocks)
					break
				}
			}
			continue
		}
		blocks := parseLiveActivity(entry)
		if entry.Kind == "tool" && entry.CallID != "" && len(blocks) > 0 &&
			slices.Contains([]string{"Create", "Edit", "Delete", "Move"}, blocks[0].Verb) {
			// A confirmed receipt replaces the provisional Run or requested
			// Edit row for this call. Only the capturer supplies saved counts.
			for i := len(v.Entries) - 1; i >= 0; i-- {
				prior := v.Entries[i].Source
				if prior.Kind != "tool" || prior.Agent != entry.Agent || prior.CallID != entry.CallID ||
					len(v.Entries[i].Values) == 0 || (v.Entries[i].Values[0].Verb != "Run" && v.Entries[i].Values[0].Verb != "Edit") {
					continue
				}
				entry.Seq = prior.Seq
				blocks[0].ExitCode, blocks[0].Exited = v.Entries[i].Values[0].ExitCode, v.Entries[i].Values[0].Exited
				for _, annotation := range v.blocks(i)[1:] {
					if annotation.BatchExit {
						blocks = retainBatchResult(blocks, annotation)
					} else if annotation.Kind == "filter" {
						blocks = append(blocks, annotation)
					}
				}
				v.replaceEntry(i, entry, blocks)
				blocks = nil
				break
			}
			if blocks == nil {
				continue
			}
		}
		v.appendEntry(entry, blocks)
	}
	if retired := v.Model.Trim(liveActivityFeedLimit); len(retired) > 0 {
		for _, seq := range retired {
			v.View.Forget(seq)
		}
		v.Runs = nil
		for seq := range v.Passed {
			if v.HistoryOrder && !slices.ContainsFunc(v.Entries, func(entry liveActivityRecord) bool { return entry.Source.Seq == seq }) || !v.HistoryOrder && seq < v.Entries[0].Source.Seq {
				delete(v.Passed, seq)
			}
		}
	}
	v.publishActivity()
	return false
}

func (v *activityAdapter) markUnreturned(thread, call string) bool {
	for i, entry := range v.Entries {
		if entry.Source.Kind != "tool" || entry.Source.native == nil || entry.Source.native.thread != thread || entry.Source.native.item != call {
			continue
		}
		output, noted := false, false
		for _, block := range v.Entries[i].Values {
			output = output || len(block.Tail)+block.TailOmitted > 0
			noted = noted || block.Kind == "filter" && block.Body == unreturnedOutputNote
		}
		if !output || noted {
			return false
		}
		v.replaceEntry(i, entry.Source, append(v.blocks(i), activityui.Block{Kind: "filter", Body: unreturnedOutputNote}))
		return true
	}
	return false
}

func (v *activityAdapter) removeThinking(native *liveActivityNativeItem) {
	for i, entry := range v.Entries {
		if entry.Source.Kind == "reasoning" && native.sameItem(entry.Source.native) {
			v.removeEntries(i, i+1)
			return
		}
	}
}
