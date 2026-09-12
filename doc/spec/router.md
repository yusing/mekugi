# Session-scoped Codex launch

## REQ-ROUTER-001 — Session-scoped Codex launch

`mekugi [flags] codex [Codex arguments...]` starts one private router on an
OS-assigned port bound to `127.0.0.1` and launches Codex from PATH only after
initialization and binding succeed. There is no standalone or daemon command,
fixed-listener flag, custom-provider flag, or installed old-name alias.

Invocation-only provider overrides select the listener, Responses transport,
and Codex-managed authentication against the fixed ChatGPT upstream. Provider
selection in config and profiles is overridden without modifying configuration.
Provider-selection arguments are rejected. Mekugi flags precede `codex`; subsequent
arguments remain intact, including subcommands and `--` delimiters.
The wrapper also enforces `include_collaboration_mode_instructions=false` in the
final command's invocation-only config layer, after user overrides and before `--`.
This disables Codex's collaboration-mode instruction injection without editing config files.

With `--grok`, the wrapper pins the selected model catalog through Codex's
`model_catalog_json` setting before launching the interactive or execution command.
The session catalog and its cleanup follow [REQ-SUBAGENTS-001](subagents.md).
Other invocations do not run the catalog command or pin model metadata.

Codex inherits cwd, stdin, stdout, stderr, and the environment, augmented only
with `MEKUGI_BASE_URL` and the private configured-plugin frontend directory at
the front of PATH. Terminal Ctrl-C remains Codex-owned. SIGTERM to the wrapper
terminates Codex and the router with bounded cleanup. Codex exit, launch failure,
and cancellation clean up owned runtime resources. Ordinary exit status is
preserved; signal exits use `128 + signal`. Unexpected router termination also
terminates Codex rather than leaving a dead provider connection.

After successful binding and before launching Codex, the wrapper prints exactly
one `mekugi dashboard: http://127.0.0.1:PORT/` line to stderr. It does not write
the announcement to stdout or repeat it during the active Codex UI. The URL and
in-memory metrics belong to this invocation and expire on shutdown.

Operational logging is absent unless `--debug` is enabled. Startup and cleanup failures are concise stderr
errors outside the active Codex UI. Critical request failures use the user-only
commentary contract. The launcher prints undelivered notices and repetition
summaries after Codex exits. In-memory metrics, explicit sanitized capture and
final metrics exports, and opt-in issue reports are not operational logging.
`mekugi` mode also retains private durable replay state so resumed and forked conversations restore
their original model-visible tools. This is correctness state, not an operational session log.
It lives at `$XDG_STATE_HOME/mekugi/replay`, or `~/.local/state/mekugi/replay` when that variable is
unset, and survives wrapper shutdown. A relative `XDG_STATE_HOME` is invalid. Passthrough mode
does not open this store. Initialization failure prevents Codex launch. The store admits at most
1 GiB of call replay data and 32 MiB per call record; reaching a limit rejects new records rather
than discarding resumable history. Exact commentary provenance has an independent 16 MiB budget;
failure to retain it suppresses new commentary instead of consuming call-record capacity.
Cleanup is explicit, never inferred from one thread's truncation.
`--capture-output PATH` appends records; `--metrics-output PATH` overwrites a final
snapshot from the same capturer. The destinations must be distinct.

`--debug` is a boolean flag requiring no argument. It creates a private, unique
`mekugi-debug-*` directory in the system temporary directory, with router diagnostics,
sanitized capture, final metrics, an instruction dump, runtime read journal, and AX report.
Debug implies AX instrumentation: the wrapper supplies the journal path to the executor
and the authenticated worker manifest retains it across child environment changes.
Explicit capture, metrics, and `MEKUGI_AX_OUTPUT` destinations retain precedence.
The wrapper prints all six absolute artifact paths to
stderr only on exit, after the child and router have stopped; it never prints debug paths
over the active Codex UI. Startup failures after debug initialization also report the paths.
The files survive shutdown. Default files use mode 0600 and the directory uses mode 0700.

The instruction JSONL records preserve instruction text and JSON values for the final
`instructions`, developer-role input messages, top-level tools, and `additional_tools`
items after all request rewriting. They include timestamp, unique local request ID,
client request ID, thread/session IDs, model, previous response ID, and cached input count.
The dump uses `scope: projected_responses_request` for the local projection before
cached-prefix removal. Separate `wire_developer_messages`, `wire_additional_tools`,
`wire_previous_response_id`, and `wire_input_items` describe the prepared outgoing subset.
`cache_rebased` identifies a full-history replacement of a stale instruction prefix;
`cached_input_items` counts only the prefix actually reused. `wire_request_present` is false
for automatic successors. A prepared snapshot does not claim successful provider acceptance;
Grok records precede Chat Completions conversion. No ordinary user messages,
tool call bodies, or authentication headers are exported. Router diagnostics record lifecycle
and parsed-request outcome/phase/status, plus a safe diagnostic code and the notice's diagnostic
reference for failures. They also record versioned, allowlisted feature observations as specified
below, without retaining feature payloads. Forwarding failures classify known wrapped transport errors without
exporting addresses, URLs, WebSocket close reasons, or arbitrary error text. Debug files remain
separate from sanitized metrics/capture. Initialization failure prevents launch; subsequent
debug write failures are surfaced on exit without changing request execution.

