# Responses transport

## REQ-TRANSPORT-001 — Responses transport

### Codex WebSocket transport

The router accepts Responses WebSocket upgrades at `GET /v1/responses`. The
OpenAI wrapper retains the built-in provider's WebSocket and prewarm support,
without modifying user configuration. Codex owns fallback to HTTP
`POST /v1/responses` for streaming SSE and nonstream terminal JSON,
with pooled provider WebSockets as described below. The WebSocket endpoint retains
its transport contract for compatible clients; native `turn/steer` remains Codex-owned,
not a router translation of HTTP input into `response.steer`. Models discovery and
Grok and OpenCode retain their HTTP provider transports.

The Codex-facing endpoint supports one unnamed response lane per connection.
A non-null `stream_id` is rejected rather than mixing independently translated
responses. Use separate connections for independent sessions.

A Codex WebSocket session owns its ChatGPT connection. It must preserve that
connection across terminal events for incremental `response.create` requests,
`generate=false` prewarming, and `response.steer`. Steering is sent while output
is still being read, on the connection that owns the target response. Accepted
steering is queued, not committed: the successor's `response.created` is the
commit point. The router forwards acceptance, pending, and failure events and
keeps reading after a steered `response.incomplete` or normal completion for an
automatic successor. A pending tool-result continuation uses the same
`previous_response_id` and does not resend accepted steering.

Codex's opt-in instant interruption uses `response.interrupt` on the owning
connection. The router forwards it unchanged while reading the active response,
including `discard_partial_items`, and preserves the acknowledgement and
item-interrupted events. An `interrupted` incomplete terminal drains that
response without declaring successful model completion. Codex supplies the next
`response.create`; the router neither fabricates a successor nor cancels tools.

Startup metadata with `request_kind="prewarm"` and explicit `generate=false`
is a non-generating transport handshake and does not require workspaces or a
supported tool catalog. When Codex supplies a supported execution catalog, prewarm
uses the same instruction, tool, and collaboration projection as a generating turn,
so the first turn can reuse that prefix. An execution-free or catalog-free handshake
remains native. Startup prewarm does not initialize replay or agent lifecycle state.
Unsupported router-local context references fail before provider forwarding;
prewarm does not restore retired local context. Generating requests cannot use
prewarm metadata to bypass ordinary turn validation.

The provider upgrade's nonempty `x-codex-turn-state` header reaches Codex as a
`response.metadata` event before the first response event, because the downstream
WebSocket is already upgraded. Only that routing header is bridged. Codex owns its
subsequent replay and reset; the router does not inject stale handshake state into
later requests or fabricate provider usage for the metadata event.

Execution-free turns pass through without Mekugi instruction or tool rewriting, regardless of their output schema. They require valid turn metadata and session
and thread IDs. Catalogs may be empty or contain native helper tools and Codex's JavaScript
`exec` with optional `wait`, flat or namespaced. Nested clock and lookup declarations
are allowed. Generic preamble examples mentioning `tools.exec_command` are not declarations.
Admission depends on advertised tool declarations, not client preamble wording or request purpose.
Malformed catalogs, duplicate tools, wrong-kind execution wrappers, and partial editing or
process-execution catalogs do not qualify. Requests advertising nested editing or
process-execution tools keep their stock execution catalog after validation. Top-level `apply_patch` or `exec_command`
catalogs fail as unsupported execution interfaces.

Request preparation and response restoration retain stock tool identity,
replay, and native execution behavior. Connection-local native history supplies ordinary
projection and durable replay. Separately, the transport fingerprints the complete
provider input plus raw completed output, reconciling streamed items with terminal
snapshots before any client-facing transformation. Only a successful, steered, or interrupted
provider terminal confirms that fingerprint. These fixed-size fingerprints are not
filesystem authority and are not restored as live connection state after restart.

