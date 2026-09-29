# Responses transport

## REQ-TRANSPORT-001 — Responses transport

### Codex WebSocket transport

The router accepts Responses WebSocket upgrades at `GET /v1/responses`. The
wrapper advertises `supports_websockets=true` in its invocation-only provider
override without modifying Codex configuration. HTTP `POST /v1/responses` remains
available for streaming SSE and nonstream terminal JSON. Models discovery and
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
remains native. Prewarm does not initialize replay, journal, or agent lifecycle state. Generating requests cannot use prewarm
metadata to bypass ordinary turn validation.

The provider upgrade's nonempty `x-codex-turn-state` header reaches Codex as a
`response.metadata` event before the first response event, because the downstream
WebSocket is already upgraded. Only that routing header is bridged. Codex owns its
subsequent replay and reset; the router does not inject stale handshake state into
later requests or fabricate provider usage for the metadata event.

Execution-free turns pass through without Mekugi instruction or tool rewriting, regardless of their output schema. They require valid turn metadata and session
and thread IDs. Catalogs may be empty or contain native helper tools and Codex's JavaScript
Code Mode `exec` with optional `wait`, flat or namespaced. Nested clock and lookup declarations
are allowed. Generic preamble examples mentioning `tools.exec_command` are not declarations.
Admission depends on advertised tool declarations, not client preamble wording or request purpose.
Malformed catalogs, duplicate tools, wrong-kind execution wrappers, and partial editing or
process-execution catalogs do not qualify. Requests advertising native or nested editing or
process-execution tools keep their stock execution catalog after validation.

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

### Provider WebSocket transport for HTTP clients

HTTP requests to ChatGPT use pooled persistent WebSockets by default in both
`mekugi` and `passthrough` modes. The following pool and fallback rules apply to
that HTTP-to-WebSocket path, not the dedicated Codex WebSocket session.

Each `response.create` carries the complete transformed request input. HTTP's
`stream` field is omitted; incremental `previous_response_id` requests are
rejected rather than silently dropping their history dependency. Existing
`client_metadata` is preserved. Codex's per-request metadata channels carry
turn metadata and sticky turn state, while authentication, account, session,
thread, window, subagent, and capability headers partition connection reuse.
The connection owns no replay or conversation history. A new logical request
never inherits another request's turn metadata or response headers.

Known ancillary events `codex.response.metadata`, `codex.rate_limits`, and
`responsesapi.websocket_timing` remain forwarded and captured but are neutral
to terminal-state validation. An explicit provider `error` event ends the exchange
as failed, not as a missing or invalid terminal. Unknown non-Responses event kinds remain invalid.
Responses terminal event types own completion even if the embedded response
omits status; nonstream JSON supplies the missing status from that event type.

A connection serves at most one active response. The pool admits at most 32
connections including pending handshakes, evicts idle entries under pressure,
and waits cancellably when every entry is busy. Idle connections close after
one minute. Connections aged 50 minutes retire before reuse or after their
active response completes. Shutdown closes active, idle, and dialing entries.
Cancellation, early body close, malformed or oversized messages, and a stream
ending before a valid terminal event discard the connection. Individual JSON
messages and reconstructed nonstream output have a 64 MiB router buffer budget.
`--timeout` covers pool wait, handshake, send, and the first non-ancillary
response or error message. An ancillary-only stream does not reset that deadline.
The router buffers at most 64 KiB of ancillary startup messages while deciding
the HTTP status. Successful streaming responses preserve their original order;
an error returns only its structured JSON body and provider status/headers,
while the ancillary prefix remains part of provider capture.
`--stream-idle-timeout` limits gaps between complete WebSocket messages, while
HTTP response streams retain their byte-inactivity timeout.

Message queues, pending-delivery reservations, cancellation, and capture belong
to individual leases, not reusable connections. Receiver admission and lease
handoff are synchronized. A terminal read ends that lease's active receive
phase before reuse; a late callback or reserved delivery cannot target the next
lease. Early close/cancellation waits for already-read messages to be observed
before finalizing capture, including queued and blocked deliveries.

Only an explicit unsupported upgrade response (HTTP 404, 405, or 501), before
any `response.create` write, permits HTTP fallback. Authentication, rate-limit,
and other upgrade errors remain visible. Once a write begins, write failures,
provider error events, and dropped streams never cause transparent replay or
HTTP fallback. Handshake-only headers are not copied to downstream responses;
provider response headers from that handshake belong only to its first exchange.
Valid error-event status and headers remain visible to the HTTP client.

Acceptance:

1. Sequential full-input requests reuse a connection while turn metadata changes
   independently; credential or session changes never share that connection.
2. Concurrent requests cannot interleave responses on one socket. Pool capacity,
   retirement, cancellation, and shutdown release their owned resources.
3. Terminal events finish responses without waiting for socket EOF. Nonstream
   output reconstructs finalized items in index order when the terminal array
   is empty or absent, and applies tool translation once.
4. SSE translation, stock tool calls, provider usage, and capture
   remain integrated; neither nonstream delivery nor Grok needs WebSocket support
   in Codex.
5. Unsupported-upgrade fallback is pre-send only. Failed sends, partial streams,
   and provider error events are not replayed, and poisoned sockets are not reused.
