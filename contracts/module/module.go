package module

import (
	"context"

	"github.com/yusing/mekugi/contracts/diagnosticsection"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/guidance"
	"github.com/yusing/mekugi/contracts/inspection"
	"github.com/yusing/mekugi/contracts/presentation"
	"github.com/yusing/mekugi/contracts/recordkind"
	"github.com/yusing/mekugi/contracts/reference"
)

type ID string
type Lifetime string

// NoBindings declares a backend-neutral module.
type NoBindings struct{}

// NoViews declares a module without renderer contributions.
type NoViews struct{}

const (
	Stateless       Lifetime = "stateless"
	Invocation      Lifetime = "invocation"
	AttachedSession Lifetime = "attached-session"
	HostOperation   Lifetime = "host-operation"
)

// Slots contains the contribution families implemented by the current binder.
// Shared extension families use slots; single-provider relationships use typed dependencies.
type Slots struct {
	DiagnosticSections []diagnosticsection.Contribution
	Inspection         []inspection.Contribution
	Guidance           []guidance.Section
	RecordKinds        []recordkind.Spec
	References         []reference.Resolver
}

// Contributions declares typed contributions before construction. Slots holds
// values for module hosts; composition supplies presentation to its application host.
type Contributions[S any] struct {
	Presentation       []presentation.Status
	DiagnosticSections []diagnosticsection.Definition[S]
	Inspection         []inspection.Definition[S]
	Guidance           []guidance.Section
	Commands           []command.Definition[S]
	RecordKinds        []recordkind.Spec
	References         []reference.Resolver
}

// Descriptor keeps typed construction separate from backend-specific bindings.
// B is the backend binding value, so contracts never imports a backend package.
// New assembles state; Start acquires declared invocation resources.
type Descriptor[D, S, B, V any] struct {
	ID                      ID
	Summary                 string
	Requires                []ID
	Lifetime                Lifetime
	Resources               []string
	Contributes             Contributions[S]
	HostsGuidance           bool
	HostsRecords            bool
	HostsReferences         bool
	HostsInspection         bool
	HostsDiagnosticSections bool
	New                     func(D, Slots) (S, error)
	Views                   V
	Bindings                B
	Start                   func(context.Context, S) error
	Stop                    func(context.Context, S) error
}