The AX report uses [REQ-AX-001](ax.md) calculations. At router shutdown it discovers
local Codex rollout filenames for at most 256 observed thread identities under
`$CODEX_HOME/sessions` and `archived_sessions`, or the default `~/.codex` location.
Discovery is bounded to 100000 entries and five seconds. Filename suffixes select
candidates only; a candidate is attributed by the exact ID in its bounded first
`session_meta` record, never by a hyphen-suffixed thread name. Unreadable or invalid
candidate metadata makes discovery incomplete rather than certifying uniqueness.
The inspector validates the full rollout identity and infers per-call workspace metadata. Missing, ambiguous, incomplete,
or mismatched evidence receives a fixed state code; available runtime read counts remain
visible even when rollout-dependent measurements are unavailable. The report contains metrics and
coverage, not scripts or command output; missing defect assessments stay unassessed.
It describes whole-rollout evidence available at shutdown, not just calls from this
router lifetime. Journal-only threads (at most 256, sorted, with explicit truncation)
and unattributed reads are reported separately, never silently filtered or guessed to
be tests/descendants. Their rollout identities can be inspected without promoting them
to known router threads. An invalid journal has an explicit state and no partial counts.
These files are separate from sanitized transport metrics.

`request_complete` records monotonic elapsed milliseconds and, when present, the
capturer's immutable `capture_id` and `request_sequence`. All request-scoped debug and
instruction records use that capture ID as their `request_id`; without capture they use
a local random ID. `tool_observation` maps this request identity to safe logical `call_id`
and tool name at local translation, not execution. AX and existing HPATCH evidence join
through call identity; no correlation header is added to either transport boundary.
Cancellation evidence is independent of replay diagnostic references. Allowlisted causes
are `router_shutdown`, `response_start_timeout`, `upstream_idle_timeout`,
`downstream_context_canceled`, `downstream_deadline_exceeded`, `cancellation_unknown`,
and `deadline_unknown`. The owned start-timeout cause travels with the forwarding
failure; a later expired start timer is not evidence of that cause. An observed idle
timeout is also reported when concurrent downstream cancellation wins. Downstream
context cancellation is not asserted to be an explicit user abort.


### Feature-usage debug evidence

`router.jsonl` MUST support `event: "feature_usage"` with `schema_version: 1`, fixed
`feature`, `source`, `stage`, and `outcome` categories, and a UTC timestamp. The
`router_start` event MUST advertise `feature_usage_schema: 1` and
`feature_usage_features: ["journal", "commentary"]`. Missing coverage markers in older logs mean
unobserved, not zero use. An interrupted log or a debug write failure cannot establish
complete coverage.

Feature records MAY include `request_id`, `thread_id`, `session_id`, `call_id`, and
`message_id` for correlation. Each retained identity MUST be at most 256 ASCII letters,
digits, `-`, `_`, `.`, `:`, or `/`, and MUST NOT contain `://`. Unsafe identities MUST
be omitted without affecting execution. No feature text, argument, script, token, URL, header, arbitrary attribute,
or error may enter these records. Category combinations MUST be allowlisted by the
debug owner. Future features extend that allowlist and the advertised coverage, not
the raw-data surface or capture metrics.

Every request emits `feature_coverage` for journals with `state` unavailable,
incomplete, or observed, plus fixed-category observation counts. Empty counts only
establish zero observations when the relevant response inspection completed; they
never establish that an unobserved worker did not attempt publication. Counts describe
branch observations, not unique messages or independent uses. Older logs without this
marker cannot establish zero commentary output.

The journal feature records `mutation / accepted` for batched and runtime mutations,
`tool / mutation / prepared` for dedicated router calls, `code_mode / lowering / prepared`
for runtime wiring, and separate `report_now` and `terminal_flush` rendering events.
Journal-only `code_mode / lowering` records one observation per carrier, not per expression:
`prepared` means a publisher route was created; `unavailable` means it could not be created
and lowering rejects. Neither outcome proves expression execution.
Only successful store admission counts as an accepted mutation. Prepared rendering does not
prove display. Existing `commentary` categories remain for automatic notice infrastructure:

- `source: provider_message`, `stage: authored`, `outcome: observed`: a completed
  assistant commentary message was observed at the provider boundary. A generated-looking
  message ID does not change its provenance. Consumers deduplicate by thread/message ID.
- `source: router_activity`, `stage: render`, `outcome: prepared`: the router constructed
  a root activity copy. This is not authored in-tool commentary or proof of UI delivery.
