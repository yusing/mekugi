package sessionpresentation

// Source: apps/mekugi/internal/router/app_server_events.go:149:374@7166de95.
import (
	"cmp"
	"strconv"
	"strings"
	"time"

	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/libraries/pathdisplay"
	"github.com/yusing/mekugi/libraries/sourceanalysis/markdownsyntax"
	"github.com/yusing/mekugi/modules/activity/feed"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

// ObservationInput contains admitted facts only. Live acquisition and retained
// storage remain outside the shared live/replay reducer.
type ObservationInput struct {
	Event                                     observation.Event
	Text                                      observation.TextEvent
	Main, Selected, Workspace, CommandTimeout string
	Start                                     string // Main's admitted start origin, absent during replay.
	Tracked                                   bool
	AuxiliaryTurn                             bool // Standalone user-shell execution, not model work.
}

type ObservationResult struct {
	Handled, Publish, Started bool
	Deliveries                []ActivityDelivery
}

// ActivityDelivery is a closed ordered set of detached terminal consequences.
// Source state is already admitted; these values cannot mutate its owners.
type ActivityDelivery struct {
	Text    *observation.TextUpdate
	Command *CommandDelivery
	Rename  *CallerRename
	Report  *CommandReportDelivery
}
type CallerRename struct{ Old, Name string }
type CommandTransition uint8

const (
	CommandBegin CommandTransition = iota + 1
	CommandDelta
	CommandComplete
	CommandRelease
)

type CommandDelivery struct {
	Kind                        CommandTransition
	Key                         [3]string
	MainSequence, ChildSequence uint64
	Text, Delta                 string
	At                          time.Time
	Tracked                     bool
	Fallback                    activitypresentation.Command
}

// CommandReportDelivery carries newly drained segment evidence to local reveal
// cursors. Snapshot contains admitted segment facts, never terminal-painted rows.
type CommandReportDelivery struct {
	Key                         [3]string
	Text                        string
	MainSequence, ChildSequence uint64
	Report                      activitypresentation.SegmentReport
	Snapshot                    activitypresentation.ReportSnapshot
	Version                     uint64
	Final                       bool
}

func ReduceObservation(s *Attachment, model *Model, mainSource, childSource *ActivitySource, transcript observation.Transcript, outputs *feed.Retention, in ObservationInput) (result ObservationResult) {
	if s.retired {
		return
	}
	p := in.Event
	sources := []*ActivitySource{mainSource, childSource}
	method := string(p.Kind)
	text := in.Text
	model.ObserveActivityProgress(p, in.Main, in.Selected, mainSource)
	main := p.ThreadID == in.Main
	result.Publish = true
	var entries []ActivityEntry
	// Source transitions complete before delivery; publication remains the adapter's job.
	defer func() {
		if !result.Publish {
			return
		}
		ApplyActivity(mainSource, childSource, entries, s.Roster())
		for i := range result.Deliveries {
			command := result.Deliveries[i].Command
			if command == nil {
				continue
			}
			identity := ActivityEntry{Native: &NativeItem{Thread: command.Key[0], Turn: command.Key[1], Item: command.Key[2]}}
			command.MainSequence, command.ChildSequence = mainSource.EntrySeq(identity), childSource.EntrySeq(identity)
		}
	}()
	textEntries := func(updates []observation.TextUpdate, paced bool) []ActivityEntry {
		admitted := s.AdmitText(updates, in.Main)
		if paced {
			for _, update := range updates {
				result.Deliveries = append(result.Deliveries, ActivityDelivery{Text: new(update)})
			}
		}
		return admitted
	}
	rename := func(old, name string) {
		model.RenameCaller(old, name)
		for _, source := range sources {
			source.RenameCaller(old, name)
		}
		result.Deliveries = append(result.Deliveries, ActivityDelivery{Rename: &CallerRename{Old: old, Name: name}})
	}
	now := p.At
	switch method {
	case "thread/started":
		info := p.Thread
		if info.ID == "" || info.ID == in.Main {
			result.Handled, result.Publish = true, false
			return
		}
		old := s.Caller(info.ID)
		s.RegisterThread(info)
		rename(old, s.KnownCaller(info.ID))

	case "thread/status/changed":
		s.Caller(p.ThreadID)
		agent := s.Lifecycle(p.ThreadID)
		switch p.ThreadStatus {
		case "active":
			agent.ObserveActive(true, now)
		case "idle", "notLoaded", "systemError":
			agent.ObserveActive(false, now)
		}
	case "thread/tokenUsage/updated":
		agent := s.Agent(s.Caller(p.ThreadID))
		s.Lifecycle(p.ThreadID).Response(now)
		agent.ContextWindow, agent.ContextKnown, agent.ContextTokens = p.Context.Window, p.Context.Known, p.Context.Tokens
	case "turn/started", "turn/completed":
		agent := s.Agent(s.Caller(p.ThreadID))
		active, start := in.Selected, in.Start
		if !main {
			active, start = agent.Turn, ""
		}
		updates := s.ObserveTurn(TurnModels{
			Transcript: transcript, Agent: s.Lifecycle(p.ThreadID), Selected: active, Start: start, Auxiliary: in.AuxiliaryTurn,
		}, TurnObservation{
			Kind: observation.Kind(method), Thread: p.ThreadID, Turn: p.TurnID,
			Status: p.TurnStatus, At: now,
		})
		if method == "turn/started" {
			if !updates.Admitted {
				result.Handled, result.Publish = true, false
				return
			}
			result.Started = true
			if !main {
				agent.Turn = p.TurnID
			}
		} else {
			entries = append(entries, textEntries(updates.Text, true)...)
			if updates.Current {
				agent.Turn = ""
			}
		}

	case "item/commandExecution/outputDelta":
		// Bounded as it arrives; the next frame shows it, so bursts cost one render.
		key := [3]string{p.ThreadID, p.TurnID, p.ItemID}
		if run := s.Commands()[key]; run != nil && run.Entry.Native.Invocation.Running() {
			result.Deliveries = append(result.Deliveries, ActivityDelivery{Command: &CommandDelivery{Kind: CommandDelta, Key: key, Delta: p.Delta}})
			run.Entry.Native.Invocation.Write(p.Delta)
		}
	case "item/reasoning/summaryTextDelta", "item/reasoning/summaryPartAdded":
		entries = append(entries, textEntries(s.ObserveText(transcript, text, main), true)...)

	case "item/started", "item/completed", "item/agentMessage/delta":
		item := p.Item
		id := cmp.Or(p.ItemID, item.ID)
		native := &NativeItem{Thread: p.ThreadID, Turn: p.TurnID, Item: id, Phase: method, Live: true, Command: item.Command, Status: item.Status, Duration: ItemDuration(item), Workdir: CommandWorkdir(item, in.Workspace)}
		agent := s.Caller(p.ThreadID)
		if method != "item/completed" && item.Type != "reasoning" && item.Type != "userMessage" && s.Transcript(p.ThreadID, p.TurnID).Turn != nil {
			if dropped := textEntries(transcript.OtherItemStarted(p.ThreadID, s.Thread(p.ThreadID)), false); len(dropped) != 0 {
				entries = append(entries, dropped...)
			}
		}

		switch item.Type {
		case "contextCompaction":
			if method == "item/completed" {
				summary := item.Text
				native.SummaryPending = summary == ""
				if summary == "" {
					summary = "Compaction summary unavailable."
				}
				entries = append(entries, ActivityEntry{Seq: s.Activity().NextSequence(), Agent: agent, Kind: "compaction", Text: summary, Observed: now, Native: native})
			}
		case "reasoning":
			entries = append(entries, textEntries(s.ObserveText(transcript, text, main), true)...)

		case "commandExecution", "webSearch":
			// Feed completion remains authoritative after operation correlation
			// retires. Repeated starts cannot create a second live invocation.
			if item.Type == "commandExecution" && method != "item/completed" {
				completed := false
				for _, source := range sources {
					for _, record := range source.Entries {
						if native.SameItem(record.Source.Native) && record.Source.Native.Phase == "item/completed" {
							completed = true
							break
						}
					}
				}
				if completed {
					break
				}
			}
			native.CommandTimeout = in.CommandTimeout
			native.UserShell = item.UserShell
			native.CommandCwd = CommandDirectory(item, in.Workspace)
			native.SearchResults = SearchResults(item)
			entry := ActivityEntry{Seq: s.Activity().NextSequence(), Agent: agent, Kind: "tool", Text: ToolText(item, in.Workspace), CallID: id, Observed: now, Native: native}
			key := [3]string{p.ThreadID, p.TurnID, id}
			if method != "item/completed" {
				if item.Type == "commandExecution" {
					native.Running, native.CommandStarted = true, now
					if run := s.Commands()[key]; run == nil {
						native.Invocation = activitypresentation.NewInvocation(CommandSource(item.Command, native.CommandCwd), now, outputs)
						native.Output = native.Invocation.Output()
						s.Commands()[key] = &CommandOperation{Entry: entry}
						result.Deliveries = append(result.Deliveries, ActivityDelivery{Command: &CommandDelivery{Kind: CommandBegin, Key: key, Text: entry.Text}})
					} else {
						native.Invocation = run.Entry.Native.Invocation
						native.Output = native.Invocation.Output()
						native.CommandStarted = native.Invocation.Snapshot().Started
						native.CommandTimeout = cmp.Or(native.CommandTimeout, run.Entry.Native.CommandTimeout)
					}
				}
				entries = append(entries, entry)
				break
			}
			if item.Type == "commandExecution" {
				native.CommandEnded = now
				if run := s.Commands()[key]; run != nil {
					native.CommandStarted = run.Entry.Native.CommandStarted
					native.CommandTimeout = cmp.Or(native.CommandTimeout, run.Entry.Native.CommandTimeout)
				}
			}
			s.CompleteCommand(native, item, outputs)
			delivery := &CommandDelivery{Kind: CommandComplete, Key: key, At: now}
			if run := s.Commands()[key]; run != nil && in.Tracked {
				run.Completion, run.Completed, run.CompletedAt = &entry, item, now
				delivery.Tracked = true
				for _, source := range sources {
					for _, record := range source.Entries {
						if native.SameItem(record.Source.Native) {
							native.Segments, native.SegmentsPending = record.Source.Native.Segments, true
							delivery.Fallback = CommandPresentation(record.Source)
							break
						}
					}
				}
			} else {
				delete(s.Commands(), key)
			}
			result.Deliveries = append(result.Deliveries, ActivityDelivery{Command: delivery})
			entries = append(entries, s.commandDone(entry, item, now)...)

		case "mcpToolCall":
			entries = append(entries, s.CoordinationEntry(ActivityEntry{Seq: s.Activity().NextSequence(), Agent: agent, CallID: id, Observed: now, Native: native}, item))
		case "imageView":
			native.Images = []string{item.Path}
			entries = append(entries, ActivityEntry{Seq: s.Activity().NextSequence(), Agent: agent, Kind: "tool", Text: "View " + markdownsyntax.QuoteCode(pathdisplay.ForWorkspace(in.Workspace, item.Path)), CallID: id, Observed: now, Native: native})
		case "fileChange":
			if len(item.Changes) > 0 {
				native.EditPages = activitypresentation.HostEditPages(item, in.Workspace, method)
				entries = append(entries, ActivityEntry{Seq: s.Activity().NextSequence(), Agent: agent, Kind: "tool", Text: activitypresentation.HostEditText(item, in.Workspace), CallID: id, Observed: now, Native: native})
			}
		case "agentMessage":
			if main {
				result.Publish = len(entries) != 0
				return
			}
			entries = append(entries, textEntries(s.ObserveText(transcript, text, false), true)...)

		default:
			if main {
				result.Publish = len(entries) != 0
				return // Main's messages belong to the transcript handler.
			}
			result.Publish, result.Handled = len(entries) != 0, true
			return
		}
	}
	result.Handled = !main || !strings.HasPrefix(method, "turn/")
	return
}

// commandDone is a completed command's entry with its output, and a failure's
// exit with the host's combined output tail.
func (s *Attachment) commandDone(entry ActivityEntry, item observation.Item, now time.Time) []ActivityEntry {
	CommandOutput(&entry, item, now)
	done := []ActivityEntry{entry}
	if item.ExitCode != nil && *item.ExitCode != 0 {
		exit := ActivityEntry{Seq: s.Activity().NextSequence(), Agent: entry.Agent, Kind: "exit", Text: strconv.Itoa(*item.ExitCode), CallID: entry.CallID, Observed: now}
		exit.OutputTail, exit.OutputOmit = OutputTail(item.AggregatedOutput)
		done = append(done, exit)
	}
	return done
}
