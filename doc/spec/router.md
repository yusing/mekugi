# Session request and storage integration

[Invocation launch](../../apps/mekugi/internal/launch/SPEC.md) owns command
selection, private startup, host handoff, arguments, signals and shutdown.
The [native UI contract](native_ui.md) owns interactive presentation.

OpenAI auxiliary requests for standalone search, native image generation/editing,
and history-note operations use authenticated endpoint forwarding. The router
preserves their request body, response status, headers, and output without tool
projection or automatic retries. These endpoints are unavailable in third-party-only
launches. Auxiliary bodies retain their native streaming and encoding. Compressed
HTTP Responses requests are decoded before projection, with
the existing request budget applied to both encoded and decoded input.

The local base URL does not claim Codex backend-route eligibility. Backend-specific
workspace routing and experimental context activation remain unavailable until
their routing can preserve interception by the session router.

ChatGPT requests explicitly select `access_programs.cyber="standard"`, including
prewarming and continuations over either transport. This overrides client Daybreak
choices without changing the model or other access programs. Grok and OpenCode
requests are unchanged. This request-body selection cannot prevent an upstream
eligibility-service failure during a WebSocket upgrade before the body is sent.

The local metrics API and `/session` dialog expose invocation-owned metrics,
which expire on shutdown.

Detailed operational logging requires `--debug`. The composed
[diagnostics module](../../modules/diagnostics/SPEC.md) retains private failure
references independently of debugging. Startup and cleanup failures go to stderr
outside the active UI. Critical request failures use the
[notice contract](../../modules/notifications/SPEC.md); the launcher prints undelivered notices and
repetition summaries after Codex exits, stripping terminal control sequences while
preserving readable text. Private diagnostic records retain complete details.
Metrics and sanitized capture are
separate from operational logging.
`mekugi` mode also retains private durable replay state so resumed and forked conversations restore
their original model-visible tools. This is correctness state, not an operational session log.
It lives at `$XDG_STATE_HOME/mekugi/replay`, or `~/.local/state/mekugi/replay` when that variable is
unset, and survives wrapper shutdown. A relative `XDG_STATE_HOME` is invalid. Passthrough mode
opens this store only when retaining a failure; it does not retain tool replay state.
Mekugi-mode initialization failure prevents Codex launch. Publication, budgets,
namespace continuity, leases, and retention follow the
[session-history specification](../../modules/sessionhistory/SPEC.md).
The router attempts an age sweep on startup and hourly in the background, outside
request preparation. Insufficient space queues a coalesced background pressure
request without exposing unfinished evidence. Response translation keeps completed facts in memory and waits up to 30 seconds for
background maintenance, then retries persistence only. It reports a capacity diagnostic if
maintenance cannot free enough space, stops making progress, or does not complete within that
wait. Cancellation interrupts the wait. Retrying persistence must not rerun the host operation
or upstream request. Background work notices pressure promptly without waiting for the next age sweep.
An age-sweep failure does not fail an
unrelated request and is reported as a router-wide notice with the failed cleanup
phase and underlying error.
Generated debug bundles follow [diagnostics retention](../../modules/diagnostics/SPEC.md#private-debug-bundles). Roster usage retains
bounded pending per-thread deltas and coalesces bursts before publication;
shutdown drains its writer.

Cross-process leases protect active turns, host handoffs, and workers, including yielded calls.
Completed turn delivery without a tool handoff and router shutdown release owned leases.
Release is independent of answer text, child presentation, and optional UI tracking.
Cleanup never stops a host
process or changes its continuation lifetime.
Cleanup reports removals through user-only notices, or worker stderr; failed
cleanup reports an error and does not claim complete reclamation. Router shutdown
cancels and joins maintenance, including a lazily opened failure-only store.
Startup and UI session handling do not wait for a full sweep.

Change-index retirement preserves stream high-water counters so old IDs are never reused.
Partially retired change histories explicitly identify removed attempts. Missing recovery references
explain session expiry or storage pressure and never replay an operation.

`mekugi inspect-storage` validates one explicitly selected retained call or snapshot
object without opening a writable store, starting a router, acquiring storage locks,
changing permissions, or executing inputs. Its JSON result uses schema
`mekugi.storage.v1` and reports the selected target, decoded byte count, content
hash and dependency count. Private text requires an explicit field selection;
text windows are UTF-8 aligned, bounded to 64 KiB, and carry a continuation offset.
Missing objects, invalid references, corrupt compressed content, hash mismatches
and invalid offsets report target-qualified errors on stderr with nonzero status.
No scan of other sessions substitutes evidence for an unavailable selection.
`--capture-output PATH` appends sanitized capture records. Main completion and
wrapper exit do not create token-usage Markdown files or announce metrics paths.

### Debug evidence

`--debug` selects the [diagnostics bundle](../../modules/diagnostics/SPEC.md)
under `$XDG_STATE_HOME/mekugi/debug`, defaulting to
`~/.local/state/mekugi/debug`. The wrapper passes its selected AX read-log path
to the executor and authenticated worker manifest, preserving it across child
environment changes. Explicit capture and `MEKUGI_AX_OUTPUT` paths take precedence.

After the child and router stop and the terminal is restored, the wrapper prints
one `To diagnose this session, ask an agent to run:` heading and one shell-quoted
`mekugi inspect-session --debug-dir DIR --field diagnostic` command to stderr.
Startup failures after bundle initialization also report that command.

The request adapter supplies final projected instruction/tool content after
rewriting and separately identifies the prepared outgoing subset. Cached-prefix
counts describe actual reuse; automatic successors have no prepared wire request,
and Grok observations precede Chat Completions conversion. The adapter supplies
observed thread identities and bounded rollout discovery at shutdown; diagnostics
owns collection, encoding, export and read-only inspection.

Private failure details and their delivery follow the [notice contract](../../modules/notifications/SPEC.md).
Model-catalog errors are catalog refresh failures, not failed inference turns.

They do not queue terminal notices. Diagnostics retain the complete error, transport class or
upstream HTTP status, reference, and upstream/downstream status. The catalog HTTP error
response remains available to Codex, and failures are not cached.

Within a running router session, `/v1/models` retains the latest complete successful HTTP 200
catalog in memory only, with no disk storage or expiry timer. Matching authenticated requests
reuse its exact body and forwarded headers without contacting upstream; simultaneous matching
requests share the successful fetch. Reuse requires matching credentials, account, session header,
and query (including client version). Missing session headers use the router lifetime as the scope.
Invalid authentication never reads the cache. A successful different key replaces the single
entry, bounding retained body size to 8 MiB. Restarting the router clears it, including on resume;
forks and model switches can reuse it only when their request key matches.
Existing transport body budgets remain: provider HTTP errors and WebSocket upgrade
rejections can be limited to 8 KiB, and catalog responses to 8 MiB. A body-limit
failure reports the limit instead of claiming the omitted body is complete.
Streaming tool-call projection and replay-persistence failures report their safe operation or
conflict class instead of collapsing into a generic translation error. Tool input, provider
field values, and raw storage errors remain absent from sanitized metrics and capture;
failure error strings are displayed and retained without redaction.
After a delivered `response.created`, a deterministic response-translation fault emits
`response.failed` with error code `invalid_prompt`, unless a terminal has already been delivered
or the downstream cannot be written. Its message carries the safe cause code and diagnostic
reference and advises switching model, using passthrough, or relaunching with `--debug` and
reporting the reference; retrying the unchanged request is not advised. Writing the
protocol failure does not acknowledge user-visible notice delivery. Repeated references in the same identified thread/turn are neither
re-injected nor counted again. Pre-stream retryable provider failures retain their retry behavior.

Stream-end diagnostics identify the actual upstream transport separately from a synthetic
HTTP status used by a WebSocket bridge. They retain bounded provider request/response IDs,
event and decoded/adapted body-byte counts, last-byte/end timestamps, and whether an event
was left without its final separator. Body-byte counts are not wire-byte measurements.
For HTTP, framing distinguishes a completed length/chunked/HTTP-stream body from a
connection-delimited end and from EOF after automatic decompression. WebSocket evidence
distinguishes an actual read error/close from a stopped reader channel; cancellation is
preserved rather than converted into a synthetic upstream EOF. A reader publishes its
actual terminal error into an available bounded slot before cancellation can discard it;
queued messages are not evicted and an abandoned consumer cannot block shutdown. The first observed
termination origin is not overwritten by outer readers. Rejected WebSocket controls
retain their request-error classification without diagnostic observation replacing
the rejection or interrupting error delivery. The sanitized stream-diagnostics
fields never contain close reasons, response text, credentials, or arbitrary headers;
the separate raw error string may contain them. These facts classify the observable
end; an unreported provider-internal cause is not inferred.

A recognized downstream HTTP or WebSocket disconnect is cancellation even when a
write or flush fails before the reader cancels the session context. HTTP and
WebSocket share transport-cause classification, preserving the original error
and any partial-write count. A disconnect does not queue a critical restart notice
or replay the interrupted request. Other write failures remain failures.
An EOF wrapped by downstream cancellation remains cancellation during body sniffing and
stream completion; unfinished tool input must not replace that cause with a translation failure.
Stream diagnostics retain a sanitized write-termination category and, when available,
a numeric write-side WebSocket close code, separately from read termination.

For [AX export](../../modules/diagnostics/SPEC.md#offline-diagnosis), the
application discovers candidate rollouts under `$CODEX_HOME/sessions` and
`archived_sessions`, or the default `~/.codex` location. Filename suffixes select
candidates; the backend validates their exact metadata identity. Invalid or
unreadable candidates make discovery incomplete, and session inspection validates
selected rollouts in full. No discovery result executes retained input.

`request_complete` records monotonic elapsed milliseconds and, when present, the
capturer's immutable `capture_id` and `request_sequence`. All request-scoped debug and
instruction records use that capture ID as their `request_id`; without capture they use
a local random ID. `tool_observation` maps this request identity to safe logical `call_id`
and tool name at local observation, not execution. AX and observed patch evidence
join through call identity; no correlation header is added to either transport boundary.
Cancellation evidence is independent of replay diagnostic references. Allowlisted causes
are `router_shutdown`, `response_start_timeout`, `upstream_idle_timeout`,
`downstream_context_canceled`, `downstream_disconnected`, `downstream_deadline_exceeded`, `cancellation_unknown`,
and `deadline_unknown`. The owned start-timeout cause travels with the forwarding
failure; a later expired start timer is not evidence of that cause. An observed idle
timeout is also reported when concurrent downstream cancellation wins. Downstream
context cancellation is not asserted to be an explicit user abort.


Streaming `request_complete` records also include `response_stream` metadata when
stream copying was attempted: the copy-stop category, last allowlisted event type
and timestamp when observed, an observed terminal event, classified reader termination
with its observed origin (upstream, downstream, context, or router processing), a
numeric WebSocket close code when available, and a safe provider request ID when
supplied. Pending tool calls retain only safe item/call identities, received delta counts and UTF-8 byte counts,
and whether input-done was observed. They do not retain arguments. Counts describe
received deltas, not completed-input size or model usage. At most 32 pending calls
are retained, with explicit truncation when identities or capacity prevent coverage.
Unknown event types and transport errors receive fixed categories; raw event names,
close reasons, error text, and headers are not exported. An EOF does not establish
successful completion. These observations are request-local and debug-only and do
not change translation, execution, cancellation, or retry behavior.

### Performance profiling

`make mekugi-pprof` builds a separate optimized diagnostic executable with symbols
and the `pprof` build tag, plus its sibling executor, without installing or replacing
the normal command. Existing launch arguments, worker dispatch, and hooks retain
their behavior. The default build starts no profiling listener.

Live router sessions and [offline replay](../../modules/sessionreplay/SPEC.md) share one profiling
lifetime. Each diagnostic invocation binds a separate OS-assigned IPv4 loopback
port and prints `mekugi-pprof: http://127.0.0.1:PORT/debug/pprof/` to stderr before
UI handoff. Workers and hooks do not start profiling listeners. Completion cancels
active captures and closes the listener; concurrent invocations have independent
URLs and cleanup.

The standard HTTP pprof routes expose CPU, heap, allocs, goroutine, block, mutex,
and execution-trace data. CPU and trace collection begin only on request. Block
sampling uses a 1 ms blocked-time rate; mutex sampling records one in ten contention
events. Profiling may perturb performance. Measurements describe the Mekugi process,
including router, UI, and replay work, not separate Codex, executor, or worker processes.

These routes have no additional authentication. Loopback callers can access command-line
arguments and other private process data, so captures must remain private. Profiling
is separate from sanitized metrics and debug evidence. The
[reader workflow](../../docs/contributing/development.md#profile-live-sessions-and-replay) owns build and capture examples.

### UI

The [UI contract](native_ui.md) owns interactive app-server presentation
and client behavior.

### Headless app-server frontend

`mekugi codex headless --yolo` runs one stdin prompt in a new thread for automation
and offline comparisons. It reuses invocation-local routing and exits after a
successful Main turn.
Codex owns compaction and continuation within that turn.

The frontend emits JSONL session evidence on stdout and diagnostics on stderr.
It rejects empty/oversized prompts, resume and unsupported CLI options before
launching the host. It fails on interactive host requests, failed or interrupted
Main turns, lost host connections, output failure or cancellation. Codex retains
execution authority. This frontend is not available in passthrough mode.

## Request acceptance

Invalid native editing/execution catalogs and forced incompatible tool choices
fail closed with actionable HTTP 400 errors rather than retryable upstream 502
errors. Detailed operational logging remains opt-in; private replay correctness
records survive shutdown and remain safe across simultaneous wrappers.

### Responses transport

The [transport contract](transport.md) owns Codex-facing WebSocket sessions and
provider WebSocket transport for HTTP clients.
