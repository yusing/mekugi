# Session API contract

## Version and authority

Control uses JSON-RPC 2.0 text messages over WebSocket. Local native clients
connect to `/api/native/control` over the private Unix socket; remote human
clients use `/api/control` through the connector. Both select WebSocket
subprotocol `mekugi.control.v5`. The socket retains kernel-confirmed same-user
peer admission, private filesystem permissions and listener/startup locks.

The first request is `initialize` with `{"version":"5"}`. Remote clients also
supply `token`, consumed by the connector and never forwarded to the server.
Initialization succeeds before any session data or operation is available.
Observation requests may follow initialization on the ordered connection before
its reply returns. Token and version admission still precede their execution;
the initialization response precedes all observation replies and publications.
Its response includes `serverTime` in Unix milliseconds. Browser timers anchor
that sample to their local monotonic clock, independently of local wall time.
The endpoint establishes authority; caller fields cannot select it. Remote
connections cannot invoke native startup or native-only semantic operations.

`GET /api/version` remains the readiness probe, with `Mekugi-Protocol: 5` on
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
Admission remains cancelable until that acknowledgement has been delivered.
A failed or timed-out observer releases its subscription explicitly; late
acknowledgements cannot reactivate a retired subscription.

| Method | Purpose / body |
| --- | --- |
| `session/options` | Execution-machine workspaces and offered models |
| `session/list` | Current session-list value |
| `session/image` | Read one image bound to a published user or View entry |
| `session/attachment-image` | Preview an explicitly attached execution-machine path |
| `session/read` | One confirmed thread's list entry |
| `session/page` | A bounded retained-feed, roster or saved-review metadata page |
| `session/content` | Complete content named by a published reference |
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
| `publication/ack` | Acknowledge the current connection-local delivery ID |

Notifications use `subscription/event` with `{subscription, kind, data}` and an
optional `delivery` receipt ID for shared publications.
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

Selected-thread shared publications and page replies have a 32 KiB envelope
budget and at most 50 entries per page. These are transport windows, not retained
history limits. One such delivery may be in flight on a connection. Its optional
`delivery` ID, on the event envelope or page result, is acknowledged after
application or intentional stale discard;
only the current ID releases the next delivery. Canceling an observation removes
unsent work but does not acknowledge already-sent bytes. Other control replies,
semantic effects, discovery and local native frames retain their delivery rules.
Browser transfer connections follow the [Web UI contract](../webui/CONTRACT.md).
Canceled content and image replies are discarded before transmission begins. Content transfers
share the endpoint's aggregate preparation capacity through response delivery.
Explicit content reads remain cancelable and do not inherit the ordinary
control-read deadline, which can expire during healthy large transfers.

Session-list data is `{"sessions":[...]}`. Entries expose `id`, `thread`,
`workspace`, title/start/status and waiting facts when known. `starting` marks a
thread whose initial input is pending. The latest list replaces its predecessor.
The list contains top-level sessions only. Prepared children remain addressable
by confirmed thread ID; `session/read` exposes their `parentThread` identity.
This is the parent thread at admission, unchanged by runtime thread switching.
The shared family publication supplies navigation independently of parent views.

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
acknowledgements. Ordinary human input becomes available after logical child
admission and uses that child's actual runtime. An idle child acquires a tracked
assignment and concurrency reservation; a running child receives a steer. Its
result follows the existing parent-delivery lifecycle. Prompt answers, approval
decisions and cancellation retain their existing authority.

Agent calls carry authenticated host-call identity through the invocation’s MCP
connection. Native/remote JSON-RPC authority cannot impersonate that identity.
Existing runtime registration, cancellation, joined shutdown and detached publication
also apply to children. A lost response never authorizes repeated creation or input.

## Publications

### Remote-human control views

The confirmed-thread `view/attach` and `view/intent` methods bind the existing
per-view projection and semantic dispatcher. They share the request/event types
with native attachments, including commands, menus, ordinary prompts, approvals,
resume/status and detail/output controls. They add no command registry
or execution owner. The simpler shared publication/input methods remain available.

