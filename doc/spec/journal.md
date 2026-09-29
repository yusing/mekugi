# Journal

## Native Main presentation

V2 journals have a Journal pane alongside Diff and Activity, selected by `Ctrl-B 5`;
composer letters never open it. When at least four rows fit, a header counts the
journal's own tasks by state above a separator. Open tasks precede finished tasks; finished
subtrees collapse by default. Navigation uses arrows or j/k, Space expands finished
subtrees, d opens full Markdown details, and Enter copies a selected path. Workspace
and unscoped journals remain separate; n switches the selected namespace. Only notes
from the presented namespace are suppressed in Main or acknowledged via the pane.
Router stamps display in local time.
Only tasks show state; blocked tasks retain their reason and dropped tasks are dimmed.
A plan strip remains above the composer while any task is open. It shows the current
task, progress and the next pending task.

Child journals mount read-only under the parent's linked task, or under an Agents
group when unlinked. Enter on a mount opens that child's Activity; an agent absent
from the roster, including an unresolved mount, yields a notice. The parent task
remains parent-owned; the mounted root observes the host lifecycle. Unknown lifecycle
has no state label. Child changes refresh mounted views without copying notes into
the parent's record or acknowledging child events on behalf of the parent. The plan
strip counts only the presented journal's own tasks.

Native Main receives event rows after persistence. Notes are rows only while the
Journal pane is hidden; blocked task rows remain visible. Successful terminal delivery
publishes a framed turn card with Outcome, This turn and Remaining. Its notes expand
in place. Empty Outcomes omit the answer line but retain the remaining work. Cards
have response-specific identity, including consecutive empty Outcomes at one sequence.
Answer reply links use the retained original question. The Journal pane restores its
current tree from durable storage independently of provider requests.

Retained v1 publications retain their milestone and grouped-answer presentation.
The authenticated mutation path publishes native Main milestones after persistence,
including those without `report_now`, even without an open provider response.
The frontend applies pending milestones before later host events and preserves
their transcript position at terminal flush. Captured answers remain terminal-only, including answers without a linked question.
Native deletion retracts an already applied milestone even without `report_now`.
Internal journal transport commands are hidden only when their exact generated
prefix matches the same thread’s durable translated call, including on resume.
Ordinary commands mentioning `mjournal` remain visible. Enqueueing is not acknowledgement: successful
UI output acknowledges exact revisions through the journal owner. Terminal
records become eligible only after successful downstream response completion.
Failed presentation leaves unacknowledged records durable and pending.

The substantive provider final remains in Codex's response. The frontend hides
only the exact captured provider item IDs associated with native journal answers,
so each answer displays once. Capture-capacity fallback stays raw. Child native
completion payloads, nonattached inline delivery and legacy exact-ID replay
provenance are unchanged. Native sinks are scoped by workspace and stable Main
thread; missing-workspace records are never rebased to the app-server cwd.

## Cross-agent reads and binding

`set` accepts `agent` to bind a direct child's canonical path to one owned task.
The binding cannot be changed or duplicated. It can precede the child's first
request; until durable ancestry proves the child, only an unresolved mount appears.
Only complete, nonconflicting ancestry in the selected workspace permits content
to appear. Reading an ancestor does not reveal the caller's siblings. Ambiguous
agent identity rejects the combined read rather than selecting a journal.

Owned paths remain ordinal. Combined reads reserve `@agents` for the unlinked
group and `@<escaped-thread-id>` for mounted roots; descendants retain their local
ordinal suffixes. These are stable JSON Pointer view addresses accepted by `read`,
not mutation targets. `read agent` addresses the child's local paths. Combined
views are bounded to 8,192 nodes; larger views require a narrower agent read.

The router records a child's accepted requests as working. Native app-server
turn completion supplies done or blocked lifecycle evidence. Provider response
completion alone is not child completion. Frontends without host completion
evidence retain the last observed state. A parent cannot become done while any
mounted descendant remains open, including an unresolved mount. The check applies
to tasks a batch completes or rebinds; a child resumed under an already done task
does not block the parent's other writes. Reading and restarting do not revive
child processes.

Forks copy task states and history, but not live agent bindings to children owned
by the source parent. Copied events retain the historical assignment. New bindings
must refer to the fork's own children. An unavailable or oversized mount view does
not suppress a parent's successful answer; its terminal card retains the own-record
outcome and reports mounted journals unavailable. Explicit combined reads still fail.

## Evidence-backed recovery

