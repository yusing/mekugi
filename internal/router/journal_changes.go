package router

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
)

const journalChangesUnavailable = "\n\n**Changes:**\nChanges unavailable: "

// childJournalChanges runs under store.lock with the journal snapshot. It uses
// durable executing-thread ownership, including recoveries in another stream,
// rather than the current router's activity tree or model-authored handoffs.
func (s *mekugiReplayStore) childJournalChanges(ctx context.Context, workspace, thread string) string {
	text, _ := s.childJournalChangesSince(ctx, workspace, thread, 0, false)
	return text
}

func (s *mekugiReplayStore) childJournalChangesSince(ctx context.Context, workspace, thread string, since uint64, delivered bool) (result string, cursor uint64) {
	if s == nil || thread == "" {
		return journalChangesUnavailable + "change storage or thread identity is unavailable.\n", since
	}
	s = s.scoped(ctx)
	index, err := s.readChangeIndex(workspace)
	if err != nil {
		return journalChangesUnavailable + err.Error() + "\n", since
	}
	return s.renderChildJournalChanges(ctx, index, thread, since, delivered)
}

// renderChildJournalChanges reuses a validated index under the replay lock.
func (s *mekugiReplayStore) renderChildJournalChanges(ctx context.Context, index changeIndex, thread string, since uint64, delivered bool) (result string, cursor uint64) {
	selected := changeIndex{Workspace: index.Workspace, Changes: make(map[string]trackedChange)}
	var ids, ranges []string
	retired := false
	totalCalls, totalChanges := 0, 0
	defer func() {
		if delivered {
			if retired {
				result += "\nCumulative: unavailable after session cleanup.\n"
			} else {
				result += fmt.Sprintf("\nCumulative: %d recorded evaluations across %d retained changes.\n", totalCalls, totalChanges)
			}
		}
	}()
	// Sort retained IDs instead of iterating retired stream positions.
	byStream := make(map[string][]int)
	for id := range index.Changes {
		prefix, number, _ := parseChangeID(id) // readChangeIndex validates IDs.
		byStream[prefix] = append(byStream[prefix], number)
	}
	for position, stream := range index.Streams {
		name := index.streamName(position)
		numbers := byStream[name]
		slices.Sort(numbers)
		first, last := 0, 0
		flush := func() {
			if first == 0 {
				return
			}
			ref := changeHandle(name, first)
			if last != first {
				ref += ".." + changeHandle(name, last)
			}
			ranges = append(ranges, ref)
			first, last = 0, 0
		}
		for _, number := range numbers {
			id := changeHandle(name, number)
			change := index.Changes[id]
			retired = retired || change.RetiredCalls != 0
			owned := 0
			for _, call := range change.Calls {
				if cmp.Or(call.Thread, stream.Thread) == thread {
					owned++
				}
			}
			totalCalls += owned
			if owned > 0 {
				totalChanges++
			}
			calls := slices.DeleteFunc(slices.Clone(change.Calls), func(call trackedCall) bool {
				return cmp.Or(call.Thread, stream.Thread) != thread || delivered && call.Sequence <= since
			})
			if len(calls) == 0 && !(!delivered && stream.Thread == thread && len(change.Calls) == 0) {
				continue
			}
			change.Calls = calls
			selected.Changes[id] = change
			ids = append(ids, id)
			if first == 0 {
				first = number
			} else if number != last+1 || number-first >= 256 {
				flush()
				first = number
			}
			last = number
		}
		flush()
		// Retention no longer has enough information to attribute removed
		// cross-thread recovery attempts. Never claim complete totals then.
		retired = retired || stream.Retired != 0
	}
	if len(ids) == 0 {
		if retired {
			return journalChangesUnavailable + "older change records were removed by session cleanup.\n", index.Sequence
		}
		return "\n\n**Changes:**\nNo recorded changes.\n", index.Sequence
	}
	var output strings.Builder
	fmt.Fprintf(&output, "\n\n**Changes:** %s\n", strings.Join(ranges, ", "))
	if retired {
		output.WriteString("Stat unavailable: older change records were removed by session cleanup.\n")
		return output.String(), index.Sequence
	}
	stat, err := s.renderChanges(ctx, changeReadOptions{workspace: index.Workspace, ids: ids, view: "summary"}, selected)
	if err != nil {
		fmt.Fprintf(&output, "Stat unavailable: %s\n", err)
	} else if stat == "" {
		output.WriteString("No recorded file changes.\n")
	} else {
		output.WriteString("\nAggregated numstat (this agent's recorded evaluations, not a net diff):\n\n")
		output.WriteString(indentJournalText(stat, "    "))
	}
	return output.String(), index.Sequence
}
