package composition

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/contracts/presentation"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/terminalui"

	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"

	"github.com/yusing/mekugi/modules/livepreview/preview"

	"github.com/yusing/mekugi/modules/activity/feed"
	activityview "github.com/yusing/mekugi/modules/activity/terminalview"

	"github.com/yusing/mekugi/modules/notifications/notice"

	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

	"github.com/yusing/mekugi/contracts/diagnosticsection"
	"github.com/yusing/mekugi/modules/diagnostics/diagnostic"

	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/activitydetail"
	"github.com/yusing/mekugi/contracts/capability"
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/guidance"
	"github.com/yusing/mekugi/contracts/inspection"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/contracts/recordkind"
	"github.com/yusing/mekugi/contracts/reference"
	"github.com/yusing/mekugi/contracts/replaytiming"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/agentmetrics/evidence"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/commandsegments/segments"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/sessioninspection/query"
	"github.com/yusing/mekugi/modules/sessionreplay/playback"
	"github.com/yusing/mekugi/modules/usage/accounting"
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
	auxiliary       []terminalui.PaneFactory[*scope.Store]
	roster          []terminalui.RosterDefinition[*activityview.View]
	presentation    []presentation.Status
	activityDetails []activitydetail.Provider
	replayTiming    []replaytiming.Reader
	diagnostics     []diagnosticsection.Contribution
	inspection      []inspection.Contribution
	id              module.ID
	requires        []module.ID
	lifetime        module.Lifetime
	resources       []string
	commands        []command.Contribution
	records         []recordkind.Spec
	references      []reference.Resolver
	guidance        []guidance.Section
	capabilities    []capability.Requirement
	validFactory    bool
	construct       func(module.Slots, binding.Binder, *Runtime) error
	start, stop     func(context.Context) error
}

type Composition struct {
	entries []entry
	sealed  bool
}

