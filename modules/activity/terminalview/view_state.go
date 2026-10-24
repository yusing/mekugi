package terminalview

import (
	"cmp"
	"maps"
	"os"
	"slices"
	"time"

	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/modules/activity/presentation"
)

// Display carries application admission without host identity or a live owner.
type Display struct {
	SideCaller           string
	Caller               string
	Attachment, Revision uint64
}
type DisplayIntent struct {
	Caller               string
	Attachment, Revision uint64
}

func NewView() *View {
	v := &View{ChildReplySummary: true, Following: true, StatusLabel: "CONNECTING", Painter: Painter{CopySource: true, Theme: diffrender.EnvironmentTheme(os.Getenv("COLORFGBG"))}}
	v.Painter.Clock = v.Now
	return v
}

// Empty reports whether the current detached transcript has entries.
func (v *View) Empty() bool { return len(v.entries) == 0 }

// FreezeTranscript retains the current feed for source selection. Publications
// and output values are immutable; mutable rendering caches are independent.
func (v *View) FreezeTranscript() *View {
	frozen := *v
	now := v.Now()
	frozen.Clock = func() time.Time { return now }
	frozen.Painter.Clock = frozen.Clock
	frozen.Painter.Syntax = diffrender.Renderer{}
	frozen.Runs, frozen.QuestionRows, frozen.outputDemands = nil, nil, nil
	frozen.syntaxWindow, frozen.commandFrames, frozen.reasoningFrames = nil, nil, nil
	frozen.outputVersions, frozen.outputCache = maps.Clone(v.outputVersions), maps.Clone(v.outputCache)
	frozen.completed, frozen.collapsed = maps.Clone(v.completed), maps.Clone(v.collapsed)
	frozen.Paced, frozen.Passed, frozen.Events = maps.Clone(v.Paced), maps.Clone(v.Passed), maps.Clone(v.Events)
	frozen.RoleColors, frozen.LivePreviews = maps.Clone(v.RoleColors), maps.Clone(v.LivePreviews)
	frozen.Disclosure = maps.Clone(v.Disclosure)
	for source, annotations := range frozen.Disclosure {
		frozen.Disclosure[source] = maps.Clone(annotations)
	}
	return &frozen
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

// Update consumes immutable detached input, retaining renderer-owned state between frames.
func (v *View) Update(publication *presentation.Publication, display Display) {
	v.Display = display
	if v.publication == publication {
		return
	}
	v.publication = publication
	entries := publication.Entries
	v.LineCounts, v.NetCounts = publication.LineCounts, publication.NetCounts
	previous := make(map[uint64]uint64, len(v.entries))
	for _, entry := range v.entries {
		previous[entry.Seq] = entry.Revision
	}
	overlay := len(v.commandFrames) > 0 || len(v.reasoningFrames) > 0
	if overlay {
		entries = slices.Clone(entries)
	}
	var seenText map[observation.ItemRef]bool
	if len(v.reasoningFrames) > 0 {
		seenText = make(map[observation.ItemRef]bool)
	}
	for i, entry := range entries {
		frame := v.commandFrames[entry.Seq]
		reasoning := v.reasoningFrames[entry.TextRef]
		if reasoning != nil {
			seenText[entry.TextRef] = true
		}
		v.updateCommandFrame(entry)
		entry = v.commandEntry(entry)
		entry = v.reasoningEntry(entry)
		if overlay {
			entries[i] = entry
		}
		revision, exists := previous[entry.Seq]
		if !exists || revision != entry.Revision || frame != nil && (frame.dirty || !frame.released.IsZero()) || reasoning != nil && reasoning.dirty {
			v.observe(entry, !exists)
		}
		if reasoning != nil {
			reasoning.dirty = false
		}
		if frame != nil {
			if !frame.released.IsZero() {
				if !entry.Completed.IsZero() && !v.collapsed[entry.Seq] {
					v.completed[entry.Seq] = frame.released
				}
				delete(v.commandFrames, entry.Seq)
			} else {
				frame.dirty = false
			}
		}
		delete(previous, entry.Seq)
	}
	for ref := range v.reasoningFrames {
		if !seenText[ref] {
			delete(v.reasoningFrames, ref)
		}
	}
	for seq := range previous {
		v.forget(seq)
	}
	if len(previous) > 0 {
		v.Runs = nil
	}
	versions := make(map[outputIdentity]int)
	for _, entry := range entries {
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
	v.entries, v.roster, v.lastSeq, v.outputVersions = entries, publication.Roster, publication.LastSeq, versions
	v.trackPace(v.Now())
	v.KeepSelection()
}

func (v *View) Roster() []presentation.AgentRow {
	return slices.DeleteFunc(slices.Clone(v.roster), func(row presentation.AgentRow) bool {
		return v.Display.SideCaller != "" && row.Agent.Name == v.Display.SideCaller
	})
}
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
		if _, exists := v.completed[entry.Seq]; !exists {
			v.completed[entry.Seq] = entry.Completed
		}
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
	delete(v.commandFrames, seq)
	delete(v.completed, seq)
	delete(v.collapsed, seq)
	delete(v.Passed, seq)
	delete(v.Paced, seq)
	v.Disclosure.Remove(seq)
	v.InvalidateRuns(seq)
}

func (v *View) InvalidateRuns(seq uint64) {
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