After all projections, one reconciler compares the desired provider input with that
confirmed prefix. A matching prefix sends only the remaining items, including any
missing router-owned result followed by new user input. Changed or shortened prefixes,
or unavailable confirmation, cause explicit continuations to send full projected
history without `previous_response_id`. This includes instruction changes,
prewarm-to-turn and model-workflow transitions. No reconciliation reruns tools.
The [duplicate-output projection](execution.md#duplicate-output-projection)
is the final model-input projection. A fresh pass over growing original host
input preserves earlier projected items and therefore the confirmed prefix.
Changing the launch option or projection rules may change earlier bytes and
require a full-history rebase; it never changes native history or retained evidence.
Accepted steering is not resent against the same parent. An automatic successor
fails if its prepared history differs from what has already been admitted; it cannot
pretend an unsent rewrite or additional result took effect.
Debug logs record `provider_history_reconciliation` with a fixed reason, reused item
count, and reconciliation duration. Instruction dumps include `projected_input_bytes`
and `wire_input_bytes`. These measure serialized input, not provider cache hits or billed
tokens; provider-reported usage and existing request timing remain the evidence for
cache effectiveness and latency. HTTP, Grok and OpenCode remain stateless full-history paths.
Continuation guidance attached to a yielded tool result remains historical after a later
wait or `write_stdin` result; ordinary completion does not rewrite the provider-confirmed
prefix merely to retire that guidance. Later results determine whether a handle is still active.
An actual tool-catalog change may replace obsolete guidance and rebase history.
Automatic successors inherit the parent
request's translation context; explicit continuations use their own settings.
Neither a dropped connection nor a failed send silently replays requests or
steering. Shutdown and downstream disconnect release the owned connection.

Router-generated WebSocket error events include a numeric HTTP-style `status`
so Codex can recognize them: incompatible requests and malformed client messages
use 400, other execution failures use 502, and provider upgrade rejections retain
the provider status and error body.

Client messages and reconstructed requests have a 32 MiB buffer budget;
provider messages have a 64 MiB budget. A session conservatively charges retained
request settings, native input, and finalized output against a cumulative
64 MiB history budget. Exceeding a budget fails the session rather than dropping
history needed for translation.
Terminal output reconciles with completed streamed items by item ID, updating matching
metadata and retaining delivered order. A partial or commentary-only terminal snapshot
must not discard completed calls needed by a later full-history continuation.
Provider answers stream immediately. A successful terminal snapshot completes an
already-started final-answer item when its item-done event is missing, without
repeating completed items or adding router-authored answers. Answer delivery and
LiveDiff completion do not depend on provider usage metadata.

`--timeout` covers each response's preparation, connection setup, write, and
first non-control, non-ancillary event. Steering acknowledgements do not satisfy
that deadline. `--stream-idle-timeout` limits message gaps during an active
response, not quiet intervals after completion or while waiting for pending
tool results. A completed session's connection is not retired merely for being
idle, because it may still own queued steering. There is no transparent
reconnect or migration of that state to another socket.

Acceptance:

1. The launch override enables Codex WebSockets without persistent config edits.
2. A client can steer after `response.created` while the parent is still running
   and receive an automatic successor through the same connection.
3. Accepted steering waiting on tool output survives parent completion, and one
   incremental tool-result continuation does not duplicate the steering input.
4. Prewarming, incremental history, and tool restoration preserve
   their existing meaning across responses.
5. HTTP clients remain supported; a dropped active WebSocket fails without
   transparent replay, and lifecycle cancellation closes owned sockets.
6. Instant interruption drains on the same socket, and a matching continuation
   reuses the interrupted response ID without replaying prior input or tools.

### Provider transport

The [OpenAI provider contract](../../providers/openai/SPEC.md) owns pooled
WebSocket inference, HTTP fallback, retry, and upstream connection lifetime.
[Codex request shaping](../../backends/codex/responses/SPEC.md#openai-request-shaping)
owns headers, full-input framing, and per-request metadata. Dedicated Codex
WebSocket session semantics remain above.
