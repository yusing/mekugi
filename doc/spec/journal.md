# Journal

## Native journal presentation

V2 journals have a Journal pane alongside Diff and Activity, selected by `Ctrl-B 5`;
composer letters never open it. The pane title counts the journal's own tasks by state
in their state colors, dropping state names before its pane keys when narrow; an
untitled short pane shows the counts as its first row when at least four rows fit.
Open tasks precede finished tasks; unlinked Agents groups follow owned work, and
Outcomes follow their sibling work. Subtrees start expanded; when they exceed the
pane height, the least recently updated subtrees collapse first, considering their
descendants too. More height expands them again. Explicit disclosure choices stay
fixed; manual expansions are not hidden by automatic ancestor collapse. A disclosure
marker shows each subtree, with its descendant count when collapsed. The title carries
the pane keys: Space expands or collapses the selected subtree and d opens the
slice's full Markdown details in one segmented dialog.
`Ctrl-E` uses the same default, expanded events, expanded all cycle as the transcript.
Both expanded choices open all Journal subtrees; default restores automatic fitting.
The choice applies to new subtrees too; individual disclosure controls
remain available. These choices are transient presentation state.
Each top-level slice includes all its descendants, even collapsed rows. Opening an
item uses that same dialog, scrolls to its segment and briefly flashes it; opening
the slice itself starts at the top without a flash. The status bar lists only the
other keys: arrows or j/k select, Enter opens the
row (a mounted agent's Activity, otherwise details), and c copies the selected path.
The right and left arrows also expand and collapse, and left on a leaf selects its parent.
A click on a disclosure marker toggles its subtree; a click elsewhere on a row opens it.
The wheel scrolls without moving the selection. Only the row under the pointer, or the
keyboard selection of the focused pane after navigation keys, is highlighted; no row
stays highlighted once the pointer leaves the pane. The selection follows its node as
rows change. Workspace and unscoped journals remain separate. Only when both hold a
journal does the title name the presented one and offer n to switch; unscoped holds
app-server requests without workspace metadata. Only notes from the presented namespace
are suppressed in Main or acknowledged via the pane. Router stamps display in local time
at the row's right edge when the pane is wide enough.
Only tasks show state; blocked tasks retain their reason and dropped tasks are dimmed.
Authored item titles and reason previews use the shared inline Markdown painter,
including bold text, code spans and links. Pane rows remain one visual row and
clip after painting; full bodies use the shared Markdown renderer in details.
A superseded node is dimmed with a `superseded by PATH` suffix, in the pane and in
journal event rows; its pane subtree starts collapsed. Dimmed pane rows retain
their link targets while foreground colors and emphasis yield to dim presentation.
A plan strip above the composer shows current owned work: working tasks precede
blocked tasks, then pending tasks. Within a state, the most recently updated task
wins. Adding pending work cannot displace working work. The strip uses the same
current state as the pane, with a short blocker preview, right-aligned owned-task
progress and the pane key, which yield to the title when narrow. It remains while
any owned task is open; during a Main turn after the last task finishes it shows
the most recently updated finished task. An idle all-finished journal has no strip.
When Main is idle and an owned blocked task pauses continuation, the strip shows
`Continuation paused` with the most recently updated blocked task's path and reason
instead of unrelated working work. The full task remains available in the Journal pane.

Child journals mount read-only under the parent's linked task, or under an Agents
group when unlinked. Mount headings name the agent once and distinguish its host
lifecycle with running/finished labels from parent-owned task state. Expanded
bindings omit the redundant agent suffix; collapsed bindings retain it. Descendant
pane rows use local ordinals, while detail headings retain the agent's display name
and local ordinal path instead of opaque mounted addresses. Copy, selection, disclosure,
and reads retain the full durable address. Enter on a mount opens that child's Activity; an agent absent
from the roster, including an unresolved mount, yields a notice. The parent task
remains parent-owned; the mounted root observes the host lifecycle. Unknown lifecycle
has no state label. Child changes refresh mounted views without copying notes into
the parent's record or acknowledging child events on behalf of the parent. The plan
strip counts only the presented journal's own tasks.

Native Main and Activity use the same rich journal event and work-report rendering,
while retaining independent pane navigation and the shared detail/copy dialog.
Native Main receives flat event rows after persistence, without a journal heading
or nested rail. Task rows retain their colored state glyph, dim path, title and
change; notes retain their diamond-led content. Wrapped rows align under the content.
A row's time shows only where it differs from the row above and fits the width.
A row with a body shows its details inline until its journal group scrolls above
the viewport, then retains a compact row. A click opens its full details.
Notes are rows only while the Journal pane is hidden; blocked task rows remain
visible. Successful terminal delivery publishes a separate work-report card only
when non-answer events remain unacknowledged
or mounted-journal diagnostics need to be shown. Captured answers and unchanged open
tasks alone do not produce a card. This turn shows the final change per owned path,
omitting add/remove transients and descendants of removed subtrees. Remaining adds
only unchanged open owned tasks; changed open tasks appear once, but still count
toward the title's open total. Captured answers are excluded. Work-report cards
start expanded and use their compact preview after scrolling above the viewport.
Collapsed node rows use at most two visual rows, with short blocker explanations.
Compact previews show the newest three changed notes in chronological order, with an
older-note disclosure when needed. Changed context remains visible independently.
Inline Markdown remains styled in event rows, compact card previews and plan
strips. Wrapping and preview limits apply to painted text, not markup delimiters.
Once no owned tasks remain open, an out-of-view preview exceeding eight rendered
body rows collapses to a disclosure with task and note counts. Wrapping at the
current width counts toward that budget, not task count; smaller cards stay readable inline.
Mount diagnostics prevent this automatic collapse.
A click opens the shared dialog with full reasons and bodies, including older notes;
copied reports also retain that detail. Time and counts appear on the detail row.
Current-node evidence remains available through reads; the router retains the
append-only event history separately. Cards have response-specific
identity. Substantive answers and their reply context use the ordinary conversation
renderer, independently of the card. The Journal pane restores its
current tree from durable storage independently of provider requests.

Retained v1 Main milestones are diamond-led content without a separate journal
heading or rail; grouped answers retain their presentation. Child journal previews
use the normal agent group described in [activity presentation](activity_display.md),
while retaining the full source for details and copying.
The authenticated mutation path publishes native Main milestones after persistence,
including those without `report_now`, even without an open provider response.
The frontend applies pending milestones before later host events and preserves
their transcript position at terminal flush. Captured answers remain terminal-only, including answers without a linked question.
Native deletion retracts an already applied milestone even without `report_now`.
Journal MCP calls retain their ordinary host tool identity and results. Journal
shell commands use the same command display, history, and replay paths as other
commands. Enqueueing is not acknowledgement: successful
UI output acknowledges exact revisions through the journal owner. Terminal
records become eligible only after successful downstream response completion.
Failed presentation leaves unacknowledged records durable and pending.
Receipt persistence must not block scrolling, input, or rendering, including
when a completed batch publishes many flat tasks or storage is busy.

The substantive provider final remains in Codex's response and displays once on
the ordinary answer path; durable capture does not hide or reframe it. Only an
empty Outcome replaced by a meaningful work report is hidden, by exact provider
item ID. Without a report, even an empty Outcome stays on the ordinary path.
Capture-capacity fallback stays raw. Child native
completion payloads, nonattached inline delivery and legacy exact-ID replay
provenance are unchanged. Native sinks are scoped by workspace and stable Main
thread; missing-workspace records are never rebased to the app-server cwd.

## Cross-agent reads and binding

Task `add` accepts optional `agent` to create and bind in one atomic operation;
`set` retains `agent` to bind a direct child's canonical path to one owned task.
The binding cannot be changed or duplicated. It can precede the child's first
request; until durable ancestry proves the child, only an unresolved mount appears.
Only complete, nonconflicting ancestry in the selected workspace permits content
to appear. Read and list agent selectors accept canonical paths or paths with the
`/root/` prefix omitted; task bindings still require canonical paths.
Reading an ancestor does not reveal the caller's siblings. Ambiguous
agent identity rejects the combined read rather than selecting a journal.

Owned paths remain ordinal. Combined reads reserve `@agents` for the unlinked
group and `@<escaped-thread-id>` for mounted roots; descendants retain their local
ordinal suffixes. These are stable JSON Pointer view addresses accepted by `read`,
not mutation targets. `read agent` addresses the child's local paths. Combined
views are bounded to 8,192 nodes; larger views require a narrower agent read.

The router records a child's accepted requests as working. Native app-server
turn completion supplies done or blocked lifecycle evidence. Ordinary stock-CLI
consumers use Codex's structured child-result trace, matched to the durable child,
parent, canonical agent path, and current turn. Journal reads and mutations refresh
this evidence so a delivered child result can be integrated without a follow-up.
Observed host starts establish follow-up boundaries even before a provider request;
a proven failure before that request remains blocked, including after a router restart.
Unmatched success keeps integration open; an unmatched failure cannot replace a live turn. Unrelated, missing,
ambiguous, or corrupt evidence cannot settle a live child.
Each new host turn ID
increments the child's durable turn count, including its initial assignment; mounted
reads and journal recovery show it, so delegation limits survive compaction. Provider response
completion alone is not child completion. Frontends without host completion
evidence retain the last observed state. A parent cannot become done while any
required mounted host lifecycle remains open, including nested or unresolved mounts.
A failed or interrupted delegate under an owned dropped assignment no longer gates
parent completion. Its recorded lifecycle remains visible; live and unresolved mounts
still gate completion, including nested delegates under the abandoned assignment. Child-authored
task states do not gate parent completion: they remain unchanged and visible at the
completion handoff, where the parent owns the integration decision. The check applies
to tasks a batch completes or rebinds; a child resumed under an already done task
does not block the parent's other writes. Reading and restarting do not revive
child processes.

Forks copy task states and history, but not live agent bindings to children owned
by the source parent. Copied events retain the historical assignment. New bindings
must refer to the fork's own children. An unavailable or oversized mount view does
not suppress a parent's successful answer; a separate terminal card reports
mounted journals unavailable. Explicit combined reads still fail.

## Evidence-backed recovery

V2 recovery is a deterministic current-work handoff, not a session-wide index of
completed work. All open tasks retain their paths, states, titles, bindings and
reasons, with bounded body excerpts. Working tasks precede pending and blocked
tasks. Context nodes are indexed by path and title; those in the resume task's
open branch also include bounded body excerpts. A shared 12 KiB budget, limited
to what the reserved half leaves after mandatory content, gives up to 2 KiB of
body to the resume task and its open ancestors, then to current root context
newest first, so accepted scope and the next step's inputs survive reset without
a read. Root context that does not fit stays indexed, and one line states that its
detail is available by path. A listed superseded node shows its replacement
pointer without its old body, so recovery does not present a replaced decision
as current.

Closed tasks appear only to orient open descendants or show a finished agent
whose directly bound parent integration remains open. Only that open direct
integration retains the finished child's latest answer excerpt. Inline notes
come from the resume task's open branch, with at most three notes within a 2 KiB
budget; an omitted count identifies additional current-work facts. Full context,
closed task bodies and other history remain durable and available through
explicit reads. Bounded excerpts point back to `read`, never claim to be complete.

One shared hint explains how to read more: `tools.mcp__mekugi__journal_read({p:"PATH",depth:1})`
retrieves a listed node with its immediate children.
`tools.mcp__mekugi__journal_read({view:"outline"})` finds own older paths, and
`tools.mcp__mekugi__journal_read({depth:1})` discovers older agents.
Each child's content appears once under `Agent NAME [state]`, using its readable
name without `/root/`, including nested names such as `child/worker`. Rows in that
group use child-local paths. To read a child row, use
`tools.mcp__mekugi__journal_read({agent:"NAME",p:"PATH",view:"own"})` with its heading name and
local path. Own-path reads remain unchanged, and combined read addresses remain
supported; recovery keeps agent UUID mount addresses internal.
Retained changes list only their ranges, pointing to
`mchanges ID[..ID] --summary` for file statistics.

When an open task's bound child has an observed done lifecycle, the parent row
states child completion and open integration. Retained result paths appear in the
child's group. Neither child prose nor a done lifecycle completes the parent's
integration.
Pre-execution observations without a retained outcome are listed separately, with
at most eight entries and an omitted count. These are not claims that processes
are still running. Recovery restores neither continuation handles nor JavaScript
store values; the host remains authoritative for live execution state.

Journal context retains constraints and decisions needed after reset. Corrections update
the owning node; supersession preserves replaced facts as history. Recovery cannot reconstruct
unrecorded decisions or replace missing document contents with a claim that a
read occurred.

Changes and failed commands captured after the latest journal event appear in a
separate section. It includes change ranges, aggregated numstat, failed commands,
observed exit status, the end of bounded host output and durable `mread`
references. Unknown exit status stays unknown. At most eight failed commands are
listed, newest kept within the remaining capacity, with earlier ones counted. A
failed command whose output was not retained is listed as such. Unordered records
from before failure capture ordering are covered by a known journal boundary. The
resume direction uses the same local-work eligibility as automatic continuation.
An owned blocked task names the paused path and reason instead of directing
continuation of unrelated work. User-stopped or delegated work does not become a
resume target. When no local task is runnable, recovery offers evidence reads
without directing a new turn. It offers evidence reads on demand,
rather than requiring every retained change or output to be read before work or
treating an investigation's hypothesis as established. Capturing failed output
does not replace or alter the host's result. An unreadable evidence boundary does
not block journal writes; the next summary treats the boundary as unknown.

Generated recovery headings are plain text and metadata uses ASCII punctuation.
Change labels have no bold markup, and execution observations omit full call IDs.
Authored bodies and retained output remain verbatim within their excerpt bounds;
presentation does not change durable execution correlation.

The summary is at most 64 KiB. Context paths and open tasks must fit its
reserved half; otherwise rendering fails rather than omitting a context path or
an open task. Corrupt failure evidence also fails rendering. The native recovery
hook
treats failure as advisory; router-side reset failure stops `auto` without a
provider request. `slice` retains provider fallback.
Neither recovery path acknowledges events or replays effects.

Recovery also names the selected workspace when one is known. The path comes
from retained routing identity, not process cwd. Recovery neither queries Git
nor includes Git status or recent commits.

## Router-answered compaction

`--journal-compaction=auto` is the default. Manual `/compact` and context-full
requests use journal context reset, including child threads, without provider
compaction. `off` opts out and restores provider compaction. `slice` resets only at
planned slice boundaries; ordinary compactions still go to the provider.
Passthrough implies `off` unless explicitly configured; explicit `auto` or `slice`
is rejected.

Reset requires one selected workspace, an unambiguous requesting thread and
durable journal or executing-thread-owned change evidence. Unavailable or
ambiguous identity, evidence or storage, rendering failures and persistence
failures stop `auto` with a visible error and no provider request. There is no
provider fallback in `auto`. Invalid compaction protocol requests retain the
existing validation errors. A summary is never empty. A downstream delivery
failure is reported, not retried as a second provider request. In `slice`, local
reset failures retain provider fallback with the local cause visible; distinct
causes remain separately visible. Intentional slice-mode bypasses are silent.

Native local reset may omit workspace metadata. Only a unique workspace already
selected in that requesting thread's durable journal/execution records can
substitute for it. Conflicting historical workspaces stop `auto`; router cwd and
another thread's records never supply a directory.

Codex retains execution and history authority. The router uses the native
compaction response shape through the same streamed Responses delivery path:
an assistant summary for local text compaction, or one compaction item for
Responses compaction V2. A router-owned V2 item refers to the exact retained
journal summary. Later requests resolve that item locally before provider
forwarding; its reference never reaches the provider. Resolution uses existing
durable scope and recovery ownership, including permitted inherited history.
When a consuming request omits workspace metadata, an authorized reference uses
its source thread's unique workspace from retained journal/execution records.
This applies to ordinary, execution-free and prewarm requests, including resume
with retained ancestry. It does not supply a filesystem execution directory.
Declared unusable or different workspaces remain errors; absent or ambiguous
source evidence fails before inference. Missing or unauthorized recovery fails
before inference. Provider-owned
encrypted compaction items remain unchanged. The summary and its thread/workspace
and response identity persist before exposure. No model request is made for the
local reset, and no model usage is fabricated. Metrics distinguish router answers from
provider answers and report zero provider attempts/tokens for router answers.
Summary bytes and evidence counts are measurements, never savings estimates.

In `auto`, `/compact`, live progress, queued commands, cancellation and
continuation text describe context reset. Generic completion and restored history
use “Context reset”, including older provider or ambiguous records. This label
alone claims neither a journal answer nor zero provider tokens.
Only exact retained thread, turn and item provenance in a router-answer receipt,
or a standalone slice-reset event, identifies “Context reset from journal” and
its zero-provider-token claim. These rows appear once without duplicate native
progress commentary. Receipts retain answered-item provenance across later resets.
Buffered manual standalone notifications bind their retained answer to the unique
host item; ordinary automatic compactions can share a turn and cannot be identified
from buffered UI observations alone. For paginated sessions, the verified
host-selected rollout associates the completed compaction item with its original
response. Missing, older, or ambiguous rollout evidence keeps the generic label.
In `off` and `slice`, ordinary provider rows
keep “Context compacted”.

The journal's “Context reset from journal” row opens the shared scrollable dialog
with the exact model-visible recovery message retained for that response. The
message remains associated with its original thread, turn and item across later
resets and resume; it is never regenerated from newer journal state. Slice-reset
notes identify their standalone turn and disclose recovery only when that turn has
one retained answer receipt. Older
receipts or unreadable retained messages open an explicit unavailable notice.
Provider compaction rows do not acquire a journal-recovery disclosure.

The recovery hook suppresses duplicate injection only when Codex's latest
compacted transcript record identifies that persisted response. Missing or
unreadable transcript evidence, older host records without response IDs, and
provider-written summaries keep normal hook recovery. Failed or interrupted local
responses cannot suppress recovery for another compaction.

### Retained guidance

A router-answered V2 reset also retains guidance the thread loaded before it, so
the agent does not reload instructions the summary cannot carry. Guidance is a
file read whose path the request's instruction text names. Instruction text is the base
instructions, developer messages and AGENTS.md instruction messages; a name
matches the absolute, `~/`, `$HOME/` or workspace-relative path as a whole token.
Reads come from literal, simple top-level `cat` and `mcat` commands in
completed `exec_command` calls and exec cells of the history the compaction
request carries, including guidance retained by an earlier reset. Other reads
and dynamic, piped, redirected or nested commands are not guidance. File snapshots require an instruction-named
resolved path and a bounded regular file; process, system and device files are
unavailable. A symlink cannot admit a different unnamed source.

At reset, the router reads each source once from the selected workspace. Ranged
or bounded reads identify the source; retained snapshots contain its whole text.
Content reflects reset time, so a read that was truncated or batched with other
output is complete. Skills are not snapshotted; the agent reloads them through
stock host execution when needed. Collection starts no subprocess.
Sources fill a 48 KiB budget and render newest first; the budget
includes source labels and bounded omission and failure notices. The rest are
listed by read command while space permits, with a count for further notices.
Sources that cannot be read are listed with their cause and never stop the reset.
Collection has one five-second deadline and stops snapshotting when no body
budget remains. Ineligible slice resets collect no guidance.

The snapshot persists with the recovery record. Later requests expand it after
the recovery message as one completed call to the source's tool with its output,
so the content keeps the authority of a tool result rather than becoming
instructions or summary. The recovery message states that the next tool result
holds retained guidance. Text-form compaction and provider compaction retain no
guidance.

### Context-pressure reminder

When the latest host-reported context use reaches at least 70% of the model context
window, the next model request's journal tool guidance reminds the agent to split
work into slices. The threshold
uses the last context snapshot and model window, not cumulative token usage.
Successful host context-compaction completion clears the reminder. Native resume
restores it from host-selected context facts; missing or unknown context does not
invent a threshold crossing. The reminder does not itself reset context or force a
slice boundary.

### Journal continuation

After a successful native Main turn, unfinished local journal tasks continue
automatically, including when the agent stopped after answering a follow-up
without changing the plan. Working tasks take priority over pending tasks.
Ordinary continuation keeps the current context, regardless of compaction mode;
only the slice boundary below can request a reset.

The native frontend offers the same cancellable three-second countdown for
ordinary and slice continuation. A blocked task pauses journal continuation;
the idle plan strip and recovery identify the blocking owned task and its reason.
Pending user questions, queued input, failed/interrupted turns, and explicit
user interruption do not trigger it. User interruption wins even when the host
races to report successful completion. Finished/dropped tasks and work owned by
delegates cannot drive a new Main turn. A parent waiting on unfinished children
does not independently drive continuation. Once no runnable local task remains,
automatic continuation stops. Plain conversational questions require the agent
to record an input-needed task as blocked; answer prose is not a state signal.

Explicit user interruption or Escape during the countdown pauses the current
unfinished tasks durably. Later questions, notes, restart, and forks retain that
pause; newly authorized unrelated tasks can proceed without reviving stopped work.
Resuming a stopped task is represented by explicitly setting it to `working` in a
later turn, which also reactivates its stopped descendants. Mutations from the
interrupted turn cannot reactivate it. Queued input and pending questions cancel
the countdown without marking the underlying work user-stopped.

Each continuation is bound to its workspace, thread and completed turn, and is
revalidated against the durable journal before dispatch. A restored pending
countdown can continue still-runnable work; dispatched requests never replay.
Forks and side threads do not inherit a live continuation. Switching models or
viewing another agent does not transfer continuation ownership.

A plan marked `reset: "slice"` can continue between successful native Main turns.
When a task becomes done during the completed turn and has an eligible pending sibling,
the frontend offers a three-second countdown to that sibling, including a sibling
with open subtasks. Finished, dropped, or delegated ancestors exclude a target
from both selection and dispatch. An excluded sibling does not hide other local
work or permit premature finish. Escape
or queued user input cancels it. Failed and interrupted turns do not continue;
child completion cannot drive a Main reset.
The [headless frontend](router.md#headless-app-server-frontend) uses the same policy
with no countdown delay and emits reset events instead of rendering a strip.

With `slice` or `auto`, the frontend asks Codex to compact before continuing
between top-level slices. Between subslices, such as `/3/1` and `/3/2`, it
continues with the current context when the latest host-reported context use is
below 150,000 tokens. At or above that threshold, or when context use is unknown,
the subslice transition also requests a reset. This uses the current thread's
latest context snapshot, not cumulative usage or another thread's context.
Manual and context-full compactions keep their configured behavior.
The UI describes that journal-driven operation as a context reset,
including its progress, completion, and restored event. Ordinary provider
compactions in `slice` retain their compaction wording; `auto` uses context-reset
wording for all compaction-related presentation.
Only a router-answered, successfully completed compaction permits the automatic
continuation. A failed reset leaves the plan available for manual continuation.
In `slice`, provider fallback also leaves continuation manual.
With `off`, the same countdown continues the plan without resetting context.
This keeps the continuing-context comparison separate from compaction policy.

Interrupted or uncertain RPC outcomes are not retried; the frontend reports that
manual continuation is needed. That retained evidence never blocks a
later slice, and an uncertain compaction dispatch cannot make a later manual
compaction count as the reset: only an intent armed for the latest ordinary turn
is answered. Escape cancels until compaction or continuation is dispatched. A
still-pending countdown for the latest turn whose next slice is still pending can
recover on resume; resume reports and discards any other intent. A fork copies
the plan without its live continuation intent.

## REQ-JOURNAL-001 — Durable work journals

Mekugi owns one durable journal per stable thread. Passthrough is unchanged.
A journal has a materialized task tree, append-only timestamped events, and delivery
cursors. Nodes have stable ordinal JSON Pointer paths such as `/1/2`; sibling keys
are never renumbered or reused after successful removal. Titles are display text,
not addresses. Notes and context nodes have no state. Only tasks have `pending`,
`working`, `done`, `blocked`, or `dropped` state. Answers remain router-owned.

Nodes contain a one-line nonblank Markdown title, optional Markdown body, author, creation
and update stamps (`seq`, RFC 3339 `at`), children, and an optional `superseded_by`
path naming the node that replaced them. Tasks can additionally contain
a reason, their first working stamp and their latest completion/drop stamp.
Reopening a finished task clears its current finished stamp without rewriting history.
Task elapsed time accumulates only while the task is working and its thread is active.
Blocked, pending, idle, interrupted and finished intervals do not accrue time;
reopening or resuming retains the stopped total. Persisted `work_timer` values
contain the known accumulated nanoseconds and, for a live observation, its active
anchor. Historical events contain stopped totals, not running anchors. Forks and
fresh router processes retain checkpointed work but do not revive inherited clocks.
Older records without active-time evidence omit elapsed time rather than presenting
wall time as work time.
The tree has at most 512 nodes; combined title, body, reason and question content is
limited to 16 KiB per node. Events are bounded at 4,096 and an 8 MiB content-and-metadata budget. At capacity, adjacent edits
without state transitions may collapse. Notes and state transitions
are retained; irreducible capacity failures reject atomically instead of losing history.
The managed record byte limit applies independently. Historical v1 timestamps are
unknown rather than fabricated.

Wrapped Mekugi sessions expose `journal_read` through an invocation-local stdio MCP server.
Its closed input schema accepts `p`, `agent`, `depth`, and `view`; results contain
the selected tree in `structuredContent.nodes` and the standard MCP text content.
Codex supplies caller identity. The router uses that thread's unique retained
journal scope, including the unscoped namespace when workspace metadata is absent,
rejects missing or ambiguous identity, and applies the same
durable ancestry rules as other journal reads. Registration preserves user
configuration and passthrough. The Codex-started process connects to the router
through a private local socket. `journal_mutate {mutations: [...]}` applies an atomic
batch through the same owner. Its closed schema constrains each operation. Accepted
results contain `structuredContent.paths`; rejected batches return an MCP error
without changing journal state. Host metadata supplies mutation receipts and binds
`finish` to its originating item and turn. Repeated calls with the same identity
and payload return the retained paths without applying effects again.
Distinct nested calls in one `exec` cell retain separate mutation receipts,
including when more than one batch ends with `finish`. Their completion markers
refer to the same enclosing host item; each batch still validates its candidate journal.

Each mutation is `plan`, `add`, `set`, `log`, or `remove`. JavaScript `exec` calls the nested
MCP tools through Codex. Stock tool schemas and arguments remain unchanged. Direct awaits and calls joined in an awaited `Promise.all`
or `Promise.allSettled` preserve normal JavaScript concurrency. Operations are:

- `plan {under?, tasks, reset?}`: each string adds a pending task; objects contain
  `p?`, `title`, `state?`, `body?`, `reason?`, and nested `tasks?`. Existing paths must
  name distinct direct task children. Unlisted pending children become dropped with
  an explanation; working and final children remain. A plan returns created paths
  in preorder. `reset: "slice"` records a slice boundary policy; driving context
  resets is a separate frontend capability.
- `add {under?, kind?, title, body?, state?, reason?, agent?, before?}`: adds one node, with
  default kind note and default task state pending. Only tasks accept state, reason
  and agent. Creation and binding share validation and persistence. `before` changes
  display order, not stable keys, and must name a sibling. Only tasks accept children. Ordinals are
  shared by every kind, so a non-task `under` rejects naming that node's kind, title
  and containing task, rather than moving a plan to another parent.
- `set {p, title?, body?, state?, reason?, agent?, superseded_by?}`: updates writable
  fields. Blocked and dropped tasks require a reason. A final task reopens only with
  working. `superseded_by` names an existing node outside the target's subtree; an
  empty string clears it. Only context nodes, notes, and done or dropped tasks can be
  superseded: an open task is replaced by dropping it, and a superseded subtree has
  no open tasks. Batch validation rejects a pointer left dangling by removal, so a
  batch that removes a replacement also retargets or clears its pointers. It also
  rejects a pointer chain, including one continued by a superseded ancestor, that
  leads back to the node. Combined views prefix a mounted node's pointer like its
  path; rows display both by the child's local path.
- `log {p?, text}`: adds a timestamped note under `p`, or else under the working
  leaf task. Several working leaves select their deepest common task, or root.
  A note or context `p` selects its containing task. Neither case attributes the
  fact to an unrelated task.
- `remove {p}`: removes a mistaken subtree, retaining its removal event.
- `finish`: a control marker with no operands or node, allowed only last in a
  MCP mutation batch.
  The preceding mutations and invocation-scoped receipt persist atomically; the marker
  contributes no returned path. A lone MCP marker returns an empty path array. A rejected batch
  records no finish receipt. The candidate journal must have no ordinary runnable
  local work, or a completed slice boundary with a pending sibling. Otherwise the
  entire batch is rejected with the runnable task path, so the agent can correct
  it in the same host turn.
  Blocked reports and user-stopped work remain eligible.
  This check applies to the caller's own work, including child journals; it neither
  completes delegated work nor changes the host-result completion checks.
  It does not stop or execute host work.
- `read {p?, agent?, depth?, view?}`: returns the selected subtree. Depth zero omits
  child nodes. Omitted agent selects the caller; explicit agents require proven
  ancestry. The default `combined` view includes read-only mounted agents. `own`
  reads only the selected journal, without mounting descendants. `tasks` reads
  only its own tasks and omits their bodies and questions for compact path/state
  recovery. `view: "tasks", depth: 0` returns only root tasks. `outline` reads
  every node kind of the selected journal without bodies or questions, so a note
  or context node to amend can be found without reading full content. Own, task
  and outline reads without an explicit agent require only the caller's record, so
  unavailable descendant journals cannot prevent local ID recovery.

Structured mutation inputs are operation-specific closed schemas. Codex lists the
journal MCP tools as deferred and omits their declarations from `exec`, so journal
guidance renders TypeScript declarations from the same MCP input schemas, including
read field descriptions. The schemas reject unsupported fields
and restrict creation-time agent binding to explicit task creation. These are input
shapes, not a new JavaScript execution or static-checking authority; callers can
type-check against the declarations before submitting. Runtime validation still
checks identity, task kind, uniqueness and immutable binding. Planned task objects
do not accept agent; bind an existing planned task with `set`.

Mutation batches validate at the end. A done task cannot retain open owned descendant
tasks or required open mounted host lifecycles. Rejection lists those paths and rolls back all
nodes, ordinals and events. Invalid bindings, including duplicate child mounts,
likewise leave the entire batch unchanged
and do not retain a success receipt.
A rejected operation in a batch names its one-based position and op. Undecodable
payloads name the offending member.
MCP mutation batches return their affected paths in order, including single-operation
batches. The caller can display those paths without another read.
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
the per-item limit or the journal is full, the provider answer remains visible unchanged.
In these capacity cases, unflushed milestones remain pending;
the router does not claim a journal flush or lose the answer from subsequent provider history.
Inference is request-local;
concurrent threads and branches do not share its source. Journal mutation schemas do not
expose an answer flag, and ordinary milestone edits preserve any attached question. Previously
retained answer-marked and direct finish calls remain replayable but are not offered to new model turns.

Reads authorize and return one locked durable snapshot. Unknown, ambiguous or
conflicted ancestry fails closed, including after a router restart with only the
requesting thread observed. Agent names alone do not authorize access.

The final useful host invocation may request completion through the finish marker.
On Codex's subsequent continuation, matching successful host results and the retained
thread/turn/call receipt select a local completed response and journal delivery without
forwarding that request to the provider. Later user input, unrelated calls, missing
receipts, failed results and unfinished work cannot select this path. Matching host
continuations remain host-owned. Completion additionally requires the completed native
trace and successful nested tool outcomes, not printed success prose. Restart can
recover a receipt for the same thread and turn; forks and later turns cannot reuse it.
When results require further provider interpretation or the user needs a substantive
answer, the agent instead finishes naturally after inspecting those results.
On a successful completed response with no client-dispatched calls, the router captures the final
answer for durable recovery and delivers Main's substantive provider final unchanged.
Pending work-report events are delivered separately in that same response, without
another provider request. Child completion retains its journal-result payload. A journal-only operation without
a final answer continues so its result remains inspectable. Mixed client calls remain
host-dispatched and prevent terminal delivery. Journal operation error results continue for correction.
Invalid or rejected mutations in a dedicated journal call return an `ok: false` tool result
for correction and prevent its primary operation. Batched fields on host-dispatched tools
still fail translation under the atomic field contract.
Failed, incomplete, or interrupted responses never flush. Completion is response-local:
replay, resume, and forks do not finish a new turn.

Native resume and offline session replay restore retained journal revisions independently
of live delivery cursors. Historical completion cards require an acknowledged report
window and a recorded successful host turn with usable timing; interrupted, failed,
or unfinished turns never gain a completion card. Cards use the revisions and task
states at that boundary, not the latest tree. Restoration is presentation-only:
it does not acknowledge revisions, flush a new turn, or replay effects.

For v2-authored journals without a native frontend, successful Main completion
emits a separate Markdown work report only for non-answer events after `flushSeq`
that have not already been acknowledged, or mounted-journal diagnostics. It uses
the same per-path final changes, transient exclusions, short reasons and newest-three
note preview selection as native cards. Remaining adds unchanged open owned tasks,
without repeating changed open tasks. Full evidence remains available through journal
reads. Neither captured answers nor unchanged open tasks alone trigger a report.
Answers and their questions remain
stored but are not echoed in the report. A blank final or a case-insensitive `done`
with an optional period is an empty Outcome: no answer node is created for a tree
journal. A meaningful report can replace a snapshot-only raw item or hide it in the
UI; streamed provider events remain unchanged and stay in the terminal snapshot.
Otherwise the original final stays visible. The final message is
still required. Retained v1 authoring without a turn card keeps such a final as its answer.

Live v2 fallback uses the `Journal` heading without an author-path label and renders
events after `liveSeq`, clipping a row that no single update
can hold; journal reads retain it whole. Successful downstream delivery advances
the corresponding cursor; failure preserves its window. Main reports precede the
substantive ordinary final in JSON output and terminal snapshots. Streaming answers are delivered
as they arrive. Inline reports are prepared before the first answer event, with
acknowledgement deferred until successful terminal delivery. Journal changes after
that preparation remain pending rather than adding commentary after the answer. Failed,
incomplete and interrupted responses never terminal-flush.
Retained v1 authoring keeps its legacy presentation and delivery receipts during replay.
A child completes without flushing and emits a result containing
revisions newer than its durable `resultSeq` cursor, followed by a Remaining section
for unchanged open owned tasks. Changed open tasks appear only in the revision delta.
Completion does not mark these tasks done or discard blocked or unfinished work.
The result has no author heading: Codex names the child on its completion notification and inter-agent
result. Activity recognizes the result by its closing change report, and still
recognizes retained results that lead with `Journal result`, painting the report
as per-file rows with the totals in the answer title. Agent recipients see neither
the echoed assignment nor opaque item IDs. Notes are Markdown bullets and answers
are standalone Markdown; stored questions and the complete journal remain available
through reads, Activity, replay and forks. An empty delta with no open tasks says
`No new journal entries.`
The cursor advances only after successful downstream terminal response delivery.
Failure leaves the previous window available for retry. Result acknowledgement
does not consume Main live or terminal delivery state.

The child result appends retained change ranges, file statistics and capture
diagnostics for this result's executing-thread-owned evaluations, including
recoveries in another stream. Capture gaps are not file statistics or confirmed
edits; their diagnostics remain visible even when no file differences were captured.
Journal revisions and change evaluations share a locked delivery snapshot and
independently monotonic cursors. Ranges identify retained changes while statistics
exclude earlier delivered evaluations and other agents' work. A later recovery of
an earlier change is included as a new evaluation. Follow-up results add one cumulative line counting retained owned evaluations and
changes. Missing or retired evidence is unavailable, never reported as zero. Change ranges preserve stream allocation order,
split at gaps and the reader's range limit. Response delivery retains the prepared
storage namespace and turn lease through the execution lifetime.
The native completion result is the sole audience payload; the router does not send an
additional completion notification or take over host lifecycle or audience routing.
Native Main refreshes mounted child journals after persistence. Frontends consuming
only root Responses items, including Codex `exec --json`, receive proven descendants'
pending live milestones on the next writable root response, before Main's terminal
card or flush. Child-stream delivery does not acknowledge root visibility: a separate
durable child cursor advances only after successful root downstream delivery. Failed
root writes remain retryable after router restart. These publications include live
events, not captured answers or child completion results, and retain exact-ID
provenance so they never enter provider message input. Retained v1 authoring keeps
`report_now` opt-in. No model request, host tool invocation or completion notification
is created to open a root publication channel; while Main is waiting outside a
response, updates remain pending. Main completion never replays descendant results,
including after router restart.
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

The projected guidance explains journal usage, without prescribing development,
delegation, review, document workflow, or personal setup. Tasks track
outcomes that need continuation; short assignments need no plan. A parent task names
the requested outcome, and its separate concrete results are child tasks. Main work updates
go into the journal, attached to useful tool calls. Journal text uses ASD-STE100 and
one topic per item. Titles have no trailing punctuation and prefer a bold action;
bodies hold detail. Notes record new results and blockers. Context retains constraints
and decisions needed after reset; corrections update the owning node.
With `reset:"slice"`, bounded tasks have concrete results and completion checks.
Completed tasks are turn boundaries so the frontend can reset and continue to a pending sibling.
Main work completion uses the finish marker in the final useful execution when its
result can establish completion. It adds neither a standalone finalization tool
call nor a follow-up `Done.` provider acknowledgment. Required result interpretation
is not skipped. A usable deliverable, usage explanation or decision beyond the work
report instead uses a natural final answer. That Outcome does not repeat progress,
validation, review status or remaining work. Requested explanations,
review findings, answers to user questions and necessary questions remain substantive
conversation, without a journal-specific length or format.

Subagent guidance is proportional to the assignment. Brief work needs no task or
plan; tasks track separate steps and name outcomes rather than roles. Useful
interim findings, blockers, decisions and recovery facts may be recorded alongside
useful tool work. The final report appears once, in the journal with a finish
marker when host results establish completion, or as a direct final answer. A
clean review needs one no-defect result, not repeated task and note entries. The
API, mutation validation and automatic child-result evidence remain shared.

Journal access uses the invocation-local MCP server rather than a shell frontend.
Reads return a complete selected tree. A rejected mutation applies nothing and
returns the standard MCP error result, so the program can inspect `isError` and
continue useful work. Unresolved agents name the normalized selector and explain
canonical addressing and local-read recovery. Missing read paths explain how to
recover paths in the same agent and view. Credentials and authored source stay out
of sanitized metrics.

### Delivery failures

Required terminal journal messages must have retained replay provenance before
natural completion can succeed. If required retention fails,
the response fails instead of silently completing without the flush or child summary.
Unacknowledged revisions remain available for a later flush.

A successful provider final message is captured for recovery; Main's ordinary answer
delivery acknowledges only its captured revision, independently of pending work
reports. A report acknowledges its own prepared window after successful delivery.
Provider answer lifecycle events and text deltas pass through immediately and unchanged,
without waiting for usage, answer capture, or terminal delivery. Terminal decoration must not
repeat a streamed item-done event or remove its item from provider history. Failed or
interrupted streams preserve the events already delivered; usage and journal acknowledgement
still require their own terminal evidence.

### Acceptance

1. MCP mutations reach the atomic journal owner; stock tool schemas, input and lifecycle
   events remain exact. Retained replay is idempotent.
2. Stable paths, nested planning, omission/drop rules, reasons and end-of-batch parent
   validation survive restart and independent forks.
3. Reads expose timestamps and children, respect subtree/depth, and require proven
   durable ancestry. MCP results contain complete selected trees from one revision.
4. Main answers remain ordinary messages, including replies while tasks are open.
   Cards require new non-answer events or diagnostics and never repeat the answer.
   Empty Outcomes create no tree answer node. Streamed items stay in provider history;
   snapshot-only items or native presentation can be replaced by a report.
   Failed delivery retains cursor windows for retry.
5. Child JSON/SSE results omit echoed questions and opaque aliases, deliver only new
   work after acknowledgement, and include only the corresponding owned evaluations.
6. Instruction projection exposes one v2 API description, keeps stock tool authority,
   uses invocation-local plan-tool disablement, and directs mutations onto useful calls, work finals to a
   concise Outcome, and ordinary replies to conversational answers.
7. Installed Codex root JSON output receives a child live milestone while the child
   is still working, independently of the child's terminal result. Root visibility
   acknowledgement is durable, scoped to proven descendants in the selected workspace,
   and does not consume child live, result or terminal cursors.
