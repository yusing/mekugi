# Session API contract

## Version and authority

Control uses JSON-RPC 2.0 text messages over WebSocket. Local native clients
connect to `/api/native/control` over the private Unix socket; remote human
clients use `/api/control` through the connector. Both select WebSocket
subprotocol `mekugi.control.v2`. The socket retains kernel-confirmed same-user
peer admission, private filesystem permissions and listener/startup locks.

The first request is `initialize` with `{"version":"2"}`. Remote clients also
supply `token`, consumed by the connector and never forwarded to the server.
Initialization succeeds before any session data or operation is available.
The endpoint establishes authority; caller fields cannot select it. Remote
connections cannot invoke native startup or native-only semantic operations.

`GET /api/version` remains the readiness probe, with `Mekugi-Protocol: 2` on
request and response. Missing/incompatible versions receive 409. WebSocket
upgrades on the private socket also carry this header. Browser clients use the
subprotocol and initialization version; the connector sets the private header.
Version mismatch never authorizes a competing server or an automatic restart.

## Requests, subscriptions and logical identities

Requests have `jsonrpc: "2.0"`, a unique connection-local string or integer `id`,
`method`, and object `params`. This protocol accepts one object per text message;
batches and client notifications are refused without executing their contents.
Responses echo the request ID with `result` or `error`. Request IDs provide
correlation, not deduplication or permission to repeat effects.

After initialization, params contain optional `thread`, `startup`, `subscription`
and `body`. Body is the operation-specific input described below. The client
selects a nonempty subscription ID before requesting a stream. It is unique
among that connection's active subscriptions. An acknowledgement containing
`subscription` precedes the first notification.

| Method | Purpose / body |
| --- | --- |
| `session/options` | Execution-machine workspaces and offered models |
| `session/list` | Current session-list value |
| `session/image` | Read one image bound to a published user entry |
| `session/read` | One confirmed thread's list entry |
| `session/start` | Create/resume using the creation body |
| `session/end` | End the selected owned runtime |
| `session/subscribe` | Session-list updates without `thread`; shared publications with `thread` |
| `session/input` | Submit input |
| `session/shell` | Submit shell input |
| `session/answer` | Answer questions |
| `session/decide` | Decide an approval |
| `session/interrupt` | Interrupt work |
| `session/control` | Recorded replay control |
| `session/redraw` | Request a live redraw; native endpoint only |
| `view/attach` | Attach a per-view semantic publication stream |
| `view/intent` | Existing semantic mutation body, retaining attachment and version |
| `native/start` | Native invocation body plus its initial subscription; native endpoint only |
| `subscription/cancel` | Release one subscription, leaving execution running |
| `request/cancel` | Cancel pending request work; body contains the original `id` |

Notifications use `subscription/event` with `{subscription, kind, data}`.
Kind is `sessions`, `snapshot`, `patch` or `exit`; data preserves the corresponding
publication shape. `subscription/closed` terminates a subscription and may carry
an error. Explicit runtime exit carries the joined outcome. EOF without that
outcome is transport loss. Subscription cancellation is separate from ending a
runtime or canceling a logical coordination wait.

IDs name confirmed logical threads, never processes. Unknown/ended targets fail;
stale target/workspace/attachment admission never retargets a mutation.
Semantic owners recheck the operation's captured destination and current gates;
unrelated publication revisions do not invalidate submission, queueing, resume, interruption
or identity-addressed overlay actions. Detail requests can capture the displayed
thread with their source, path or caller. Progress and display navigation do not
invalidate that captured source. Workspace and attachment admission remain mandatory.
Requests without an independent destination identity bind to the uninterrupted
displayed/input destination of their observed publication. Raw commands and
unqualified detail requests survive progress within that destination, while an
intervening destination replacement prevents their admission.
An error's `data.status` retains the semantic HTTP status classification used by
native feedback; `data.outcome` is `not_admitted` or `unknown`. Success acknowledges
admission/dispatch where applicable; execution outcomes arrive in publications.
After transport loss, unresolved mutations remain unknown and are never retried
automatically. Reconnect creates fresh subscriptions and snapshots only.