Frames publish `processes` with each live command's captured thread, turn, item
and host process identity. `stopProcess` carries that captured value; admission
refuses missing, replaced or completed identities before host dispatch. A successful
request acknowledges dispatch; ordinary command completion removes the process.
Host refusal or termination failure appears as view feedback.

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
Remote question answers use text/choice/skip and optional ordered attachment
descriptors, including the secret-question exclusion.
Remote `operation.answers` therefore uses the public `Answer` shape from the
answer method; native answers retain their final-batch payload. Conversion
resolves choices through the actual pending question owner at admission.

Unsent submissions remain intact with their originating live view. Remote effects
publish a bounded recovery preview instead of attachment snapshots. The
`recovery` operation takes `{effect, action}`, where action is `retry`, `edit` or `discard`. Edit exposes ordinary text
segments separated by immutable attachment labels. Retry may supply replacement
text segments after Edit; retained bindings and attachment snapshots remain unchanged.
Retry uses the retained snapshot through ordinary admission for its original
destination and delivery mode. Queued recovery publishes `queued: true` and
returns to the next-turn queue; only an explicit Steer action promotes it.
An explicit waiting-input edit transfers restoration to the requesting view;
automatic failure recovery stays with the originating view. Refusal keeps the
retained input available. Discard releases it.
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

A detail request's `Displayed` identity captures content within its attachment;
it does not route to another live runtime. Clients first attach that runtime,
then request its details using the destination's published version and workspace.

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

Native snapshots contain complete frames. Local native patches carry a `retained`
bitmask: 1 retains `main`, 2 retains `agents`, 4 retains `review`, and 8 retains
`family` from the preceding publication instead of repeating those fields.
All other frame fields remain complete replacements. A clear owner is an explicit
`null` with its bit unset. Retained owners never cross an attachment reset or thread switch, and
reconnection starts with a complete snapshot. Queues fold retained owners into
newer pending updates without dropping the initial snapshot or switch boundary.
Remote human frames remain complete projections. The current protocol version is required;
older clients must reconnect with a matching version rather than applying deltas.

Native image file details carry an admitted local source tied to the detail
selection. The physical frontend acquires bounded preview pixels using its own
terminal capabilities; pixels stay outside serialized frames. Closing or replacing
the selection retires its acquisition and graphics.

Large remote detail pages and retained output may instead carry a content
reference. Selection, output identity and semantic effects remain owned by the
admitted view; the read returns that same public, redacted projection. Closing or
replacing the view, detail selection or referenced revision makes the read stale.
Local native detail and effect publications retain their existing shape.

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
bounded preview. Native saved review carries the saved pane's projection; complete
retained evidence remains with its detail and edit-total owners. If saved review
cannot fit alongside state, semantic delivery and selected output, Diff reports its capacity failure
and directs narrower reads through `mchanges`; the attachment remains interactive.
Diff recovers when publication capacity returns, preserving its selected file.
A remaining complete state or single consequence that exceeds the transport's
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
| `main`, `agents` | The latest bounded feed window | Changes to that window |
| `roster` | A bounded roster page | The latest roster page when it changed |
| `review` | Saved-review identity, coverage, failure and counts | Updated metadata when its revision changes |
| `live` | Current previews for a live session | The whole list when it changes, including `[]` to clear it |
| `deferred` | References for oversized auxiliary sections | The current reference set |

Main’s `live` list includes the current admitted family’s previews with original
source attribution. Selecting a child keeps its own live list. Attachment
replacement and scope withdrawal retire the corresponding previews independently
of saved review and host execution.

A feed is an object with `entries`, `order`, a history revision and an optional
earlier-page cursor. In a snapshot, `entries` holds the latest window and `order`
its sequence IDs in display order. In a patch,
`entries` and `order` replace the latest window whenever that feed changes.
An entry's `position` preserves display order independently of sequence identity;
its optional `turn` is the user-turn ordinal across the retained feed.

A connection receives a snapshot as soon as anything has been published, then
patches. Every viewer receives a new snapshot when the attachment or the
session's thread changes, as seeking backward, restarting and switching a live
session's thread do; a snapshot replaces patches still queued for a viewer. A
snapshot replaces attachment-scoped client state when the attachment changes,
because a new attachment may reuse entry sequence numbers. A catch-up snapshot
with unchanged attachment and history revision preserves acquired earlier pages
and the reader's position.

