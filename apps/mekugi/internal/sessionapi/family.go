package sessionapi

import (
	"cmp"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	changes "github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/modules/livepreview/preview"
	threadcontrol "github.com/yusing/mekugi/modules/threadcontrol/control"
)

// Family is shared navigation, independent of the selected runtime's transcript.
// Threads includes attachable admitted cold targets, but never history-only roster rows.
type Family struct {
	Live      []preview.Preview         `json:"-"`
	LiveScope preview.Scope             `json:"-"`
	Root      string                    `json:"root"`
	Threads   []string                  `json:"threads"`
	Agents    *activity.Publication     `json:"agents"`
	Status    sessionpresentation.Label `json:"status"`
}

// Family borrows ordinary raw publications in the selected logical family.
// A runtime's lifetime parent does not grant membership after thread switching.
func (h *Hub) Family(thread string) (*Family, *changes.ReviewView) {
	if thread == "" {
		return nil, nil
	}
	h.mu.Lock()
	var selected *managedSession
	if session := h.sessions[thread]; session != nil {
		if frame := session.server.frame.Load(); frame != nil && frame.Session.Thread == thread {
			selected = session.runtime
		}
	}
	if selected == nil {
		// Initial publication precedes asynchronous list registration. The
		// admitted runtime already owns its confirmed raw thread identity.
		for runtime := range h.running {
			if frame := runtime.server.frame.Load(); frame != nil && frame.Session.Thread == thread {
				selected = runtime
				break
			}
		}
	}
	if selected == nil {
		h.mu.Unlock()
		return nil, nil
	}
	selectedFrame := selected.server.frame.Load()
	if selectedFrame == nil {
		h.mu.Unlock()
		return nil, nil
	}
	familyRoot := cmp.Or(selectedFrame.FamilyRoot, selectedFrame.Session.Thread)
	frames := make(map[string]familySource)
	active := make(map[string]bool)
	for runtime := range h.running {
		if frame := runtime.server.frame.Load(); frame != nil && frame.Session.Thread != "" {
			logical := cmp.Or(frame.FamilyRoot, frame.Session.Thread)
			active[logical] = true
			if logical == familyRoot {
				frames[frame.Session.Thread] = familySource{Frame: frame, owner: runtime}
			}
		}
	}
	h.mu.Unlock()
	rootFrame := frames[familyRoot].Frame
	if rootFrame == nil {
		// Main may have switched logical threads while its former children keep
		// working. Borrow only their admitted coordination metadata, never the
		// replacement Main's transcript, status or review.
		metadata := selectedFrame
		for _, frame := range frames {
			if frame.Session.Thread < metadata.Session.Thread {
				metadata = frame.Frame
			}
		}
		rootFrame = &Frame{Session: Session{Thread: familyRoot, Caller: "/root"}, Members: metadata.Members,
			Status: sessionpresentation.Label{Text: "Main unavailable"}}
	}
	cache := &h.familyProjection
	cache.mu.Lock()
	if cache.groups == nil {
		cache.groups = make(map[string]*familyState)
	}
	for id := range cache.groups {
		if !active[id] {
			delete(cache.groups, id)
		}
	}
	state := cache.groups[familyRoot]
	if state == nil {
		state = &familyState{}
		cache.groups[familyRoot] = state
	}
	cache.mu.Unlock()
	return state.project(rootFrame, frames, selectedFrame.Session.Workspace, &cache.revision)
}

// Retain only active logical families across the admitted runtime registry.
// One revision sequence prevents identity reuse when a family is replaced.
type familyProjection struct {
	mu       sync.Mutex
	groups   map[string]*familyState
	revision atomic.Uint64
}

// Source identity/revision checks precede roster cloning and composition.
type familyState struct {
	mu      sync.Mutex
	sources map[string]familySource
	members []threadcontrol.Participant
	family  *Family
	review  *changes.ReviewView
	diffs   map[string]*changes.ReviewView
}

// Review revisions are local to a runtime's attached capture owner. Thread IDs
// survive runtime replacement and cannot identify that revision sequence.
type familySource struct {
	*Frame
	owner *managedSession
}

