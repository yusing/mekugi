# Workspace foundation

## CTR-WORKSPACE-001 — Composition and extraction boundaries

`go.work` owns workspace membership. Build, test, lint, generation, snapshot,
and release recipes enumerate those members explicitly. Local modules resolve
through the workspace and ship as one product; member-only releases and
standalone builds with `GOWORK=off` are not supported.

The application builds both executables and selects the compiled frontend
catalog. The Codex binding collects the selected registrations, then the existing
router authenticates, pins, and dispatches them under the
[frontend contract](plugin.md). During extraction, the root module remains the
migration source. Existing features remain there until their own migration step;
there is one implementation and one retained store.

The initial descriptor surface supports typed dependencies, executable command
contributions, record-kind contributions, capability requirements, and stateless,
invocation, or host-operation lifetimes.
Composition validates the complete list before construction: unique identities
and command names, dependency presence and order, lifecycle consistency, and
required capabilities. Optional unavailable mechanisms remain explicit gaps.
Constructors assemble state without acquiring resources. Startup acquires declared
invocation resources after construction and binding succeed; shutdown and failed
startup drain in reverse order, including the partially started owner.

Typed dependency closures resolve earlier handles. The command host receives the
complete collected slot, including later contributors, and accepts an empty list.
The bounded-command, source-outline, and symbol-query descriptors contribute
compiled `mrun`, `inspect_file`, and `msymbol` implementations and descriptions. Reader contributions declare
read observation so the host retains AX evidence without command-specific dispatch. The authenticated worker dispatches the selected implementation;
adding another extracted command requires no new name switch. The application
supplies the remaining legacy catalog until those owners are extracted.
The session-history descriptor hosts the record-kind slot. It receives feature
recognition, dependency inspection, budgets, and semantic retirement functions;
feature schemas remain with their owners. Its constructed service is passed to
live sessions, standalone frontend workers, and offline readers. Backend-neutral
descriptors do not register a Codex binding.

There are no placeholder feature modules. Observation callbacks, scoped stores,
and presentation contracts are introduced with their real consumers
in the [target design](../design/modular-monorepo.md).

The import check enforces the extracted workspace boundaries. Libraries declare
only the allowed library relationships; contracts depend on no local implementation;
backend and provider packages remain separate; production imports exclude test
harnesses. Feature domain packages cannot import terminal views or backend APIs.
The feature's `codexbinding` alone imports the public Codex binding package.
Feature `module.go` declares `Module` as a descriptor literal, `Requires` as literal
module IDs, and its `Deps` type names the required public APIs. The check compares
these dependencies with production imports, including platform-tagged source.
The unextracted root remains an explicit transition boundary, not a precedent for
new library or backend dependencies.

`testsupport/moduletest` constructs descriptors with supplied real dependencies,
temporary storage, and a scripted binder. The initial mechanism conformance cases
cover executable registration with both scripted and production binders. Host
execution, permissions, output publication, and cancellation remain covered at
the retained router and installed-Codex consumers; registration tests do not
establish those runtime guarantees.
