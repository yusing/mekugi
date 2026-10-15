# Authenticated frontend catalog

## CTR-PLUGIN-001 — One immutable executable catalog

The router owns the compiled command catalog, duplicate-name validation, pinned
worker executable, and invocation-private PATH exposure. Frontend workers verify
that identity before dispatch. Codex owns process execution and its permissions,
cancellation, and continuation.

The catalog provides descriptions and native dispatch for `mcat`, `inspect_file`,
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
