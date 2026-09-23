# Authenticated plugin registry

## CTR-PLUGIN-001 — One immutable executor snapshot

The router owns plugin discovery, complete-registry validation, stable global
name ownership, and fail-before-serve behavior. A process-lifetime snapshot
contains built-in and configured declarations, a pinned worker executable,
and an authenticated manifest. Children verify that identity instead of
rediscovering live configuration.

The snapshot provides one session-private PATH directory. Codex starts each
frontend through its stock executor and retains process, sandbox, permission,
and continuation authority. Worker dispatch owns only authenticated argv,
stdin separation, bounded output, and optional managed-output retention.
`mread` and `mchanges` delegate to existing stores; `mrun` executes one
foreground child within Codex's frontend process. Reader implementations and
AX observation are not duplicated in the registry.

`mekugi:core/v1` owns deterministic portable helpers, not file access or
execution. Configured JavaScript hosts are isolated per call. The registry
never fabricates Codex carrier calls or replay mappings for stock execution.
Response observation and change evidence belong to
[CTR-EXECUTION-001](execution.md); capture metrics belong to
[CTR-METRICS-001](metrics.md). Cleanup owns only session-created frontends,
snapshot files, and leased worker resources.
