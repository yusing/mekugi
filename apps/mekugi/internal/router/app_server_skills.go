package router

import (
	json "encoding/json/v2"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yusing/mekugi/modules/conversation/completion"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"

	"github.com/yusing/mekugi/modules/conversation/composer"

	"github.com/yusing/mekugi/backends/codex/appserver"
)

// Source: codex-rs/tui/src/chatwidget/skills.rs:29:136 and
// bottom_pane/skills_toggle_view.rs:98:299@86be5320b068ef67b56348b02aa8c33706955da6.
func (u *appServerUI) closeSkillsModal() {
	p := &u.picker
	if p.modal == "manage" {
		enabled, disabled := p.SkillChanges()
		if enabled+disabled > 0 {
			u.setNotice(fmt.Sprintf("%d skills enabled, %d skills disabled", enabled, disabled), false)
		}
		p.SkillsLoaded = false // Codex reloads the catalog after leaving management.
	}
	p.modal, p.Open, p.Query, p.Initial = "", false, "", nil
	p.Dismissed = u.completionTarget()
}

func (u *appServerUI) chooseSkillsAction() {
	p := &u.picker
	if p.Selected == 0 {
		p.modal, p.Open = "", false
		p.Dismissed = completion.Target{}
		u.insertDraft("$")
	} else {
		p.modal, p.Selected, p.Top, p.Query, p.Problem = "manage", 0, 0, "", ""
		p.Dismissed = completion.Target{}
		p.Resolved = completion.Target{}
		if p.SkillsLoaded {
			u.picker.RememberSkills()
		}
	}
}

func (u *appServerUI) toggleSkill() {
	p := &u.picker
	if p.toggleID != "" || p.Loading || len(p.Choices) == 0 {
		return
	}
	choice := p.Choices[p.Selected]
	id, err := u.requests.Send("skills/config/write", map[string]any{"path": choice.Path, "enabled": !choice.Enabled}, "main", appServerRequest{})
	if err != nil {
		p.Problem = "Could not save skill: " + err.Error()
		return
	}
	p.toggleID, p.togglePath, p.Problem = id, choice.Path, ""
}

func (u *appServerUI) skillToggleResponse(m appserver.Message) {
	p := &u.picker
	if string(m.ID) != p.toggleID {
		return
	}
	path := p.togglePath
	p.toggleID, p.togglePath = "", ""
	if m.Error != nil {
		p.Problem = "Could not save skill: " + m.Error.Message
	} else {
		var result struct {
			Enabled bool `json:"effectiveEnabled"`
		}
		if err := json.Unmarshal(m.Result, &result); err != nil {
			p.Problem = "Invalid skill save response: " + err.Error()
		} else {
			p.SkillSaved(path, result.Enabled)
		}
	}
	if p.modal != "manage" && p.Problem != "" {
		u.setNotice(p.Problem, true)
	}
	if p.modal == "manage" {
		u.picker.FilterSkills(p.Query, u.picker.modal == "manage")
	} else if p.Open && p.Target.Kind == '$' {
		u.picker.FilterSkills(p.Target.Query, u.picker.modal == "manage")
	}
}

// Source: codex-rs/tui/src/bottom_pane/chat_composer/completion_target.rs:327:353
// and mention_codec.rs:306:321@86be5320b068ef67b56348b02aa8c33706955da6.
// The catalog, not a textual lookalike, authorizes a skill binding. Repeated
// names with different paths are ambiguous and remain literal until selected.
func (u *appServerUI) bindSkills(d *conversationmodel.Input, final bool) {
	p := &u.picker
	if strings.HasPrefix(d.Text, "!") || d.Question != nil {
		return
	}
	if !p.SkillsLoaded || p.SkillsCwd != u.session.cwd {
		return
	}
	var catalog []composer.Token
	for _, choice := range p.Skills {
		if choice.Enabled && u.picker.SkillQueryCompletable(choice.Name) {
			catalog = append(catalog, composer.Token{Name: choice.Name, Path: choice.Path})
		}
	}
	spans := draftDisplaySpans(d.Draft)
	for _, binding := range unambiguousSkillBindings(d.Text, catalog, nil) {
		if !final && binding.End == len(d.Text) {
			continue // A still-growing word stays editable.
		}
		if !slices.ContainsFunc(spans, func(span activitypresentation.TextSpan) bool {
			return binding.Start < span.End && binding.End > span.Start
		}) {
			d.Skills = append(d.Skills, binding)
		}
	}
	slices.SortFunc(d.Skills, func(a, b composer.Token) int { return a.Start - b.Start })
}

func (u *appServerUI) skillBindingQuery(d conversationmodel.Input, final bool) string {
	if strings.HasPrefix(d.Text, "!") || d.Question != nil {
		return ""
	}
	at := 0
	spans := draftDisplaySpans(d.Draft)
	for word := range strings.FieldsSeq(d.Text) {
		start := at + strings.Index(d.Text[at:], word)
		at = start + len(word)
		name, ok := strings.CutPrefix(word, "$")
		if !ok || name == "" || !u.picker.SkillQueryCompletable(name) || !final && at == len(d.Text) {
			continue
		}
		bound := false
		for _, span := range spans {
			if start < span.End && at > span.Start {
				bound = true
				break
			}
		}
		if !bound {
			return name
		}
	}
	return ""
}

func (u *appServerUI) waitForSkillBindings(parts []conversationmodel.Input) bool {
	p := &u.picker
	if p.SkillsLoaded && p.SkillsCwd == u.session.cwd {
		return false
	}
	if p.SkillsProblem != "" && p.SkillsCwd == u.session.cwd {
		return false // Catalog failure must not borrow foreground completion state.
	}
	for _, part := range parts {
		if query := u.skillBindingQuery(part, true); query != "" {
			if p.Pending == nil && filepath.IsAbs(u.session.cwd) {
				u.requestPicker(completion.Target{Kind: '$', Query: query}, u.session.cwd)
			}
			return p.SkillsProblem == "" || p.SkillsCwd != u.session.cwd
		}
	}
	return false
}
