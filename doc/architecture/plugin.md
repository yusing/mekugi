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
The compiled built-in registry owns descriptions and native dispatch for `mcat`,
`inspect_file`, `msymbol`, `mread`, `mchanges`, and `mrun`. Their executors run within
Codex's frontend process, reuse the native reader/parser/tokenizer owners, and delegate
to existing stores or invocation-owned resolver/command processes. Shell argv is
validated by the native implementation; built-ins have no custom-tool grammar.
Configured plugin declarations cannot claim a native executor. Reader
implementations and AX observation are not duplicated in the registry.

The shared tokenizer owns bounded LF-row admission for native source, outline,
and semantic reads. It reuses counts for completed lexer pieces and recounts only
the piece at a row boundary (plus its following piece for a tail cut), rather than
tokenizing the accumulated output again for every row. Readers retain their
source normalization, numbering, byte bounds, framing, and omitted-output stores.

Native-only snapshots contain no JavaScript or WASM runtime assets and need no Node.js
lookup. Configured extensions add the authenticated JavaScript host and shared core.
`mekugi:core/v1` owns deterministic portable helpers, not file access or
execution. Configured JavaScript hosts are isolated per call. The registry
never fabricates Codex carrier calls or replay mappings for stock execution.
Response observation and change evidence belong to
[CTR-EXECUTION-001](execution.md); capture metrics belong to
[CTR-METRICS-001](metrics.md). Cleanup owns only session-created frontends,
snapshot files, and leased worker resources.