Each subscription and connection has bounded queues. A server-to-client message,
envelope included, is at most 64 MiB, and relays admit that limit. Writes fail
when a peer stops accepting data for the write timeout, not when a slow link takes
longer. Replaceable progress catches up to complete state without retiring the
interaction. Native frames preserve
unacknowledged semantic effects and ordered attachment switches. Reacquirable
output windows have a separate backlog budget; `reacquireOutput` identifies
catch-up frames that discarded older windows. The requester retains unresolved
source identities and reacquires those reads individually without repeating mutations.
Irreducible cross-attachment deliveries remain bounded; a failed physical connection
still closes its subscriptions. Neither stalls other clients or server-owned execution. Attachment acquisition
and method execution do not block the reader. Attachment-bearing submissions
use a separate authenticated connection with the same methods and originating
view/target/version; its lifetime does not replace the view's lifetime. Limits
apply to both each submission and aggregate concurrent preparation.

Session-list data is `{"sessions":[...]}`. Entries expose `id`, `thread`,
`workspace`, title/start/status and waiting facts when known. `starting` marks a
thread whose initial input is pending. The latest list replaces its predecessor.

## Creation, resume and end

Start options include recent absolute `workspaces`, offered `models` grouped by
label, `defaultModel`, and an optional catalog-availability `note`. Each model
has `id`, `label`, `efforts` and `defaultEffort`. Recent workspaces are suggestions,
not an allowlist. Model IDs retain provider/default selection semantics.

Creation accepts `workspace`, `model`, optional `effort`, `text`, ordered
`attachments`, and optional `resume` naming a saved thread. The workspace must be an existing absolute
directory; model/effort must be offered. Initial input may be omitted so a client
can attach its control view before submitting. Initial slash/shell input is refused. An already
running or pending resume receives 409.

Creation is server-owned once admitted. Client cancellation after admission does
not cancel execution. A successful result is `{"id":ID}` after readiness
and initial-message admission. Only an explicit initial-input refusal permits
retry on a later readiness publication. Startup failure receives 502; losing
that response is still an ambiguous outcome to the client.

End returns a successful null result after canceling the selected runtime. Server shutdown cancels
and joins every runtime it owns. Disconnecting a stream or connector leaves
execution running. Saved history remains with Session History; startup never
implicitly resumes saved work or resubmits earlier requests.

### Recorded playback creation

Creation may instead carry `replay: {session, speed, seed, childReplySummary}`.
`session` selects retained history on the execution machine through existing replay
admission; other fields are optional playback settings. Replay is mutually exclusive
with live creation, resume and input. Its workspace and logical thread identity come
from admitted history, not the browser or UI host.

The server owns playback until End or server shutdown. A live or replay owner already
using the same logical thread, including a pending start, causes 409. Replay cannot
be attached as a native live thread. Shared events and playback controls remain
available; input, shell, answers, approvals and live semantic views are refused.
Replay starts no backend/provider and opens no writable history store.

## Internal agent coordination

[Thread Control](../../../../modules/threadcontrol/SPEC.md) admits prepared native
children through the server’s runtime owner, independently of renderer subscriptions.
Preparation supplies frozen execution settings and an optional retained fork import.
The server starts no assignment from ordinary creation text: confirmed creation,
durable logical admission, attributed assignment retention and execution are distinct
acknowledgements. Ordinary child input remains unavailable; prompt answers,
approval decisions and cancellation retain their existing authority.

Agent calls carry authenticated host-call identity through the invocation’s MCP
connection. Native/remote JSON-RPC authority cannot impersonate that identity.
Existing runtime registration, cancellation, joined shutdown and detached publication
also apply to children. A lost response never authorizes repeated creation or input.

## Publications

### Remote-human control views

The confirmed-thread `view/attach` and `view/intent` methods bind the existing
per-view projection and semantic dispatcher. They share the request/event types
with native attachments, including commands, menus, ordinary prompts, approvals,
resume/status, history and detail/output controls. They add no command registry
or execution owner. The simpler shared publication/input methods remain available.

The handler establishes the view's remote-human authority; neither JSON nor
headers can supply it. Subsequent intents must use the same endpoint authority as
their attachment. Remote connections cannot act through a native attachment ID or
answer a secret question, including with an empty or skipped answer. Native
preference/history persistence and local editor/acquisition operations stay
native-only. Remote publications omit native integration facts and redact secret
question content throughout prompts, Activity and details, using the shared
public projection policy. They also omit terminal renderer state that remote
clients read through `session/subscribe`: review, agent feeds, live previews,
output windows and pacing. Main remains only while a side conversation is
displayed. A raw command sent without editor metadata leaves every draft
unchanged. Attachment submissions retain the ordered descriptors, limits and
preparation ownership defined below.

