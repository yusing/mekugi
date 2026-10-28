package launch

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/libraries/commandargs"
	"github.com/yusing/mekugi/modules/conversation/completion"
	"github.com/yusing/mekugi/modules/conversation/composer"
	"github.com/yusing/mekugi/modules/conversation/presets"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

func presetsAvailable(a *attachedPresentation, control *sessioncontrol.Client[appServerRequest], restoring bool) bool {
	view := a.presentation.View()
	return control.Thread != "" && !restoring && !control.Replacement.Pending() && view.ReadOnly == sessionpresentation.Editable && view.PromptDraft.Call == "" && view.PromptDraft.Request == "" && view.Input.ID == view.Main.ID && view.Displayed.ID == view.Main.ID
}

// refreshPresetCatalog publishes the admitted catalog without starting reads.
func (p *composerPicker) refreshPresetCatalog(a *attachedPresentation, control *sessioncontrol.Client[appServerRequest], restoring bool) {
	allowed := presetsAvailable(a, control, restoring)
	changed := allowed != p.presetAllowed
	p.presetAllowed = allowed
	if p.presetThread != control.Thread || p.presetCwd != a.cwd {
		p.presetCatalog = nil
		p.presetThread, p.presetCwd = control.Thread, a.cwd
		changed = true
	}
	if allowed && p.presetCatalog != nil && p.presets == nil && p.presetCatalog.Poll() {
		changed = true
	}
	if !changed {
		return
	}
	p.Presets = nil
	if allowed && p.presetCatalog != nil {
		p.Presets = p.presetCatalog.NamedChoices()
	}
	p.skillsRevision++
	if p.Open && p.Kind == "" && p.Target.Kind == '$' {
		p.includePresets(p.Target.Query)
	}
}

// The first dollar completion loads the ten slots with the requesting view's
// lifetime. Later draft edits reuse them, and management supplies saved changes.
func (p *composerPicker) acquirePresetCatalog(ctx context.Context, requests *sessioncontrol.Requests[appServerRequest]) {
	if ctx == nil || !p.presetAllowed || p.presetCatalog != nil {
		return
	}
	p.presetCatalog = new(presets.Flow)
	p.presetCatalog.Open(requests.Presets(ctx, p.presetThread, p.presetCwd), "", "")
}

func (p *composerPicker) includePresets(query string) {
	p.IncludePresets(query)
	if !p.presetAllowed || p.presetCatalog == nil {
		return
	}
	if len(p.Choices) > 0 {
		// Available references remain usable while another catalog is pending or
		// unavailable. Skill failures also have their existing composer notice.
		p.Problem, p.Loading = "", false
		return
	}
	p.Loading = p.Pending != nil || p.presetCatalog.Pending
	if problem := p.presetCatalog.Problem; !p.presetCatalog.Loaded && problem != "" && !strings.Contains(p.Problem, problem) {
		if p.Problem != "" {
			p.Problem += "; "
		}
		p.Problem += "Presets unavailable: " + problem + ". Retry with /presets."
	}
}

func openPresets(ctx context.Context, a *attachedPresentation, control *sessioncontrol.Client[appServerRequest], p *composerPicker, editor *composer.Editor, applied preferences.Applied, restoring bool) {
	if !presetsAvailable(a, control, restoring) {
		a.feedback.Set("Presets are unavailable in this conversation", true, time.Now())
		return
	}
	seed := editor.Text
	if strings.TrimSpace(seed) == "/presets" {
		editor.Replace(composer.Draft{})
		seed = ""
	}
	if len(editor.Spans()) > 0 || len(editor.Attachments) > 0 || len(editor.AnswerImages) > 0 {
		seed = ""
	}
	settings := "/model " + commandargs.Quote(applied.Model)
	if applied.Effort != "" {
		settings += " --reasoning=" + commandargs.Quote(applied.Effort)
	}
	tier := applied.Tier
	switch tier {
	case "", "default":
		tier = "standard"
	case "priority":
		tier = "fast"
	}
	settings += " --speed=" + commandargs.Quote(tier)
	p.presets = new(presets.Flow)
	p.presets.Open(control.Requests.Presets(ctx, control.Thread, a.cwd), seed, settings)
	p.presetCatalog = p.presets
	p.presetThread, p.presetCwd = control.Thread, a.cwd
	p.syncPresets()
}
func (p *composerPicker) syncPresets() {
	f := p.presets
	if f.Loaded && !f.Pending {
		choices := f.NamedChoices()
		if !slices.Equal(p.Presets, choices) {
			p.Presets = choices
			p.skillsRevision++
		}
	}
	p.openMenu(string(f.Stage))
	p.Choices, p.Problem, p.Loading, p.Query = f.Choices(), f.Problem, f.Pending, f.Query()
}
func (p *composerPicker) pollPresets() {
	if p.presets != nil && p.presets.Poll() {
		p.syncPresets()
	}
}
func choosePreset(a *attachedPresentation, control *sessioncontrol.Client[appServerRequest], p *composerPicker, action sessionpresentation.MenuAction, choice completion.Choice, restoring bool) bool {
	if !presetsAvailable(a, control, restoring) {
		p.Problem = "Presets are unavailable in this conversation"
		return false
	}
	input := p.presets.Choose(choice, action.Query)
	if input != "" {
		if a.effects == nil {
			p.Problem = "Preset recall requires an attached client"
			return false
		}
		a.effects.add(sessionapi.NativeEffect{Thread: control.Thread, Recall: new(input)})
		p.presets = nil
		p.Open = false
		p.Kind = ""
		p.Choices = nil
		p.Query = ""
		return true
	}
	p.syncPresets()
	return true
}
