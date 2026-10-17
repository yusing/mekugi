package router

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/modules/conversation/completion"

	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"

	"github.com/yusing/mekugi/modules/conversation/composer"

	"github.com/yusing/mekugi/backends/codex/appserver"
)

// Source: codex-rs/tui/src/bottom_pane/chat_composer.rs:2094:2250,2744:2805
// @86be5320b068ef67b56348b02aa8c33706955da6. File and skill completion. Searches
// remain Codex-owned, through app-server rather than a second filesystem index.

type composerPicker struct {
	*completion.State
	*sessionpresentation.MenuState
	scanResults                     chan pickerScanResult
	scanCancel                      context.CancelFunc
	scanID                          uint64
	scanTarget                      completion.Target
	scanCwd                         string
	toggleID, togglePath, toggleCwd string
	toggleOwnsManager               bool
}

func (p *composerPicker) completionTarget(draft composer.Draft, shellMode bool) completion.Target {
	if shellMode {
		return completion.Target{}
	}
	return p.FindTarget(draft)
}

func (p *composerPicker) refreshPicker(attached *attachedPresentation, terminal *terminalsession.Attachment, clock func() time.Time, requests *sessioncontrol.Requests[appServerRequest], editor *composer.Editor, parent context.Context, environment []string) {
	for _, stack := range [][]conversationmodel.Input{attached.conversation.Unsent, attached.conversation.Queued} {
		for i := range stack {
			before := len(stack[i].Skills)
			p.bindSkills(attached.cwd, &stack[i], true)
			if len(stack[i].Skills) > before {
				snapshotDraftSkills(attached.cwd, environment, &stack[i])
			}
		}
	}
	if attached.conversation.Questions.Current() != nil || attached.conversation.Approvals.Open {
		p.Open = false
		return
	}
	if terminal.Main.Pasting() || terminal != nil && terminal.Decoder.Pending() || !utf8.ValidString(editor.Text) {
		return
	}
	draft := draftSnapshot(editor)
	p.bindSkills(attached.cwd, &draft, false)
	if !slices.Equal(editor.Skills, draft.Skills) {
		editor.Skills = draft.Skills
		editor.Changed()
	}
	target := p.completionTarget(editor.Draft, attached.conversation.ShellMode())
	bindingOnly := false
	if target.Kind == 0 && p.Kind == "" && (!p.SkillsLoaded || p.SkillsCwd != attached.cwd) {
		query := p.skillBindingQuery(draft, false)
		for _, part := range slices.Concat(attached.conversation.Unsent, attached.conversation.Queued) {
			if query == "" {
				query = p.skillBindingQuery(part, true)
			}
		}
		if query != "" {
			target, bindingOnly = completion.Target{Kind: '$', Query: query}, true
		}
	}
	defer func() {
		if bindingOnly {
			p.Open = false
		}
	}()
	if p.Kind == "menu" || p.Kind == "copy" || p.Kind == "settings" || p.Kind == "archive" {
		p.Open = true
		return
	}
	if p.Kind == "manage" {
		target = completion.Target{Kind: '$', Query: p.Query}
	}
	wasOpen := p.Open
	if !p.Activate(target, bindingOnly) {
		p.cancelScan()
		return
	}
	if !wasOpen && !bindingOnly {
		p.ID++
	}
	if target.Kind == '/' {
		p.cancelScan()
		p.filterCommands(target.Query)
		return
	}
	cwd := attached.cwd
	if !filepath.IsAbs(cwd) {
		p.Loading, p.Problem = false, "Waiting for the thread workspace…"
		return
	}
	if target.Kind != '@' || !strings.HasPrefix(target.Query, "!") {
		p.cancelScan()
	}
	if target.Kind == '$' && (p.SkillsLoaded || p.SkillsProblem != "") && p.SkillsCwd == cwd {
		p.Loading = false
		p.FilterSkills(target.Query, p.Kind == "manage")

		return
	}
	if target.Kind == '@' && target.Query == "" {
		p.Loading, p.Choices, p.Problem = false, nil, "Type to search files"
		return
	}
	if p.Resolved == target && p.ResolvedCwd == cwd {
		return
	}
	p.Loading, p.Choices, p.Problem = true, nil, ""
	if target.Kind == '@' && strings.HasPrefix(target.Query, "!") {
		p.searchExcludedFiles(parent, target, cwd)
		return
	}
	if p.Pending != nil {
		return
	}
	p.requestPicker(attached, terminal, clock, requests, target, cwd)
}

