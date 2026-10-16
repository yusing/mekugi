# Responses transport integration

## REQ-TRANSPORT-001

The application accepts Codex Responses WebSockets at `GET /v1/responses` and
HTTP streaming SSE or terminal JSON at `POST /v1/responses`. Invocation-local
launch settings retain stock WebSocket and prewarm support. Codex chooses HTTP
fallback and owns native steering, interruption, tools and continuations.
Models discovery, Grok and OpenCode retain HTTP provider transports.

The [backend adapter](../../backends/codex/responses/SPEC.md) owns envelopes,
native identities, connection-local history and response framing. The
[OpenAI provider](../../providers/openai/SPEC.md) owns credentials, upstream
connections, pooled inference and its pre-send-only fallback. Application
integration owns downstream admission, dedicated socket lifetime, deadlines,
accounting and delivery diagnostics.

## Catalog and auxiliary exchanges

`/v1/models` retains only the latest complete successful HTTP 200 catalog in
invocation memory, bounded to 8 MiB, without disk persistence or an expiry timer.
Authenticated requests with identical
credentials, account, session header and query, including client version, reuse
its exact body and forwarded headers; concurrent matches share the fetch.
Invalid authentication cannot read the cache. An absent session header scopes
reuse to the router lifetime. A successful different key replaces the entry.
Router restart, including resume, clears it; forks and model switches reuse only
a matching key. Failures are not cached: Codex receives the catalog error, and private
diagnostics retain its complete cause, transport class or upstream HTTP status,
reference and upstream/downstream statuses. Catalog refresh failures are not
inference failures and queue no terminal notice.

OpenAI standalone search, native image generation/editing and history-note
operations use authenticated forwarding without projection or automatic retry.
Their request bodies, response status, forwarded headers, output, streaming and
encoding are preserved. Third-party-only launches disable these endpoints.
The local base URL does not establish Codex backend-route eligibility;
backend-specific workspace routing and experimental context activation remain
unavailable until they can preserve session interception.

## Admission and projection

Compressed HTTP Responses bodies are decoded before projection, applying the
request budget to both encoded and decoded input. Auxiliary bodies stay encoded.

One connection carries one unnamed response lane. A non-null `stream_id` fails;
independent sessions require separate connections.

Metadata with `request_kind="prewarm"` and explicit `generate=false` admits a
non-generating handshake without workspace or execution-catalog requirements.
When a supported execution catalog is supplied, the same instruction, tool and
collaboration projections as a generating turn preserve prefix reuse. Otherwise
the handshake stays native. Prewarm initializes no retained replay or agent
lifecycle state and cannot bypass generating-turn validation. Unsupported local
context references fail before forwarding rather than restoring retired context.

Execution-free turns require valid turn metadata, session and thread identity,
but receive no Mekugi instruction or tool rewriting regardless of output schema.
An empty catalog, native helpers, JavaScript `exec` with optional `wait`, and
nested clock or lookup declarations can qualify. Flat and namespaced forms retain
their meaning. Admission follows advertised declarations, not request purpose or
preamble wording. Malformed or duplicate tools, wrong-kind wrappers and partial
execution catalogs fail admission.
Nested editing/process tools retain their validated stock catalog; top-level
`apply_patch` or `exec_command` are unsupported interfaces.
Invalid native catalogs and forced incompatible tool choices return actionable
HTTP 400 errors, not retryable upstream 502 errors.

ChatGPT requests select `access_programs.cyber="standard"` for prewarming and
continuations over either transport, overriding client Daybreak selection while
preserving the model and other access programs. Grok and OpenCode are unchanged.
This body selection cannot prevent an upstream eligibility-service failure
during WebSocket upgrade before the body is sent.

