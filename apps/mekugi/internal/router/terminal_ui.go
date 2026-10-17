package router

import (
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/contracts/scope"

	previewview "github.com/yusing/mekugi/modules/livepreview/terminalview"
)

// The native shell owns terminal presentation; Codex app-server owns execution.
type terminalUI struct {
	terminalsession.Interaction
	output        *terminalsession.ContentDialog // Open over both panes, taking every key.
	diffScreen    *terminalsession.DiffScreen
	preparePane   func(*scope.Store)
	agents        *activityui.View
	main          *appServerUI
	width, height int
	terminalsession.Shell
	diffUnseen bool // Saved changes arrived while Diff was closed.
	liveDock   previewview.PreviewPane
	liveHidden bool // Presentation only, local to this frontend session.
	faint      bool // Output capability is local to this terminal, not request or global state.
}