A remote composer Submit/Queue operation carries `input` with the same `text`
and ordered `attachments` as ordinary input. Raw native `Draft` content is refused;
native composer operations instead use `Draft` and cannot mix the two forms.
Remote completion/editor metadata contains text and caret only, without source
bindings or prepared snapshots. A completion request may set `Explicit: true`
to reopen a dismissed target; passive draft updates retain the dismissal.
Remote question answers retain the existing
text/choice/skip contract, including its secret-question exclusion.
Remote `operation.answers` therefore uses the public `Answer` shape from the
answer method; native answers retain their final-batch payload. Conversion
resolves choices through the actual pending question owner at admission.

Unsent submissions remain intact with their originating live view. Remote effects
publish a bounded recovery preview instead of attachment snapshots. The
`recovery` operation takes `{effect, action}`, where action is `retry`, `edit` or `discard`. Edit exposes ordinary text
segments separated by immutable attachment labels. Retry may supply replacement
text segments after Edit; retained bindings and attachment snapshots remain unchanged.
Retry uses the retained snapshot through ordinary admission for its original
destination; refusal keeps it available. Discard releases that retained input.
Missing or consumed effects refuse, and ambiguous responses never trigger retries.
Delivery receipts preserve remote recoveries until this explicit resolution.
Detaching the originating view releases its unsent recovery, as it releases its
other undelivered editor state; execution and retained history remain independent.

### Native attachments

The private socket also provides native renderer attachments. This endpoint is
excluded from the connector. `native/start` accepts `workspace`,
`routerArgs` and `args`, and atomically establishes its initial subscription.
The first snapshot identifies the admitted startup operation until the execution
owner confirms a logical thread. Startup never invents a thread ID. Subsequent intents carry `startup` until an explicit originating-view switch
confirms `thread`. Reattachment uses `view/attach` with the selected identity.

Resuming a thread already owned by this server attaches the requesting native
renderer to that owner rather than executing another host resume. The source
view remains alive until the destination supplies a valid initial snapshot and
its own pending input and editor consequences have settled. A
failed attachment keeps the source view usable and never falls back to starting
another runtime. Saved-thread resume retains the existing startup path.

Native streams finish with an `exit` event containing `code` and optional
`message` after runtime cleanup. Initialization failure can produce this event
before the first snapshot. EOF without an exit event is a transport failure,
not evidence that work ended. Disconnecting at either stage leaves admitted work
running. Clients never repeat startup automatically after an ambiguous response.

Native composer submissions preserve their detached editor bindings. The server
snapshots missing file contents and copies client image sources before admission,
without deleting those sources. Existing text snapshots are reused. Only the
server's new image copies transfer to queued-input ownership; canceled, refused
or stale submissions clean up those copies. Native operation admission rechecks
the originating view, thread, workspace and attachment after acquisition. Prepared
composer and answer snapshots remain admissible across newer revisions of that
same attachment; the execution owner still rechecks operation and prompt state.

Native frames repeat an ordered, size-bounded prefix of unacknowledged semantic
`effects` with immutable IDs. Receipts advance delivery to the retained suffix;
activity and notification watermarks never skip a withheld semantic effect. Clients
apply each once and acknowledge the completed delivery watermark independently of
output hydration. `pendingEffects` covers withheld consequences and admitted inputs whose later host
settlement may require restoration. Navigation retains the source view until those
reservations and its deliveries settle; input acknowledgement alone is insufficient.
Admission reserves retained consequence capacity before consuming input or
dispatching work. Native restored inputs remain individually deliverable, in their
original editor order. Remote recovery retains its joined submission behind the
bounded preview. A complete state or single consequence that exceeds the transport's
publication limit is an explicit resource failure, distinct from observation backlog.
The `activity` window is reconstructible pacing state: a newer `Reset` watermark
clears transient reveal state against the complete Main/Agents publication without
discarding semantic effects. `notification` retains only the latest desktop wake-up.
Composer `Consumed` identifies a client that already took its submitted draft;
known rejection restores that source rather than deleting text from a newer editor.

### Shared publications

`session/subscribe` publishes `snapshot` and `patch` notifications whose data is:

| Field | Snapshot | Patch |
| --- | --- | --- |
| `session` | Always | When it changed |
| `playback` | Always during replay | When it changed |
| `input` | Always for a live session | When it changed |
| `prompts` | Always for a live session | The whole value when it changed |
| `status` | Always | When it changed |
| `main`, `agents` | The complete feed | The feed's changes, when it changed |
| `roster` | The complete roster | The complete roster, when it changed |

A feed is an object with `entries` and `order`. In a snapshot, `entries` holds
every entry and `order` every entry's `seq` in display order. In a patch,
`entries` holds only added or changed entries, and `order` is present when
membership or order changed.

