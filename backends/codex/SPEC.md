# Codex backend

## Requirements

- Keep Codex as root execution authority and preserve stock JavaScript execution (`exec`),
  `exec_command`, `apply_patch`, permissions, sandboxing, processes, continuations, and
  batch/parallel execution.
- Support bounded/batched reads, captured-change review, live diffs, and retained capture of shell
  writes without replacing stock tools with custom edit languages (e.g. hashline) or transports.

## Current approach (replaceable)

### Execution authority and process transport

- `backends/codex/appserver` launches `mekugi-app-server`, owns newline-delimited JSON-RPC stdio
  framing (bounded backpressure queue, no fixed frame-size ceiling, bounded deduplicated stderr
  tail), and joins a bounded 3s shutdown/interrupt sequence on close.
- `backends/codex/binding` decodes native notifications, items, transcripts, history, static
  transparent JS tool invocations, execution outcomes, and native-trace receipts into neutral
  values without executing effects or substituting printed output for host provenance.
- `backends/codex/responses` owns Responses envelopes, connection-local history, and stream framing
  under [Responses transport](../../docs/contracts/responses-transport.md).
- `backends/codex/messagehistory` shares Codex's `history.jsonl` with stock locking/retention.
- Native runtime enforces JavaScript-only mode (`code_mode_only`), owns V2 collaboration/waits
  ([coordination contract](../../docs/contracts/coordination.md)), and owns demand-read
  launch-local `featureUse/read` counters.

### Native shell hooks and approvals

- `backends/codex/shellhook` owns the `PreToolUse` command-hook schema and `config/read` discovery
  used for optional command-segment tracking and native boundary observation.
- `backends/codex/binding` decodes native v2 `shellGuard/requestApproval` and
  `shellGuard/requestSecret` server requests emitted by `mekugi-app-server`'s built-in shell guard
  (VCS remote writes, `sudo`, and `gpg`/`gpg2`), which runs inside `codex-core` after user
  `PreToolUse` hooks.

### Output deduplication

- Native history-to-model-input projection replaces repeated text tool outputs, supplied source
  bodies, and exact line runs (≥192 bytes, saving ≥128 bytes within a 256 KiB window, or
  single-base line-range diffs against the 4 newest bodies when strictly smaller or replacing
  fragmented multi-segment references within a bounded clarity margin) with forward references
  (`[call cN]`, `[identical to call cN#L...]`).
- User text, assistant prose, images, stored history, UI, and evidence consumers retain originals.
  Appending input preserves projected prefixes; reset/removal restores literals. Net saved
  bytes/estimated tokens report via native FeatureUse.

### Retained rollout evidence

- `backends/codex/rollout` reads plain and Zstandard-compressed rollouts with regular-file/identity
  validation (no named-pipe blocking, 64 KiB metadata bound, 8 MiB tail for context/resume
  settings, 64 MiB limit for offline inspection/replay, 100k-entry thread discovery cap under
  `$CODEX_HOME/{sessions,archived_sessions}`). Reads never resume threads or replay effects.
