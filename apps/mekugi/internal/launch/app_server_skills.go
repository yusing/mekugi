package launch

import (
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"

	json "encoding/json/v2"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yusing/mekugi/modules/conversation/completion"

	"github.com/yusing/mekugi/backends/codex/appserver"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/conversation/composer"

	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
)

// Source: codex-rs/tui/src/chatwidget/skills.rs:29:136 and
// bottom_pane/skills_toggle_view.rs:98:299@86be5320b068ef67b56348b02aa8c33706955da6.
func (p *composerPicker) closeSkillsModal(attached *attachedPresentation, terminal *terminalsession.Attachment, clock func() time.Time, editor *composer.Editor) {
	p.toggleOwnsManager = false
	if p.Kind == "manage" {
		enabled, disabled := p.SkillChanges()
		if enabled+disabled > 0 {
			setNotice(&attached.feedback, terminal, presentationTime(clock), fmt.Sprintf("%d skills enabled, %d skills disabled", enabled, disabled), false)
		}
		p.SkillsLoaded = false // Codex reloads the catalog after leaving management.
	}
	p.Kind, p.Open, p.Query, p.Initial = "", false, "", nil
	p.Dismissed = p.completionTarget(editor.Draft, attached.conversation.ShellMode())
}

func (p *composerPicker) chooseSkillsAction(attached *attachedPresentation, editor *composer.Editor, terminal *terminalsession.Attachment, clock func() time.Time, owned map[string]bool, questions *nativeQuestionDock, approvals *nativeApprovalDock, side *sessioncontrol.Side, choice completion.Choice) {
	if choice.Name == "list" {
		p.Kind, p.Open = "", false
		p.Dismissed = completion.Target{}
		insertDraft(attached, editor, terminal, clock, owned, questions, approvals, side, "$")
	} else {
		p.toggleOwnsManager = false
		p.openMenu("manage")
		p.Query, p.Problem = "", ""
		p.Dismissed = completion.Target{}
		p.Resolved = completion.Target{}
		if p.SkillsLoaded {
			p.RememberSkills()
		}
	}
}

func toggleSkill(p *composerPicker, requests *sessioncontrol.Requests[appServerRequest], workspace string, choice completion.Choice) {
	if p.toggleID != "" || p.Loading || len(p.Choices) == 0 {
		return
	}
	id, err := requests.Send("skills/config/write", map[string]any{"path": choice.Path, "enabled": !choice.Enabled}, "main", appServerRequest{})
	if err != nil {
		p.Problem = "Could not save skill: " + err.Error()
		return
	}
	p.toggleID, p.togglePath, p.toggleCwd = id, choice.Path, workspace
	p.toggleOwnsManager, p.Problem = p.Kind == "manage", ""
}

func (p *composerPicker) skillSaveOwnsManager(workspace string) bool {
	return p.toggleOwnsManager && p.Kind == "manage" && p.toggleCwd == workspace
}

func (p *composerPicker) skillToggleResponse(attached *attachedPresentation, terminal *terminalsession.Attachment, clock func() time.Time, m appserver.Message) {
	if string(m.ID) != p.toggleID {
		return
	}
	path, cwd := p.togglePath, p.toggleCwd
	ownsManager := p.skillSaveOwnsManager(attached.cwd)
	p.toggleID, p.togglePath, p.toggleCwd, p.toggleOwnsManager = "", "", "", false
	problem, updated := "", false
	if m.Error != nil {
		problem = "Could not save skill: " + m.Error.Message
	} else {
		var result struct {
			Enabled *bool `json:"effectiveEnabled"`
		}
		if err := json.Unmarshal(m.Result, &result); err != nil {
			problem = "Invalid skill save response: " + err.Error()
		} else if result.Enabled == nil {
			problem = "Invalid skill save response: effectiveEnabled must be a boolean"
		} else {
			updated = p.SkillSaved(cwd, path, *result.Enabled)
		}
	}
	if ownsManager {
		p.Problem = problem
	} else if problem != "" {
		setNotice(&attached.feedback, terminal, presentationTime(clock), problem, true)
	}
	if !updated || cwd != attached.cwd {
		return
	}
	if p.Kind == "manage" {
		p.FilterSkills(p.Query, true)
	} else if p.Open && p.Target.Kind == '$' {
		p.FilterSkills(p.Target.Query, false)
	}
}

// Source: codex-rs/tui/src/bottom_pane/chat_composer/completion_target.rs:327:353
// and mention_codec.rs:306:321@86be5320b068ef67b56348b02aa8c33706955da6.
// The catalog, not a textual lookalike, authorizes a skill binding. Repeated
// names with different paths are ambiguous and remain literal until selected.
func (p *composerPicker) bindSkills(workspace string, d *conversationmodel.Input, final bool) {
	if strings.HasPrefix(d.Text, "!") || d.Question != nil {
		return
	}
	if !p.SkillsLoaded || p.SkillsCwd != workspace {
		return
	}
	var catalog []composer.Token
	for _, choice := range p.Skills {
		if choice.Enabled && p.SkillQueryCompletable(choice.Name) {
			catalog = append(catalog, composer.Token{Name: choice.Name, Path: choice.Path})
		}
	}
	spans := sessionpresentation.DraftSpans(d.Draft)
	for _, binding := range conversationbinding.SkillBindings(d.Text, catalog, nil) {
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

func (p *composerPicker) skillBindingQuery(d conversationmodel.Input, final bool) string {
	if strings.HasPrefix(d.Text, "!") || d.Question != nil {
		return ""
	}
	at := 0
	spans := sessionpresentation.DraftSpans(d.Draft)
	for word := range strings.FieldsSeq(d.Text) {
		start := at + strings.Index(d.Text[at:], word)
		at = start + len(word)
		name, ok := strings.CutPrefix(word, "$")
		if !ok || name == "" || !p.SkillQueryCompletable(name) || !final && at == len(d.Text) {
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

func (p *composerPicker) waitForSkillBindings(attached *attachedPresentation, terminal *terminalsession.Attachment, clock func() time.Time, requests *sessioncontrol.Requests[appServerRequest], parts []conversationmodel.Input) bool {
	if p.SkillsLoaded && p.SkillsCwd == attached.cwd {
		return false
	}
	if p.SkillsProblem != "" && p.SkillsCwd == attached.cwd {
		return false // Catalog failure must not borrow foreground completion state.
	}
	for _, part := range parts {
		if query := p.skillBindingQuery(part, true); query != "" {
			if p.Pending == nil && filepath.IsAbs(attached.cwd) {
				p.requestPicker(attached, terminal, clock, requests, completion.Target{Kind: '$', Query: query}, attached.cwd)
			}
			return p.SkillsProblem == "" || p.SkillsCwd != attached.cwd
		}
	}
	return false
}
