package changes

import (
	"context"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/libraries/persistence"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

// MaxReadBytes bounds retained review projections and mutation source reads.
const MaxReadBytes = 64 << 20

// Scope carries authenticated retained ownership and the existing publication
// handle. Workspace operands remain explicit, separate from namespace identity.
type Scope struct {
	Store             *history.Store
	Namespace, Thread string
	Limits            history.Limits
	Writes            *persistence.Counter
}

type Query struct {
	Workspace  string
	IDs, Paths []string
	View       string
	Mine       bool
	MaxTokens  int
}

// Publication associates already-published evidence with an allocated change.
// Publish and Repair key each publication by its host call ID. Sequence is the
// source batch ordering, not the durable index sequence.
type Publication struct {
	ChangeID, CorrelationID, ExecutingThread string
	Sequence                                 uint64
	Managed, Confirmed                       bool
	// Dependency retains evidence without publishing a completed attempt.
	Dependency bool
	// NoEffect settles an unfinished reservation without publishing an attempt.
	NoEffect bool
}

type Call struct {
	Sequence   uint64
	Thread, ID string
	Managed    bool
}
type Change struct {
	Correlation  string
	Calls        []Call
	Dependencies []string
	RetiredCalls int
}
type Stream struct {
	Name, Thread  string
	Next, Retired int
}

// Snapshot contains domain membership, not a writable index or record encoding.
type Snapshot struct {
	Sequence             uint64
	Workspace, Namespace string
	Streams              []Stream
	Changes              map[string]Change
}

type Update struct {
	Workspace, Namespace, Thread string
	Stream                       int
	ID                           string
	Change                       Change
}

// ForkPlan contains frozen membership. Prepare and Publish belong to the same
// caller-owned transaction; dependencies are retained before Publish is called.
type ForkPlan interface {
	Dependencies() []string
	Publish(*history.Transaction) error
}

// Service owns retained change meaning. Publish and PrepareFork borrow the
// caller's transaction. Reserve, Repair, Read, and MutationPaths acquire their
// own transaction; callers must not nest them inside another publication.
type Service interface {
	Reserve(context.Context, Scope, string, string, string) (string, error)
	Publish(*history.Transaction, Scope, string, map[string]Publication) ([]Update, error)
	Repair(context.Context, Scope, string, map[string]Publication) ([]Update, error)
	Inspect(Scope, string) (Snapshot, error)
	InspectVisible(Scope, string, map[string]bool) ([]Snapshot, error)
	Dependencies(Scope, string, []string, bool) ([]string, error)
	PrepareFork(Scope, string, string, string) (ForkPlan, error)
	Parse([]string, string) (Query, error)
	Read(context.Context, Scope, Query) (string, error)
	MutationPaths(context.Context, Scope, Query) ([]string, error)
	Execute(context.Context, command.Request) (command.Output, error)
}

// PublicationError identifies failed retention/admission/publication so the
// application can present its storage diagnostic without classifying read errors.
type PublicationError struct{ Err error }

func (e *PublicationError) Error() string { return e.Err.Error() }
func (e *PublicationError) Unwrap() error { return e.Err }
