package sessioncontrol

// InterruptTarget captures the host work observed by a consumer. Start keeps an
// admitted start identifiable through acknowledgement, commitment and turn start.
type InterruptTarget struct {
	Thread, Turn, Start, Wait, Stop string
}

type InterruptMode uint8

const (
	RestoreWaiting InterruptMode = iota
	ExpediteSteers
)

func (h *Live) InterruptTarget() InterruptTarget {
	return InterruptTarget{Thread: h.Thread, Turn: h.Turn, Start: h.startOrigin, Wait: h.Waiting, Stop: h.LocalStop}
}

// RequestInterrupt admits the disposition before applying the existing control
// transition. Automatic continuation of an admitted interrupt uses Interrupt.
// Source: apps/mekugi/internal/router/app_server_controls.go:24:50@74368197.
func (h *Live) RequestInterrupt(mode InterruptMode, unsentSteer bool) Interrupt {
	if mode == ExpediteSteers {
		if !h.PrepareEscape(unsentSteer) {
			return Interrupt{}
		}
	} else {
		h.SendSteersAfterInterrupt = false
	}
	return h.Interrupt()
}
