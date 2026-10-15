package composition

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/capability"
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/contracts/recordkind"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

// Handle resolves one explicitly declared dependency during ordered construction.
// It is application wiring, not a service locator exposed to feature code.
type Handle[S any] struct {
	id    module.ID
	value S
	ready bool
}

func (h *Handle[S]) Value() S {
	if !h.ready {
		panic(fmt.Sprintf("module %s has not been constructed", h.id))
	}
	return h.value
}

type entry struct {
	id           module.ID
	requires     []module.ID
	lifetime     module.Lifetime
	resources    []string
	commands     []command.Spec
	records      []recordkind.Spec
	capabilities []capability.Requirement
	validFactory bool
	construct    func(module.Slots, binding.Binder) error
	start, stop  func(context.Context) error
}

type Composition struct {
	// LegacyCommands is the existing application catalog until step 3 moves its
	// implementations into descriptors. It is not represented as a fake module.
	LegacyCommands    []command.Spec
	LegacyRecordKinds []recordkind.Spec
	entries           []entry
	sealed            bool
}

// Add declares a factory, without constructing or starting it. The typed
// dependency closure resolves prior handles after all slots have been collected.
func (c *Composition) Add[D, S any, B interface {
	module.NoBindings | binding.Bindings[S]
}](descriptor module.Descriptor[D, S, B], deps func() D) *Handle[S] {
	if c.sealed {
		panic("composition is already activated")
	}
	backend, _ := any(descriptor.Bindings).(binding.Bindings[S])
	handle := &Handle[S]{id: descriptor.ID}
	node := entry{
		id: descriptor.ID, requires: slices.Clone(descriptor.Requires),
		lifetime: descriptor.Lifetime, resources: slices.Clone(descriptor.Resources),
		commands:     slices.Clone(descriptor.Contributes.Commands),
		records:      slices.Clone(descriptor.Contributes.RecordKinds),
		capabilities: slices.Clone(backend.Requires),
		validFactory: descriptor.New != nil && deps != nil,
	}
	node.construct = func(slots module.Slots, binder binding.Binder) error {
		if !descriptor.HostsCommands {
			slots.Commands = nil
		}
		if !descriptor.HostsRecords {
			slots.RecordKinds = nil
		}
		service, err := descriptor.New(deps(), slots)
		if err != nil {
			return err
		}
		handle.value, handle.ready = service, true
		registrar := &moduleRegistrar{binder: binder, allowed: node.commands, registered: make(map[string]bool)}
		if backend.Register != nil {
			if err := backend.Register(registrar, service); err != nil {
				return err
			}
		}
		if len(registrar.registered) != len(node.commands) {
			return errors.New("executable contributions and Codex registrations differ")
		}
		return nil
	}
	if descriptor.Start != nil {
		node.start = func(ctx context.Context) error { return descriptor.Start(ctx, handle.Value()) }
	}
	if descriptor.Stop != nil {
		node.stop = func(ctx context.Context) error { return descriptor.Stop(ctx, handle.Value()) }
	}
	c.entries = append(c.entries, node)
	return handle
}

type moduleRegistrar struct {
	binder     binding.Binder
	allowed    []command.Spec
	registered map[string]bool
}

func (r *moduleRegistrar) Frontend(spec command.Spec) error {
	if !slices.Contains(r.allowed, spec) {
		return fmt.Errorf("undeclared frontend %q", spec.Name)
	}
	if r.registered[spec.Name] {
		return fmt.Errorf("duplicate frontend %q", spec.Name)
	}
	if err := r.binder.Frontend(spec); err != nil {
		return err
	}
	r.registered[spec.Name] = true
	return nil
}

type Gap struct {
	Module    module.ID
	Mechanism capability.Mechanism
	Result    capability.Result
}

type Runtime struct {
	History  *history.Service
	Commands []command.Spec
	Gaps     []Gap
	started  []entry
}

