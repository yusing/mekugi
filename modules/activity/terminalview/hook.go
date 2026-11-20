package terminalview

import (
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/modules/activity/presentation"
)

// HookDetails specializes Run without borrowing command exits or source.
type HookDetails = presentation.HookDetails

func (b Block) runColor(theme diffrender.Theme) string {
	if b.ExitCode != 0 || b.Hook != nil && b.Hook.Failed() {
		return theme.Danger()
	}
	return VerbColor(theme, "Run")
}
