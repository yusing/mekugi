package router

import (
	json "encoding/json/v2"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/yusing/mekugi/internal/appserver"
)

// Source: codex-rs/tui/src/chatwidget/skills.rs:29:136 and
// bottom_pane/skills_toggle_view.rs:98:299@86be5320b068ef67b56348b02aa8c33706955da6.
func (u *appServerUI) filterSkills(query string) {
	p := &u.picker
	selected := ""
	if p.selected < len(p.choices) {
		selected = p.choices[p.selected].path
	}
	p.choices = nil
	for _, skill := range p.skills {
		if !skill.enabled && p.modal != "manage" {
			continue
		}
		_, displayMatch := pickerMatchScore(skill.label(), query)
		_, nameMatch := pickerMatchScore(skill.name, query)
		if displayMatch || nameMatch {
			p.choices = append(p.choices, skill)
		}
	}
	if query != "" || p.modal != "manage" {
		slices.SortStableFunc(p.choices, func(a, b composerChoice) int {
			ax, am := pickerMatchScore(a.label(), query)
			bx, bm := pickerMatchScore(b.label(), query)
			if !am {
				ax, _ = pickerMatchScore(a.name, query)
			}
			if !bm {
				bx, _ = pickerMatchScore(b.name, query)
			}
			if p.modal != "manage" && am != bm {
				if am {
					return -1
				}
				return 1
			}
			if ax != bx {
				return ax - bx
			}
			return strings.Compare(a.label(), b.label())
		})
	}
	if p.modal == "manage" {
		p.selected = max(0, slices.IndexFunc(p.choices, func(c composerChoice) bool { return c.path == selected }))
	}
	p.selected = min(p.selected, max(0, len(p.choices)-1))
}

func (u *appServerUI) rememberSkillState() {
	u.picker.initial = make(map[string]bool)
	for _, skill := range u.picker.skills {
		u.picker.initial[skill.path] = skill.enabled
	}
}

func (u *appServerUI) closeSkillsModal() {
	p := &u.picker
	if p.modal == "manage" {
		enabled, disabled := 0, 0
		for _, skill := range p.skills {
			if before, ok := p.initial[skill.path]; ok && before != skill.enabled {
				if skill.enabled {
					enabled++
				} else {
					disabled++
				}
			}
		}
		if enabled+disabled > 0 {
			u.setNotice(fmt.Sprintf("%d skills enabled, %d skills disabled", enabled, disabled), false)
		}
		p.skillsLoaded = false // Codex reloads the catalog after leaving management.
	}
	p.modal, p.open, p.query, p.initial = "", false, "", nil
	p.dismissed = u.completionTarget()
}

func (u *appServerUI) skillsModalKey(key string) bool {
	p := &u.picker
	if key == "\x1b[200~" || key == "\x1b[201~" {
		return false
	}
	n := len(p.choices)
	if p.modal == "menu" {
		n = 2
	}
	switch key {
	case "\x1b", "\x03":
		u.closeSkillsModal()
	case "\x1b[A", "\x1bOA", "\x10":
		if n > 0 {
			p.selected = (p.selected + n - 1) % n
		}
	case "\x1b[B", "\x1bOB", "\x0e":
		if n > 0 {
			p.selected = (p.selected + 1) % n
		}
	case "\x1b[5~":
		p.selected = max(0, p.selected-8)
	case "\x1b[6~":
		p.selected = min(max(0, n-1), p.selected+8)
	case "\x1b[H", "\x1bOH":
		p.selected = 0
	case "\x1b[F", "\x1bOF":
		p.selected = max(0, n-1)
	case "\r", " ":
		if p.modal == "manage" {
			u.toggleSkill()
			break
		}
		if key == " " {
			break
		}
		u.chooseSkillsAction()
	case "1", "2":
		if p.modal == "menu" {
			p.selected = int(key[0] - '1')
			u.chooseSkillsAction()
		} else {
			p.query += key
		}
	case "\x7f", "\x08":
		if p.modal == "manage" && p.query != "" {
			_, n := utf8.DecodeLastRuneInString(p.query)
			p.query = p.query[:len(p.query)-n]
		}
	default:
		if p.modal == "manage" && len(key) == 1 && key[0] >= 32 {
			p.query += key
		}
	}
	return true
}

func (u *appServerUI) chooseSkillsAction() {
	p := &u.picker
	if p.selected == 0 {
		p.modal, p.open = "", false
		p.dismissed = composerTarget{}
		u.insertDraft("$")
	} else {
		p.modal, p.selected, p.top, p.query, p.problem = "manage", 0, 0, "", ""
		p.dismissed = composerTarget{}
		p.resolved = composerTarget{}
		if p.skillsLoaded {
			u.rememberSkillState()
		}
	}
}

func (u *appServerUI) toggleSkill() {
	p := &u.picker
	if p.toggleID != "" || p.loading || len(p.choices) == 0 {
		return
	}
	choice := p.choices[p.selected]
	id, err := u.client.Send("skills/config/write", map[string]any{"path": choice.path, "enabled": !choice.enabled}, true)
	if err != nil {
		p.problem = "Could not save skill: " + err.Error()
		return
	}
	u.requests[id] = "skills/config/write"
	p.toggleID, p.togglePath, p.problem = id, choice.path, ""
}

func (u *appServerUI) skillToggleResponse(m appserver.Message) {
	p := &u.picker
	if string(m.ID) != p.toggleID {
		return
	}
	path := p.togglePath
	p.toggleID, p.togglePath = "", ""
	if m.Error != nil {
		p.problem = "Could not save skill: " + m.Error.Message
	} else {
		var result struct {
			Enabled bool `json:"effectiveEnabled"`
		}
		if err := json.Unmarshal(m.Result, &result); err != nil {
			p.problem = "Invalid skill save response: " + err.Error()
		} else {
			for i := range p.skills {
				if p.skills[i].path == path {
					p.skills[i].enabled = result.Enabled
				}
			}
			p.problem = ""
		}
	}
	if p.modal != "manage" && p.problem != "" {
		u.setNotice(p.problem, true)
	}
	if p.modal == "manage" {
		u.filterSkills(p.query)
	} else if p.open && p.target.kind == '$' {
		u.filterSkills(p.target.query)
	}
}

// Source: codex-rs/tui/src/bottom_pane/chat_composer/completion_target.rs:327:353
// and mention_codec.rs:306:321@86be5320b068ef67b56348b02aa8c33706955da6.
func (u *appServerUI) skillQueryCompletable(query string) bool {
	if query == "" {
		return true
	}
	end := 0
	for _, r := range query {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == ':') {
			break
		}
		end++
	}
	if end == 0 {
		return false
	}
	name := query[:end]
	if slices.Contains([]string{"PATH", "HOME", "USER", "SHELL", "PWD", "TMPDIR", "TEMP", "TMP", "LANG", "TERM", "XDG_CONFIG_HOME"}, name) {
		return false
	}
	if name == "-" || name == "_" || strings.Trim(name, "0123456789") == "" {
		return false
	}
	if name[0] == '-' || name[0] >= '0' && name[0] <= '9' {
		for _, skill := range u.picker.skills {
			if skill.enabled {
				if _, ok := pickerMatchScore(skill.name, query); ok {
					return true
				}
			}
		}
		return false
	}
	return true
}