func (p *familyState) project(root *Frame, frames map[string]familySource, workspace string, revision *atomic.Uint64) (*Family, *changes.ReviewView) {
	p.mu.Lock()
	defer p.mu.Unlock()
	rosterSame := p.family != nil && p.family.Root == root.Session.Thread && len(p.sources) == len(frames) && slices.Equal(p.members, root.Members)
	reviewSame := p.family != nil && len(p.sources) == len(frames)
	liveSame := reviewSame
	for id, frame := range frames {
		old := p.sources[id]
		liveSame = liveSame && old.Frame != nil && old.owner == frame.owner && old.Attachment == frame.Attachment && reflect.DeepEqual(old.Live, frame.Live) && reflect.DeepEqual(old.LiveScope, frame.LiveScope)
		rosterSame = rosterSame && old.Frame != nil && old.Agents == frame.Agents &&
			old.Session.Caller == frame.Session.Caller && old.Session.Role == frame.Session.Role
		reviewSame = reviewSame && old.Frame != nil && old.owner == frame.owner && old.Attachment == frame.Attachment && sameFamilyReview(old.Review, frame.Review)
	}
	if !reviewSame {
		// Source: apps/mekugi/internal/launch/native_children.go:119:157@ab359afb publishChild.
		var chunks []changes.Chunk
		seen := make(map[string]bool)
		var combined *changes.ReviewView
		ids := slices.Sorted(maps.Keys(frames))
		// Root coverage and failure have priority without making root evidence a
		// prerequisite for a child's admitted saved changes.
		ids = append([]string{root.Session.Thread}, slices.DeleteFunc(ids, func(id string) bool { return id == root.Session.Thread })...)
		for _, id := range ids {
			frame := frames[id]
			if frame.Frame == nil || frame.Review == nil {
				continue
			}
			view := frame.Review
			if combined == nil {
				combined = new(*view)
			}
			if combined.Failure == "" {
				combined.Failure = view.Failure
			}
			for _, file := range view.Files {
				for _, chunk := range file.Chunks {
					key := chunk.Workspace + "\x00" + chunk.Key
					if !seen[key] {
						seen[key] = true
						chunks = append(chunks, chunk)
					}
				}
			}
		}
		if combined != nil {
			combined.Publication = combined.Publication.With(changes.GroupCaptures(chunks))
			combined.Revision = revision.Add(1)
		}
		p.review, p.diffs = combined, make(map[string]*changes.ReviewView)
	}
	if !rosterSame {
		family := &Family{Root: root.Session.Thread, Agents: familyRoster(root, frames)}
		if p.family != nil {
			family.Live, family.LiveScope = p.family.Live, p.family.LiveScope
		}
		p.family = family
		admitted := make(map[string]bool, len(frames)+len(root.Members))
		for id := range frames {
			admitted[id] = true
		}
		parents := make(map[string]string, len(root.Members))
		for _, member := range root.Members {
			parents[member.Thread] = member.Parent
		}
		for _, member := range root.Members {
			ancestor := member.Parent
			for range len(parents) {
				if ancestor == "" {
					break
				}
				if frames[ancestor].Frame != nil {
					admitted[member.Thread] = true
					break
				}
				ancestor = parents[ancestor]
			}
		}
		for _, row := range family.Agents.Roster {
			if row.Agent.NonAttachable {
				delete(admitted, row.Agent.Thread)
			}
		}
		p.family.Threads = slices.Sorted(maps.Keys(admitted))
	}
	if !liveSame || !reviewSame || !rosterSame || p.family.Status != root.Status {
		family := *p.family
		family.Status = root.Status
		if !liveSame {
			previous := family.Live
			family.Live = nil
			family.LiveScope = preview.Scope{Workspaces: make(map[string]map[string]bool)}
			current := make(map[string]preview.Preview)
			local := make(map[string]bool)
			var order []string
			for _, id := range slices.Sorted(maps.Keys(frames)) {
				source := frames[id]
				for workspace, threads := range source.LiveScope.Workspaces {
					if family.LiveScope.Workspaces[workspace] == nil {
						family.LiveScope.Workspaces[workspace] = make(map[string]bool)
					}
					maps.Copy(family.LiveScope.Workspaces[workspace], threads)
				}
				for _, live := range source.Live {
					if id == root.Session.Thread {
						local[live.ID] = true
					}
					if _, seen := current[live.ID]; !seen {
						current[live.ID] = live
						order = append(order, live.ID)
					}
				}
			}
			// Main's own source stays accessible even when children fill their
			// separate window. Keep surviving cards in place before filling slots.
			total, children := len(current), 0
			admit := func(id string) {
				live, ok := current[id]
				if !ok || !local[id] && children == preview.MaxPreviews {
					return
				}
				if !local[id] {
					children++
				}
				family.Live = append(family.Live, live)
				delete(current, id)
			}
			for _, old := range previous {
				admit(old.ID)
			}
			for _, id := range order {
				admit(id)
			}
			if omitted := total - len(family.Live); omitted > 0 {
				last := &family.Live[len(family.Live)-1]
				if last.Footer != "" {
					last.Footer += " · "
				}
				last.Footer += fmt.Sprintf("%d more live edits; select an agent to inspect", omitted)
			}
		}
		if !reviewSame || !rosterSame {
			roster := *family.Agents
			if p.review != nil {
				roster.LineCounts, roster.NetCounts = p.review.Counts.Callers, p.review.Counts.Net
			} else if root.Agents != nil {
				roster.LineCounts, roster.NetCounts = root.Agents.LineCounts, root.Agents.NetCounts
			}
			family.Agents = &roster
		}
		p.family = &family
	}
	p.sources, p.members = frames, root.Members
	if p.review == nil {
		return p.family, nil
	}
	if diff := p.diffs[workspace]; diff != nil {
		return p.family, diff
	}
	diff := *p.review
	diff.Workspace = workspace
	diff.Publication = diff.Publication.ForDiff(workspace)
	// Workspace changes discard the obsolete selected view; this cache is
	// bounded by currently published runtime workspaces, not selection history.
	for cached := range p.diffs {
		current := false
		for _, frame := range frames {
			current = current || frame.Session.Workspace == cached
		}
		if !current {
			delete(p.diffs, cached)
		}
	}
	p.diffs[workspace] = &diff
	return p.family, &diff
}