func (p *composerPicker) requestPicker(attached *attachedPresentation, terminal *terminalsession.Attachment, clock func() time.Time, requests *sessioncontrol.Requests[appServerRequest], target completion.Target, cwd string) {
	method := "fuzzyFileSearch"
	params := map[string]any{"query": target.Query, "roots": []string{cwd}}
	if target.Kind == '$' {
		method, params = "skills/list", map[string]any{"cwds": []string{cwd}, "forceReload": true}
	}
	p.Pending, p.PendingCwd = new(target), cwd
	if _, err := requests.Send(method, params, "main", appServerRequest{}); err != nil {
		p.Pending, p.Loading = nil, false
		p.Resolved, p.ResolvedCwd = target, cwd
		p.Problem = "Search failed: " + err.Error()
		if target.Kind == '$' {
			p.SkillsCwd, p.SkillsProblem = cwd, p.Problem
			setNotice(&attached.feedback, terminal, presentationTime(clock), "Skill attachment unavailable: "+p.Problem+". References remain text.", true)
		}
	}
}

func (p *composerPicker) pickerMessage(attached *attachedPresentation, terminal *terminalsession.Attachment, clock func() time.Time, requests *sessioncontrol.Requests[appServerRequest], editor *composer.Editor, parent context.Context, environment []string, method string, m appserver.Message) bool {
	if method == "skills/config/write" {
		p.skillToggleResponse(attached, terminal, clock, m)
		return true
	}
	if method != "skills/list" && method != "fuzzyFileSearch" {
		return false
	}
	if p.Pending == nil {
		return true
	}
	target, cwd := *p.Pending, p.PendingCwd
	p.Pending = nil
	var choices []completion.Choice
	var err error
	problem := ""
	if m.Error != nil {
		problem = "Search failed: " + m.Error.Message
	} else if method == "skills/list" {
		var result struct {
			Data []struct {
				Cwd    string `json:"cwd"`
				Skills []struct {
					Name             string `json:"name"`
					Path             string `json:"path"`
					Description      string `json:"description"`
					ShortDescription string `json:"shortDescription"`
					Interface        *struct {
						ShortDescription string `json:"shortDescription"`
						DisplayName      string `json:"displayName"`
					} `json:"interface"`
					Enabled bool `json:"enabled"`
				} `json:"skills"`
				Errors []struct {
					Message string `json:"message"`
				} `json:"errors"`
			} `json:"data"`
		}
		err = json.Unmarshal(m.Result, &result)
		for _, entry := range result.Data {
			if filepath.Clean(entry.Cwd) != filepath.Clean(cwd) {
				continue
			}
			for _, skill := range entry.Skills {

				description := skill.Description
				if skill.ShortDescription != "" {
					description = skill.ShortDescription
				}
				if skill.Interface != nil && skill.Interface.ShortDescription != "" {
					description = skill.Interface.ShortDescription
				}
				display := skill.Name
				if namespace, name, ok := strings.Cut(skill.Name, ":"); ok && namespace != "" && name != "" {
					display = name + " (" + namespace + ")"
				}
				if skill.Interface != nil && skill.Interface.DisplayName != "" {
					display = skill.Interface.DisplayName
				}
				choices = append(choices, completion.Choice{Name: skill.Name, Path: skill.Path, Description: description, Display: display, Enabled: skill.Enabled})
			}
			if len(entry.Errors) > 0 {
				problem = fmt.Sprintf("%d skill load errors: %s", len(entry.Errors), entry.Errors[0].Message)
			}
		}
	} else {
		var result struct {
			Files []struct {
				Root string `json:"root"`
				Path string `json:"path"`
			} `json:"files"`
		}
		err = json.Unmarshal(m.Result, &result)
		for _, file := range result.Files {
			if filepath.Clean(file.Root) != filepath.Clean(cwd) {
				continue
			}
			// Codex can return the .git directory itself even though its walk
			// excludes its contents. Normal @ must not expose git internals.
			if composer.VCSPath(file.Path) {
				continue
			}
			choices = append(choices, completion.Choice{Name: file.Path, Path: file.Path})
		}
	}
	if err != nil {
		choices = nil
		problem = "Invalid search response: " + err.Error()
	}
	if method == "skills/list" {
		available := m.Error == nil && err == nil
		if environment != nil && p.Kind != "manage" && cwd == attached.cwd {
			requestContext := parent
			if requestContext == nil {
				requestContext = context.Background()
			}
			ctx, cancel := context.WithTimeout(requestContext, 15*time.Second)
			managed, loadErr := readManagedSkills(ctx, cwd, environment)
			cancel()
			if loadErr == nil {
				names := make(map[string]bool)
				for _, choice := range managed {
					names[choice.Name] = true
				}
				choices = slices.DeleteFunc(choices, func(choice completion.Choice) bool { return names[choice.Name] })
				choices = append(choices, managed...)
				available = true
			} else {
				if problem != "" {
					problem += "; "
				}
				problem += "skills-mgr: " + loadErr.Error()
			}
		}
		p.toggleOwnsManager = false
		p.Catalog(choices, cwd, available, problem)
		if p.Kind == "manage" && p.Initial == nil {
			p.RememberSkills()
		}
		if problem != "" && cwd == attached.cwd {
			label := "Skill catalog incomplete: "
			if !available {
				label = "Skill attachment unavailable: "
			}
			setNotice(&attached.feedback, terminal, presentationTime(clock), label+problem+". Unavailable references remain text.", true)
		}
	}
	p.Resolve(target, cwd, attached.cwd, choices, problem)
	p.refreshPicker(attached, terminal, clock, requests, editor, parent, environment)
	return true
}

