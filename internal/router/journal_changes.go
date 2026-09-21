package router

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
)

// childJournalChanges runs under store.lock with the journal snapshot. It uses
// durable executing-thread ownership, including recoveries in another stream,
// rather than the current router's activity tree or model-authored handoffs.
func (s *mekugiReplayStore) childJournalChanges(ctx context.Context, workspace, thread string) string {
	const unavailable = "\n\n**Changes:**\nChanges unavailable: "
	if s == nil || thread == "" {
		return unavailable + "change storage or thread identity is unavailable.\n"
	}
	s = s.scoped(ctx)
	index, err := s.readChangeIndex(workspace)
	if err != nil {
		return unavailable + err.Error() + "\n"
	}
	selected := changeIndex{Workspace: workspace, Changes: make(map[string]trackedChange)}
	var ids, ranges []string
	retired := false
	// Sort retained IDs instead of iterating retired stream positions.
	byStream := make(map[string][]int)
	for id := range index.Changes {
		prefix, number, _ := parseChangeID(id) // readChangeIndex validates IDs.
		byStream[prefix] = append(byStream[prefix], number)
	}
	for position, stream := range index.Streams {
		name := changeStreamName(position)
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
			calls := slices.DeleteFunc(slices.Clone(change.Calls), func(call trackedCall) bool {
				return cmp.Or(call.Thread, stream.Thread) != thread
			})
			if len(calls) == 0 && !(stream.Thread == thread && len(change.Calls) == 0) {
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
			return unavailable + "older change records were removed by session cleanup.\n"
		}
		return "\n\n**Changes:**\nNo recorded changes.\n"
	}
	var output strings.Builder
	fmt.Fprintf(&output, "\n\n**Changes:** %s\n", strings.Join(ranges, ", "))
	if retired {
		output.WriteString("Stat unavailable: older change records were removed by session cleanup.\n")
		return output.String()
	}
	stat, err := s.renderChanges(ctx, changeReadOptions{workspace: workspace, ids: ids, view: "summary"}, selected)
	if err != nil {
		fmt.Fprintf(&output, "Stat unavailable: %s\n", err)
	} else if stat == "" {
		output.WriteString("No recorded file changes.\n")
	} else {
		output.WriteString("\nAggregated numstat (this agent's recorded evaluations, not a net diff):\n\n")
		output.WriteString(indentJournalText(stat, "    "))
	}
	return output.String()
}
