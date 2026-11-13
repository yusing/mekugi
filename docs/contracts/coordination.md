# Native agent coordination

## Required outcomes

Codex owns root and descendant execution and coordination, with one admitted
tree, work, messages, waits and delivery owner. Mekugi retains one interactive UI and
the stock JavaScript execution interface for Codex.

Parents can wait for any or all current descendants. Addressed messages wake either
mode; queued composer input does not cancel a wait. Compact-and-wait releases an
outcome only after reset settles. Independent side questions neither replace an
assignment nor become ordinary input. Active Mekugi sessions can exchange attributed
messages and side questions and inspect each other's trees without sharing control
authority. Resume remains product-local.

## Ownership

The native agent registry owns membership and canonical task paths. Native
`StateRuntime` transactions retain accepted work, selected waits, deliveries and
execution receipts. The native scheduler, execution-cell broker and task finalizer
own execution capacity, pending calls and completion ordering. Conversation reset, fork and rewind
do not clone live subscriptions or erase logical identity.

Mekugi owns capture/output resources, presentation, human controls and authenticated
active-session routing. Codex children share their invocation's native process and
connection, with thread-scoped endpoints and one request ledger. Host resource callbacks remain bound to the original connection, cancellable
and non-replayed. Native requests must make progress while those
callbacks are outstanding. Subscribing to an agent does not acquire callback ownership.
Human controls retain the ordinary attached-client policy and validate native tree
membership.

## Operations and admission

- `spawn_agent` creates a fresh or full-history child with an assignment,
  role, optional cwd and selected startup sources. It returns confirmed native
  identity and task path. Each task segment is nonempty lowercase ASCII letters,
  digits, underscores or hyphens and is unique under its parent.
- `followup_task` starts idle work, steers the actual running turn or supersedes an
  undispatched wait on an admitted non-root target. Its acknowledgement belongs to
  that operation, not later queued input, and includes current context metrics.
- `send_message` retains attributed input without starting idle work. An addressed
  message can satisfy a pending wait. `btw:true` instead requests an independent
  side answer without primary delivery.
- `interrupt_agent` interrupts non-root work. Waiting work without a running turn
  cancels its wait and settles interrupted. A request acknowledgement is not an
  observed interruption. Human control can also interrupt a root.
- `list_agents` returns admitted tree, work and delivery state, including retained,
  paused and uncertain members. `path_prefix` filters canonical paths; `session_id`
  selects another active session by exact session/thread ID.
- `inspect_agent` reports applied model, effort, tier, context usage/window and
  completed compactions for a live or retained target without starting or restoring
  it. Unavailable observations are null, not inferred defaults or measured zeros.
- Native `new_context` resets context without a summary and reloads selected sources
  before continuing unfinished work.
- `wait_agent` accepts any/all mode, optional timeout and progress interval, and
  optional compact/reload. Registration snapshots unsettled descendant work and
  unread outcomes once. A pending acknowledgement carries no content.

Arguments and task labels never grant authority. Native invocations supply it. Same-tree canonical paths,
unambiguous relative names and exact IDs resolve locally. Only exact active foreign
IDs are eligible for forwarding; a local operation failure never becomes foreign
fallback. Invalid identity, settings or permissions reject before admitting work.
Native admission and provider limits remain authoritative, without a second Go cap.

### Active-session coordination

Foreign operations are message, side question, list and inspect only. The source
owner supplies its immutable root/actor/path/turn/call attribution; the destination
owner authenticates the route and retains delivery before acknowledging it.
Retries reuse the original identity and payload. Aliases, spawn, follow-up,
interruption, reset and cold restoration do not cross this boundary. A foreign
sender or recipient is never enrolled into the local tree.

### File references in assignments and messages

