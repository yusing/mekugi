# Authenticated frontend catalog

## CTR-PLUGIN-001 — One immutable executable catalog

Application composition selects the compiled command catalog and validates
duplicate names. The Codex binding collects executable registrations; the router
retains authenticated snapshots, pinned workers, and invocation-private PATH
exposure during extraction. Frontend workers verify
that identity before dispatch. Codex owns process execution and its permissions,
cancellation, and continuation.

Contributions use portable executable leaf names and resolve to a compiled
implementation before snapshot publication. The dispatch owner supplies that
availability check; the manifest remains authenticated and validated at use.

Each extracted command contribution pairs its description with its compiled
implementation and whether the host observes read completion. Observation failure
cannot prevent command execution. Stateful command implementations receive their
constructed module service through typed composition, alongside an explicit
authenticated per-call scope. Descriptors declare commands before construction;
execution begins only after successful activation. Executable identity recognition
selects worker dispatch before composition. The worker then receives the composed
catalog before authenticating the manifest and selected frontend. The catalog provides descriptions and native dispatch for `mcat`, `inspect_file`,
`msymbol`, `mread`, `mchanges`, and `mrun`. It does not execute declarations from
user configuration or own a JavaScript/WASM plugin runtime. Reader parsing,
output budgeting, and resolver orchestration remain with their native owners.
Frontend operands never select launcher commands; a filename such as `mcp-bridge`
remains an operand of the authenticated frontend.

The [guidance module](../../modules/agentguidance/SPEC.md) renders the selected
validated descriptions and command-owned supplements. The registry hashes the
result into its identity and publishes its invocation snapshot before exposing
commands. Guidance assembly is optional; its absence leaves the authenticated
command catalog and dispatch intact.

Commands return omitted payloads or a pure delivery plan over already captured
output or a frozen retained-evidence selection. For coordinated results, the host reserves reference identities before
the command finalizes exact framing and admission. The plan receives identities,
never a store or execution callback. A command that has already changed the
workspace may supply a pure recovery-failure result preserving its full report
and original mutation status. Cancellation still suppresses delivery.
Managed output is published before a recovery reference is returned. Storage
failure cannot claim recovery is available. Replay consumes retained results
without executing workers. Cleanup owns only invocation-created frontends,
snapshot files, and leased worker resources. Command execution scope distinguishes
direct callers, the host process group, and a dedicated authenticated worker.
Only the dedicated worker permits process-wide descendant cleanup; resolver
process policy stays with the symbol-query owner.

Host effects and change evidence belong to
[CTR-EXECUTION-001](execution.md); measurements belong to
[transport diagnostics](../../modules/transportdiagnostics/SPEC.md).
