package sessionpresentation

import (
	"time"

	"github.com/yusing/mekugi/contracts/observation"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
)

// TurnModels borrows existing owners for one observation. Selected remains the
// control-owned Main turn or the child roster's selection, never a second ledger.
type TurnModels struct {
	Transcript observation.Transcript
	Agent      *activity.AgentLifecycle
	Selected   string
	Start      string // Admitted start origin for this thread, when supplied by live control.
}

type TurnObservation struct {
	Kind         observation.Kind
	Thread, Turn string
	Status       string
	At           time.Time
}

type TurnUpdates struct {
	Admitted, Current bool
	Text              []observation.TextUpdate
}

// ObserveTurn joins scoped presentation without issuing control or storage
// effects. A stale completion closes only its own correlation; commands have an
// independent operation lifetime and are not part of turn retirement.
// Source: apps/mekugi/internal/router/app_server_events.go:263:300@[772b32f9].
func (a *Attachment) ObserveTurn(models TurnModels, e TurnObservation) TurnUpdates {
	stores := a.Transcript(e.Thread, e.Turn)
	result := TurnUpdates{Admitted: stores.Turn != nil}
	switch e.Kind {
	case observation.TurnStarted:
		if !result.Admitted {
			return result
		}
		key := [2]string{e.Thread, e.Turn}
		record := a.turns[key]
		if record.start == "" && models.Start != "" && (models.Selected == "" || models.Selected == e.Turn) {
			record.start = models.Start
			a.turns[key] = record
		}
		models.Agent.TurnStarted(e.At)
	case observation.TurnCompleted:
		result.Current = result.Admitted && (models.Selected == "" || models.Selected == e.Turn)
		if result.Admitted {
			result.Text = models.Transcript.TurnCompleted(e.Thread, e.Turn, e.Status, e.At, result.Current, stores)
			a.CloseTurn(e.Thread, e.Turn)
		}
		if result.Current {
			models.Agent.TurnCompleted(e.Status == "completed", e.At)
		}
	}
	return result
}
