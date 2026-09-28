package router

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/yusing/mekugi/internal/appserver"
)

// Source: codex-rs/tui/src/bottom_pane/chat_composer.rs:2094:2250,2744:2805
// @86be5320b068ef67b56348b02aa8c33706955da6. File and skill completion. Searches
// remain Codex-owned, through app-server rather than a second filesystem index.
type composerFile struct {
	start, end int
	path       string
}

type composerSkill struct {
	start, end int
	name, path string
}

type composerTarget struct {
	kind       byte
	start, end int
	query      string
}

type composerChoice struct {
	name, path, description string
	display                 string
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
	rect                 terminalRect
	target, dismissed    composerTarget
	open                 bool
	choices              []composerChoice
	selected             int
	loading              bool
	problem              string
	// One in-flight request; edits coalesce until its response, which is never
	// allowed to replace results for a different query or workspace.
	pending      *composerTarget
	pendingCwd   string
	resolved     composerTarget
	resolvedCwd  string
	skills       []composerChoice
	skillsCwd    string
	skillsLoaded bool
}

func (u *appServerUI) completionTarget() composerTarget {
	if u.shellMode() {
		return composerTarget{}
	}
	at := u.cursor()
	if strings.HasPrefix(u.draft, "/") && at > 0 && !strings.ContainsFunc(u.draft, unicode.IsSpace) {
		return composerTarget{kind: '/', end: len(u.draft), query: u.draft[1:]}
	}
	start := at
	for start > 0 {
		r, n := utf8.DecodeLastRuneInString(u.draft[:start])
		if unicode.IsSpace(r) {
			break
		}
		start -= n
	}
	if start == len(u.draft) || (u.draft[start] != '@' && u.draft[start] != '$') {
		return composerTarget{}
	}
	end := at
	for end < len(u.draft) {
		r, n := utf8.DecodeRuneInString(u.draft[end:])
		if unicode.IsSpace(r) {
			break
		}
		end += n
	}
	if at < start {
		return composerTarget{}
	}
	for _, file := range u.files {
		if start < file.end && end > file.start {
			return composerTarget{}
		}
	}
	for _, skill := range u.skills {
		if start < skill.end && end > skill.start {
			return composerTarget{}
		}
	}
	for _, image := range u.images {
		if start < image.end && end > image.start {
			return composerTarget{}
		}
	}
	query := u.draft[start+1 : end]
	if u.draft[start] == '$' && !u.skillQueryCompletable(query) {
		return composerTarget{}
	}
	return composerTarget{u.draft[start], start, end, query}
}

func (u *appServerUI) refreshPicker() {
	if u.paste || u.escape != "" || !utf8.ValidString(u.draft) {
		return
	}
	p := &u.picker
	target := u.completionTarget()
	if p.modal == "menu" {
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
	if !p.open {
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
	if target.kind == '$' && p.skillsLoaded && p.skillsCwd == cwd {
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
		if err == nil {

			p.skills, p.skillsCwd, p.skillsLoaded = choices, cwd, true
			if p.modal == "manage" && p.initial == nil {
				u.rememberSkillState()
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
			if pickerVCSPath(file.Path) {
				continue
			}
			choices = append(choices, composerChoice{name: file.Path, path: file.Path, directory: file.MatchType == "directory", indices: file.Indices})
		}
	}
	if err != nil {
		choices = nil
		problem = "Invalid search response: " + err.Error()
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
			u.insertDraftText(choice.name + " ")
			u.run, p.open = runNone, false
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
				text = fmt.Sprintf("[Image %d]", len(u.images)+1)
			}
		}
		// One undo step restores the original token, not a half-completed edit.
		u.deleteDraftRange(target.start, target.end)
		u.insertDraftText(text)
		if imagePath != "" {
			u.images = append(u.images, composerImage{target.start, target.start + len(text), imagePath})
			u.renumberImages()
		}
		if target.kind == '@' && imagePath == "" {
			u.files = append(u.files, composerFile{target.start, target.start + len(text), choice.path})
			slices.SortFunc(u.files, func(a, b composerFile) int { return a.start - b.start })
		}
		if target.kind == '$' {
			u.skills = append(u.skills, composerSkill{target.start, target.start + len(text), choice.name, choice.path})
		}
		if u.cursor() == len(u.draft) || u.draft[u.cursor()] != ' ' {
			u.insertDraftText(" ")
		} else {
			u.cursorBack--
		}
		u.run, p.open = runNone, false
		return true
	}
	return false
}

func (u *appServerUI) pickerVisible() bool {
	return u.picker.open && (u.shell == nil || u.shell.focus == 0)
}

// Rebind only complete, unambiguous skill tokens from authoritative history or
// the edited draft. A similarly prefixed word must not activate a stale skill.
func restoredSkillBindings(text, name, path string) []composerSkill {
	var skills []composerSkill
	at := 0
	for word := range strings.FieldsSeq(text) {
		start := at + strings.Index(text[at:], word)
		if word == "$"+name {
			skills = append(skills, composerSkill{start, start + len(word), name, path})
		}
		at = start + len(word)
	}
	return skills
}
