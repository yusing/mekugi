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
cannot prevent command execution. The worker receives the composed catalog before authenticating
the selected frontend. The catalog provides descriptions and native dispatch for `mcat`, `inspect_file`,
`msymbol`, `mread`, `mchanges`, and `mrun`. It does not execute declarations from
user configuration or own a JavaScript/WASM plugin runtime. Reader parsing,
output budgeting, and resolver orchestration remain with their native owners.

Managed output is published before a recovery reference is returned. Storage
failure cannot claim recovery is available. Replay consumes retained results
without executing workers. Cleanup owns only invocation-created frontends,
snapshot files, and leased worker resources.

Host effects and change evidence belong to
[CTR-EXECUTION-001](execution.md); measurements belong to
[CTR-METRICS-001](metrics.md).
