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

ChatGPT requests explicitly select `access_programs.cyber="standard"`, including
prewarming and continuations over either transport. This overrides client Daybreak
choices without changing the model or other access programs. Grok and OpenCode
requests are unchanged. This request-body selection cannot prevent an upstream
eligibility-service failure during a WebSocket upgrade before the body is sent.

With `--grok` or configured OpenCode providers, the wrapper pins the selected model catalog through Codex's
`model_catalog_json` setting before launching the interactive or execution command.
The session catalog and its cleanup follow [REQ-THIRD-PARTY-001](third_party.md).
Other invocations do not run the catalog command or pin model metadata.

Codex inherits cwd, stdin, stdout, stderr, and the environment, augmented only
with `MEKUGI_BASE_URL` and the private configured-plugin frontend directory at
the front of PATH. Before child launch, terminal Ctrl-C cancels startup and prevents
launch, including signals already consumed by the startup receiver. After handoff,
terminal Ctrl-C remains Codex-owned without double forwarding. SIGTERM to the wrapper
terminates Codex and the router with bounded cleanup. Codex exit, launch failure,
and cancellation clean up owned runtime resources. Ordinary exit status is
preserved; signal exits use `128 + signal`. Unexpected router termination also
terminates Codex rather than leaving a dead provider connection.

After successful binding and before launching Codex, the wrapper prints exactly
one `mekugi dashboard: http://127.0.0.1:PORT/` line to stderr. It does not write
the announcement to stdout or repeat it during the active Codex UI. The URL and
in-memory metrics belong to this invocation and expire on shutdown.

