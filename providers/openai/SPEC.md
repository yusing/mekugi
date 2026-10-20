# OpenAI provider transport

## Purpose and scope

Own OpenAI credential validation, opaque model-catalog exchange, HTTP inference,
pooled Responses WebSockets, and dedicated upstream connection establishment.
Codex owns login and refresh. The caller supplies prepared request bytes and
headers; the [Codex adapter](../../backends/codex/responses/SPEC.md) owns its
header selection and request metadata. The provider owns no agent, transcript,
conversation history, model selection, or downstream renderer.

## HTTP exchange and lifetime

Credential validation requires exactly one valid bearer Authorization header
and one nonempty account header; the application validates before preparing
provider requests. Catalog responses retain upstream bytes, status,
headers, and query parameters. Catalog caching belongs to the application.

HTTP inference retries only the existing selected-model-at-capacity rejection,
at most five times, respecting bounded Retry-After or exponential backoff.
Other rejected responses retain their status and body. Start cancellation covers
connection setup and response startup; body consumption retains the response
lifetime and idle-read policy after startup completes.

Dedicated WebSocket connections belong to their caller, separately from the
full-input pool. Rejected upgrades retain the original status and response body.
Dedicated connections and auxiliary HTTP exchanges do not retry or follow
redirects. The backend selects permitted auxiliary operations and prepares their
headers. Neither a failed write nor disconnection authorizes replay.

Optional observations receive exact payloads at the receive boundary, before
consumer delivery. Closing an incomplete exchange settles already-received
observations before finalization. HTTP observation uses the supplied transport.
Missing observations do not prevent inference.

## Prediction configuration

The provider uses the invocation's latest authenticated request snapshot to fetch
composer prediction configuration and the desktop moderation policy. Credentials
remain in memory and account changes invalidate cached configuration. These bounded
auxiliary exchanges use the existing HTTP client, without redirects or retries;
they neither generate a suggestion nor provide an inference fallback. The remote
configuration owns eligibility, sampling, prompt and reasoning effort. The desktop
Statsig bootstrap supplies only the prediction moderation policy to this consumer;
unrelated assignments and telemetry are not adopted. Missing moderation policy
has the desktop's no-additional-suppression behavior.

## Pooled inference transport

HTTP requests to ChatGPT use pooled persistent WebSockets by default in both
`mekugi` and `passthrough` modes. The following pool and fallback rules apply to
that HTTP-to-WebSocket path, not the dedicated Codex WebSocket session.

The pool receives a prepared full-input request from the backend adapter.
Handshake headers partition reuse; the pool owns no replay or conversation
history. A new request never inherits prior response headers.

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
