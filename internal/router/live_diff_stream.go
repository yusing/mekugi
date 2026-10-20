package router

import (
	"errors"
	"fmt"
)

func validateLiveDiffEvent(event liveDiffEvent) error {
	switch event.Kind {
	case "scope":
		if event.Scope == nil || event.Scope.Workspaces == nil {
			return errors.New("missing live diff scope")
		}
	case "change":
		if len(event.Changes) == 0 {
			return errors.New("missing live diff change")
		}
	case "preview":
		if event.Preview == nil || event.Preview.ID == "" {
			return errors.New("missing streaming preview")
		}
	case "turn":
		if event.TurnRevision == 0 || event.Status != "active" && event.Status != "completed" {
			return errors.New("invalid live diff turn status")
		}
	case "coverage", "heartbeat", "end":
	case "error":
		return errors.New(event.Status)
	default:
		return fmt.Errorf("unknown live diff event %q", event.Kind)
	}
	return nil
}
