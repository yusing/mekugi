package router

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/yusing/mekugi/internal/session"
)

func (u *appServerUI) sendRuntimeInput() error {
	if len(u.images) == 0 {
		return u.runtime.client.Send(u.ctx, u.draft)
	}
	client, ok := u.runtime.client.(session.InputClient)
	if !ok {
		return fmt.Errorf("native image input is unavailable")
	}
	return client.SendInput(u.ctx, runtimeInputParts(u.draftSnapshot()))
}

func runtimeInputParts(draft composerDraft) []session.InputPart {
	var parts []session.InputPart
	at := 0
	for _, image := range draft.images {
		if image.start > at {
			parts = append(parts, session.InputPart{Text: draft.text[at:image.start]})
		}
		parts = append(parts, session.InputPart{ImagePath: image.path})
		at = image.end
	}
	if at < len(draft.text) {
		parts = append(parts, session.InputPart{Text: draft.text[at:]})
	}
	for _, attachment := range draft.attachments {
		frames, ok := decodeFileAttachments(attachment)
		if !ok {
			frames = []string{attachment}
		}
		for _, frame := range frames {
			parts = append(parts, session.InputPart{Text: frame})
		}
	}
	return parts
}

// Claude skills use native slash names, not Codex's dollar-token binding.
// Native pushes replace this snapshot; advertised aliases retain native precedence.
func (u *appServerUI) runtimeCommands(commands []session.Command) {
	byName := make(map[string]session.Command)
	for _, c := range commands {
		old, exists := byName[c.Name]
		if c.Name != "" && (!exists || !old.Builtin || c.Builtin) {
			byName[c.Name] = c
		}
	}
	for _, c := range commands {
		for _, name := range c.Aliases {
			if _, exists := byName[name]; !exists && name != "" {
				alias := c
				alias.Name = name
				byName[name] = alias
			}
		}
	}
	r := u.runtime
	r.commandInfo = nil
	for _, name := range slices.Sorted(maps.Keys(byName)) {
		r.commandInfo = append(r.commandInfo, byName[name])
	}
}

func (u *appServerUI) refreshRuntimePicker() {
	if !u.runtime.ready {
		u.picker.open = false
		u.cancelPickerScan()
		return
	}
	if u.currentQuestion() != nil || u.paste || u.escape != "" || !utf8.ValidString(u.draft) {
		return
	}
	p := &u.picker
	if p.modal != "" {
		return
	}
	target := u.completionTarget()
	if target.kind != '/' && target.kind != '@' || target == p.dismissed {
		p.open = false
		u.cancelPickerScan()
		return
	}
	if target != p.target {
		p.target, p.choices, p.problem = target, nil, ""
		p.resolved = composerTarget{}
	}
	p.open = true
	if target.kind == '/' {
		u.cancelPickerScan()
		u.runtimeCommandChoices(target.query)
		return
	}
	if target.query == "" {
		p.loading, p.choices, p.problem = false, nil, "Type to search workspace files"
		u.cancelPickerScan()
		return
	}
	if p.resolved == target && p.resolvedCwd == u.session.cwd {
		return
	}
	// Reuse the shared read-only scan. Claude discovery includes ignored files,
	// excluding VCS internals; only native @path expansion reads them for the model.
	u.searchExcludedFiles(target, u.session.cwd)
}

func (u *appServerUI) runtimeCommandChoices(query string) {
	p := &u.picker
	previous := ""
	if p.selected < len(p.choices) {
		previous = p.choices[p.selected].name
	}
	choices := map[string]composerChoice{}
	for _, c := range u.runtime.commandInfo {
		choices[c.Name] = composerChoice{name: "/" + c.Name, description: strings.TrimSpace(c.Description + " " + c.Arguments)}
	}
	for _, c := range []composerChoice{
		{name: "/copy", description: "Copy a response"}, {name: "/usage", description: "Show native usage and limits"},
		{name: "/session", description: "Show native usage and limits"}, {name: "/status", description: "Show session settings and usage limits"}, {name: "/quit", description: "Quit when idle"},
	} {
		choices[c.name[1:]] = c
	}
	for _, c := range nativeCommands {
		if c.name == "/lock" || c.name == "/unlock" || c.name == "/live" {
			choices[c.name[1:]] = c
		}
	}
	if _, ok := u.runtime.client.(session.TitleClient); ok {
		choices["title"] = composerChoice{name: "/title", description: "Rename this session"}
	}
	if _, ok := u.runtime.client.(session.SideClient); ok {
		choices["btw"] = composerChoice{name: "/btw", description: "Ask a side question without interrupting Main"}
	}
	if _, ok := u.runtime.client.(session.SessionChangeClient); ok {
		choices["clear"] = composerChoice{name: "/clear", description: "Clear the transcript and start a new session"}
		if _, ok := u.runtime.client.(session.SessionListClient); ok {
			choices["resume"] = composerChoice{name: "/resume", description: "Resume a saved session"}
		}
	}
	if _, ok := u.runtime.client.(session.SettingsClient); ok {
		choices["model"] = composerChoice{name: "/model", description: "Choose a native model"}
		choices["effort"] = composerChoice{name: "/effort", description: "Choose native effort"}
		choices["reasoning"] = composerChoice{name: "/reasoning", description: "Choose native effort"}
	}
	p.choices, p.loading, p.problem = nil, false, ""
	for _, name := range slices.Sorted(maps.Keys(choices)) {
		if _, ok := pickerMatchScore(name, query); ok {
			p.choices = append(p.choices, choices[name])
		}
	}
	slices.SortStableFunc(p.choices, func(a, b composerChoice) int {
		as, _ := pickerMatchScore(a.name[1:], query)
		bs, _ := pickerMatchScore(b.name[1:], query)
		return as - bs
	})
	p.selected = max(0, slices.IndexFunc(p.choices, func(c composerChoice) bool { return c.name == previous }))
}
