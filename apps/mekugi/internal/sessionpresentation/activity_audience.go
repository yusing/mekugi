package sessionpresentation

import (
	"slices"
)

// Source: apps/mekugi/internal/router/app_server_ui.go:1088:1162@45fbe279
// ActivityOwner names the Activity agent an entry belongs to. A directed
// message shows under its sender, or under its recipient when Main sent it.
func ActivityOwner(entry ActivityEntry) string {
	if entry.Kind == "reply" && entry.Message != nil {
		if entry.Message.From == "/root" || entry.Message.From == "Main" {
			return entry.Message.To
		}
		return entry.Message.From
	}
	return entry.Agent
}

// MainActivityEntry projects one collector entry into Main's audience.
// Ordinary root activity stays in Main, but a directed message belongs at
// both ends, and a child's start and answer show where Main follows them.
func MainActivityEntry(entry ActivityEntry) (ActivityEntry, bool) {
	main := entry.Agent == "/root" && (entry.Kind == "compaction" || entry.Kind == "compact_reload" || entry.Kind == "output_dedupe" || entry.Kind == "tool" || entry.Kind == "hook" || entry.Kind == "exit" || entry.Kind == "error" || entry.Kind == "reasoning" || entry.Kind == "progress")
	if (entry.Kind == "assignment" || entry.Kind == "start") && entry.Assignment != nil {
		main = true
		entry.Agent = entry.Assignment.From
		if entry.Agent == "" {
			entry.Agent = "Main"
		}
	}
	if entry.Kind == "reply" && entry.Message != nil {
		main = entry.Message.From == "/root" || entry.Message.From == "Main" || entry.Message.To == "/root" || entry.Message.To == "Main"
		entry.Agent = entry.Message.From
	}
	main = main || (entry.Kind == "final" || entry.Kind == "start") && entry.Agent != "/root"
	// A received envelope's sender is global, while Main names this runtime.
	incoming := entry.Message != nil && entry.Message.To == "Main" || entry.Assignment != nil && entry.Assignment.To == "Main"
	if entry.Agent == "/root" && !incoming {
		entry.Agent = "Main"
	}
	return entry, main
}

// MainActivityLinked reports a Main entry that excerpts its Activity entry.
func MainActivityLinked(entry ActivityEntry) bool {
	return entry.Kind == "final" || entry.Kind == "reply" || entry.Assignment != nil
}

func (v *ActivitySource) ApplyMain(entry ActivityEntry) {
	entry.Seq = v.LastSeq + 1
	v.Apply(ActivityEvent{Entries: []ActivityEntry{entry}})
}

// applyActivity projects the collector once into the two audiences. Ordinary
// root activity stays in Main, but a directed message belongs at both ends.
func ApplyActivity(main, children *ActivitySource, entries []ActivityEntry, agents []ActivityAgent) {
	paneEntries := slices.Clone(entries)
	for i := range paneEntries {
		paneEntries[i].Agent = ActivityOwner(paneEntries[i])
	}
	children.Apply(ActivityEvent{Entries: paneEntries, Agents: agents})
	for i, entry := range entries {
		if entry.Native != nil && children.OutsideMain(entry.Native.Thread) {
			continue
		}
		if entry, admitted := MainActivityEntry(entry); admitted {
			if MainActivityLinked(entry) {
				entry.ActivitySeq = children.EntrySeq(paneEntries[i])
			}
			main.ApplyMain(entry)
		}
	}
}

// OutsideMain reads the published host identity, never a display-name prefix.
func (v *ActivitySource) OutsideMain(thread string) bool {
	for _, agent := range v.Agents {
		if agent.Thread == thread {
			return agent.Isolated || agent.AudiencePending
		}
	}
	return false
}
