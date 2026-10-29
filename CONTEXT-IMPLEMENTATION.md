# Implementation

For feature work, locate the semantic owner in [the owner map](CONTEXT-MEKUGI.md) and read its
specification. Load the [boundary contracts](CONTEXT-MEKUGI.md#boundary-contracts) the change crosses.
The [composition contract](docs/contracts/composition.md) owns workspace and dependency rules.

- **Extend the owner, not the wiring.** Keep each feature's behavior, records, presentation and
  bindings together. Extend an existing responsibility before adding a module; a new module needs
  one coherent responsibility, `SPEC.md`, a declarative `module.go` and explicit typed dependencies.
  Application packages coordinate cross-owner workflows; launch constructs actual owners and joins
  their lifetimes. Neither becomes a successor router, aggregate feature object or service locator.
- **Keep dependencies directional.** Contracts import no local implementation; libraries import
  no application, feature, backend or provider. Feature domain/presentation imports only contracts,
  focused libraries and required modules' public APIs, never their roots, bindings, views or internals.
  Only the application imports feature descriptors; only a feature's `codexbinding` imports
  the public Codex binding API. Backends and providers stay independent. Terminal dependencies stay
  in renderer/view packages; test harnesses stay test-only. `Requires` matches actual module API use.
- **Compose once.** Descriptors own commands, records, bindings and views; derive catalogs and
  guidance from their contributions instead of adding parallel lists or command-name switches.
  Optional relationships use typed slots whose hosts accept zero contributions. With existing slots
  and binding mechanisms, a new module needs only workspace membership and one composition entry;
  a new shared mechanism is an explicit contract change, not a runtime plugin layer.
- **Pass actual owners.** Give consumers the selected store, authenticated snapshot or narrow typed
  dependency, not an application back-pointer, ambient accessor or directory from which to reconstruct
  it. External entrypoints still validate and attach through the owning API. Keep one authoritative
  implementation and store; adapters do not duplicate policy, ledgers, registries or transcripts.
- **Make state and lifetime explicit.** Put module thread/turn state in the matching scoped stores.
  Invocation startup acquires and closes owned resources; request/view consumers borrow them and
  release only their own work. Keep feature codecs with their feature, publication/retention with
  `sessionhistory`, and cross-owner publication with `recordtransactions`. Avoid package-global
  mutable state, `init` registration and competing lifecycle maps or shutdown paths.
  Thread-scoped behavior covers every thread kind: Main, delegates (running, idle, completed or
  resumed), Side and One-off forks, and rewound roots. Do not assume a running runtime, readable
  native history or root identity; features verified only on Main have broken on each of these.
- **Separate semantics from painting.** Module models own renderer-neutral state and semantic intents;
  session presentation publishes detached values; session control issues host requests. Renderers own
  geometry, keys, selection and painting, not feature policy, storage access or backend dispatch.
  The browser binding is a renderer too: when it needs a decision the terminal also makes, such as
  agent display names, disclosure eligibility, folding, outcome tone or wait labels, publish that
  fact through the Session API instead of re-deriving it in TypeScript, because parallel
  derivations drift with each new producer shape. Measured geometry, such as actual clipping,
  stays with the renderer. Reuse admitted snapshots instead of copying whole sessions for narrow
  internal queries.
- **Require a present consumer.** Do not add contracts or slots for speculative backends or
  renderers.

Keep behavioral specifications at their owners and update affected consumer checks. Workspace checks
enforce imports and declarations; generated references derive from those declarations. Select checks
through `CONTEXT-TESTS.md`; neither static checks nor generated indexes prove runtime behavior.

## Presentation

Reuse stock/shared UI and path formatting. Format workspace paths with
`libraries/pathdisplay.ForWorkspace`, preserving external and operational paths. If genuinely new
UI lacks a stock counterpart, settle its shape with the user.

A displayed fact usually reaches the screen through several paths: live, restored, paged or
replayed; Main or child view; linked or unlinked delivery; native or thread-control traffic;
pending, running or settled rows and their detail pages; terminal or browser. Before fixing a
display defect, find where the paths showing that fact converge and fix it there. Treat reported
examples as samples of their input class, such as every chain, pipeline and heredoc position for
shell display, and check each sibling path and shape the converged fix changes with one
representative, not every combination.