Interactive launches use Mekugi-owned terminal splits, including a
[live diff pane](changes.md#live-terminal-view), under its session and lifecycle contract.

Detailed operational logging is absent unless `--debug` is enabled. Failure records
are always retained with time, thread, phase, diagnostic code/reference, the complete
error string, and bounded stream diagnostics. Error strings may contain request content
or credentials supplied by an upstream error. They share the
managed store's quota and retention policy and can be looked up after restart using
`mekugi inspect-session --failures [REF]`. Failure to retain a record produces a notice,
not a claim of successful persistence or a different request outcome. Startup and cleanup failures are concise stderr
errors outside the active Codex UI. Critical request failures use the user-only
commentary contract. The launcher prints undelivered notices and repetition
summaries after Codex exits. In-memory metrics, explicit sanitized capture and
final metrics exports, and opt-in issue reports are not operational logging.
`mekugi` mode also retains private durable replay state so resumed and forked conversations restore
their original model-visible tools. This is correctness state, not an operational session log.
It lives at `$XDG_STATE_HOME/mekugi/replay`, or `~/.local/state/mekugi/replay` when that variable is
unset, and survives wrapper shutdown. A relative `XDG_STATE_HOME` is invalid. Passthrough mode
opens this store only when retaining a failure; it does not retain tool replay state.
Mekugi-mode initialization failure prevents Codex launch. The store admits at most
1 GiB of managed data, including session ownership catalogs, journals, read outputs, and change
indexes, with 32 MiB per encoded record. Exact commentary provenance has an independent
16 MiB budget; failure to retain it suppresses new commentary with a diagnostic instead of
consuming the managed-data allowance.

Automatic retention removes Mekugi-owned data after 14 days without activity and reclaims the
least recently active inactive sessions when a byte budget would be exceeded. Requests check
space before exposing retained facts; the router attempts an age sweep on startup and then
hourly in the background, outside request preparation. An age-sweep failure does not fail an
unrelated request and is reported as a router-wide notice.
The policy never deletes Codex transcripts, workspace files, exported metrics, or explicit debug
bundles. It never infers expiry from request truncation or compaction.

Small per-thread namespace bindings and allocation high-water marks outlive reclaimed
payloads, like storage lease metadata. They prevent resumed sessions from reusing
expired handles. Subagents share their root's namespace; ordinary forks and side
threads clone it once. These bindings survive a fresh router or standalone worker.

Durable catalogs bind file dependencies to stable thread IDs across workspaces, routing remaps,
forks, and restarts. Visible inherited calls, commentary provenance, and read-reference dependencies
gain another owner. A shared record survives removal of another owner. Snapshot validation and
ownership publication cannot race cleanup. Journal receipts and change streams provide
ownership where available; recent records with no trustworthy owner remain protected, while
unattributed records older than 14 days use last-write time for cleanup.

Cross-process leases protect active turns, host handoffs, and workers, including yielded calls.
A successfully delivered journal terminal releases an idle turn's lease; router shutdown also
releases its leases. Cleanup never stops a host process or changes its continuation lifetime.
If no inactive data can be reclaimed, publication fails with required bytes, the limiting budget,
and an actionable explanation rather than a generic initialization error. Catalog growth counts
toward the budget too. Cleanup reports removals through user-only notices, or worker stderr;
failed cleanup reports an error and does not claim complete reclamation.

Change-index retirement preserves stream high-water counters so old IDs are never reused.
Partially retired change histories explicitly identify removed attempts. Missing recovery references
explain session expiry or storage pressure and never replay an operation.
`--capture-output PATH` appends sanitized capture records. Token-usage Markdown
snapshots use a stable Codex-session-keyed file in the system temporary directory;
the wrapper prints each written path on exit.

`--debug` is a boolean flag requiring no argument. It creates a private, unique
`mekugi-debug-*` directory in the system temporary directory, with router diagnostics,
sanitized capture, final metrics, an instruction dump, runtime read journal, and AX report.
Debug implies AX instrumentation: the wrapper supplies the journal path to the executor
and the authenticated worker manifest retains it across child environment changes.
Explicit capture and `MEKUGI_AX_OUTPUT` destinations retain precedence.
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
tool call bodies, or authentication headers are exported as separate diagnostic fields;
error strings can contain any of them. Router diagnostics record lifecycle
and parsed-request outcome/phase/status, plus a diagnostic code, reference, and complete error
string for failures. They also record versioned, allowlisted feature observations as specified
below, without retaining feature payloads. Known provider error codes are retained through a fixed allowlist; unknown codes,
messages, and error payloads are not exported to sanitized metrics or capture. Caller-facing
failure notices include the actual provider error for terminal `error` and `response.failed`
events and failed non-stream responses, including provider status when available. These
details retain the provider error text and original error payload without credential redaction
or display-length truncation;
different details remain distinct notices. Forwarding failures classify known wrapped
transport errors and display the complete wrapped error, including addresses, URLs,
WebSocket close reasons, and arbitrary error text. Debug files remain
separate from sanitized metrics/capture. Initialization failure prevents launch; subsequent
debug write failures are surfaced on exit without changing request execution.
Model-catalog failures are identified as catalog refresh failures, not failed inference turns.
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
reporting the reference; retrying the unchanged request is not advised. The terminal itself
delivers the notice. Repeated references in the same identified thread/turn are neither
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
termination origin is not overwritten by outer readers. The sanitized stream-diagnostics
fields never contain close reasons, response text, credentials, or arbitrary headers;
the separate raw error string may contain them. These facts classify the observable
end; an unreported provider-internal cause is not inferred.

A recognized downstream WebSocket disconnect is cancellation even when the write
fails before the reader cancels the session context. It does not queue a critical
restart notice or replay the interrupted request. Other write failures remain failures.
An EOF wrapped by downstream cancellation remains cancellation during body sniffing and
stream completion; unfinished tool input must not replace that cause with a translation failure.
Stream diagnostics retain a sanitized write-termination category and, when available,
a numeric write-side WebSocket close code, separately from read termination.

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

### Native app-server preview

`MEKUGI_APP_SERVER_UI=1` selects an opt-in client of `codex app-server` for
interactive terminal launches only. It maps explicit `--yolo`, model and config
arguments plus `resume THREAD_ID`, and rejects other interactive arguments rather
than ignoring them.
Router readiness, provider catalogs, invocation overrides, native recovery hooks
and frontend environment keep their owners; redirected and noninteractive
commands keep their original path. The client speaks newline-delimited stdio RPC.

The client replaces presentation, not projection policy. Codex remains the agent
runtime and execution authority, and Mekugi's router stays in the model-request
path; UI plumbing adds no model calls. The client connects to app-server, never
to the Code Mode host, and submits intent rather than executing tools.

| Concern | Owner |
| --- | --- |
| Tools, permissions, sandbox, native agents, Code Mode | Codex; the client submits intent and answers server requests. |
| Thread, turn and item lifecycle and history | Codex app-server; never reconstructed from rendered text. |
| Routing, projection, frontend PATH | The router and launcher, with invocation configuration carried into app-server. |
| Changes and recovery references | The capturer and replay store; app-server patches are display input only. |
| Journals and delivery receipts | The [journal owner](journal.md); native events carry its records without new receipts or model-context insertion. |
| Tokens, prices, missing usage | Router accounting; app-server usage is never added to cost totals. |
| Tool classification | App-server's typed command actions and Mekugi classifiers; renderers never parse shell text. |
| Skills and guidance | The [guidance contract](guide.md) and router projection. |

Startup `resume THREAD_ID` uses `thread/resume`, not a new thread or a replayed
prompt. The returned thread identity must match the requested ID; failure exits
without falling back to a new conversation. Main hydrates text messages and
command/edit items from the returned turns before accepting input, using the
existing retained transcript window. Buffered notifications then reconcile by
item identity. Historical tools are display-only: they do not recreate live
edit previews, processes or delivery receipts. Subsequent input
starts a turn on the same thread; an active snapshot retains its steer/interrupt
target. Resume keeps the returned workspace and effective model metadata, with
journal sinks scoped to that thread. Explicit invocation model/effort settings
and the routed provider are forwarded as resume overrides; Codex owns their
precedence and reports the effective configuration. Picker, `--last` and in-session switching remain outside this increment. Full-history
resume is limited by the 16 MiB RPC frame cap; oversized histories fail rather
than bypassing the transport bound. Paginated hydration remains unfinished.

Resume also restores the Agents roster and Activity from Codex's observational
history APIs, including archived descendants. Names/roles, retained assignments,
messages, commands, edit descriptions and answers are presentation history;
children are not resumed and historical unfinished turns never imply live work.
Only completed collaboration items imply delivered assignments or messages;
other attempts retain their recorded status without claiming delivery.
Usage is shown only when available from its existing owner, never reconstructed
from transcript text. Missing child history is marked incomplete without
preventing the parent conversation from continuing.

The saved Diff pane reloads the existing durable change projections for the root
and discovered child workspace/thread identities before any new model turn.
It does not derive edit evidence from app-server history or include unrelated
workspace threads. Subsequent requests retain that scope through the existing
automatic-diff owner. Retention gaps remain gaps, not successful recapture.

Activity hydration reports progress, buffers live notifications, and keeps typed
input as an unsent draft until reconciliation; `/quit` can exit while loading.
Discovery is bounded to 128 descendants and eight list pages, with an explicit
partial-history notice at the limit. Child histories share the current 16 MiB
RPC frame limit; paginated turn/item hydration remains unfinished. No additional
model requests or execution occur merely to restore pane content.

Native pane preferences persist separately from replay/correctness records under
`$XDG_STATE_HOME/mekugi/ui` (or `~/.local/state/mekugi/ui`), keyed by workspace
and Codex thread identity. Successful resume restores the Main/right-column
split, active Diff/Activity pane, keyboard focus, and diff navigator width.
Geometry is clamped by the current terminal layout; new threads and other
workspaces never borrow these preferences. Roster height remains automatically
fitted. Scroll positions, filters, selections, drafts and transient live docks
are not persisted in this increment.

Preference writes coalesce interaction bursts and flush pending changes on
orderly exit or cancellation. Files are private, atomically replaced, versioned
and bounded to 4 KiB on read. Missing state uses defaults. Invalid/unavailable
state reports a presentation notice but cannot fail resume, submit a prompt or
change execution. Simultaneous clients for the same workspace/thread use the
last completed preference write; no process resources are restored.

Approval controls and `/side` are deferred; pending server requests stay
visible and are never auto-approved. Not in scope: Codex's TUI, PTY emulation
or screen scraping for Main; a second execution, permission or Code Mode control
path; settings clones, onboarding, cloud tasks, voice; Git write actions or edit
rollback; browser frontends or remote hosting; new auth flows; a second
transcript store or cost calculator; model-visible UI commentary.

The client launches app-server with `features.apply_patch_streaming_events`
enabled; the user's own `-c` values follow and can disable it. Real-time
activity comes from app-server notifications, not from intercepted provider
responses: `thread/started` names child agents from their spawn path and role,
typed items supply commands, edits, collaboration calls, messages and reasoning
summaries, turn events drive each agent's state, and
`thread/tokenUsage/updated` supplies token counts. Cost stays with the router's
usage accounting. The client claims Main's thread in the router's activity
collector only so child activity is never injected into Main's provider
responses; the collector does not queue that activity.

Run cards omit literal Bash, Zsh, or Sh `-c`/`-lc` launch wrappers and PowerShell
`-Command`/`-c` wrappers (optionally preceded by `-NoLogo`/`-NoProfile`), matching
Codex's shell recognition. The inner source uses the existing shell highlighting. Commands with outer redirects, assignments, additional arguments,
or dynamic wrapper words remain intact. This is display-only, including resumed items.

The shell frames Main on the left and one right pane: the saved diff (2) or
Activity (3), toggled and each filling the pane. A roster (4) above them fits its
content, four rows unfocused and up to 40% of the screen when focused; finished
agents fold into one row, and each row shows role, state, timer, tokens, cost
and turns, dropping from the right when narrow. Every pane has a title bar with
its tab number, focus and scroll state, and the status bar shows the tabs with
contextual key hints. Ctrl-B + number focuses a pane.

Streaming `apply_patch` edits dock at the bottom of the pane that owns them:
Main's in Main above the composer, subagents' at the bottom of the right pane.
A dock takes 30% of its pane, within 5 to 14 rows, and lingers briefly after
the last card completes. Concurrent edits share the dock as an accordion: cards
split evenly when each gets five rows, otherwise one stays open, chosen as the
roster-selected agent's card, then the current card, then the newest; Ctrl-B e
cycles and pins it. Router previews of exec and Code Mode edits dock the same
way; its predictions of `apply_patch` calls are dropped as duplicates. A new
saved diff never replaces Activity; the Diff tab shows an unseen badge instead,
and the saved diff lists its files or changes above the content when the pane is
narrow; focusing that list (Tab, s) enlarges it without covering the diff, and s
again hides it. A roster pick that changes Activity's agent filter shows
Activity in place of the saved diff.

Main and Activity share the activity view's block parsing, operation grouping
and viewport logic; each keeps its own entries and follow/unseen state. Typed
app-server items update entries in place. Main renders them as an unclipped transcript: user messages on a tinted
band, assistant text under one `main` heading, tool runs drawn as a tree, agent
start/message/finish events labelled `sender → recipient`, final answers as
cards, and journal blocks. Its composer supports a new thread, submission, steering and
interruption. Submitted text appears immediately and is reconciled with the
server's user message without a duplicate; rejection restores the draft; a
steer never becomes a new turn. Only `/quit` is a command, and only while idle;
unknown commands are reported, never sent as prompts. The
composer border carries turn state and the model; Main's title bar carries the
scroll position and unseen-message count. History
beyond the retained window is not hydrated. Unexpected server requests stay
visibly pending, never auto-approved.

Activity shows only child agents; Main stays in the roster for status and usage.
Each agent run has one heading with the agent's role and start time. Its events
put a short label on its own row, such as the started model, message direction
or answer, then the body at full width, separated by blank rows so narrow panes
stay readable. Directed Main/agent messages appear at both ends. Native
assignments, including follow-ups, keep their own identities; spawn and its first
prompt form one event, and full-history requests do not replay them. In Main,
child answers link (`↩ re:`) to their retained assignment, not to a message with
similar text. Hovering a loaded link underlines it; clicking it scrolls to and
briefly shades the linked message. A child turn that completes without a final answer promotes its
last message to the answer.

Native child reasoning follows Codex's summary presentation: the current summary
updates the agent's status and a transient status row with a left-to-right
brightness sweep that stops when superseded or finished. Raw and encrypted
reasoning stay excluded; the legacy pane keeps its reasoning policy.

Scrolling stops at the last full viewport, including after resizing or following
a link, and the link target briefly highlights. Frames replace changed rows
without blanking the terminal. Journal records arrive typed from the journal
owner and follow the [native journal presentation](journal.md#native-main-presentation)
contract. A request without workspace metadata keeps its unscoped journal
namespace; the app-server cwd never grants it filesystem authority.

`make preview-native-ui` replays a scripted session of fake app-server
notifications through this frontend: delegation, concurrent child edits in both
docks, a failing test and follow-up, answers and a saved diff.

The wrapped Codex terminal and dashboard remain the default and are no longer
extended. The remaining work before this client replaces them is tracked in the
[app-server proposal](../proposals/app-server-ui.md).

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
by message ID. Subagent activity copies, critical notices, and
standalone provider commentary MUST NOT be classified as explicit in-tool usage.

Evidence is opt-in through `--debug`, remains in the existing operational log, and
shares its serialized writes and shutdown error reporting. It adds no capture
callback, metric counter, listener, or execution dependency. JSON, SSE, runtime
publication, repeated observation, excluded content, suppression, and auxiliary
write failure must preserve those boundaries.

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
snapshots before any client-facing transformation. Only a successful or steered
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
