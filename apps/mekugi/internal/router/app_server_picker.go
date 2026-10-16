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

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"

	"github.com/yusing/mekugi/modules/conversation/composer"

	"github.com/yusing/mekugi/backends/codex/appserver"
)

// Source: codex-rs/tui/src/bottom_pane/chat_composer.rs:2094:2250,2744:2805
// @86be5320b068ef67b56348b02aa8c33706955da6. File and skill completion. Searches
// remain Codex-owned, through app-server rather than a second filesystem index.

type composerTarget struct {
	kind       byte
	start, end int
	query      string
}

type composerChoice struct {
	name, path, description string
	display                 string
	copyText                string
	enabled                 bool
	directory               bool
	indices                 []int
}

func (c composerChoice) label() string {
	if c.display != "" {
		return c.display
	}
	return c.name
}

type composerPicker struct {
	scanResults          chan pickerScanResult
	scanCancel           context.CancelFunc
	scanID               uint64
	scanTarget           composerTarget
	scanCwd              string
	modal                string // "menu" or "manage" replaces the composer, as in Codex.
	query                string
	initial              map[string]bool
	toggleID, togglePath string
	top                  int
	rowStart, rowCount   int
	rect                 terminalsession.Rect
	target, dismissed    composerTarget
	open                 bool
	choices              []composerChoice
	selected             int
	loading              bool
	problem              string
	// One in-flight request; edits coalesce until its response, which is never
	// allowed to replace results for a different query or workspace.
	pending       *composerTarget
	pendingCwd    string
	resolved      composerTarget
	resolvedCwd   string
	skills        []composerChoice
	skillsCwd     string
	skillsLoaded  bool
	skillsProblem string
}

func (u *appServerUI) completionTarget() composerTarget {
	if u.shellMode() {
		return composerTarget{}
	}
	at := u.Cursor()
	if strings.HasPrefix(u.Text, "/") && at > 0 {
		end := strings.IndexFunc(u.Text, unicode.IsSpace)
		if end < 0 {
			end = len(u.Text)
		}
		if at <= end {
			return composerTarget{kind: '/', end: end, query: u.Text[1:end]}
		}
	}
	start := at
	for start > 0 {
		r, n := utf8.DecodeLastRuneInString(u.Text[:start])
		if unicode.IsSpace(r) {
			break
		}
		start -= n
	}
	if start == len(u.Text) || (u.Text[start] != '@' && u.Text[start] != '$') {
		return composerTarget{}
	}
	end := at
	for end < len(u.Text) {
		r, n := utf8.DecodeRuneInString(u.Text[end:])
		if unicode.IsSpace(r) {
			break
		}
		end += n
	}
	if at < start {
		return composerTarget{}
	}
	for _, file := range u.Files {
		if start < file.End && end > file.Start {
			return composerTarget{}
		}
	}
	for _, skill := range u.Skills {
		if start < skill.End && end > skill.Start {
			return composerTarget{}
		}
	}
	for _, image := range u.Images {
		if start < image.End && end > image.Start {
			return composerTarget{}
		}
	}
	for _, selection := range u.Selections {
		if start < selection.End && end > selection.Start {
			return composerTarget{}
		}
	}
	query := u.Text[start+1 : end]
	if u.Text[start] == '$' && !u.skillQueryCompletable(query) {
		return composerTarget{}
	}
	return composerTarget{u.Text[start], start, end, query}
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
		u.picker.open = false
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
	if target.kind == 0 && p.modal == "" && (!p.skillsLoaded || p.skillsCwd != u.session.cwd) {
		query := u.skillBindingQuery(draft, false)
		for _, part := range slices.Concat(u.conversation().Unsent, u.conversation().Queued) {
			if query == "" {
				query = u.skillBindingQuery(part, true)
			}
		}
		if query != "" {
			target, bindingOnly = composerTarget{kind: '$', query: query}, true
		}
	}
	defer func() {
		if bindingOnly {
			p.open = false
		}
	}()
	if p.modal == "menu" || p.modal == "copy" || p.modal == "settings" || p.modal == "archive" {
		p.open = true
		return
	}
	if p.modal == "manage" {
		target = composerTarget{kind: '$', query: p.query}
	}
	if target != p.dismissed {
		p.dismissed = composerTarget{}
	}
	if target.kind == 0 || target == p.dismissed {
		p.open = false
		u.cancelPickerScan()
		return
	}
	if target != p.target {
		p.target, p.choices, p.problem = target, nil, ""
		p.resolved = composerTarget{}
	}
	if !p.open && !bindingOnly {
		p.resolved = composerTarget{}
		p.problem = ""
	}
	p.open = true
	if target.kind == '/' {
		u.cancelPickerScan()
		u.filterCommands(target.query)
		return
	}
	cwd := u.session.cwd
	if !filepath.IsAbs(cwd) {
		p.loading, p.problem = false, "Waiting for the thread workspace…"
		return
	}
	if target.kind != '@' || !strings.HasPrefix(target.query, "!") {
		u.cancelPickerScan()
	}
	if target.kind == '$' && (p.skillsLoaded || p.skillsProblem != "") && p.skillsCwd == cwd {
		p.loading = false
		u.filterSkills(target.query)

		return
	}
	if target.kind == '@' && target.query == "" {
		p.loading, p.choices, p.problem = false, nil, "Type to search files"
		return
	}
	if p.resolved == target && p.resolvedCwd == cwd {
		return
	}
	p.loading, p.choices, p.problem = true, nil, ""
	if target.kind == '@' && strings.HasPrefix(target.query, "!") {
		u.searchExcludedFiles(target, cwd)
		return
	}
	if p.pending != nil {
		return
	}
	u.requestPicker(target, cwd)
}

