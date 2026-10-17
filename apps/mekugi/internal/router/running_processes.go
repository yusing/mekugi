package router

import (
	"cmp"
	"iter"
	"maps"
	"slices"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// Only live host commands enter this map. A completed command can stay there
// while its output rolls through the transcript or its segment report drains.
func (s *appServerSession) runningProcesses() iter.Seq[activityPaneEntry] {
	return func(yield func(activityPaneEntry) bool) {
		for key, run := range s.commands {
			if run.done != nil || run.completion != nil {
				continue
			}
			entry := run.entry
			entry.Agent = s.paths[key[0]]
			if !yield(entry) {
				return
			}
		}
	}
}

func (u *appServerUI) runningProcessCount() int {
	count := 0
	for range u.session.runningProcesses() {
		count++
	}
	return count
}

// showRunningProcesses opens the shared output dialog on every live command.
func (u *appServerUI) showRunningProcesses() {
	u.deleteDraftRange(0, len(u.Text))
	u.ensureShell()
	u.shell.selection = nil
	u.shell.output = &outputDialog{view: u.agents, processes: true}
	u.shell.output.refreshPages(activitypresentation.DetailPages(activityui.Values(u.processPages(nil))))
}

// processPages rebuilds the /ps pages from Activity, which retains every
// agent's running rows, Main's included. Each page is one whole invocation:
// its display source and retained output, not a feed row's projection of it.
// Live commands join in start order; a completed one keeps its page until the
// dialog closes, as last seen if its row ages out.
func (u *appServerUI) processPages(pages []outputPage) []activityui.Block {
	bySource := make(map[uint64]activityui.Block)
	for _, page := range pages {
		if page.Source != 0 {
			bySource[page.Source] = activityui.Block{Value: page.Value}
		}
	}
	for entry := range u.session.runningProcesses() {
		if seq := u.agents.entrySeq(entry); seq != 0 {
			if _, known := bySource[seq]; !known {
				bySource[seq] = activityui.Block{Source: seq, Owner: entry.Agent}
			}
		}
	}
	for _, record := range u.agents.Entries {
		page, ok := bySource[record.Source.Seq]
		if n := record.Source.native; ok && n != nil {
			bySource[record.Source.Seq] = activityui.Block{
				Source: record.Source.Seq, Owner: page.Owner, Kind: "op", Verb: "Run", Lang: "bash",
				Code:   toolActivityShellSource(appServerDisplayCommand(n.command)),
				Output: retainedOutputID(n.output, 1), Tail: record.Source.outputTail, TailOmitted: record.Source.outputOmit,
				Running: n.running, Approval: n.approval,
				Started: n.commandStarted, Ended: n.commandEnded, Duration: n.duration, NotificationTiming: true,
			}
		}
	}
	if len(bySource) == 0 {
		return []activityui.Block{{Kind: "text", Verb: "Running processes", Body: "No running processes"}}
	}
	return slices.SortedFunc(maps.Values(bySource), func(a, b activityui.Block) int { return cmp.Compare(a.Source, b.Source) })
}