V2 recovery uses a deterministic summary of constraints, open tasks, established
results and retained changes, with actionable paths first. Context nodes are kept
in full. Working tasks precede pending and blocked tasks; completed work and notes
follow. When completed work exceeds its budget, the newest results are kept in
tree order and the omitted count is stated. Bounded detail excerpts point back to
`read`, never claim to be complete.

Changes and failed commands captured after the latest journal event appear in a
separate section. It includes change ranges, aggregated numstat, failed commands,
observed exit status, the end of bounded host output and durable `mread`
references. Unknown exit status stays unknown. At most eight failed commands are
listed, newest kept within the remaining capacity, with earlier ones counted. A
failed command whose output was not retained is listed as such. Unordered records
from before failure capture ordering are covered by a known journal boundary. The
resume direction points at this evidence and the working task, rather than
treating an investigation's hypothesis as established. Capturing failed output
does not replace or alter the host's result. An unreadable evidence boundary does
not block journal writes; the next summary treats the boundary as unknown.

The summary is at most 64 KiB. Mandatory constraints and open tasks must fit its
reserved half; otherwise rendering fails rather than omitting a constraint or a
task. Corrupt failure evidence also fails rendering. The native recovery hook
treats failure as advisory; router-side synthesis uses provider fallback.
Neither recovery path acknowledges events or replays effects.

## Router-answered compaction

`--journal-compaction=auto` opts into journal summaries for validated local Codex
compaction requests, including child threads. The default remains `off` until the
offline harness and separately authorized paid evaluation establish the proposed
success, redo and token-cost gate. `slice` is reset-only, with ordinary compactions
forwarded to the provider; slice-reset driving is not yet delivered.

Synthesis requires one selected workspace, an unambiguous requesting thread and
durable journal or executing-thread-owned change evidence. Conflicted identities,
missing evidence, rendering failures and persistence failures forward the request
without journal/tool projection. Existing model aliases, service-tier policy and
successful-compaction Mentor resets still apply. Invalid compaction protocol requests retain the existing validation
errors. A summary is never empty. A downstream delivery failure is reported, not
retried as a second provider request.

Native local compaction may omit workspace metadata. Only a unique workspace
already selected in that requesting thread's durable journal/execution records
can substitute for it. Conflicting historical workspaces fall back to the provider;
router cwd and another thread's records never supply a directory.

Codex retains execution and history authority. The router supplies a completed
assistant summary through the same streamed Responses delivery path, persisting
its thread/workspace and response identity before exposure. No model request is
made, and no model usage is fabricated. Metrics distinguish router answers from
provider answers and report zero provider attempts/tokens for router answers.
Summary bytes and evidence counts are measurements, never savings estimates.

The recovery hook suppresses duplicate injection only when Codex's latest
compacted transcript record identifies that persisted response. Missing or
unreadable transcript evidence, older host records without response IDs, and
provider-written summaries keep normal hook recovery. Failed or interrupted local
responses cannot suppress recovery for another compaction.

## REQ-JOURNAL-001 — Durable work journals

Mekugi owns one durable journal per stable thread. Passthrough is unchanged.
A journal has a materialized task tree, append-only timestamped events, and delivery
cursors. Nodes have stable ordinal JSON Pointer paths such as `/1/2`; sibling keys
are never renumbered or reused after successful removal. Titles are display text,
not addresses. Notes and context nodes have no state. Only tasks have `pending`,
`working`, `done`, `blocked`, or `dropped` state. Answers remain router-owned.

Nodes contain a one-line nonblank title, optional Markdown body, author, creation
and update stamps (`seq`, RFC 3339 `at`), and children. Tasks can additionally contain
a reason, their first working stamp and their latest completion/drop stamp.
Reopening a finished task clears its current finished stamp without rewriting history.
The tree has at most 512 nodes; combined title, body, reason and question content is
limited to 16 KiB per node. Events are bounded at 4,096 and an 8 MiB content-and-metadata budget. At capacity, adjacent edits
without state transitions may collapse. Notes and state transitions
are retained; irreducible capacity failures reject atomically instead of losing history.
The managed record byte limit applies independently. Historical v1 timestamps are
unknown rather than fabricated.

Each mutation is `plan`, `add`, `set`, `log`, or `remove`. Native eligible non-strict
function tools accept an optional atomic `journal` array, applied before execution
and removed from host arguments. Code Mode uses the exec-local `journal(...)` helper.
No dedicated journal tool is exposed. Operations are:

- `plan {under?, tasks, reset?}`: each string adds a pending task; objects contain
  `p?`, `title`, `state?`, `body?`, `reason?`, and nested `tasks?`. Existing paths must
  name distinct direct task children. Unlisted pending children become dropped with
  an explanation; working and final children remain. A plan returns created paths
  in preorder. `reset: "slice"` records a slice boundary policy; driving context
  resets is a separate frontend capability.