func (u *appServerUI) requestPicker(target composerTarget, cwd string) {
	p := &u.picker
	method := "fuzzyFileSearch"
	params := map[string]any{"query": target.query, "roots": []string{cwd}}
	if target.kind == '$' {
		method, params = "skills/list", map[string]any{"cwds": []string{cwd}, "forceReload": true}
	}
	p.pending, p.pendingCwd = new(target), cwd
	if err := u.request(method, params); err != nil {
		p.pending, p.loading = nil, false
		p.resolved, p.resolvedCwd = target, cwd
		p.problem = "Search failed: " + err.Error()
		if target.kind == '$' {
			p.skillsCwd, p.skillsProblem = cwd, p.problem
			u.setNotice("Skill attachment unavailable: "+p.problem+". References remain text.", true)
		}
	}
}

// Source: codex-rs/utils/fuzzy-match/src/lib.rs:12:72
// @86be5320b068ef67b56348b02aa8c33706955da6. Lower scores favor compact prefix matches.
func pickerMatchScore(name, query string) (int, bool) {
	lower := func(s string) string { return strings.ToLower(strings.ReplaceAll(s, "İ", "i\u0307")) }
	needle := []rune(lower(query))
	if len(needle) == 0 {
		return 0, true
	}
	first, last, matched := -1, 0, 0
	for i, r := range []rune(lower(name)) {
		if r != needle[matched] {
			continue
		}
		if first < 0 {
			first = i
		}
		last = i
		matched++
		if matched == len(needle) {
			break
		}
	}
	if matched != len(needle) {
		return 0, false
	}
	score := last - first + 1 - len(needle)
	if first == 0 {
		score -= 100
	}
	return score, true
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
	if p.pending == nil {
		return true
	}
	target, cwd := *p.pending, p.pendingCwd
	p.pending = nil
	var choices []composerChoice
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
				choices = append(choices, composerChoice{name: skill.Name, path: skill.Path, description: description, display: display, enabled: skill.Enabled})
			}
			if len(entry.Errors) > 0 {
				problem = fmt.Sprintf("%d skill load errors: %s", len(entry.Errors), entry.Errors[0].Message)
			}
		}
	} else {
		var result struct {
			Files []struct {
				Root      string `json:"root"`
				Path      string `json:"path"`
				MatchType string `json:"match_type"`
				Indices   []int  `json:"indices"`
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
			choices = append(choices, composerChoice{name: file.Path, path: file.Path, directory: file.MatchType == "directory", indices: file.Indices})
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
					names[choice.name] = true
				}
				choices = slices.DeleteFunc(choices, func(choice composerChoice) bool { return names[choice.name] })
				choices = append(choices, managed...)
				available = true
			} else {
				if problem != "" {
					problem += "; "
				}
				problem += "skills-mgr: " + loadErr.Error()
			}
		}
		p.skills, p.skillsCwd, p.skillsLoaded = choices, cwd, available
		p.skillsProblem = ""
		if !available {
			p.skillsProblem = problem
		}
		if p.modal == "manage" && p.initial == nil {
			u.rememberSkillState()
		}
		if problem != "" && cwd == u.session.cwd {
			label := "Skill catalog incomplete: "
			if !available {
				label = "Skill attachment unavailable: "
			}
			u.setNotice(label+problem+". Unavailable references remain text.", true)
		}
	}
	if target == p.target && cwd == u.session.cwd {
		p.choices, p.loading, p.problem = choices, false, problem
		p.selected = min(p.selected, max(0, len(choices)-1))
		p.resolved, p.resolvedCwd = target, cwd
	}
	u.refreshPicker()
	return true
}

