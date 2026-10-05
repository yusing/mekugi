package router

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

type activeSkillSet []string

func (s activeSkillSet) with(names ...string) activeSkillSet {
	for _, name := range names {
		if !slices.Contains(s, name) {
			s = append(s, name)
		}
	}
	return s
}

func (s activeSkillSet) label() string {
	if len(s) == 1 {
		return "1 active skill"
	}
	return fmt.Sprintf("%d active skills", len(s))
}

// The revision cache avoids rescanning unchanged Activity on every paint.
func (v *liveActivityView) activeSkills() map[string]activeSkillSet {
	if v.skills != nil && v.skills.revision == v.revision {
		return v.skills.sets
	}
	sets := make(map[string]activeSkillSet, len(v.retiredSkills))
	for owner, names := range v.retiredSkills {
		sets[owner] = slices.Clone(names)
	}
	foldSkills(sets, v.entries, v.skillHistory)
	for owner, known := range v.skillHistory {
		if !known {
			delete(sets, owner)
		}
	}
	v.skills = &liveActivitySkills{v.revision, sets}
	return sets
}

func (v *liveActivityView) retireSkills(records []liveActivityRecord) {
	if v.retiredSkills == nil {
		v.retiredSkills = make(map[string]activeSkillSet)
	}
	foldSkills(v.retiredSkills, records, v.skillHistory)
}

func (v *liveActivityView) renameRetiredSkills(old, name string) {
	if known, found := v.skillHistory[old]; found {
		delete(v.skillHistory, old)
		v.skillHistory[name] = known
	}
	if names := v.retiredSkills[old]; names != nil {
		v.retiredSkills[name] = v.retiredSkills[name].with(names...)
		delete(v.retiredSkills, old)
	}
	v.skills = nil
}

// A paginated owner's complete context scan owns historical loads. Live
// boundaries clear its set; Activity retention and older pages do not.
func foldSkills(sets map[string]activeSkillSet, records []liveActivityRecord, history map[string]bool) {
	for _, record := range records {
		owner := record.Agent
		if _, restored := history[owner]; restored && record.native != nil && !record.native.live {
			continue
		}
		if contextBoundary(record.activityPaneEntry) {
			delete(sets, owner)
			if _, restored := history[owner]; restored {
				history[owner] = true
			}
		} else if names := skillLoads(record); len(names) > 0 {
			sets[owner] = sets[owner].with(names...)
		}
	}
}

type liveActivitySkills struct {
	revision uint64
	sets     map[string]activeSkillSet
}

func contextBoundary(entry activityPaneEntry) bool {
	switch entry.Kind {
	case "progress":
		return entry.Text == "Context compacted" || entry.Text == "Context reset from journal"
	case "journal_event":
		return entry.journalEvent != nil && entry.journalEvent.Op == "reset"
	}
	return false
}

// Only confirmed reads and submitted attachment receipts are load evidence.
func skillLoads(record liveActivityRecord) []string {
	entry := record.activityPaneEntry
	if entry.native != nil && entry.native.running {
		return nil
	}
	unconfirmed := entry.native.unconfirmed() || slices.ContainsFunc(record.blocks, func(b activityui.Block) bool { return b.BatchExit && b.ExitCode != 0 })
	var names []string
	for _, block := range record.blocks {
		if block.Running || block.Skipped || block.ExitCode != 0 || unconfirmed && !block.Segment {
			continue
		}
		if block.Kind == "op" && block.Verb == "Attached skill" && block.Label != "" {
			names = append(names, block.Label)
		}
		if block.Kind != "reads" || block.Verb != "Skill" && block.Verb != "Read" {
			continue
		}
		for _, read := range block.Reads {
			if block.Verb == "Skill" {
				for _, field := range strings.Fields(read.Path) {
					if !strings.HasPrefix(field, "-") {
						name, _, _ := strings.Cut(field, "/")
						names = append(names, name)
						break
					}
				}
			} else if filepath.Base(read.Path) == "SKILL.md" && filepath.Dir(read.Path) != "." {
				names = append(names, filepath.Base(filepath.Dir(read.Path)))
			}
		}
	}
	return names
}

func (u *terminalUI) openSkills(view *liveActivityView, agent string) bool {
	names := slices.Clone(view.activeSkills()[agent])
	if len(names) == 0 {
		return false
	}
	slices.Sort(names)
	u.openBlocks(view, []activityui.Block{{Kind: "op", Verb: "Skill", Label: names.label(), Body: "- " + strings.Join(names, "\n- ")}})
	return true
}
