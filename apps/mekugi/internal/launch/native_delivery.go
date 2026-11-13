package launch

import (
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/appserver"
)

// A semantic event without a response item uses the same projection and item
// identity as retained replay; Activity owns deduplication and ordering.
func publishNativeCoordination(attached *attachedPresentation, thread, turn string, item sessioncontrol.NativeMessage, facts *appserver.CoordinationProjection, at time.Time) {
	entries := sessionpresentation.NativeCoordinationEntries(thread, turn, item, facts, at, false)
	for i := range entries {
		entries[i].Seq = attached.session.Activity().NextSequence()
	}
	sessionpresentation.ApplyActivity(attached.source, attached.childSource, entries, nil)
}

// Native resumes model work inside the shell turn after durable shell output or
// a claimed wait delivery. This only updates the existing physical-turn gate.
func (session *nativeSession) observeNativeShellContinuation(turn string, facts *appserver.CoordinationProjection) {
	if facts == nil || facts.Wait == nil {
		return
	}
	wait := facts.Wait
	if wait.CallerThreadID != session.Client.Thread || wait.CallerWorkID == "" || wait.CallerWorkID != session.Client.NativeWork {
		return
	}
	if wait.Result.Kind == "cancelled" {
		if wait.Result.Cause == nil || wait.Result.Cause.Kind != "shellOutput" {
			return
		}
		turn = wait.Result.Cause.TurnID
	} else if wait.Result.Kind != "released" {
		return
	}
	session.Client.Shell.ModelTurn(turn)
}
