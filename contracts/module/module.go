package module

import (
	"context"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/recordkind"
)

type ID string
type Lifetime string

// NoBindings declares a backend-neutral module.
type NoBindings struct{}

const (
	Stateless  Lifetime = "stateless"
	Invocation Lifetime = "invocation"
)

// Slots contains the contribution families implemented by the current binder.
// Further families are added with their first consumer, rather than placeholders.
type Slots struct {
	Commands    []command.Spec
	RecordKinds []recordkind.Spec
}

// Descriptor keeps typed construction separate from backend-specific bindings.
// B is the backend binding value, so contracts never imports a backend package.
// New assembles state; Start acquires declared invocation resources.
type Descriptor[D, S, B any] struct {
	ID            ID
	Summary       string
	Requires      []ID
	Lifetime      Lifetime
	Resources     []string
	Contributes   Slots
	HostsCommands bool
	HostsRecords  bool
	New           func(D, Slots) (S, error)
	Bindings      B
	Start         func(context.Context, S) error
	Stop          func(context.Context, S) error
}