A connection receives a snapshot as soon as anything has been published, then
patches. Every viewer receives a new snapshot when the attachment or the
session's thread changes, as seeking backward, restarting and switching a live
session's thread do; a snapshot replaces patches still queued for a viewer. A
snapshot replaces all client state, because a new attachment may reuse entry
sequence numbers.

A client applies a feed patch by replacing each listed entry whole, keyed by
`seq` within that feed, and by adopting a listed `order`; entries whose `seq` is
absent from that order are removed. Fields a patch omits keep their previous
value. Each viewer has a bounded queue; pressure replaces pending deltas with an
authoritative snapshot of the same owner. A session's stream ends when the
session leaves, and stopping the server closes every stream.

## Messages

Times are Unix milliseconds. Durations are milliseconds, and `playback.position`
and `playback.until` are recorded milliseconds from the session start. An omitted
field is zero, false or empty, except facts that may be unknown: a step's
`exitCode`; an agent's `work`, `cost`, `inputTokens`, `outputTokens`, `context`,
`added` and `removed`; and the status's `completed` are omitted when unknown.

- `session`: `thread` is the Main thread ID; `title`, `workspace` and `started`
  describe the session.
- `playback`: `position`, `until`, `speed`, `paused` and `simulated`, which marks
  synthesized streaming.
- `input`: a live session's input state. `reason`, when present, says why Main
  cannot take input now; `stoppable` means Main has work to interrupt; `waiting`
  counts the questions and approvals Main waits on. All
  describe Main, whichever thread the terminal displays.
- `prompts`: `questions` holds pending calls (`id`, optional `agent`, and
  `questions`). Calls contain only unanswered questions, with IDs stable when
  another client answers part of a call. Each question has `id`, `header`, `text`, and `options` with
  `label` and `description`; a secret question carries only `id` and `secret`.
  `approvals` holds pending approvals in order, each with `id`, optional `agent`,
  `title`, `subject`, `details`, and `choices` (`id`, `label`, `deny`).
- `status`: Main's [session status](../sessionpresentation/SPEC.md) as `text`,
  with `failure`, `alert`, `running` and `completed`, the turn's duration.
- `main`: Main's transcript, including other agents' answers and failures as
  Main received them. `agents`: every other agent's own activity, including the
  tasks and messages it received. Entries the terminal does not show as rows are
  not sent.
- An entry's `seq` identifies it within its feed and one attachment. `agent` is
  the caller path of the agent it belongs to; Main's own entries in `main` may
  name it `Main`. `kind` names the item, `text` is its Markdown summary,
  `observed` is its time and `running` marks unfinished work. A task sets
  `assignment`, with its recipient's caller path in `assignmentTo` and its
  Markdown in `assignmentText`. `values` holds the entry's presentation values.
- `roster`: `agents` in tree order, and the session's net changed lines as
  `added` and `removed` when known. Each agent has its caller path as `name`,
  its display name as `label`, `role` (absent for Main), tree `depth` and a
  `state` of `working`, `idle`, `done` or `error`. `turns` and `roundtrips`
  count its work. `work` is its accumulated work time; while it works,
  `workSince` is when the current work started, so the current total is `work`
  plus the time since. `lastResponse` is when it last replied.
  `cost` and the `inputTokens`/`outputTokens` pair are its usage, with
  `costPartial` and `usagePartial` marking lower bounds; `context` is the tokens
  in its context window of `window`. `added` and `removed` are the lines it
  changed, `skills` its active skills and `items` its status items.

