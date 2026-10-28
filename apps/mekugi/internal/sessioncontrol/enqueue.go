package sessioncontrol

type InputMode uint8

const (
	SendInput InputMode = iota
	QueueNextTurn
)

type QueuePlan struct {
	Queued, Flush bool
}

// PlanInput separates local queue admission from readiness to issue a request.
// Source: apps/mekugi/internal/router/app_server_ui.go:1124:1150@9a873ac2.
func (h *Live) PlanInput(mode InputMode, waitingQuestion bool) QueuePlan {
	next := mode == QueueNextTurn && (h.Turn != "" || h.LocalStop != "" || h.Starting() || h.InputPending())
	return QueuePlan{Queued: next || waitingQuestion, Flush: !next}
}
