package module

import (
	"context"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/recordkind"
	"github.com/yusing/mekugi/contracts/reference"
)

type ID string
type Lifetime string

// NoBindings declares a backend-neutral module.
type NoBindings struct{}

const (
	Stateless     Lifetime = "stateless"
	Invocation    Lifetime = "invocation"
	HostOperation Lifetime = "host-operation"
)

// Slots contains the contribution families implemented by the current binder.
// Further families are added with their first consumer, rather than placeholders.
type Slots struct {
	Commands    []command.Contribution
	RecordKinds []recordkind.Spec
	References  []reference.Resolver
}

// Contributions declares typed commands before construction. Slots holds the
// complete collected runtime values passed to hosts.
type Contributions[S any] struct {
	Commands    []command.Definition[S]
	RecordKinds []recordkind.Spec
	References  []reference.Resolver
}

// Descriptor keeps typed construction separate from backend-specific bindings.
// B is the backend binding value, so contracts never imports a backend package.
// New assembles state; Start acquires declared invocation resources.
type Descriptor[D, S, B any] struct {
	ID              ID
	Summary         string
	Requires        []ID
	Lifetime        Lifetime
	Resources       []string
	Contributes     Contributions[S]
	HostsCommands   bool
	HostsRecords    bool
	HostsReferences bool
	New             func(D, Slots) (S, error)
	Bindings        B
	Start           func(context.Context, S) error
	Stop            func(context.Context, S) error
}