Projection never changes original tool arguments, host history or retained
evidence. [Output deduplication](../../modules/outputdeduplication/SPEC.md) runs
last on model input. Growing original input preserves earlier projected items;
changed options, instructions or tool catalogs can require the backend to rebase
an explicit continuation. HTTP, Grok and OpenCode send full history rather than
using a dedicated connection's confirmed prefix. Reconciliation diagnostics
measure serialized input and reuse, not provider cache hits or billed tokens.
Debug logs emit `provider_history_reconciliation` with a fixed reason, reused-item
count and duration. Instruction dumps expose `projected_input_bytes` and
`wire_input_bytes`; provider usage and request timing remain the evidence for
cache effectiveness and latency.

## Dedicated connection lifetime

Each Codex WebSocket session retains its owning ChatGPT connection across terminal
events, incremental requests and `generate=false` prewarming. Steering is sent
while the target response is still being read. Acceptance, pending and failure
events reach Codex, and reading continues after completion or a steered incomplete
terminal for an automatic successor. Backend history owns the distinction between
accepted input and successor commitment, including pending tool-result continuations.

`response.interrupt`, including `discard_partial_items`, is forwarded unchanged
on the owning socket. Its acknowledgement and item-interrupted events are
preserved. An interrupted terminal drains that response without claiming model
success; Codex supplies the next request. No request, tool or steering input is
silently replayed after failed sends or disconnection. Shutdown and downstream
disconnect release the owned connection.

Only a nonempty provider `x-codex-turn-state` upgrade header is bridged, as a
`response.metadata` event before response events. Codex owns its later replay
and reset. The application neither injects stale handshake state into later
requests nor invents usage for metadata.

Application-generated WebSocket errors carry numeric HTTP-style `status`: 400
for incompatible requests or malformed client messages, 502 for other execution
failures. Provider upgrade rejections retain their original status and body.

Client messages are bounded to 32 MiB and provider messages to 64 MiB. The
backend's request and cumulative history budgets fail overflow rather than
discarding facts needed for translation. Answers stream immediately. A successful
terminal snapshot completes an already-started final answer whose item-done
event is missing, without repeating completed items or creating an application
answer. Answer delivery and LiveDiff completion do not depend on provider usage.

`--timeout` covers preparation, connection setup, writing and the first
non-control, non-ancillary event; steering acknowledgements do not satisfy it.
`--stream-idle-timeout` bounds gaps during an active response, not quiet intervals
after completion or while waiting for tool results. Idle completed connections
remain available for queued steering. No transparent reconnect migrates their state.

## Failure delivery

Provider HTTP errors and WebSocket upgrade rejection bodies may be limited to
8 KiB; a body-limit failure reports the bound rather than claiming complete
evidence. Streaming projection and replay-persistence errors retain their safe
operation or conflict class instead of a generic translation error. Complete
private errors remain separate from sanitized metrics and capture.

After a delivered `response.created`, a deterministic translation fault emits
`response.failed` with `invalid_prompt` unless a terminal was already delivered
or downstream is unwritable. Its message includes a safe cause and diagnostic
reference, advising a model switch, passthrough, or a `--debug` relaunch and report,
not retrying unchanged input. Pre-stream retryable provider failures retain their
retry behavior. Protocol delivery does not acknowledge a user-visible
[notice](../../modules/notifications/SPEC.md).

A recognized downstream HTTP or WebSocket disconnect is cancellation, including
write/flush failure before context cancellation and EOF wrapped by cancellation.
Both transports preserve the original error and partial-write count. Unfinished
tool input cannot replace that cause with a translation fault. Disconnects queue
no critical restart notice and never replay the interrupted request; other write
failures remain failures. Stream observations follow
[transport diagnostics](../../modules/transportdiagnostics/SPEC.md#provider-and-stream-evidence).

## Acceptance

Actual transport consumers cover invocation-local launch, concurrent steering,
successor commitment, pending tool continuations, prewarming, incremental history,
terminal restoration, HTTP delivery, same-socket instant interruption, cancellation
and failed-send/disconnection behavior. Provider pool acceptance is separate from
dedicated-session acceptance; offline protocol checks do not establish installed
Codex behavior.