A client applies a feed patch by replacing each listed entry whole, keyed by
`seq` and entry revision within that feed, and by adopting the latest window's
listed `order`. Explicitly loaded earlier pages survive ordinary latest updates.
Attachment replacement or historical mutation invalidates their cursors and
loaded history. An oversized entry preserves its compact semantic presentation
and carries a typed reference to its complete published content. Inline content
is acquired automatically; content behind existing disclosures is acquired when
opened. This transport deferral is distinct from
Activity's source/output omission facts. Fields a patch omits keep their previous
value. Each viewer has a bounded queue; pressure replaces pending deltas with an
authoritative snapshot of the same owner. A session's stream ends when the
session leaves, and stopping the server closes every stream.

Page cursors bind attachment, feed or metadata category, revision, caller filter
and boundary. Stale cursors fail rather than mixing attachments or compositions.
Saved-review file metadata loads when Changes opens; full file content is read by
the exact caller, revision and file identity. Caller filtering composes the
selected caller's changes against other callers' captures as baseline. It never
filters an all-callers diff after composition. Oversized auxiliary sections remain
available through explicit content references rather than silent truncation.

## Messages

Times are Unix milliseconds. Durations are milliseconds, and `playback.position`
and `playback.until` are recorded milliseconds from the session start. An omitted
field is zero, false or empty, except facts that may be unknown: a step's
`exitCode`; an agent's `work`, `cost`, `inputTokens`, `outputTokens`, `context`,
`added` and `removed`; an entry's `outcome`; and the status's `completed` are
omitted when unknown.

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
- `review`: `revision`, `workspace`, `coverage`, optional `failure`, `fileCount`,
  `counts` and a metadata `page` cursor. `callers` lists each caller with captures,
  Main first, as `caller` (canonical path, or `?` when unknown) and file `count`.
  Review counts contain `Net` (null without project evidence) and `Callers`.
  Metadata pages return files with `key`, `path`, `counts` and a `content`
  reference. The complete file adds composed `chunks` and `origins`, retaining
  Change History's wire shapes and provenance. File counts use `Added` and
  `Removed`; negative counts mean unavailable. Metadata may abbreviate long paths;
  complete reads retain them. Compositions include admitted runtime descendants
  and share the terminal Diff pane's temporary-only exclusion and caller policy.
  Loading and failure remain explicit. An unavailable review clears the previous
  value with `null`; attachment replacement resets it.
- `live`: the ordered current Live Preview snapshots, with `ID`, `Caller`,
  `Status`, `Files` (including `Diff` text), `Truncated` and `Complete`, plus the
  preview owner's remaining metadata. Live sessions sample these at the shared
  frame interval. They are provisional, separate from saved review, and absent
  from recorded playback.
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
  For delivered agent results, optional `outcome` is the authoritative terminal
  outcome: `completed`, `failed` or `interrupted`. It is independent of authored
  text. Omission means no outcome is available, including historical messages
  without retained outcome metadata; clients must not infer it from the payload.
- `roster`: a page of `agents` in tree order, `total`, optional `next` cursor,
  and the session's net changed lines as
  `added` and `removed` when known. Each agent has its caller path as `name`,
  its display name as `label`, `role` (absent for Main), tree `depth` and a
  `state` of `working`, `waiting`, `idle`, `done` or `error`. A waiting agent
  may publish `wait`, using the same presentation-value shape as transcript
  progress, including its target names and effective timeout. Optional `activity`
  carries its owning runtime's latest shared roster summary as entry-shaped
  `kind`, `observed` and `values`. It is display-only: it supplies labels, read
  paths, progress, short reasoning and a bounded code preview, without retained
  output, full authored bodies or a navigable source identity. `turns` and `roundtrips`
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
`name` and `status`, `waitReason` for a completed wait's actual outcome,
`waitTimeout` for a waiting subscription's effective duration, `exitCode`,
`editOutcome`, `running` and `live` state, the
output `tail` with `tailOmitted` earlier lines, `changes` with their counts and
`footer`, and `hook` status. A child's reply that carries its own summary has
it in `summary`, alongside the whole reply in `body`, while the invocation's
`child_reply_summary` feature is on. Labels, bodies and `text` are display-sanitized
Markdown; `code` is literal text in the language `lang` names.
`detailCode` preserves original JSON tool evidence for a semantic operation's
content dialog, separately from its compact transcript presentation.
`batchExit` marks a compound command's combined result; it carries the command that ran as `code`
only when its entry has no other value, and otherwise no label.

