package sessionpresentation

import (
	"errors"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
)

type InterruptIntent struct {
	Version Version
	Target  sessioncontrol.InterruptTarget
	Mode    sessioncontrol.InterruptMode
}

var ErrInterruptUnavailable = errors.New("Original work is unavailable.")

// ResolveInterrupt settles a delayed action against its original work. Only a
// retained closed turn proves completion; missing evidence cannot target a successor.
func (m *Model) ResolveInterrupt(intent InterruptIntent, control *sessioncontrol.Live, attachment *Attachment) (active bool, err error) {
	if err := m.admitInterruptOwner(intent, control); err != nil {
		return false, err
	}
	if attachment == nil || attachment.retired {
		return false, errors.New("The original session is unavailable.")
	}
	target := intent.Target
	if target.Wait != "" || target.Stop != "" {
		current := control.InterruptTarget()
		if target.Stop != "" && target.Stop == current.Stop && target.Wait == current.Wait &&
			(current.Turn == "" || current.Turn == target.Turn) &&
			(current.Start == "" || current.Start == target.Start) {
			return true, nil
		}
		return false, ErrInterruptUnavailable
	}
	if target.Turn != "" {
		if record, known := attachment.turns[[2]string{target.Thread, target.Turn}]; known && record.store == nil &&
			(target.Start == "" || target.Start == record.start) {
			return false, nil
		}
	} else if target.Start != "" {
		for key, record := range attachment.turns {
			if key[0] == target.Thread && record.start == target.Start && record.store == nil {
				return false, nil
			}
		}
	}
	if sameInterruptWork(target, control.InterruptTarget()) {
		return true, nil
	}
	return false, ErrInterruptUnavailable
}

func (m *Model) admitInterruptOwner(intent InterruptIntent, control *sessioncontrol.Live) error {
	if m.admitOwner(intent.Version) != nil {
		return errors.New("The original session is unavailable.")
	}
	if m.replay {
		return ErrReplayIntent
	}
	if intent.Mode != sessioncontrol.RestoreWaiting && intent.Mode != sessioncontrol.ExpediteSteers {
		return errors.New("Invalid interruption mode.")
	}
	if control == nil || intent.Target.Thread == "" || intent.Target.Thread != control.Thread || intent.Target.Thread != m.view.Main.ID {
		return errors.New("The interruption target does not belong to this session.")
	}
	return nil
}

func sameInterruptWork(target, current sessioncontrol.InterruptTarget) bool {
	if target.Wait != current.Wait || target.Stop != current.Stop || target.Thread != current.Thread || target.Start != "" && target.Start != current.Start {
		return false
	}
	if target.Turn != "" {
		return target.Turn == current.Turn
	}
	return target.Start != "" || current.Turn == "" && current.Start == ""
}
