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
descriptors. Its remaining integration lives in `apps/mekugi/internal/router`,
with one implementation and retained store for each responsibility. Executable
commands come from descriptor contributions; the application authenticates,
pins and dispatches that catalog under the [frontend contract](../../apps/mekugi/internal/frontends/SPEC.md).

## Construction and lifetime

Descriptors declare typed dependencies, executable commands, record kinds,
capability requirements and resource lifetimes. Composition validates the whole
list before construction: unique identities and command names, dependency
presence and order, lifecycle consistency, and required capabilities. Optional
unavailable mechanisms remain explicit gaps.

Typed dependency closures resolve earlier handles. Constructors assemble state
without acquiring resources. Startup acquires invocation resources after
construction and binding succeed; shutdown and failed startup drain in reverse
order, including the partially started owner.

The command host receives all collected contributions, including later entries,
and accepts an empty catalog. Reader contributions declare read observation so
the host can retain AX evidence without a command-name switch.

Session history receives record-kind recognition, dependency enumeration,
retention budgets and semantic retirement policy. Record meaning stays with its
feature owner. Composition supplies the constructed history service to live
sessions, frontend workers and offline readers; backend-neutral descriptors need
no Codex binding. Unextracted record kinds remain explicit application
contributions.

The application owns each attachment's memory store. Modules use private typed
keys, initialize entries on first use and cannot read another module's entries.
The attachment owner serializes access. Stores and keys are not copied after
use; replacing the store creates fresh state. Attached-session modules acquire
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
import terminal libraries, feature terminal views, the terminal adapter or the
remaining router. Session presentation reaches the backend through session
control; headless is independent of session presentation. Frontend hosting
cannot depend on launch, composition or the remaining router.

## Acceptance

`testsupport/moduletest` constructs descriptors with real supplied dependencies,
temporary storage and a scripted binder. Executable registration has shared
scripted and production-binder conformance cases. Import fixtures exercise both
allowed and forbidden dependencies through the workspace checker. Host
execution, permissions, publication and cancellation require their actual
application and installed-Codex acceptance; registration checks alone do not
establish those guarantees.
