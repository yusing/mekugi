package terminalview

import (
	"cmp"
	"os"
	"slices"
	"time"

	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/modules/activity/presentation"
)

// Display carries application admission without host identity or a live owner.
type Display struct {
	Caller               string
	Attachment, Revision uint64
}
type DisplayIntent struct {
	Caller               string
	Attachment, Revision uint64
}

func NewView() *View {
	v := &View{Following: true, StatusLabel: "CONNECTING", Painter: Painter{CopySource: true, Theme: diffrender.EnvironmentTheme(os.Getenv("COLORFGBG"))}}
	v.Painter.Clock = v.Now
	return v
}

func (v *View) Now() time.Time {
	if v.Clock != nil {
		return v.Clock()
	}
	return time.Now()
}

func (v *View) displayingChild() bool   { return v.Display.Caller != "" && v.Display.Caller != "/root" }
func (v *View) displayedCaller() string { return v.Display.Caller }
func (v *View) requestDisplay(caller string) {
	if caller != "" && caller != "/root" {
		v.Selected = caller
	}
	v.DisplayRequest = &DisplayIntent{Caller: cmp.Or(caller, "/root"), Attachment: v.Display.Attachment, Revision: v.Display.Revision}
}

// Update replaces detached input, retaining only renderer-owned state between frames.
func (v *View) Update(publication presentation.Publication, display Display) {
	v.LineCounts, v.NetCounts = publication.LineCounts, publication.NetCounts
	previous := make(map[uint64]uint64, len(v.entries))
	for _, entry := range v.entries {
		previous[entry.Seq] = entry.Revision
	}
	v.Display = display
	for _, entry := range publication.Entries {
		revision, exists := previous[entry.Seq]
		if !exists || revision != entry.Revision {
			v.observe(entry, !exists)
		}
		delete(previous, entry.Seq)
	}
	for seq := range previous {
		v.forget(seq)
	}
	if len(previous) > 0 {
		v.Runs = nil
	}
	versions := make(map[outputIdentity]int)
	for _, entry := range publication.Entries {
		for output, version := range entry.Outputs {
			versions[outputIdentity{entry.Seq, output}] = version
		}
	}
	for key := range v.outputCache {
		if version, exists := versions[outputIdentity{key.Source, key.Output}]; !exists || version != key.Version {
			delete(v.outputCache, key)
			v.InvalidateRuns(key.Source)
		}
	}
	if !slices.EqualFunc(v.roster, publication.Roster, func(a, b presentation.AgentRow) bool {
		return a.Agent.Name == b.Agent.Name && a.Agent.Role == b.Agent.Role
	}) {
		v.Runs = nil
		v.Hovered = ""
	}
	v.entries, v.roster, v.lastSeq, v.Display, v.outputVersions = publication.Entries, publication.Roster, publication.LastSeq, display, versions
	v.trackPace(v.Now())
	v.KeepSelection()
}

func (v *View) Roster() []presentation.AgentRow { return slices.Clone(v.roster) }
func (v *View) runRevision(first, last int) (revision uint64) {
	for _, entry := range v.entries[first:last] {
		revision = max(revision, entry.Revision)
	}
	return
}
func (v *View) blocks(index int) []Block {
	entry := v.entries[index]
	blocks := v.Disclosure.Blocks(entry.Values)
	if v.collapsed[entry.Seq] {
		for i := range blocks {
			if blocks[i].Collapsible() {
				blocks[i].Collapsed = true
			}
		}
	}
	return blocks
}

// observe updates terminal deadlines without mutating the source record.
func (v *View) observe(entry presentation.Entry, appended bool) {
	blocks := Disclosure(nil).Blocks(entry.Values)
	for i, block := range blocks {
		blocks[i].Collapsed = entry.Kind == "reasoning" && block.Collapsible() && !(entry.Live && entry.ItemComplete) ||
			entry.Kind == "attachments" && block.ReadOutput() ||
			(entry.Kind == "tool" || block.Segment) && block.ReadOutput() && block.Collapsible()
	}
	measureGroups(blocks)
	v.Disclosure.Keep(entry.Seq, blocks)
	if entry.Restored && entry.OutputComplete {
		if v.collapsed == nil {
			v.collapsed = make(map[uint64]bool)
		}
		v.collapsed[entry.Seq] = true
	}
	if v.collapsed[entry.Seq] {
		for i, value := range entry.Values {
			if (Block{Value: value}).Collapsible() {
				v.Disclosure.Collapse(entry.Seq, i)
			}
		}
	}
	if !entry.Completed.IsZero() && !v.collapsed[entry.Seq] {
		if v.completed == nil {
			v.completed = make(map[uint64]time.Time)
		}
		v.completed[entry.Seq] = entry.Completed
	}
	if appended && entry.Standalone() {
		if v.Events == nil {
			v.Events = make(map[string]Event)
		}
		v.Events[entry.Agent] = Event{entry.Seq, cmp.Or(entry.Observed, v.Now())}
	}
	if appended && !v.Following && v.Visible(entry) {
		v.Unseen++
	}
	v.InvalidateRuns(entry.Seq)
}

func (v *View) forget(seq uint64) {
	delete(v.completed, seq)
	delete(v.collapsed, seq)
	delete(v.Passed, seq)
	delete(v.Paced, seq)
	v.Disclosure.Remove(seq)
	v.InvalidateRuns(seq)
}

func (v *View) InvalidateRuns(seq uint64) {
	if v.HistoryOrder {
		v.Runs = nil
		return
	}
	for key := range v.Runs {
		if seq <= key.Last && (v.Conversation || key.First <= seq) {
			delete(v.Runs, key)
		}
	}
}

// Settle shares one quiet-period deadline across Main and child terminal views.
func Settle(now time.Time, views ...*View) bool {
	var deadline time.Time
	for _, v := range views {
		for _, entry := range v.entries {
			completed := v.completed[entry.Seq]
			next := v.Events[entry.Agent]
			if !completed.IsZero() && next.Seq > entry.Seq {
				deadline = maxTime(deadline, maxTime(completed, next.At).Add(OutputDebounce))
			}
		}
	}
	if now.Before(deadline) {
		return false
	}
	changed := false
	for _, v := range views {
		for index, entry := range v.entries {
			if v.completed[entry.Seq].IsZero() || v.Events[entry.Agent].Seq <= entry.Seq {
				continue
			}
			delete(v.completed, entry.Seq)
			if v.collapsed == nil {
				v.collapsed = make(map[uint64]bool)
			}
			v.collapsed[entry.Seq] = true
			for i, block := range v.blocks(index) {
				if block.Collapsible() {
					v.Disclosure.Collapse(entry.Seq, i)
					changed = true
				}
			}
			v.InvalidateRuns(entry.Seq)
		}
	}
	return changed
}
