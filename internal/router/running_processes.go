package router

import (
	"cmp"
	"iter"
	"slices"

	activityui "github.com/yusing/mekugi/internal/ui/activity"
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

func (u *appServerUI) renderRunningProcesses() {
	p := u.statusPanel
	p.fields = nil
	entries := slices.Collect(u.session.runningProcesses())
	slices.SortFunc(entries, func(a, b activityPaneEntry) int { return cmp.Compare(a.Seq, b.Seq) })
	for _, entry := range entries {
		p.fields = append(p.fields, statusField{group: "Processes", label: activityui.AgentDisplayName(entry.Agent), value: appServerDisplayCommand(entry.native.command)})
	}
	if len(p.fields) == 0 {
		p.fields = []statusField{{group: "Processes", value: "No running processes"}}
	}
}