func sameFamilyReview(a, b *changes.ReviewView) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Revision == b.Revision && a.Workspace == b.Workspace && a.Coverage == b.Coverage && a.Failure == b.Failure
}

func familyRoster(root *Frame, frames map[string]familySource) *activity.Publication {
	publication := &activity.Publication{}
	rows := make(map[string]int)
	add := func(row activity.AgentRow) {
		key := cmp.Or(row.Agent.Thread, row.Agent.Name)
		if index, exists := rows[key]; exists {
			publication.Roster[index].Agent.NonAttachable = publication.Roster[index].Agent.NonAttachable || row.Agent.NonAttachable
			return
		}
		rows[key] = len(publication.Roster)
		publication.Roster = append(publication.Roster, row)
	}
	if root.Agents != nil {
		publication.MainSummary = root.Agents.MainSummary
		for _, row := range root.Agents.Roster {
			add(row)
		}
	}
	members := make(map[string]threadcontrol.Participant, len(root.Members))
	for _, member := range root.Members {
		members[member.Thread] = member
		if member.Thread == "" {
			continue
		}
		add(activity.AgentRow{Agent: activity.Agent{AgentFacts: activity.AgentFacts{Thread: member.Thread}}})
		row := &publication.Roster[rows[member.Thread]]
		row.Agent.Name, row.Agent.Role = member.Task, member.Role
		row.Depth = max(strings.Count(member.Task, "/")-1, 0)
	}
	for _, id := range slices.Sorted(maps.Keys(frames)) {
		frame := frames[id]
		if frame.Agents != nil {
			for _, row := range frame.Agents.Roster {
				if row.Agent.Thread == id || row.Agent.Name != "/root" && row.Agent.Name != "Main" {
					add(row)
				}
			}
		}
		add(activity.AgentRow{Agent: activity.Agent{AgentFacts: activity.AgentFacts{Thread: id}}})
		row := &publication.Roster[rows[id]]
		name := cmp.Or(frame.Session.Caller, members[id].Task, row.Agent.Name)
		if id == root.Session.Thread {
			name = "/root"
		}
		role := cmp.Or(frame.Session.Role, members[id].Role, row.Agent.Role)
		if frame.Agents != nil {
			for _, own := range frame.Agents.Roster {
				if own.Agent.Thread == id {
					own.Agent.NonAttachable = own.Agent.NonAttachable || row.Agent.NonAttachable
					row.Agent = own.Agent
					break
				}
			}
		}
		row.Agent.Name, row.Agent.Role = name, role
		row.Depth = max(strings.Count(name, "/")-1, 0)
		if id != root.Session.Thread {
			row.Agent.Summary = familySummary(row.Agent.Summary, name)
		}
	}
	// Runtime publications enrich activity and usage; admitted logical work
	// owns lifecycle even when its host turn has stopped or has not started yet.
	for _, member := range root.Members {
		if member.Thread == "" {
			continue
		}
		row := &publication.Roster[rows[member.Thread]]
		row.Agent.NonAttachable = member.NonAttachable
		row.Agent.Responding = member.Status == threadcontrol.Working || member.Status == threadcontrol.Pending
		row.Agent.Waiting = member.Status == threadcontrol.Waiting
		row.Agent.Paused = member.Status == threadcontrol.Paused || member.Status == threadcontrol.Uncertain
		row.Agent.Final = member.Status == threadcontrol.Completed
		if member.Status == threadcontrol.Failed {
			row.Agent.LatestKind = "error"
		} else if row.Agent.LatestKind == "error" {
			row.Agent.LatestKind = ""
		}
		row.Agent.Status = row.Agent.ObservedStatus(row.Agent.LatestKind)
	}
	if frames[root.Session.Thread].Frame == nil {
		if index, exists := rows[root.Session.Thread]; exists {
			main := &publication.Roster[index].Agent
			main.Responding, main.Waiting, main.Paused = false, false, false
			main.Status = "Unavailable"
		}
	}
	return publication
}

// Source: apps/mekugi/internal/launch/native_children.go:172:201@ab359afb childEntries.
// Relabel only presentation aliases; source identities remain at their owner.
func familySummary(entries []activity.Entry, caller string) []activity.Entry {
	entries = slices.Clone(entries)
	for i := range entries {
		entry := &entries[i]
		if entry.Agent == "Main" || entry.Agent == "/root" {
			entry.Agent = caller
		}
		entry.Values = activity.CloneValues(entry.Values)
		var relabel func([]activity.Value)
		relabel = func(values []activity.Value) {
			for j := range values {
				value := &values[j]
				for _, label := range []*string{&value.Owner, &value.From} {
					if *label == "Main" || *label == "/root" {
						*label = caller
					}
				}
				if value.To == "Main" {
					value.To = caller
				}
				relabel(value.Members)
				relabel(value.RequestedOperations)
			}
		}
		relabel(entry.Values)
	}
	return entries
}
