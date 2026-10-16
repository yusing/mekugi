package terminalview

import "github.com/yusing/mekugi/libraries/terminalui"

// HookDetails specializes Run without borrowing command exits or source.
type HookDetails struct {
	HandlerType, ExecutionMode, Status string
	HasError                           bool
}

func (b Block) runColor() string {
	if b.ExitCode != 0 || b.Hook != nil && (b.Hook.Status == "failed" || b.Hook.HasError) {
		return terminalui.Red
	}
	return VerbColor("Run")
}
