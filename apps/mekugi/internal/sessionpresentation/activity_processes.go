package sessionpresentation

// Source: apps/mekugi/internal/router/running_processes.go:18:33,52:88@41b4b1af.
import (
	"cmp"
	"iter"
	"maps"
	"slices"

	"github.com/yusing/mekugi/apps/mekugi/internal/frontends"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

// Only live host commands enter this map. A completed command can stay there
// while its segment report drains. Terminal reveal owns no operation lifetime.
func (s *Attachment) RunningProcesses() iter.Seq[ActivityEntry] {
	return func(yield func(ActivityEntry) bool) {
		if s.retired {
			return
		}
		for key, run := range s.Commands() {
			if !run.Entry.Native.Invocation.Running() {
				continue
			}
			entry := run.Entry
			entry.Agent = s.KnownCaller(key[0])
			if !yield(entry) {
				return
			}
		}
	}
}

// processValues rebuilds the /ps pages from Activity, which retains every
// agent's running rows, Main's included. Each page is one whole invocation:
// its display source and retained output, not a feed row's projection of it.
// Live commands join in start order; a completed one keeps its page until the
// dialog closes, as last seen if its row ages out.
func processValues(source *ActivitySource, pages []activitypresentation.DetailPage, running iter.Seq[ActivityEntry]) []activitypresentation.Value {
	bySource := make(map[uint64]activitypresentation.Value)
	for _, page := range pages {
		if page.Source != 0 {
			bySource[page.Source] = page.Value
		}
	}
	for entry := range running {
		if seq := source.EntrySeq(entry); seq != 0 {
			if _, known := bySource[seq]; !known {
				bySource[seq] = activitypresentation.Value{Source: seq, Owner: entry.Agent}
			}
		}
	}
	for _, record := range source.Entries {
		page, ok := bySource[record.Source.Seq]
		if n := record.Source.Native; ok && n != nil {
			bySource[record.Source.Seq] = activitypresentation.Value{
				Source: record.Source.Seq, Owner: page.Owner, Kind: "op", Verb: "Run", Lang: "bash",
				Code:   frontends.ShellDisplaySource(DisplayCommand(n.Command)),
				Output: RetainedOutputID(n.Output, 1), Tail: record.Source.OutputTail, TailOmitted: record.Source.OutputOmit,
				Running: n.Running, Approval: n.Approval,
				Started: n.CommandStarted, Ended: n.CommandEnded, Duration: n.Duration, NotificationTiming: true,
			}
		}
	}
	if len(bySource) == 0 {
		return []activitypresentation.Value{{Kind: "text", Verb: "Running processes", Body: "No running processes"}}
	}
	return slices.SortedFunc(maps.Values(bySource), func(a, b activitypresentation.Value) int { return cmp.Compare(a.Source, b.Source) })
}

// Process identifies the actual host invocation, not a row whose retention or
// rendering lifetime can outlast the command.
type Process struct {
	Thread, Turn, Item, ID, Agent, Command string
}

func processIdentity(entry ActivityEntry) Process {
	n := entry.Native
	return Process{Thread: n.Thread, Turn: n.Turn, Item: n.Item, ID: n.ProcessID, Agent: entry.Agent, Command: frontends.ShellDisplaySource(DisplayCommand(n.Command))}
}

func (s *Attachment) Processes() []Process {
	var processes []Process
	for entry := range s.RunningProcesses() {
		processes = append(processes, processIdentity(entry))
	}
	slices.SortFunc(processes, func(a, b Process) int {
		return cmp.Or(cmp.Compare(a.Thread, b.Thread), cmp.Compare(a.Turn, b.Turn), cmp.Compare(a.Item, b.Item))
	})
	return processes
}

func (s *Attachment) AdmitProcess(process Process) error {
	if s.retired || process.ID == "" {
		return ErrStaleIntent
	}
	run := s.Commands()[[3]string{process.Thread, process.Turn, process.Item}]
	if run == nil || !run.Entry.Native.Invocation.Running() || run.Entry.Native.ProcessID != process.ID {
		return ErrStaleIntent
	}
	return nil
}