func acceptCompletion(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, childKey *scope.Key[*appServerChildHistory], settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring bool, side *sessioncontrol.Side, clock func() time.Time, questions *nativeQuestionDock, approvals *nativeApprovalDock, overlay bool, owned map[string]bool, parent context.Context, environment []string, choice completion.Choice) bool {
	p := picker
	target := p.Target
	if picker.completionTarget((*editor).Draft, attached.conversation.ShellMode()) != target {
		setNotice(&attached.feedback, terminal, presentationTime(clock), sessionpresentation.ErrStaleIntent.Error(), true)
		picker.refreshPicker(attached, terminal, clock, control.Requests, (*editor), parent, environment)
		return false
	}
	if target.Kind == '/' {
		deleteDraftRange(attached, terminal, control, picker, editor, childKey, settings, tiers, title, backend, restoring, side, clock, questions, approvals, overlay, owned, target.Start, target.End)
		(*editor).InsertText(choice.Name + " ")
		(*editor).Run, p.Open = composer.NoRun, false
		// Enter continues through the existing local command dispatcher.
		return true
	}
	text := choice.Path
	if target.Kind == '$' {
		text = "$" + choice.Name
	} else if strings.ContainsFunc(text, unicode.IsSpace) && !strings.Contains(text, "\"") {
		text = "\"" + text + "\""
	}
	if target.Kind == '@' {
		text = "@" + text
	}
	imagePath := ""
	if target.Kind == '@' {
		path := choice.Path
		if !filepath.IsAbs(path) {
			path = filepath.Join(attached.cwd, path)
		}
		if imageFile(path) {
			imagePath = path
			text = fmt.Sprintf("[Image %d]", len((*editor).Images)+1)
		}
	}
	// One undo step restores the original token, not a half-completed edit.
	deleteDraftRange(attached, terminal, control, picker, editor, childKey, settings, tiers, title, backend, restoring, side, clock, questions, approvals, overlay, owned, target.Start, target.End)
	(*editor).InsertText(text)
	if imagePath != "" {
		(*editor).Images = append((*editor).Images, composer.Token{Start: target.Start, End: target.Start + len(text), Path: imagePath})
		(*editor).RenumberImages()
	}
	if target.Kind == '@' && imagePath == "" {
		(*editor).Files = append((*editor).Files, composer.Token{Start: target.Start, End: target.Start + len(text), Path: choice.Path})
		slices.SortFunc((*editor).Files, func(a, b composer.Token) int { return a.Start - b.Start })
	}
	if target.Kind == '$' {
		(*editor).Skills = append((*editor).Skills, composer.Token{Start: target.Start, End: target.Start + len(text), Name: choice.Name, Path: choice.Path})
	}
	if (*editor).Cursor() == len((*editor).Text) || (*editor).Text[(*editor).Cursor()] != ' ' {
		(*editor).InsertText(" ")
	} else {
		(*editor).CursorBack--
	}
	(*editor).Run, p.Open = composer.NoRun, false
	return true
}

func pickerVisible(picker *composerPicker, presentation *sessionpresentation.Model, terminal *terminalsession.Attachment) bool {
	return presentation.View().ReadOnly == sessionpresentation.Editable && picker.Open && (terminal == nil || terminal.Focus == 0)
}
