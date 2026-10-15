// Package binding exposes the Codex mechanisms implemented during extraction.
package binding

import (
	"fmt"
	"slices"

	"github.com/yusing/mekugi/contracts/capability"
	"github.com/yusing/mekugi/contracts/command"
)

type Registrar interface {
	Frontend(command.Spec) error
}

type Binder interface {
	Registrar
	Capability(capability.Mechanism) capability.Result
}

type Bindings[S any] struct {
	Requires []capability.Requirement
	Register func(Registrar, S) error
}

// Registry collects executable registrations for the existing authenticated
// router host. Registration does not execute a command or publish a wrapper.
type Registry struct{ commands []command.Spec }

func (r *Registry) Capability(mechanism capability.Mechanism) capability.Result {
	if mechanism == capability.ExecutableFrontend {
		return capability.Result{State: capability.Available, Reason: "compiled frontend registration"}
	}
	return capability.Result{State: capability.Unsupported, Reason: "mechanism has no implemented binding"}
}

func (r *Registry) Frontend(spec command.Spec) error {
	if err := spec.Validate(); err != nil {
		return err
	}
	if slices.ContainsFunc(r.commands, func(old command.Spec) bool { return old.Name == spec.Name }) {
		return fmt.Errorf("duplicate frontend %q", spec.Name)
	}
	r.commands = append(r.commands, spec)
	return nil
}

func (r *Registry) Commands() []command.Spec { return slices.Clone(r.commands) }
