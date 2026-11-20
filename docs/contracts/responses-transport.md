# Responses transport integration

## Requirements

- Codex remains root execution authority with one stock JavaScript execution interface (`exec` with
  nested stock tools). Top-level `apply_patch`/`exec_command` catalogs and direct execution mode are
  unsupported.
- Preserve stock WebSocket/HTTP fallback, prewarm, streaming, steering, interruption, batching, and
  existing features while improving [product outcomes](../product.md#user-requirements).

## Boundary contract

- **Endpoints & admission**: accepts Codex Responses WebSockets (`GET /v1/responses`), HTTP
  SSE/JSON (`POST /v1/responses`), catalog forwarding (`GET /v1/models`), and authenticated
  auxiliary routes (standalone search, native image edit/gen, history notes). Rejects multi-lane
  `stream_id`, unsupported/malformed tool catalogs, and unsupported local context references with
  actionable HTTP/WS 400 errors (never retryable 502s).
- **Prewarm & execution-free turns**: `request_kind="prewarm"` with `generate=false` admits
  non-generating handshakes without creating replay/agent state; execution-free turns pass through
  without tool/guidance rewriting.
- **Projection boundary**: projection never mutates original tool arguments, host history, or
  retained evidence. Native [output deduplication](../../backends/codex/SPEC.md#output-deduplication)
  runs before transport; growing input preserves projected prefixes.
- **Connection & failure semantics**: dedicated WebSocket connections forward concurrent steering
  and `response.interrupt` unchanged; no request or steer is silently replayed after send failure or
  disconnect. Downstream disconnects are recorded as cancellation, not translation faults. A
  deterministic translation fault after `response.created` emits `response.failed` (`invalid_prompt`)
  with a diagnostic reference; pre-stream provider errors stay retryable by Codex.

## Current approach (replaceable)

- `inferencegateway` owns admission, catalog/auxiliary forwarding, and WebSocket lifetimes;
  `backends/codex/responses` owns envelopes and connection-local history; `providers/openai` owns
  native OpenAI credentials/transport; `requestexecution` owns shared attempt execution, accounting,
  and diagnostics.
- `/v1/models` bounds responses to 8 MiB and preserves upstream ETags; WebSocket frames cap at 32
  MiB client / 64 MiB provider; provider error bodies cap at 8 KiB.
- ChatGPT requests force `access_programs.cyber="standard"` (not applied to custom providers).
- `--timeout` bounds first non-control event; `--stream-idle-timeout` bounds active-response gaps.
- `x-codex-turn-state` upgrade headers bridge once as `response.metadata`.