Presentation values carry [Activity's](../../../../modules/activity/CONTRACT.md)
renderer-neutral facts: `kind`, `verb`, `label`, `code` and its language `lang`
(`fenced` marks a code block), `body`, `reads` with their `path` and `ranges`,
`path` for an attachment source, `autoReload` for compaction reload provenance,
`approval` for its current approval outcome, `subsumed` steps, `duration`, `thought` time, `workdir`, a message's `from` and
`to` caller paths, `questions` with their `answer`, `waits` with each target's
`name` and `status`, `exitCode`, `editOutcome`, `running` and `live` state, the
output `tail` with `tailOmitted` earlier lines, `changes` with their counts and
`footer`, and `hook` status. A child's reply that carries its own summary has
it in `summary`, alongside the whole reply in `body`, while the invocation's
`child_reply_summary` feature is on. Labels, bodies and `text` are display-sanitized
Markdown; `code` is literal text in the language `lang` names. `batchExit` marks
a compound command's combined result; it carries the command that ran as `code`
only when its entry has no other value, and otherwise no label.

## Attachment input

Creation and later input accept the same ordered attachment descriptors:
`{"source":"path","path":PATH}` or
`{"source":"upload","name":LABEL,"data":BASE64}`. Upload names are display labels,
never filesystem destinations; content bytes, not a browser MIME declaration,
determine supported image/text semantics. Paths resolve on the execution machine
against the selected thread's absolute workspace. User files are borrowed.

Combined decoded uploads are limited to 32 MiB per message. Existing composer
text/directory framing and text-envelope limits remain in force. Oversized or
unreadable snapshot content retains explicit omission frames and a `notice` in
the successful acknowledgement; it is not silently truncated. Creation adds
that optional notice alongside `id`; later-input acceptance may return it alone.

Acquisition runs outside the session event loop. Admission rechecks captured
thread/workspace and operation state, then transfers newly acquired image files
to existing queued-input ownership. Cancellation, refusal or stale completion
reclaims only files acquired by that request; admitted image retention supports
continuation. Uploaded text snapshots use bytes directly, not temporary files or
invented host paths. Retried admission reuses snapshots without rereading sources.

Malformed/unsupported content has error status 400; size violations have status
413. Status codes below refer to `error.data.status`, not HTTP responses.
Only an explicit admission refusal permits retry; transport failure after dispatch
leaves the outcome unknown and never triggers automatic resubmission.

## Session requests

Operation bodies are JSON of at most 1 KiB, with these exceptions:

- Creation/input and all native or remote semantic-intent bodies allow base64
  expansion of 32 MiB plus 1 MiB for text and metadata, including intents without
  attachment input.
- Native startup and shell bodies allow at most 1 MiB.
- Answer/decide bodies allow at most 64 KiB.

A larger body has error status 413.

`session/input` takes `{"text": TEXT, "attachments": [...]}`, sent as Main's input as
Enter sends it. Success means the session accepted it; 409 means it cannot take
input now; 400 means no usable input or malformed/permanently invalid content.
`session/interrupt` ignores its body; success means the interruption was issued,
and 409 means there is nothing to interrupt. Reasons use `error.message`. A live session that does not answer within a few seconds, such as
while its owner is unavailable, receives 503.

`session/shell` takes `{"command": TEXT}`. Text is the shell
command itself, without the terminal's Shell Mode prefix. An initial `!` retains
its shell meaning. Success acknowledges dispatch to Main's host shell path; execution
results arrive through the ordinary activity stream. 409 carries an admission
refusal, 400 rejects empty/malformed commands, and replay returns 405. The request
can run during Main's work. Submission and host rejection leave the terminal's
current draft in place; rejected remote commands remain in input history.

`session/answer` takes `{"call": ID, "answers": [...]}`. Every
remaining question needs an answer with `question` (its ID), `choice` (zero-based option
index), `text`, and `skip`. An absent choice or the index after the last option
selects a written answer. `skip: true` explicitly leaves a question unanswered.
Secret questions cannot be answered through this endpoint.

`session/decide` takes `{"approval": ID, "choice": CHOICE_ID,
"reason": TEXT}`. The choice must be offered by that approval; only a denial
accepts a reason. Both methods return success when accepted, error status 409
when refused (including already answered prompts), and 400 for malformed requests.

`session/control` takes `{"action": ACTION, "position": MILLISECONDS}`.
`position` applies only to `seek`. Success means the request was queued; its effect
arrives on the event stream. 400 means the body was malformed or named an
unknown action. Refused requests leave playback unchanged.

| Action | Effect |
| --- | --- |
| `toggle-pause` | Pause or resume |
| `faster`, `slower` | Step to the next speed preset |
| `back`, `forward` | Seek ten recorded seconds |
| `restart` | Resume from the start |
| `seek` | Move to `position`, clamped to the recording |

## Browser types and validation

`protocol.ts` is generated from the Go wire types and the admitted RPC method
catalog. Run `go run ./apps/mekugi/cmd/sessionapi-schema` to refresh these types and `make check-generated`
to check drift. Native-only fields remain subject to endpoint and operation
admission; TypeScript types do not establish authority.


### Published user images

User entries carry bound token spans and opaque image identities. `session/image`
takes `{seq, agent, index, image}` in its body on the selected thread. It resolves
only an image in that current published entry, never a caller-supplied path or URL,
and returns `{mime, data}` with base64 raster bytes. Reads use the admitted source
and selected workspace, run outside publication locks, and share attachment-transfer
capacity. Missing, oversized or non-raster sources fail explicitly. Local
files and inline image data are supported up to the upload limit; remote URLs are
not fetched. Browser image transfers use an independent authenticated connection.