- `add {under?, kind?, title, body?, state?, reason?, before?}`: adds one node, with
  default kind note and default task state pending. `before` changes display order,
  not stable keys, and must name a sibling.
- `set {p, title?, body?, state?, reason?, agent?}`: updates writable fields. Blocked and
  dropped tasks require a reason. A final task reopens only with working.
- `log {p?, text}`: adds a timestamped note under the single working leaf task,
  or root if none is working. Several working leaves require an explicit path.
- `remove {p}`: removes a mistaken subtree, retaining its removal event.
- `read {p?, agent?, depth?}`: returns the selected subtree. Depth zero omits child
  nodes. Omitted agent selects the caller; explicit agents require proven ancestry.

Mutation batches validate at the end. A done task cannot retain open descendant
tasks. Rejection lists those paths and rolls back all nodes, ordinals and events.
Single mutations return their affected path; plans and batches return paths in order.
Receipt replay returns the original result without applying effects twice.

Ordinary forks copy the source's latest journal at their first accepted request and
then evolve independently. Resumes and routing remaps retain stable thread identity.
V1 items migrate in order to notes or answers with stable paths and preserved questions;
retained v1 IDs resolve as legacy aliases, not writable v2 paths. Retained add/edit/delete/list
calls remain replayable, but new model catalogs expose only v2 operations.
Session retention, selected-workspace scope, initialization errors and durable identity
requirements are unchanged. Active turns and shared inherited records remain protected.

The router associates a completed assistant final answer with the latest actual user message
or native `NEW_TASK` assignment addressed to the requesting child from that request's visible
history, not instruction or environment context.
Assignments use their plaintext payload, not the routing header. The request's canonical child
name must match both the native recipient and task header, and the header sender must match the
native author. Ordinary inter-agent messages, completion notifications, and assignments to other
agents are not answer sources. A later user message or eligible assignment replaces the earlier
source. An encrypted assignment cannot supply a question and blocks fallback to an older source.
The agent does not repeat the message. The completed answer is normally captured as a durable
journal item before terminal delivery, and its original question is retained by list, durable
replay, restart, and forks, independently of later user messages. If the automatic answer exceeds
the per-item limit, the journal is full, or streaming already exposed the raw answer, the provider
answer remains visible unchanged. In these capacity cases, unflushed milestones remain pending;
the router does not claim a journal flush or lose the answer from subsequent provider history.
Inference is request-local;
concurrent threads and branches do not share its source. Journal mutation schemas do not
expose an answer flag, and ordinary milestone edits preserve any attached question. Previously
retained answer-marked and finish calls remain replayable but are not offered to new model turns.

Reads authorize and return one locked durable snapshot. Unknown, ambiguous or
conflicted ancestry fails closed, including after a router restart with only the
requesting thread observed. Agent names alone do not authorize access.

The agent finishes naturally with a final answer after inspecting required tool results.
On a successful completed response with no client-dispatched calls, the router captures the final
answer for the journal renderer, suppresses the raw provider final-answer message, and returns the
terminal journal delivery without another provider request. A journal-only operation without
a final answer continues so its result remains inspectable. Mixed client calls remain
host-dispatched and prevent terminal delivery. Journal operation error results continue for correction.
Invalid or rejected mutations in a dedicated journal call return an `ok: false` tool result
for correction and prevent its primary operation. Batched fields on host-dispatched tools
still fail translation under the atomic field contract.
Failed, incomplete, or interrupted responses never flush. Completion is response-local:
replay, resume, and forks do not finish a new turn.

For v2-authored journals, successful Main completion emits a Markdown turn card
when no native frontend is attached. Outcome is the captured final answer; This turn
contains events after `flushSeq`; Remaining contains open tasks. A card with none of
these says so instead of ending at its heading. Answer questions
remain stored but are not echoed in the card. A blank final or a case-insensitive
`done` with an optional period is an empty Outcome: no answer node is created and
the exact raw provider item is hidden. The final message is still required.
Without a turn card, a v1-authored journal keeps such a final as its answer.

Live fallback renders events after `liveSeq`, clipping a row that no single update
can hold; the turn card keeps it whole. Successful downstream delivery advances
the corresponding cursor; failure preserves its window. Main cards remain the last
assistant message. Failed, incomplete and interrupted responses never terminal-flush.
Retained v1 authoring keeps its legacy presentation and delivery receipts during replay.
A child completes without flushing and emits `Journal result` containing only
revisions newer than its durable `resultSeq` cursor. Agent recipients see neither
the echoed assignment nor opaque item IDs. Notes are Markdown bullets and answers
are standalone Markdown; stored questions and the complete journal remain available
through reads, Activity, replay and forks. An empty delta says `No new journal entries.`
The cursor advances only after successful downstream terminal response delivery.
Failure leaves the previous window available for retry. Result acknowledgement
does not consume Main live or terminal delivery state.

