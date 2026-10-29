package requestexecution

import (
	responseevents "github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/libraries/persistence"
	"github.com/yusing/mekugi/modules/diagnostics/diagnostic"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
)

// FailureStorage borrows the composed publication and diagnostic owners.
type FailureStorage struct {
	Diagnostics *diagnostic.Service
	Store       *history.Store
	Limits      history.Limits
	Writes      *persistence.Counter
}

func (c *Failures) Configure(storage FailureStorage) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.diagnostics, c.failureStore, c.failureLimits, c.writes = storage.Diagnostics, storage.Store, storage.Limits, storage.Writes
}

// Diagnostic contains the published classification and private reference.
type Diagnostic struct{ Code, Reference, Error, Message string }

// Failure is an already-failed operation, detached from an attempt.
// A models-phase failure retains diagnostics without interrupting model turns.
type Failure struct {
	Session, Thread, Turn, Phase string
	UpstreamStatus               int
	Terminal                     responseevents.TerminalState
	Stream                       *transportcapture.Stream
}

func (c *Failures) Report(failure Failure, err error) Diagnostic {
	f := requestFinalization{sessionID: failure.Session, threadID: failure.Thread, turnID: failure.Turn,
		failurePhase: requestFailurePhase(failure.Phase), upstreamStatusCode: failure.UpstreamStatus,
		upstreamTerminalState: failure.Terminal, streamDiagnostics: failure.Stream,
		outcome: requestOutcomeFailed}
	c.record(&f, err)
	c.persistFailure(&f)
	return Diagnostic{f.diagnosticCode, f.diagnosticReference, f.diagnosticError, f.diagnosticMessage}
}
