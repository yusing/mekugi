package launch

import (
	"context"
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
	p.syncPresets()
}
func (p *composerPicker) syncPresets() {
	f := p.presets
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
