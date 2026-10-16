package presentation

import (
	"path/filepath"
	"slices"
	"strings"
)

// Source: internal/router/live_activity_skills.go:12:133@[ebde9ad5].
type SkillSet []string

func (s SkillSet) With(names ...string) SkillSet {
	for _, name := range names {
		if !slices.Contains(s, name) {
			s = append(s, name)
		}
	}
	return s
}

// The revision cache avoids rescanning unchanged Activity on every paint.
func (v *Model[S, A]) ActiveSkills() map[string]SkillSet {
	if v.skills != nil && v.skillsRevision == v.Revision {
		return v.skills
	}
	sets := make(map[string]SkillSet, len(v.retiredSkills))
	for owner, names := range v.retiredSkills {
		sets[owner] = slices.Clone(names)
	}
	foldSkills(sets, v.Entries, v.skillHistory)
	for owner, known := range v.skillHistory {
		if !known {
			delete(sets, owner)
		}
	}
	v.skills, v.skillsRevision = sets, v.Revision
	return sets
}

func (v *Model[S, A]) retireSkills(records []Record[S]) {
	if v.retiredSkills == nil {
		v.retiredSkills = make(map[string]SkillSet)
	}
	foldSkills(v.retiredSkills, records, v.skillHistory)
}

func (v *Model[S, A]) RenameRetiredSkills(old, name string) {
	if known, found := v.skillHistory[old]; found {
		delete(v.skillHistory, old)
		v.skillHistory[name] = known
	}
	if names := v.retiredSkills[old]; names != nil {
		v.retiredSkills[name] = v.retiredSkills[name].With(names...)
		delete(v.retiredSkills, old)
	}
	v.skills = nil
}

// A paginated owner's complete context scan owns historical loads. Live
// boundaries clear its set; Activity retention and older pages do not.
func foldSkills[S Source](sets map[string]SkillSet, records []Record[S], history map[string]bool) {
	for _, record := range records {
		facts := record.Source.ActivityFacts()
		owner := facts.Agent
		if _, restored := history[owner]; restored && facts.Restored {
			continue
		}
		if facts.ContextBoundary {
			delete(sets, owner)
			if _, restored := history[owner]; restored {
				history[owner] = true
			}
		} else if names := SkillLoads(record); len(names) > 0 {
			sets[owner] = sets[owner].With(names...)
		}
	}
}

// Only confirmed reads and submitted attachment receipts are load evidence.
func SkillLoads[S Source](record Record[S]) []string {
	facts := record.Source.ActivityFacts()
	if facts.Running {
		return nil
	}
	unconfirmed := facts.Unconfirmed || slices.ContainsFunc(record.Values, func(b Value) bool { return b.BatchExit && b.ExitCode != 0 })
	var names []string
	for _, block := range record.Values {
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

// MarkSkillHistoryUnknown hides partial context until history or a live reset
// establishes the agent's complete current context.
func (v *Model[S, A]) MarkSkillHistoryUnknown(name string) {
	if v.skillHistory == nil {
		v.skillHistory = make(map[string]bool)
	}
	v.skillHistory[name] = false
	v.skills = nil
}

// NeedsSkillHistory observes pending live resets before admitting an older scan.
func (v *Model[S, A]) NeedsSkillHistory(name string) bool {
	v.ActiveSkills()
	return !v.skillHistory[name]
}

// RestoreSkills adopts completed context evidence unless a newer live reset won.
func (v *Model[S, A]) RestoreSkills(name string, names SkillSet) {
	if v.NeedsSkillHistory(name) {
		if v.retiredSkills == nil {
			v.retiredSkills = make(map[string]SkillSet)
		}
		if v.skillHistory == nil {
			v.skillHistory = make(map[string]bool)
		}
		v.retiredSkills[name] = names.With(v.retiredSkills[name]...)
		v.skillHistory[name] = true
	}
	v.skills = nil
}
