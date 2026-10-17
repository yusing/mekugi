package router

import (
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/executionobservation"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
)

// Native turn completion retires display state even when interrupted tools
// never return. It neither finalizes capture nor stops a host-owned process.
func endTurnPreviews(windows *executionobservation.Windows, review *sessionreview.Review, workspace string, terminal *terminalsession.Attachment, at time.Time, thread, turn string) {
	windows.StopTurnPreviews(thread, turn)
	if live := review.Preview(); live != nil {
		live.SetTurn(thread, turn, false)
		live.FinishTurn(workspace, thread, turn)
	}
	if terminal != nil {
		terminal.Live.EndTurn(thread, turn, at)
	}
}
