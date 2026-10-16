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

	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/modules/conversation/completion"
	conversationui "github.com/yusing/mekugi/modules/conversation/terminalview"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"

	"github.com/yusing/mekugi/modules/conversation/composer"

	"github.com/yusing/mekugi/backends/codex/appserver"
)

// Source: codex-rs/tui/src/bottom_pane/chat_composer.rs:2094:2250,2744:2805
// @86be5320b068ef67b56348b02aa8c33706955da6. File and skill completion. Searches
// remain Codex-owned, through app-server rather than a second filesystem index.

type composerPicker struct {
	completion.State
	terminalui.ChoiceListLayout
	scanResults          chan pickerScanResult
	scanCancel           context.CancelFunc
	scanID               uint64
	scanTarget           completion.Target
	scanCwd              string
	modal                string
	toggleID, togglePath string
	rect                 terminalsession.Rect
	copyTexts            []string
}

func (u *appServerUI) completionTarget() completion.Target {
	if u.shellMode() {
		return completion.Target{}
	}
	return u.picker.FindTarget(u.Draft)
}

func (u *appServerUI) refreshPicker() {
	for _, stack := range [][]conversationmodel.Input{u.conversation().Unsent, u.conversation().Queued} {
		for i := range stack {
			before := len(stack[i].Skills)
			u.bindSkills(&stack[i], true)
			if len(stack[i].Skills) > before {
				u.snapshotDraftSkills(&stack[i])
			}
		}
	}
	if u.currentQuestion() != nil || u.approvals.Open {
		u.picker.Open = false
		return
	}
	if u.paste || u.escape != "" || !utf8.ValidString(u.Text) {
		return
	}
	p := &u.picker
	draft := u.draftSnapshot()
	u.bindSkills(&draft, false)
	u.Skills = draft.Skills
	target := u.completionTarget()
	bindingOnly := false
	if target.Kind == 0 && p.modal == "" && (!p.SkillsLoaded || p.SkillsCwd != u.session.cwd) {
		query := u.skillBindingQuery(draft, false)
		for _, part := range slices.Concat(u.conversation().Unsent, u.conversation().Queued) {
			if query == "" {
				query = u.skillBindingQuery(part, true)
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
	if p.modal == "menu" || p.modal == "copy" || p.modal == "settings" || p.modal == "archive" {
		p.Open = true
		return
	}
	if p.modal == "manage" {
		target = completion.Target{Kind: '$', Query: p.Query}
	}
	if !p.Activate(target, bindingOnly) {
		u.cancelPickerScan()
		return
	}
	if target.Kind == '/' {
		u.cancelPickerScan()
		u.filterCommands(target.Query)
		return
	}
	cwd := u.session.cwd
	if !filepath.IsAbs(cwd) {
		p.Loading, p.Problem = false, "Waiting for the thread workspace…"
		return
	}
	if target.Kind != '@' || !strings.HasPrefix(target.Query, "!") {
		u.cancelPickerScan()
	}
	if target.Kind == '$' && (p.SkillsLoaded || p.SkillsProblem != "") && p.SkillsCwd == cwd {
		p.Loading = false
		u.picker.FilterSkills(target.Query, u.picker.modal == "manage")

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
		u.searchExcludedFiles(target, cwd)
		return
	}
	if p.Pending != nil {
		return
	}
	u.requestPicker(target, cwd)
}

func (u *appServerUI) requestPicker(target completion.Target, cwd string) {
	p := &u.picker
	method := "fuzzyFileSearch"
	params := map[string]any{"query": target.Query, "roots": []string{cwd}}
	if target.Kind == '$' {
		method, params = "skills/list", map[string]any{"cwds": []string{cwd}, "forceReload": true}
	}
	p.Pending, p.PendingCwd = new(target), cwd
	if err := u.request(method, params); err != nil {
		p.Pending, p.Loading = nil, false
		p.Resolved, p.ResolvedCwd = target, cwd
		p.Problem = "Search failed: " + err.Error()
		if target.Kind == '$' {
			p.SkillsCwd, p.SkillsProblem = cwd, p.Problem
			u.setNotice("Skill attachment unavailable: "+p.Problem+". References remain text.", true)
		}
	}
}

func (u *appServerUI) pickerMessage(method string, m appserver.Message) bool {
	if method == "skills/config/write" {
		u.skillToggleResponse(m)
		return true
	}
	if method != "skills/list" && method != "fuzzyFileSearch" {
		return false
	}
	p := &u.picker
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
		if u.skillEnvironment != nil && p.modal != "manage" && cwd == u.session.cwd {
			parent := u.ctx
			if parent == nil {
				parent = context.Background()
			}
			ctx, cancel := context.WithTimeout(parent, 15*time.Second)
			managed, loadErr := readManagedSkills(ctx, cwd, u.skillEnvironment)
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
		p.Catalog(choices, cwd, available, problem)
		if p.modal == "manage" && p.Initial == nil {
			u.picker.RememberSkills()
		}
		if problem != "" && cwd == u.session.cwd {
			label := "Skill catalog incomplete: "
			if !available {
				label = "Skill attachment unavailable: "
			}
			u.setNotice(label+problem+". Unavailable references remain text.", true)
		}
	}
	p.Resolve(target, cwd, u.session.cwd, choices, problem)
	u.refreshPicker()
	return true
}

func (u *appServerUI) pickerKey(key string) bool {
	p := &u.picker
	if p.modal == "archive" && p.Open {
		return u.archivePickerKey(key)
	}
	if p.modal == "settings" && p.Open {
		return u.settingsPickerKey(key)
	}
	if p.modal == "copy" && p.Open {
		return u.copyPickerKey(key)
	}
	if p.modal != "" && p.Open {
		switch conversationui.SkillsKey(&p.State, p.modal == "menu", key) {
		case conversationui.PickerPass:
			return false
		case conversationui.PickerCloseSkills:
			u.closeSkillsModal()
		case conversationui.PickerToggleSkill:
			u.toggleSkill()
		case conversationui.PickerChooseSkills:
			u.chooseSkillsAction()
		}
		return true
	}
	if !p.Open {
		return false
	}
	// Drafts can also change outside key(), through editor or transcript reference.
	if u.completionTarget() != p.Target {
		u.refreshPicker()
		return false
	}
	switch conversationui.PickerKey(&p.State, key) {
	case conversationui.PickerDismiss:
		u.cancelPickerScan()
		return true
	case conversationui.PickerHandled:
		return true
	case conversationui.PickerAccept:
		choice, target := p.Choices[p.Selected], p.Target
		if target.Kind == '/' {
			u.deleteDraftRange(target.Start, target.End)
			u.InsertText(choice.Name + " ")
			u.Run, p.Open = composer.NoRun, false
			// Enter continues through the existing local command dispatcher.
			return key == "\t"
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
				path = filepath.Join(u.session.cwd, path)
			}
			if imageFile(path) {
				imagePath = path
				text = fmt.Sprintf("[Image %d]", len(u.Images)+1)
			}
		}
		// One undo step restores the original token, not a half-completed edit.
		u.deleteDraftRange(target.Start, target.End)
		u.InsertText(text)
		if imagePath != "" {
			u.Images = append(u.Images, composer.Token{Start: target.Start, End: target.Start + len(text), Path: imagePath})
			u.RenumberImages()
		}
		if target.Kind == '@' && imagePath == "" {
			u.Files = append(u.Files, composer.Token{Start: target.Start, End: target.Start + len(text), Path: choice.Path})
			slices.SortFunc(u.Files, func(a, b composer.Token) int { return a.Start - b.Start })
		}
		if target.Kind == '$' {
			u.Skills = append(u.Skills, composer.Token{Start: target.Start, End: target.Start + len(text), Name: choice.Name, Path: choice.Path})
		}
		if u.Cursor() == len(u.Text) || u.Text[u.Cursor()] != ' ' {
			u.InsertText(" ")
		} else {
			u.CursorBack--
		}
		u.Run, p.Open = composer.NoRun, false
		return true
	}
	return false
}

func (u *appServerUI) pickerVisible() bool {
	return !u.composerReadOnly() && u.picker.Open && (u.shell == nil || u.shell.Focus == 0)
}
