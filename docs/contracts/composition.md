# Workspace composition

## CTR-WORKSPACE-001

`go.work` is the workspace membership authority. Build, test, lint, generation,
snapshot and release recipes enumerate its members explicitly. Members resolve
through that workspace and ship as one product; independent releases and
standalone `GOWORK=off` builds are unsupported.

Every Go package belongs to an explicit application, backend, contract, library,
module, provider or test-support member. The repository root owns workspace
coordination and has no Go module or shared implementation package.

The application builds both executables and composes the selected feature
descriptors, with one implementation and retained store for each responsibility. Executable
commands come from descriptor contributions; the application authenticates,
pins and dispatches that catalog under the [frontend contract](../../apps/mekugi/internal/frontends/SPEC.md).

## Construction and lifetime

Descriptors declare typed dependencies, executable commands, record kinds,
capability requirements and resource lifetimes. Composition validates the whole
list before construction: unique identities and command names, dependency
presence and order, lifecycle consistency, and required capabilities. Optional
unavailable mechanisms remain explicit gaps.

Codex bindings declare their supported connection families. The backend registers
those declarations at activation; capability requirements remain separate from
which connections a module declares. The executable-frontend connection exposes
the descriptor's complete command contributions, without a second command list in
the binding. A missing connection or a connection without commands fails activation.

Typed dependency closures resolve earlier handles. Constructors assemble state
without acquiring resources. Startup acquires invocation resources after
construction and binding succeed; shutdown and failed startup drain in reverse
order, including the partially started owner.

The application frontend host receives all collected command contributions,
including later entries, and accepts an empty catalog. Reader contributions declare read observation so
the host can retain AX evidence without a command-name switch.

Activity hosts detail providers with unique nonempty names and callable resolvers.
It tries them in composition order and uses the first matching result. Resolution
uses already-published memory only. Removing a contribution does not remove retained
records. [Activity](../../modules/activity/SPEC.md#edit-evidence) owns detail selection
and absence behavior.

Session presentation hosts descriptor-owned status contributions in composition
order. Each declares a unique nonempty identity, an existing placement and a pure
projection of admitted thread facts. Composer captions and agent metrics consume
detached identity/text values; terminal layout, styling and separators remain with
the renderer. Empty contributions or unavailable facts add no item. Publication
does not read storage, query a provider or establish a second accounting cache.

Descriptors select terminal views through typed definitions. Auxiliary-pane and
agent-roster placements each accept zero or one contribution, with a nonempty
name, label and constructor. Roster constructors supply terminal views; the
application delivers detached Activity publications to them. A typed attachment bridge publishes the owning module's admitted
in-memory snapshot before rendering; the pane receives values and retains only
terminal interaction state. Construction and publication callbacks belong to
application wiring, never to published view values. Removing the contribution
removes its pane and input targets while retaining independent source consumers.

Session history receives record-kind recognition, dependency enumeration,
retention budgets and semantic retirement policy. Record meaning stays with its
feature owner. Composition supplies the constructed history service to live
sessions, frontend workers and offline readers; backend-neutral descriptors need
no Codex binding.

The application owns memory stores for each attachment, thread and turn. Modules
use private typed keys, initialize entries on first use and cannot read another
module's entries. The attachment owner serializes access and retires stores at
their scope boundaries; late observations cannot recreate a closed turn's state.
Stores and keys are not copied after use. Attached-session modules acquire
no invocation resources and have no Start/Stop lifecycle of their own.

## Import boundaries

Contracts depend on no local implementation. Libraries have explicit allowed
library dependencies. Backends and providers remain separate. Production code
cannot import test harnesses.

A feature's domain cannot import terminal views or backend APIs; only its
`codexbinding` imports the public Codex binding package. Its descriptor declares
`Module`, literal `Requires` identities and typed public APIs in `Deps`. The
import check compares these declarations against all production source,
including platform-tagged files.

Application session control, session presentation and headless code cannot
import terminal libraries, feature terminal views or the terminal adapter.
Session presentation reaches the backend through session control; headless is
independent of session presentation. Frontend hosting cannot depend on launch
or composition.

## Generated reference

The [feature module index](../reference/modules.md) derives identity, dependencies,
lifetime, records, source and terminal contribution slots, and Codex connections from the declarations.
Workspace membership comes from `go.work`; selection comes from the application's
composition list. An unselected module appears without being activated.

Collection uses ordinary Go package initialization but never invokes module
constructors, lifecycle functions, contribution callbacks or binding factories.
The reference describes declarations, not installed-host capability availability
or successful runtime activation. `make generate` updates it; `make check-generated`
detects drift without modifying files.

## Acceptance

`testsupport/moduletest` constructs descriptors with real supplied dependencies,
temporary storage and a scripted binder. Executable registration has shared
scripted and production-binder conformance cases. Import fixtures exercise both
allowed and forbidden dependencies through the workspace checker. Host
execution, permissions, publication and cancellation require their actual
application and installed-Codex acceptance; registration checks alone do not
establish those guarantees.