Spawn assignments, follow-ups, messages and side questions acquire explicit
[source references](../../modules/agentguidance/SPEC.md#source-reference-syntax)
under the recipient's workspace and authority before acceptance. Original text and
attribution remain intact. Successful bodies, reader status and individual failure
diagnostics freeze with the operation; confirmation, restored delivery and retries
do not reread paths. Publication failure rejects the operation.

Relative roots use the recipient's cwd; nested references resolve beside their
containing file through depth three. Duplicate roots acquire once. Existing reader
and total acquisition bounds apply, and host-loaded `AGENTS.md` is excluded.
Missing or denied sources remain explicit, not empty successful attachments.
Large bodies use retained resources rather than copies of an entire logical tree.
Explicit preload and composer attachments keep their separate meanings.

## Prepared children and guidance

Preparation captures the actual invoking native settings, provider, model, effort,
tier, instructions, cwd, environments, permissions and role restrictions. Config
files alone are not live authority. Role files and resolved values freeze before
dispatch; resume does not silently acquire changed role files. Roles can reduce
capabilities, not replace provider credentials or widen permissions.

Fresh children apply the selected/default role. Full-history children inherit their
native model and reject explicit model/effort overrides; an explicitly selected role can
still supply its allowed instructions. Native configured defaults and role precedence
remain at the native preparation owner. Inherited ultra uses the selected native
model's ordinary equivalent unless an explicit/default/role effort selects otherwise.
An applied parent tier takes precedence; native normalization and final-model support
filtering decide the effective tier.

Fresh children accept `preload:{sources:[...]}` only with `fork_turns:"none"`.
Empty selections are valid. Before first inference, sources are acquired under the
child's workspace, permissions and approvals. Partial read failures preserve the
other sources. Acquired context survives resume and inherited history without
another read, until normal context replacement retires it. Subsequent reload uses
the thread's retained reset selection.

Native model/role/mode owners assemble effective guidance; Go does not rewrite a
stock collaboration prompt into a replacement tool interface. Custom role text and
effective mode guidance retain their authority. Fresh assignments preload complete
relevant startup guidance and evidence, not policy summaries. The native tool catalog supplies operation descriptions to roots and
delegates without a discovery inference.

## Full-history children and workspace

A full-history fork preserves the complete authorized calling prefix, including
active compaction replacements, attributed messages, commentary, reasoning and tool
items through the invoking execution. Native Codex history/fork facilities produce
it directly, without a Go-built legacy import or transcript replay. Execution,
approval and process handles are not inherited.

Omitted cwd inherits the caller's admitted cwd. Relative cwd resolves there, never
at the process directory. An existing authorized directory applies to instructions,
commands, capture, follow-ups and resume without changing the parent workspace or
granting permissions. Admission establishes shared retained-resource access before
the child can publish execution evidence.

## Logical waits and ordered delivery

Work identity is independent of host turns. Starting new idle work admits a new
identity; steering, logical checkpoints, auxiliary reset and wake continuation retain
it. Accepted intent precedes dispatch and exact confirmation follows it. Uncertain
dispatch is retained for reconciliation, never blindly retried.

A pending wait keeps its original task, turn and tool future. Codex parks the
JavaScript cell at its native pending frontier. It neither yields to the model, ends a host turn or starts
inference while waiting. Quiescent execution can release native capacity without
releasing task ownership. An event resumes the original call with its result, not
a synthetic provider response or replacement turn.

Registration freezes the selection, not delivery. Failure retires that execution's
registration without consuming unread events. A result discarded or transformed
by JavaScript is not delivery; exact inclusion in a confirmed provider attempt
establishes consumption. Duplicate evidence cannot consume events twice.

One pending wait belongs to each caller. Repeated calls in its exact enclosing
execution reuse it; a different execution cannot replace it. Descendants admitted
later do not join its frozen selection. In any mode, one eligible outcome wakes
the caller. In all mode, every selected work item must settle, including failure
and interruption, and pending result delivery must finish. An addressed message
wakes either mode without consuming queued completion events. Already-read history
does not become awaited work. No selected work or pending message means rejection.

Omitted timeout waits indefinitely. Explicit timeout uses native configured minimum
and maximum, retains requested/effective duration and starts at registration. An
elapsed deadline still waits for reset barriers. Explicit human
input, steering or interruption supersedes undispatched waits. Queued composer input,
approval questions and their replies do not. A dispatched or uncertain continuation
retains its reconciliation identity. Confirmed consumption, not injection acknowledgement,
consumes the exact delivered events.

### Reset barriers

Compact-and-wait retains outcomes behind one native reset lifecycle, including when
an outcome is already ready. Codex resets inline while retaining the original call.

Requested resets have no cost veto or preflight acquisition. Accepted reload selection
is retained: omission reuses it, explicit empty clears it, and reset failure does not
roll it back. Sources are acquired after the actual reset, before continuation. Failure preserves wait/events, surfaces a diagnostic and accompanies eventual delivery;
failure alone starts no inference. Auxiliary manual compaction services a pending
native call or provider wait without replacing its task, waiting for a child wake,
finishing work or creating a competing continuation. See
[context recovery](../../modules/agentguidance/SPEC.md#compaction-context).

### Completion and recovery

Successful completion waits for transitive descendants with working, waiting or
pending-start work. One retained continuation names remaining active descendants;
if they settle before dispatch, the original completion proceeds. Paused and
uncertain members do not block completion. Failure/interruption keeps its actual
outcome rather than becoming successful because a wait was registered.

Confirmed overload, internal-server and response-stream failures can retain recovery
waiting while descendants remain active. A new selected terminal outcome or addressed
message resumes through ordinary ordered delivery. Authentication, invalid requests,
interruption and unknown failures remain terminal. Recognized provider usage-limit
and capacity rejection uses native provider-timing waits. Capacity without timing
uses per-work exponential backoff from one to thirty seconds; successful inference
resets it. Usage limits without timing remain paused until explicit input/interruption.
Recovery adds no model switch or second host-retry mechanism.

Ordinary nonempty answers from the successful terminal turn retain their order.
Final delivery keeps sender, recipient and actual outcome; acknowledgement alone
does not mark traffic delivered. Failed delivery remains visible. Wait completion
carries identity/status without duplicating an already-delivered answer.

Successful final answers may select captured-change references. Acquisition freezes
immutable evidence before delivery; file/skill references remain ordinary answer text.
Attachments share a 4,000-token budget including diagnostics and exact `mread` recovery.
Confirmed progress intervals already received in the current context can be omitted;
later intervals remain eligible. Reset, rewind, resume or unavailable context restores
the full frozen projection, not a live reread. Explicit `mchanges` and reloads still
return full selected evidence. Failed/interrupted work acquires no successful final
attachment. Ordinary answer text remains unchanged in live, restored and replayed views.

### Capture-aware progress

Optional `yield_time_ms` has a five-minute minimum; omission disables it. Completion,
messages and elapsed timeout take precedence. No changed captures means no inference
or progress wake. One immutable unfiltered capture publication freezes changed agents,
record order and inclusive chunk intervals. Later changes belong to another batch.

Each changed agent supplies a tool-free independent summary from its current context
at an actual completed-tool boundary. It does not investigate, interrupt/replay tools,
finish the assignment or inject primary summary prompts. All slots settle before one
combined yield; unavailable targets and acquisition failures retain explicit reasons.
Cancellation cannot revive an abandoned collection. Process and continuation handles
survive. Summaries are developer claims, not independent verification.

Combined output includes agent identity, substantive summary and exact capture
selection. It is bounded near 12 KiB, with complete retained recovery when larger.
Only intervals represented in confirmed delivered output advance a recipient-context
cursor; recovery-only output, failed/uncertain delivery and probes acknowledge nothing.
Cold reconciliation reuses frozen output. A progress yield is not completion and
requires another wait to continue waiting. Wait results also carry selected actors'
context/window/compaction observations; unavailable metrics do not fail delivery.

## Independent side questions

An authenticated caller asks a loaded target through its restricted native auxiliary
executor with an empty tool policy. It neither supplies earlier side-question history,
changes primary history, satisfies waits, occupies the human side dock nor starts
ordinary work. Source snapshots use recipient authority. Changed target work identity
invalidates a stale answer. Cancellation owns and joins auxiliary cleanup. Failures
retain dispatch acceptance and never fall back to ordinary input or cold restore.

## Lifetime, presentation and recovery

Native observations supply detached tree/work/wait facts to terminal and browser
consumers. Logical working/waiting remains distinct from a running host turn in
timing, roster, composer, input locks and cancellation. Wait previews use actual
selected descendant names and expose complete detail. Child role/model/effort facts
come from that child, never parent defaults. Absent observations remain unavailable. View switches or disconnection do not stop work.

Human input to an idle admitted child uses native work admission and parent-result
delivery; running input steers its actual turn while keeping human attribution.
Known refusal releases new admission; uncertain input retains exact receipt identity.
Explicit child/subtree end stops admission, cancels and joins sessions, pending
starts/restores, timers and auxiliary work without archiving history. Root exit joins
its owned tree. A human Side/One-off fork is an independent native root with its own
work/checkpoints; it is not enrolled as a selectable source-tree child.

Cold resume restores retained membership and paused facts without restarting children,
rearming waits or redispatching messages. Explicit work reconciles uncertain receipts
before resuming. Selecting an admitted Codex child can restore its runtime without
starting work; historical/replay-only views remain read-only. Rewind replaces
conversation history, not task identity, descendants or live work. Process handles
do not become continuous merely because a runtime is restored. Retained roots preserve
historical delivery identity while undispatched addressed input follows the selected root.

## Feature use

The native app-server owns one launch-local per-actor counter and deduplication owner independently of loaded Codex sessions. Native semantic facts feed
it directly, without per-use host notifications or reflected record requests.
Mekugi retrieves snapshots for Features display and explicit refresh, aggregates
them over admitted membership and presents availability; it maintains no parallel
counter implementation.

- Compactions count completed resets and separately self-requested resets, effective
  nonempty reload selections, self-request completion and confirmed failure. Manual/
  automatic reload alone adds no self-request. Accepted requests can outnumber resets.
- Spawns count confirmed admission, nonempty preload and rejected preload attempts.
- Delegate finals count successful delivered answers and their captured-change subset.
- Messages count accepted ordinary sends and dispatched side questions, with rejected
  pre-dispatch BTW and failed accepted BTW tracked separately.
- Waits count new registrations, all mode, enabled progress and actually delivered
  progress. Probes, unchanged captures and failed/uncertain wakes add no progress delivery.
- Source-bearing sends count accepted assignments/messages/BTW with frozen selections,
  including retained read failures, not individual files. Explicit preload is separate.

Each use belongs to its initiating actor, including foreign sends, and repeated facts
count once. Confirmed retained operations may count in the current launch without
backfilling old requests. Counts survive child replacement and UI reconnect, but a
new root launch starts empty rather than replaying history. Missing measurement is
unavailable, not zero. Metric failure neither retries nor rolls back accepted work.
General usage and output deduplication keep their separate owners.

## Acceptance

Validate through normal native tools, human controls
and both presentation consumers. Cover preparation/authority, complete fork prefixes,
same/foreign addressing, ordered any/all stop/wake, nested completion, reset interleavings,
progress/final capture, refusal/uncertainty, passive resume and joined shutdown. Source
and schema checks establish no provider or rendering guarantee. Report deterministic,
live-provider and renderer evidence separately.