// Add declares a factory, without constructing or starting it. The typed
// dependency closure resolves prior handles after all slots have been collected.
func (c *Composition) Add[D, S any, B interface {
	module.NoBindings | binding.Bindings[S]
}, V interface {
	module.NoViews | terminalui.Definitions[S, *scope.Store] | terminalui.RosterDefinition[*activityview.View]
}](descriptor module.Descriptor[D, S, B, V], deps func() D) *Handle[S] {
	if c.sealed {
		panic("composition is already activated")
	}
	backend, _ := any(descriptor.Bindings).(binding.Bindings[S])
	handle := &Handle[S]{id: descriptor.ID}
	node := entry{
		presentation: slices.Clone(descriptor.Contributes.Presentation),
		id:           descriptor.ID, requires: slices.Clone(descriptor.Requires),
		lifetime: descriptor.Lifetime, resources: slices.Clone(descriptor.Resources),
		records:      slices.Clone(descriptor.Contributes.RecordKinds),
		references:   slices.Clone(descriptor.Contributes.References),
		guidance:     slices.Clone(descriptor.Contributes.Guidance),
		capabilities: slices.Clone(backend.Requires),
		validFactory: descriptor.New != nil && deps != nil,
	}
	if roster, ok := any(descriptor.Views).(terminalui.RosterDefinition[*activityview.View]); ok {
		node.roster = []terminalui.RosterDefinition[*activityview.View]{roster}
	}
	views, _ := any(descriptor.Views).(terminalui.Definitions[S, *scope.Store])
	for _, definition := range views.Auxiliary {
		factory := terminalui.PaneFactory[*scope.Store]{Name: definition.Name, Label: definition.Label}
		if definition.Attach != nil {
			factory.Attach = func(theme diffrender.Theme) terminalui.PaneAttachment[*scope.Store] {
				return definition.Attach(handle.Value(), theme)
			}
		}
		node.auxiliary = append(node.auxiliary, factory)
	}
	for _, definition := range descriptor.Contributes.ActivityDetails {
		provider := activitydetail.Provider{Name: definition.Name}
		if definition.Resolve != nil {
			provider.Resolve = func(store *scope.Store, request activitydetail.Request) activitydetail.Result {
				return definition.Resolve(handle.Value(), store, request)
			}
		}
		node.activityDetails = append(node.activityDetails, provider)
	}
	for _, definition := range descriptor.Contributes.Commands {
		contribution := command.Contribution{Spec: definition.Spec, ObserveRead: definition.ObserveRead}
		if definition.Run != nil {
			contribution.Run = func(ctx context.Context, request command.Request) (command.Output, error) {
				return definition.Run(handle.Value(), ctx, request)
			}
		}
		node.commands = append(node.commands, contribution)
	}
	for _, definition := range descriptor.Contributes.ReplayTiming {
		var read replaytiming.Reader
		if definition.Read != nil {
			read = func(ctx context.Context, path string, threads map[string]bool, observe func(replaytiming.Interval) error) error {
				return definition.Read(handle.Value(), ctx, path, threads, observe)
			}
		}
		node.replayTiming = append(node.replayTiming, read)
	}
	for _, definition := range descriptor.Contributes.Inspection {
		var contribution inspection.Contribution
		if definition.NewAX != nil {
			contribution.NewAX = func() inspection.AXCollector { return definition.NewAX(handle.Value()) }
		}
		if definition.ProviderUsage != nil {
			contribution.ProviderUsage = func(ctx context.Context, path string, filter inspection.UsageFilter) (map[string]inspection.Usage, error) {
				return definition.ProviderUsage(handle.Value(), ctx, path, filter)
			}
		}
		node.inspection = append(node.inspection, contribution)
	}
	for _, definition := range descriptor.Contributes.DiagnosticSections {
		contribution := diagnosticsection.Contribution{Transport: definition.Transport}
		if definition.PrepareReads != nil {
			contribution.PrepareReads = func(path string, outputs ...string) error {
				return definition.PrepareReads(handle.Value(), path, outputs...)
			}
		}
		if definition.ReadSnapshot != nil {
			contribution.ReadSnapshot = func(ctx context.Context, path string) (diagnosticsection.Reads, error) {
				return definition.ReadSnapshot(handle.Value(), ctx, path)
			}
		}
		node.diagnostics = append(node.diagnostics, contribution)
	}
	node.construct = func(slots module.Slots, binder binding.Binder, runtime *Runtime) error {
		if !descriptor.HostsActivityDetails {
			slots.ActivityDetails = nil
		}
		if !descriptor.HostsDiagnosticSections {
			slots.DiagnosticSections = nil
		}
		if !descriptor.HostsReplayTiming {
			slots.ReplayTiming = nil
		}
		if !descriptor.HostsInspection {
			slots.Inspection = nil
		}
		if !descriptor.HostsGuidance {
			slots.Guidance = nil
		}
		if !descriptor.HostsCommands {
			slots.Commands = nil
		}
		if !descriptor.HostsRecords {
			slots.RecordKinds = nil
		}
		if !descriptor.HostsReferences {
			slots.References = nil
		}
		service, err := descriptor.New(deps(), slots)
		if err != nil {
			return err
		}
		handle.value, handle.ready = service, true
		registrar := &moduleRegistrar{binder: binder, runtime: runtime, allowed: node.commands, registered: make(map[string]bool)}
		if err := backend.Register(registrar, service); err != nil {
			return err
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
	runtime    *Runtime
	binder     binding.Binder
	allowed    []command.Contribution
	registered map[string]bool
}

func (r *moduleRegistrar) Frontend(spec command.Spec) error {
	index := slices.IndexFunc(r.allowed, func(declared command.Contribution) bool { return declared.Spec == spec })
	if index < 0 {
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

func (r *moduleRegistrar) InputProjection(projection binding.InputProjection) error {
	if err := r.binder.InputProjection(projection); err != nil {
		return err
	}
	r.runtime.InputProjections = append(r.runtime.InputProjections, projection)
	return nil
}

func (r *moduleRegistrar) Guidance(guidance binding.Guidance) error {
	if err := r.binder.Guidance(guidance); err != nil {
		return err
	}
	r.runtime.Guidance = &guidance
	return nil
}

func (r *moduleRegistrar) SessionNaming(naming binding.SessionNaming) error {
	if err := r.binder.SessionNaming(naming); err != nil {
		return err
	}
	r.runtime.Naming = &naming
	return nil
}

func (r *moduleRegistrar) Observation(observation binding.Observation) error {
	if err := r.binder.Observation(observation); err != nil {
		return err
	}
	r.runtime.Observation = &observation
	return nil
}

type Gap struct {
	Module    module.ID
	Mechanism capability.Mechanism
	Result    capability.Result
}

func (r *moduleRegistrar) ShellApproval(guard binding.ShellApproval) error {
	if err := r.binder.ShellApproval(guard); err != nil {
		return err
	}
	r.runtime.Guards = append(r.runtime.Guards, guard)
	return nil
}

type Runtime struct {
	Terminal             terminalsession.Views
	Presentation         *sessionpresentation.Service
	Conversation         *conversationmodel.Service
	Replay               *playback.Service
	LivePreview          *preview.Service
	Activity             *feed.Service
	Notifications        *notice.Service
	Preferences          *preferences.Service
	Diagnostics          *diagnostic.Service
	Inspection           *query.Service
	TransportDiagnostics *transportcapture.Service
	AgentMetrics         *evidence.Service
	Usage                *accounting.Service
	Guards               []binding.ShellApproval
	Naming               *binding.SessionNaming
	Observation          *binding.Observation
	Guidance             *binding.Guidance
	InputProjections     []binding.InputProjection
	Changes              *changecapture.Service
	Segments             *segments.Service
	Recovery             *recovery.Service
	History              *history.Service
	Commands             []command.Contribution
	Gaps                 []Gap
	started              []entry
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
	runtime := new(Runtime)
	var auxiliary []terminalui.PaneFactory[*scope.Store]
	var roster []terminalui.RosterDefinition[*activityview.View]
	var status []presentation.Status
	var records []recordkind.Spec
	var activityDetails []activitydetail.Provider
	var inspections []inspection.Contribution
	var replayTiming []replaytiming.Reader
	var diagnostics []diagnosticsection.Contribution
	var references []reference.Resolver
	var guidance []guidance.Section
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
		auxiliary = append(auxiliary, node.auxiliary...)
		roster = append(roster, node.roster...)
		status = append(status, node.presentation...)
		activityDetails = append(activityDetails, node.activityDetails...)
		records = append(records, node.records...)
		references = append(references, node.references...)
		guidance = append(guidance, node.guidance...)
		inspections = append(inspections, node.inspection...)
		replayTiming = append(replayTiming, node.replayTiming...)
		diagnostics = append(diagnostics, node.diagnostics...)
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
		if node.lifetime != module.Stateless && node.lifetime != module.Invocation && node.lifetime != module.HostOperation && node.lifetime != module.AttachedSession {
			failures = append(failures, fmt.Errorf("%s: unsupported lifetime %q", node.id, node.lifetime))
		}
		if (node.start == nil) != (node.stop == nil) || len(node.resources) != 0 && node.start == nil || node.lifetime != module.Invocation && (node.start != nil || len(node.resources) != 0) {
			failures = append(failures, fmt.Errorf("%s: lifecycle does not match declared resources", node.id))
		}
		for _, contribution := range node.commands {
			if contribution.Run == nil {
				failures = append(failures, fmt.Errorf("%s: frontend %q has no compiled implementation", node.id, contribution.Name))
			}
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
	terminal, err := terminalsession.NewViews(auxiliary, roster)
	if err != nil {
		failures = append(failures, err)
	}
	runtime.Terminal = terminal
	presentations, err := sessionpresentation.New(status)
	if err != nil {
		failures = append(failures, err)
	}
	runtime.Presentation = presentations
	if err := errors.Join(failures...); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, node := range c.entries {
		if err := node.construct(module.Slots{ActivityDetails: slices.Clone(activityDetails), ReplayTiming: slices.Clone(replayTiming), DiagnosticSections: slices.Clone(diagnostics), Inspection: slices.Clone(inspections), Commands: slices.Clone(runtime.Commands), RecordKinds: slices.Clone(records), References: slices.Clone(references), Guidance: slices.Clone(guidance)}, binder, runtime); err != nil {
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

func (r *moduleRegistrar) Rollout(attach func(binding.RolloutSource) error) error {
	return r.binder.Rollout(attach)
}
