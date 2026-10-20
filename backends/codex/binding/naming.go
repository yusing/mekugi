package binding

import (
	"context"
	"errors"

	"github.com/yusing/mekugi/contracts/inference"
)

// ThreadNaming is one module-owned typed policy entry held by an application
// logical thread scope. Its admitted operation carries no thread identity.
type ThreadNaming interface {
	ObserveMetadata(name string, hasTurns bool)
	NameChanged(string)
	ObserveInput([]byte)
	Admit() func(context.Context, inference.Complete) (string, error)
	AcceptCandidate() bool
}

type SessionNaming struct {
	NewThread func(name string, hasTurns bool) ThreadNaming
}

func (r *Registry) SessionNaming(naming SessionNaming) error {
	if naming.NewThread == nil {
		return errors.New("session naming requires a thread factory")
	}
	if r.naming != nil {
		return errors.New("duplicate session naming registration")
	}
	r.naming = &naming
	return nil
}

func (r *Registry) RegisteredNaming() *SessionNaming { return r.naming }