func (u *appServerUI) pickerKey(key string) bool {
	p := &u.picker
	if p.modal == "archive" && p.open {
		return u.archivePickerKey(key)
	}
	if p.modal == "settings" && p.open {
		return u.settingsPickerKey(key)
	}
	if p.modal == "copy" && p.open {
		return u.copyPickerKey(key)
	}
	if p.modal != "" && p.open {
		return u.skillsModalKey(key)
	}
	if !p.open {
		return false
	}
	// Drafts can also change outside key(), through editor or transcript reference.
	if u.completionTarget() != p.target {
		u.refreshPicker()
		return false
	}
	switch key {
	case "\x1b":
		p.dismissed, p.open = p.target, false
		u.cancelPickerScan()
		return true
	case "\x1b[A", "\x1bOA", "\x10", "\x1b[B", "\x1bOB", "\x0e":
		n := len(p.choices)
		if n > 0 {
			step := 1
			if key == "\x1b[A" || key == "\x1bOA" || key == "\x10" {
				step = -1
			}
			p.selected = (p.selected + step + n) % n
		}
		return true
	case "\t", "\r":
		if len(p.choices) == 0 {
			p.dismissed, p.open = p.target, false
			return key == "\t" || p.target.kind == '$'
		}
		choice, target := p.choices[p.selected], p.target
		if target.kind == '/' {
			u.deleteDraftRange(target.start, target.end)
			u.InsertText(choice.name + " ")
			u.Run, p.open = composer.NoRun, false
			// Enter continues through the existing local command dispatcher.
			return key == "\t"
		}
		text := choice.path
		if target.kind == '$' {
			text = "$" + choice.name
		} else if strings.ContainsFunc(text, unicode.IsSpace) && !strings.Contains(text, "\"") {
			text = "\"" + text + "\""
		}
		if target.kind == '@' {
			text = "@" + text
		}
		imagePath := ""
		if target.kind == '@' {
			path := choice.path
			if !filepath.IsAbs(path) {
				path = filepath.Join(u.session.cwd, path)
			}
			if imageFile(path) {
				imagePath = path
				text = fmt.Sprintf("[Image %d]", len(u.Images)+1)
			}
		}
		// One undo step restores the original token, not a half-completed edit.
		u.deleteDraftRange(target.start, target.end)
		u.InsertText(text)
		if imagePath != "" {
			u.Images = append(u.Images, composer.Token{Start: target.start, End: target.start + len(text), Path: imagePath})
			u.RenumberImages()
		}
		if target.kind == '@' && imagePath == "" {
			u.Files = append(u.Files, composer.Token{Start: target.start, End: target.start + len(text), Path: choice.path})
			slices.SortFunc(u.Files, func(a, b composer.Token) int { return a.Start - b.Start })
		}
		if target.kind == '$' {
			u.Skills = append(u.Skills, composer.Token{Start: target.start, End: target.start + len(text), Name: choice.name, Path: choice.path})
		}
		if u.Cursor() == len(u.Text) || u.Text[u.Cursor()] != ' ' {
			u.InsertText(" ")
		} else {
			u.CursorBack--
		}
		u.Run, p.open = composer.NoRun, false
		return true
	}
	return false
}

func (u *appServerUI) pickerVisible() bool {
	return !u.composerReadOnly() && u.picker.open && (u.shell == nil || u.shell.Focus == 0)
}