- `source: shell` or `code_mode`, `stage: publication`: an authenticated nonempty
  runtime submission reached the broker. Outcomes are `accepted`, `blank` (whitespace),
  `oversized` (rendered size), or `capacity`. Empty completion signals are excluded.
  Malformed, oversized HTTP bodies and unauthorized requests never reach this boundary
  and MUST NOT count as feature usage. Shell commands that cannot discover or reach
  a publisher remain unobserved. Successful HTTP status alone is not acceptance.
- `stage: render`, with the originating source: `prepared` means commentary passed
  provenance checks and was prepared for a response; `suppressed` means it did not.
  Accepted publications and prepared messages share `message_id`. Neither stage proves
  that the client received or displayed the message.

Request-bound observations use the same request ID as the existing debug request log.
Runtime publication has no inferred originating request or public routing-session ID;
shell publications also have no inferred tool-call ID. Runtime records use the route's
originating thread when known. The broker's internal replay key is not a public session
and MUST NOT be exported. Rendering uses the consuming request's identity, including
its public routing session, and joins accepted publications through message ID. Consumers
MUST select a stage rather than summing stages as independent feature uses. Rendering
can be reconsidered during response reconstruction, so consumers MUST deduplicate it
by message ID. Automatic usage reports, subagent activity copies, critical notices, and
standalone provider commentary MUST NOT be classified as explicit in-tool usage.

Evidence is opt-in through `--debug`, remains in the existing operational log, and
shares its serialized writes and shutdown error reporting. It MUST NOT add a capture
callback, metric counter, new listener, or execution dependency. Tests MUST cover JSON
and SSE, runtime publication, repeated observations, excluded content, suppression,
and auxiliary write failure.

Acceptance:

1. Each invocation owns a bound random loopback port without close-and-rebind races.
2. Codex can reach it immediately on launch; no config or persistent service is changed.
3. Startup failure does not launch Codex; all exits release owned resources.
4. Codex arguments, exit status, terminal input, stdout, and stderr remain intact.
5. Simultaneous configured-plugin sessions have disjoint frontends and independent cleanup.
6. Without `--debug`, no operational logs or session log files are created or mixed with Codex output. Private replay
   correctness records survive shutdown and are shared safely by simultaneous wrappers.
7. Invalid native editing/execution catalogs and forced incompatible tool choices
   fail closed with actionable HTTP 400 errors, not retryable upstream 502 errors.
8. Fixed listener and provider flags, bare serving, and the former wrap command reject.

### Codex WebSocket transport

The router accepts Responses WebSocket upgrades at `GET /v1/responses`. The
wrapper advertises `supports_websockets=true` in its invocation-only provider
override without modifying Codex configuration. HTTP `POST /v1/responses` remains
available for streaming SSE and nonstream terminal JSON. Models discovery and
Grok retain their HTTP provider transports.

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

Startup metadata with `request_kind="prewarm"` and explicit `generate=false`
is a non-generating transport handshake and does not require workspaces or a
supported tool catalog. It retains native input for the next turn without
performing tool rewriting or CTP encoding. Generating requests cannot use prewarm metadata to
bypass ordinary turn validation.

Execution-free turns pass through without Mekugi instruction or tool rewriting or CTP
encoding, regardless of their output schema. They require valid turn metadata and session
and thread IDs. Catalogs may be empty or contain native helper tools and Codex's JavaScript
Code Mode `exec` with optional `wait`, flat or namespaced. Nested clock and lookup declarations
are allowed. Generic preamble examples mentioning `tools.exec_command` are not declarations.
Admission depends on advertised tool declarations, not client preamble wording or request purpose.
Malformed catalogs, duplicate tools, wrong-kind execution wrappers, and partial editing or
process-execution catalogs do not qualify. Requests advertising native or nested editing or
process-execution tools retain the existing Mekugi admission and rewriting checks.

Request preparation and response restoration retain HPATCH tools, replay,
CTP/2, and native carrier behavior. Incremental input must retain enough
connection-local native history to resolve those transformations while sending
only new transformed input upstream when inherited instruction-bearing items still match
the provider's retained prefix. The transport fingerprints projected developer/system messages
and `additional_tools` actually sent upstream. If their later projection changes, an explicit
continuation sends the full projected history without `previous_response_id`; it does not
discard rewritten instructions or declarations. This includes prewarm-to-turn and model-workflow
transitions. Unchanged continuations retain incremental delivery. Accepted steering is not
resent against the same parent. An automatic successor cannot silently adopt a changed
instruction prefix: it fails rather than pretending an unsent rewrite took effect.
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
4. Prewarming, incremental history, tool restoration, and CTP references preserve
   their existing meaning across responses.
5. HTTP clients remain supported; a dropped active WebSocket fails without
   transparent replay, and lifecycle cancellation closes owned sockets.

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
to terminal-state validation. Unknown non-Responses event kinds remain invalid.
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
4. SSE translation, native tool carriers, CTP/2, provider usage, and capture
   remain integrated; neither nonstream delivery nor Grok needs WebSocket support
   in Codex.
5. Unsupported-upgrade fallback is pre-send only. Failed sends, partial streams,
   and provider error events are not replayed, and poisoned sockets are not reused.
