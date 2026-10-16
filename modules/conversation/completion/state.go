// Package completion owns draft targets and query results, without search effects.
// Source: apps/mekugi/internal/router/app_server_picker.go:24:74,163:182,398:405
// and app_server_skills.go:70:96,204:210@[0a356401] composer completion state.
package completion

import "slices"

type Target struct {
	Kind       byte
	Start, End int
	Query      string
}

type Choice struct {
	Name, Path, Description, Display string
	Enabled                          bool
}

func (c Choice) Label() string {
	if c.Display != "" {
		return c.Display
	}
	return c.Name
}

// State belongs to the attached composer. Pending retains query identity only;
// the application owns requests, scan cancellation, and skill-write correlation.
type State struct {
	Target, Dismissed Target
	Open              bool
	Choices           []Choice
	Selected          int
	Loading           bool
	Problem           string
	Pending           *Target
	PendingCwd        string
	Resolved          Target
	ResolvedCwd       string
	Skills            []Choice
	SkillsCwd         string
	SkillsLoaded      bool
	SkillsProblem     string
	Query             string
	Initial           map[string]bool
}

// Activate keeps a dismissed token closed until its exact target changes.
func (p *State) Activate(target Target, bindingOnly bool) bool {
	if target != p.Dismissed {
		p.Dismissed = Target{}
	}
	if target.Kind == 0 || target == p.Dismissed {
		p.Open = false
		return false
	}
	if target != p.Target {
		p.Target, p.Choices, p.Problem = target, nil, ""
		p.Resolved = Target{}
	}
	if !p.Open && !bindingOnly {
		p.Resolved, p.Problem = Target{}, ""
	}
	p.Open = true
	return true
}

func (p *State) Resolve(target Target, cwd, activeCwd string, choices []Choice, problem string) bool {
	if target != p.Target || cwd != activeCwd {
		return false
	}
	p.Choices, p.Loading, p.Problem = choices, false, problem
	p.Selected = min(p.Selected, max(0, len(choices)-1))
	p.Resolved, p.ResolvedCwd = target, cwd
	return true
}

func (p *State) Catalog(choices []Choice, cwd string, available bool, problem string) {
	p.Skills, p.SkillsCwd, p.SkillsLoaded = choices, cwd, available
	p.SkillsProblem = ""
	if !available {
		p.SkillsProblem = problem
	}
}

func (p *State) RememberSkills() {
	p.Initial = make(map[string]bool)
	for _, skill := range p.Skills {
		p.Initial[skill.Path] = skill.Enabled
	}
}

func (p *State) SkillChanges() (enabled, disabled int) {
	for _, skill := range p.Skills {
		if before, ok := p.Initial[skill.Path]; ok && before != skill.Enabled {
			if skill.Enabled {
				enabled++
			} else {
				disabled++
			}
		}
	}
	return
}

func (p *State) SkillSaved(path string, enabled bool) {
	for i := range p.Skills {
		if p.Skills[i].Path == path {
			p.Skills[i].Enabled = enabled
		}
	}
	p.Problem = ""
}

type View struct {
	Target         Target
	Choices        []Choice
	Selected       int
	Loading        bool
	Problem, Query string
}

func (p *State) View() View {
	return View{p.Target, slices.Clone(p.Choices), p.Selected, p.Loading, p.Problem, p.Query}
}