func (r *Runtime) Close(ctx context.Context) error {
	var failures []error
	for _, node := range slices.Backward(r.started) {
		if node.stop != nil {
			if err := node.stop(ctx); err != nil {
				failures = append(failures, fmt.Errorf("stop %s: %w", node.id, err))
			}
		}
	}
	r.started = nil
	return errors.Join(failures...)
}

// Activate validates the entire list before construction or resource startup.
func (c *Composition) Activate(ctx context.Context, binder binding.Binder) (*Runtime, error) {
	if c.sealed {
		return nil, errors.New("composition is already activated")
	}
	c.sealed = true
	runtime := &Runtime{Commands: slices.Clone(c.LegacyCommands)}
	records := slices.Clone(c.LegacyRecordKinds)
	positions := make(map[module.ID]int)
	var failures []error
	for i, node := range c.entries {
		if node.id == "" {
			failures = append(failures, errors.New("empty module identity"))
		}
		if _, exists := positions[node.id]; exists {
			failures = append(failures, fmt.Errorf("duplicate module %s", node.id))
		}
		positions[node.id] = i
		runtime.Commands = append(runtime.Commands, node.commands...)
		records = append(records, node.records...)
	}
	for i, node := range c.entries {
		if !node.validFactory {
			failures = append(failures, fmt.Errorf("%s: missing typed constructor or dependencies", node.id))
		}
		for _, required := range node.requires {
			position, exists := positions[required]
			if !exists {
				failures = append(failures, fmt.Errorf("%s requires missing module %s", node.id, required))
			} else if position >= i {
				failures = append(failures, fmt.Errorf("%s requires %s before it", node.id, required))
			}
		}
		if node.lifetime != module.Stateless && node.lifetime != module.Invocation {
			failures = append(failures, fmt.Errorf("%s: unsupported lifetime %q", node.id, node.lifetime))
		}
		if (node.start == nil) != (node.stop == nil) || len(node.resources) != 0 && node.start == nil || node.lifetime == module.Stateless && (node.start != nil || len(node.resources) != 0) {
			failures = append(failures, fmt.Errorf("%s: lifecycle does not match declared resources", node.id))
		}
		for _, requirement := range node.capabilities {
			result := binder.Capability(requirement.Mechanism)
			if result.State == capability.Available {
				continue
			}
			if requirement.Required {
				failures = append(failures, fmt.Errorf("%s: required %s unavailable: %s", node.id, requirement.Mechanism, result.Reason))
			} else {
				runtime.Gaps = append(runtime.Gaps, Gap{node.id, requirement.Mechanism, result})
			}
		}
	}
	names := make(map[string]bool)
	for _, spec := range runtime.Commands {
		if err := spec.Validate(); err != nil {
			failures = append(failures, err)
		}
		if names[spec.Name] {
			failures = append(failures, fmt.Errorf("duplicate frontend %q", spec.Name))
		}
		names[spec.Name] = true
	}
	if err := errors.Join(failures...); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, spec := range c.LegacyCommands {
		if err := binder.Frontend(spec); err != nil {
			return nil, err
		}
	}
	for _, node := range c.entries {
		if err := node.construct(module.Slots{Commands: slices.Clone(runtime.Commands), RecordKinds: slices.Clone(records)}, binder); err != nil {
			return nil, fmt.Errorf("construct %s: %w", node.id, err)
		}
	}
	for _, node := range c.entries {
		if err := ctx.Err(); err != nil {
			return nil, errors.Join(err, runtime.Close(context.WithoutCancel(ctx)))
		}
		if node.start == nil {
			continue
		}
		runtime.started = append(runtime.started, node)
		if err := node.start(ctx); err != nil {
			return nil, errors.Join(fmt.Errorf("start %s: %w", node.id, err), runtime.Close(context.WithoutCancel(ctx)))
		}
	}
	return runtime, nil
}