## Attachment input

Creation and later input accept the same ordered attachment descriptors:
`{"source":"path","path":PATH}` or
`{"source":"upload","name":LABEL,"data":BASE64}`. Upload names are display labels,
never filesystem destinations; content bytes, not a browser MIME declaration,
determine supported image/text semantics. Paths resolve on the execution machine
against the selected thread's absolute workspace. User files are borrowed.

Selected screen text uses `{"source":"selection","name":DESCRIPTION,"data":BASE64,"path":SOURCE}`.
Its data is UTF-8 text; optional `path` is quoted provenance and is never read as a file.
Preparation uses Conversation's selection-label and quoted-content framing, including its
mention-size limit. These references remain separate from ordinary input text and retain
their source through history and continuation. Selection bytes share the decoded upload budget.

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
- Answer bodies use the attachment input envelope, with at most 1 MiB combined
  text/metadata and 32 MiB combined decoded uploads across the questions.
  Decide bodies allow at most 64 KiB.

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
Written answers may include `attachments` using the ordinary input descriptors and
limits. Preparation snapshots each question separately, then transfers the batch's
acquired images together at admission. Images follow the existing host answer
delivery, with numbered image markers in the corresponding answer. Offered choices
reject attachments; skipped answers do not acquire them. Preparation or admission
failure releases unadmitted uploads without changing authoritative answer drafts.
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


### Published images

User entries carry bound token spans and opaque image identities; View and
Generated entries carry their bound image identities, including live, restored
and resumed history. Generated entries use the host's saved artifact path.
`session/image`
takes `{seq, agent, index, image}` in its body on the selected thread. It resolves
only an image in that current published entry, never a caller-supplied path or URL,
and returns `{mime, data}` with base64 raster bytes. Reads use the admitted source
and selected workspace, run outside publication locks, and share attachment-transfer
capacity. Missing, oversized or non-raster sources fail explicitly. Local
files and inline image data are supported up to the upload limit; remote URLs are
not fetched. Browser image transfers use an independent authenticated connection.

`session/attachment-image` takes `{path}` on a selected live thread, or
`{path, workspace}` without a thread before creation. The initial workspace must
be an existing absolute directory on a machine that supports thread creation;
live requests use the selected thread's workspace and reject workspace overrides.
The read-only request uses the same authenticated connection, transfer capacity,
regular-file bounds and raster validation as published images, and returns
`{mime, data}`. It never fetches URLs or retains server-side drafts. Relative paths
resolve in the selected workspace. Non-raster paths return 415 and remain ordinary
file chips; unreadable paths show a preview failure. Previewing does not change
which source is acquired when the draft is eventually submitted.

Live `view/attach` selects a confirmed `thread` identity. The server resolves
admitted cold membership and preparation without client-supplied parent context;
selection resumes execution ownership without submitting input or restarting work.
Unadmitted historical identities remain observational.

Every ordinary view includes `family`: its `root` thread, admitted `threads`,
navigation `agents` and root `status`. Navigation contains roster summaries and
aggregate change counts, not another thread's transcript. Each selected thread
retains its own feed, workspace, settings, prompts and effects. The server derives
family values from ordinary runtime publications and retained membership,
independently of connected viewers. Shared publication rosters use that same
family value. Admitted logical membership owns each agent's lifecycle status;
runtime activity and usage enrich the row without replacing that status.
Saved review composes captures once for the selected workspace.
Retained members without a live runtime keep their logical lifecycle status in
the roster, including completed delegates on resume. Finished rows remain subject
to the terminal's ordinary compact-roster policy.