The child result appends retained change ranges and aggregated numstat for this
result's executing-thread-owned evaluations, including recoveries in another stream.
Journal revisions and change evaluations share a locked delivery snapshot and
independently monotonic cursors. Ranges identify retained changes while statistics
exclude earlier delivered evaluations and other agents' work. A later recovery of
an earlier change is included as a new evaluation. Follow-up results add one cumulative line counting retained owned evaluations and
changes. Missing or retired evidence is unavailable, never reported as zero. Change ranges preserve stream allocation order,
split at gaps and the reader's range limit. Response delivery retains the prepared
storage namespace and turn lease through the execution lifetime.
The native completion result is the sole audience payload; the router does not send an
additional completion notification or take over host lifecycle or audience routing.
Live updates remain immediate. Main completion never replays descendant journals, including after router restart.
Child journal revisions remain available through authorized journal reads and subsequent child results.
Failed main delivery leaves its own unacknowledged revisions pending. Failed,
incomplete, and interrupted responses neither flush nor discard already-streamed provider output.
Router-owned messages use generated IDs and are removed from later provider input by exact ID.
Before adding journal results or notices to a streaming terminal with an absent or empty
output snapshot, the router MUST preserve completed streamed items in the projected snapshot.
Internal journal continuations MUST preserve client-dispatched calls and their paired
results in subsequent WebSocket history.
Visible named journal results MUST also invalidate the provider's cached input prefix when
the current workspace has no matching replay record, including a new turn whose workspace
metadata has not arrived yet. Replay sends those standalone results unchanged without
`previous_response_id`; it does not borrow records or filesystem authority from another
workspace. Matching records still restore the exact original call and paired result. A confirmed
provider prefix ending with that call allows the WebSocket reconciler to send only
the missing result and new input; restoration alone does not establish cache validity.

Mekugi mode forces `tools.update_plan.enabled=false` and removes `update_plan` declarations from
the request catalog, including nested additional-tool namespaces. The caller's base instructions
remain unchanged; the execution-tool Journal sections supply additive guidance. Passthrough retains the
stock tool catalog and prompt.

### Runtime authoring

Plans are pending tasks. Task transitions and established facts are recorded as they
happen, attached to the next useful tool call rather than a standalone request.
Constraints belong in context nodes. Ongoing narration is not a note. Questions and
direct replies remain conversational. Parents record integration decisions, not copies
of child journals. The final answer is a short Outcome or exactly `Done.` when there
is nothing beyond the journal. It does not repeat progress, validation, or remaining work.

Code Mode lowers the helper to authenticated `mjournal` through stock `exec_command`;
it neither runs the surrounding program nor owns the host lifecycle. Read transport
uses bounded flat pages, authorizing each page against the full snapshot revision,
then assembles the tree inside the helper. A concurrent revision fails rather than
mixing snapshots. Pagination fields are internal, not model-facing. Read and publication
failures throw; credentials and authored source stay out of sanitized metrics.

### Delivery failures

Required terminal journal messages must have retained replay provenance before
natural completion can succeed. If required retention fails,
the response fails instead of silently completing without the flush or child summary.
Unacknowledged revisions remain available for a later flush.

A successful provider final message is captured as the journal answer and triggers a flush.
Ordinary token-usage buffering remains bounded and releases provider events unchanged on
failure or overflow.

### Acceptance

1. Native and Code Mode mutations reach the same atomic owner; host input remains
   exact apart from removing the optional journal field. Retained replay is idempotent.
2. Stable paths, nested planning, omission/drop rules, reasons and end-of-batch parent
   validation survive restart and independent forks.
3. Reads expose timestamps and children, respect subtree/depth, and require proven
   durable ancestry. Bounded transport reassembles complete trees without mixed revisions.
4. Empty Outcomes create no answer node or duplicate raw final. Main cards contain
   new events and open tasks. Failed delivery retains cursor windows for retry.
5. Child JSON/SSE results omit echoed questions and opaque aliases, deliver only new
   work after acknowledgement, and include only the corresponding owned evaluations.
6. Instruction projection exposes one v2 API description, keeps stock tool authority,
   removes update_plan, and directs mutations onto useful calls and finals to Outcome.
