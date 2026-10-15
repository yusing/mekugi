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
	InputProjection(InputProjection) error
}

type Binder interface {
	Registrar
	Capability(capability.Mechanism) capability.Result
}

type Bindings[S any] struct {
	Requires []capability.Requirement
	Register func(Registrar, S) error
}

// Registry collects composition-ordered bindings without executing them.
type Registry struct {
	commands    []command.Spec
	projections []InputProjection
}

func (r *Registry) Capability(mechanism capability.Mechanism) capability.Result {
	if mechanism == capability.ExecutableFrontend {
		return capability.Result{State: capability.Available, Reason: "compiled frontend registration"}
	}
	if mechanism == capability.InputProjection {
		return capability.Result{State: capability.Available, Reason: "request-local input projection"}
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
