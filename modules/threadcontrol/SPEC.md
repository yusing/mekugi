# Independent thread coordination

## Required outcomes

Codex remains the root execution authority. Wait for any or all current descendants; an
addressed message wakes either selection mode, while queued composer input does not
cancel the wait. Compact-and-wait is atomic: compaction failure retains the wait and
reports failure with the eventual outcome. Independent delegate questions do not replace
assignments or ordinary messages/follow-ups.

Active Mekugi sessions can exchange messages, including independent side questions,
and inspect or list agents across session boundaries. Foreign recipients
receive the authenticated source session and sender identity. Communication within
one session retains its existing behavior and receipt format.

Checkpoint layout, native admission phases, transport, identifiers and dispatch
machinery below are current design.

## Ownership

Thread control owns admitted logical trees, work generations, admission reservations,
attributed messages/results, descendant waits and their durable dispositions. Each admitted
backend owns its thread and turn execution, tools, processes, permissions, approvals
and interruption. Codex owns root execution; qualified Claude models select the
[Claude delegate backend](../../backends/claude/SPEC.md). The Mekugi server owns each native runtime. Its Session Control retains that
runtime’s sole host connection and request ledger. Launch supplies the selected
execution owners; the server admits children and joins their lifetimes.

## Current coordination design

One durable checkpoint records logical members, attributed operations and selected
waits. A spawn advances through fixed creation, assignment retention, admission and
execution phases. Each acknowledgement is retained before the next phase; an
uncertain operation never permits a blind retry. Session Control owns host request
correlation, independently of the logical checkpoint.

Each runtime exposes one authenticated MCP server, `threadcontrol`, through stock
JavaScript execution. Its invocation configuration requests tool declarations in the
initial execution catalog, without a discovery turn. Codex's strict third-party
deferral setting takes precedence when enabled. User-configured MCP servers retain
their exposure settings. Coordination calls the Mekugi server's typed native-thread
API for creation, attributed input, interruption and retained-outcome confirmation.
Session Control translates those semantic operations into backend requests. Its operations replace native collaboration; no native alias or
fallback remains callable. Only Thread Control records supply coordination history. The Codex
binding supplies the final invocation and thread-admission policy:
`features.multi_agent_v2.enabled=true` and
`features.multi_agent_v2.disable_native_tools=true`. Root, child and mode hints
remain enabled. Leaked original catalogs reject before provider forwarding.
Admission validates actual host identities, settings and acknowledgements.
Missing required host semantics fail explicitly without enabling native spawning.

Independent runtime admission is established by authenticated Mekugi membership
and host-confirmed settings. It does not require a native Codex agent-graph edge.
Agent assignments, messages and continuations retain their source attribution
without becoming human composer input. Retained message IDs remain local to host
history and delivery reconciliation; provider input omits those IDs and displays
coordination addresses as `agent:main` and `agent:main/<descendant>`. Generated
guidance, attributed message headers, tool results and accepted targets use that
visible namespace, including for independent root-shaped runtimes and retained-session
requests. Internal task paths and saved history retain their canonical `/root`
identities. Payloads, custom instructions and arbitrary filesystem text are unchanged.
Observation uses that admitted membership for child ancestry and attribution;
native request metadata remains unchanged on the provider wire.

## Operations and admission

- `list_models` takes no arguments and returns models for the other delegate backend:
  Codex callers receive native SDK choices qualified with `claude:`, while Claude
  callers receive Codex identifiers from the captured selected provider catalog.
  Discovery requires admitted caller identity, creates no logical member or work
  generation, and submits no model input. Missing runtime/catalog acquisition
  fails explicitly. Native catalog entries advertise supported choices, not
  guaranteed provider access or account entitlement.
- `spawn_agent` preserves task name, assignment, role, context, model and effort
  conventions and adds optional `cwd`. It returns confirmed independent host
  identity and visible logical agent name. Task names are nonempty and contain
  only lowercase ASCII letters, digits, underscores and hyphens; each name forms
  one segment of its parent's logical task path.
  Claude effort choices use the native model's advertised capabilities; a
  full-history fork inherits its selected effort. Inspection reports native
  applied effort only after observation, leaving it absent until then.
- `followup_task` controls an admitted non-root target: start when idle, steer its
  actual running turn, or supersede an unsent wait/wake before reconciling host
  state. Original caller attribution remains attached. Action results identify
  the resolved target thread and visible agent name, including when input used an alias.
  Follow-ups return after their own host-confirmed start or steer, including when
  lifecycle dispatch owns the acknowledgement, without draining later queued input.
- `send_message` queues attributed input without starting an idle thread. A
  matching message can satisfy an existing selected wait. Optional `btw:true`
  instead returns an independent side answer without delivering primary input.
  Omitted or false retains ordinary message behavior.
- `interrupt_agent` interrupts the actual running turn. A waiting task without
  one cancels its wait and settles interrupted, retaining the thread for later work.
  Request acknowledgement alone is not completion.
- `list_agents` reads admitted tree state, including pending, paused and uncertain
  work/delivery identities, with the existing `path_prefix` filter. Optional
  `session_id` selects another active Mekugi session by exact session/thread ID.
- `inspect` reads an admitted agent by `target`, including `agent:main` and idle
  or completed agents. It reports applied model, reasoning effort and service tier, context
  usage/window, and completed compactions. Unavailable observations are `null`,
  distinct from a measured zero. Inspection uses live session facts or retained
  observations without starting work or restoring an inactive runtime.
- `compact` requests an authenticated self-directed context checkpoint and then
  continues unfinished work once. Codex accepts optional `reload:{"sources":[...]}`
  using [selected-source recovery](../agentguidance/SPEC.md#compaction-context).
  Claude retains native compaction without this argument. Context-usage reminders appear starting at 50%;
  agents choose safe checkpoints where discarding obsolete context materially
  reduces what remaining work must carry. Small steps, preloads and waiting
  transitions alone do not call for compaction. Notices below 90% are not compaction
  triggers; the 90% notice instructs the agent to compact now.
- `wait_agent` accepts `wait_mode?: "any" | "all"`,
  `timeout_ms?: integer`, `yield_time_ms?: integer`, and `compact?: boolean`. Codex also accepts `reload`
  when `compact:true`; invalid selections reject before wait registration.
  Each wait snapshots descendants with unsettled work or unread outcomes;
  omitted mode means `"any"`; omitted compaction means false.

Follow-up acknowledgements include the target's current context and compaction
metrics. Wait acknowledgements (`waiting` or `compacting`) omit metrics. Immediate
outcomes and delivered post-wait outputs, including timeout, addressed-message and
progress wakes, include fresh metrics for each selected descendant, keyed by visible
agent name. Compaction waits sample metrics after the checkpoint settles, when the
original wait delivers an outcome. Unavailable observations are `null` without
failing an otherwise successful coordination operation.
Dispatched wait metrics freeze with that wait's existing checkpoint so a resumed
runtime can confirm the exact retained continuation without replacing its facts.

Host MCP thread/session/turn/call/item metadata binds each call to admitted ownership.
Task labels and tool arguments confer no authority. Members communicate with
admitted peers/ancestors and control non-root targets in their tree. Human/session
control retains root authority. Foreign, retired or conflicting callers reject.

### Active-session coordination

Same-tree names resolve first. `send_message` and `inspect` also accept exact
thread IDs owned by another active Mekugi session.
`list_agents.session_id` selects that session's admitted tree and retains its
prefix filter. A registered runtime authenticates both source ownership and target
availability; aliases never cross session boundaries. Unavailable runtimes fail
without being resumed. Foreign message and inspection receipts identify the target
session. Ordinary input carries the source root session and sender as delivery
metadata; side questions prefix those identities to the question text.

Cross-session coordination does not grant spawn, follow-up, interruption or
compaction authority over the foreign tree. It does not import another product's
sessions or provide cross-product resume compatibility.

Codex retains execution admission and resource failures; Mekugi imposes no
thread-count cap. Effective Codex V2 configuration supplies timeout bounds.
Absent overrides use explicit waits from ten seconds to one hour.
Delegation depth is unrestricted, matching V2; the legacy `agents.max_depth`
setting does not constrain its logical tree. Admission records work before dispatch
and releases that identity only on confirmed logical settlement; restarting idle
work records a new generation. Stop-wait retains its reservation.
Unknown role/settings and unavailable authority fail explicitly.

### Independent side questions

Side questions resolve authenticated caller and target before executing outside
ordinary message reconciliation, assignment dispatch and wake handling. The target's
live runtime answers without starting logical work, satisfying waits, changing drafts
or occupying a human side-question slot; a target without one fails rather than
being resumed. Each question is independent; prior side-question exchanges are not
supplied. Codex uses its restricted answer-only /btw fork; Claude uses its native
side-question control request, never a session fork. A caller that stops waiting
abandons the answer; its auxiliary work finishes and cleans up independently.

The result carries the target thread, task name and answer. An answer whose target
work generation changed meanwhile is rejected. Failures return explicitly, without
delivering the question as a message or steering the target. `followup_task` retains
its assignment/start/steer semantics.

## Prepared children and guidance

### Claude delegates

An explicit `claude:MODEL` selection with `fork_turns:"none"` creates a Claude
delegate. Backend selection survives role preparation: role instructions remain
applicable, while a pinned Codex model or effort cannot replace the explicitly
selected Claude model. For fresh children inheriting Claude, a pinned Codex role
model replaces inheritance. Full-history children keep their source backend.
Codex preparation retains its existing precedence.
Claude model availability is validated by its runtime, with no provider fallback.
Claude inherits approval policy and role capability limits, and runs without
filesystem or network sandboxing. Human decisions use the ordinary approval dock
of its nearest attachable ancestor, retaining the requesting child's attribution.
Fresh delegates acquire selected preload bodies before inference and retain them
with their attributed assignment. Claude full-history children fork their own
backend session; a Codex transcript is never imported as Claude execution history.

Claude receives authenticated coordination tools and invocation-private executable
frontends. It has its own durable caller identity in the parent's namespace.
Messages to idle work remain retained-only. Native input UUIDs and persisted
operation receipts distinguish acceptance from uncertain dispatch. Restart restores
paused logical work, and explicit follow-up reconciles before resuming the exact
Claude session. Native background work and interruption settle only on observed outcomes.

A direct Claude `wait_agent` call uses the existing wait state machine. Successful
batch completion commits its exact local stop, and the backend's post-tool-batch
hook stops before another provider request. Confirmed hook-stop completion retains
logical waiting. A later wake appends an attributed synthetic, non-user tool-outcome
envelope to the same session; it neither replays the original tool nor fabricates
human input. Nested coordinators preserve descendant completion guards and ordered
message/result delivery.

Claude roster members are nonattachable. Shared presentation carries this capability
through live, retained and historical navigation; unavailable metrics remain absent.
Native Claude edits publish ordinary captured-change receipts through the existing
capture owner, including observed partial effects from failed or interrupted tools.

### Codex preparation

The Codex binding captures configuration provenance plus actual invoking
thread/turn settings, permissions, runtime roots, model/provider, effort/summary,
tier, base/developer instructions and cwd. `config/read` alone is not live state.
Configured subagent model and effort defaults apply to fresh and full-history
children before role overrides. Without those defaults, children inherit the
invoking turn's model and effort, except inherited ultra becomes the selected model's
stock ordinary equivalent so proactive delegation is not inherited automatically.
Explicit effort settings, configured subagent defaults, fixed role effort and custom
mode guidance retain their precedence. This applies to new children, including those
of resumed parents; existing resumed agents keep their retained settings.
Configured roles precede built-ins; role files
and their resolved values are frozen before dispatch. Fresh children apply the
default role; full-history children apply an explicitly selected role or preserve
stock inheritance. Explicit model/effort arguments still require a fresh child.
An applied parent session tier takes precedence; otherwise a configured role tier
is used. The selected tier follows native alias normalization and routing feature
gates before final-model support filtering. Roles may reduce supported
capabilities, not widen authority or replace provider credentials.

Fresh children accept optional `preload:{"sources":[...]}` with the same source
selection as [compaction reload](../agentguidance/SPEC.md#compaction-context):
file `path`, optional inclusive `ranges`, or `skill` with an optional native
`SKILL.md` path, or `changes` with an array of explicit `mchanges` IDs/ranges
supplying their composed retained diff. Change sources exclude file/skill fields.
A preload requires explicit `fork_turns:"none"`; an empty sources
array is valid. Relative file paths resolve in the selected child cwd. Codex
supplies `AGENTS.md`, which is excluded from acquisition.
The prepared assignment freezes the selection before creation. Before the child's
first provider inference, the shared native reload reader acquires selected
sources under that child's permissions and approvals. Successful bodies and
individual failures become reusable context alongside the attributed assignment;
one failed source does not discard the others. Completed capture survives resume
and inherited history without rereading. Compaction retires the initial preload
when its assignment leaves context and selects subsequent reloads normally.

The spawn tool description directs agents to preload known, assignment-relevant startup
instructions, skills, task files and retained changes for fresh children. This supplies exact source
content at the first inference without child discovery/read turns or unrelated parent history.
Agents omit preload for full-history forks or when no startup sources are known,
and leave `AGENTS.md` to the host.

Permission profiles remain complete, including filesystem deny rules and network
policy. Creation that cannot express them starts no work; the first turn applies
the captured full policy. Host-confirmed settings are compared before exposing a
running child. Resume uses retained preparation and later confirmed settings, not
new role-file contents.

The initial attributed assignment is retained as assistant context before its
turn starts. That acknowledgement checkpoints the child's canonical authority,
which Session Control compares with frozen preparation before starting execution.
Retention alone neither runs work nor grants human-input authority.
Creation, retained input and assignment execution keep distinct acknowledgements;
reconciliation confirms retained facts without silently starting paused work.

The selected raw model catalog supplies model-specific role guidance for the current
request model, including after model changes; explicit custom role hints remain frozen.
When the catalog omits a role baseline, the corresponding stock Codex root or
subagent usage guidance supplies the default, including message framing and
parent delivery for delegates.
Generated child defaults do not become configuration overrides. Named role
settings resolve from the invoking workspace’s effective configuration layers and
are frozen before child creation. Mekugi supplies its calling convention and
admitted identity and shared workspace. Tool descriptions own operation lifecycles;
field descriptions own argument formats, defaults, authorization and usage guidance.
These descriptions are included in the initial execution catalog for roots and
delegates, including naming constraints and preload/timeout choices.
No copied stock policy catalog or host-version profile is required.

Native content-kind provenance authenticates each generated role/mode fragment by
thread, response-item ID, content index and exact bytes. Raw host observations or
identity-validated rollout history supply it; text resemblance never does.
Threadcontrol retains those facts before exposing references. Agent Guidance only
projects immutable admitted fragments and logical caller values.

Independent children receive the selected model’s child baseline, with one Mekugi
namespace calling reference, visible logical identity and selected cwd/shared-filesystem
wording. The effective mode
fragment stays byte-for-byte unchanged. Child guidance recognizes explicit delegation
requests in authenticated parent assignments as authority within that assignment and
the parent's authority, subject to higher-priority limits. An assignment alone does
not grant further delegation authority. Custom text stays verbatim outside proven
adaptations; incompatible calling instructions fail with a configuration diagnostic.
Missing/ambiguous provenance blocks only the affected preparation after bounded
acquisition. Repeated preparation is idempotent; caller-authored lookalikes survive.

## Full-history children and workspace

`fork_turns:"all"` freezes the invoking execution's admitted prefix while its
parent may be blocked in the spawn call. The adapter materializes copied or
referenced/paginated history and usable compaction replacements into a private,
self-contained legacy import. Authenticated observed records may complete the
prefix; incomplete evidence rejects rather than choosing an earlier boundary.
The original rollout and host database remain unchanged.

Full-history children inherit every conversation item in order, from the first
message or active compaction replacement through the invoking execution item.
This includes attributed assignments/messages, assistant commentary and reasoning,
and tool calls/results, independently of the host's preserve-fork-prefix setting.
Compaction replacements preserve the same complete item sequence.

Import sanitation annotates inherited envelopes and clears parent input ordering
and local authorization/review evidence, preserving incomplete authority and
retained root provenance. Generated parent identity/role/cwd context is replaced
only by content provenance; ordinary conversation and unrelated developer text
survive. Current child identity/role/cwd context appears once. No process, approval
or dispatch handle is inherited. Legacy compaction still resets obsolete context
baselines without filtering conversation items.

Session History publishes the snapshot and preparation before `thread/fork.path`.
Its validated header establishes source identity. Confirmed child lineage, cwd and
settings precede assignment. Lost acknowledgement stays uncertain until reconciled.
Import resources retire only after confirmed host ownership and durable publication.

Omitted cwd inherits admitted caller cwd; relative cwd resolves there, never process
cwd. The selected existing authorized directory applies to fresh and inherited
children, instruction loading and commands without per-command workdir. Parent cwd
is unchanged. Follow-ups, resume, frontend scope, capture and display retain the
confirmed child workspace. Directory selection grants no additional permission.

## Logical waits and ordered delivery

Host events reduce logical work and its subscription together. A registered wait
is uncommitted until its exact enclosing execution succeeds and its local stop is
claimed. A committed stop may precede host settlement; stopped waiting still owns
logical work. A queued wake is undispatched; dispatching or uncertain wakes remain
host-owned until confirmed or reconciled.

The request executor can also commit a stop for a recognized provider usage-limit
or overload/capacity rejection. Its wait selects provider timing rather than
descendants. Provider reset or retry timing permits continuation after host
settlement. Without provider timing, capacity rejections use backoff v7 exponential
intervals from one to thirty seconds, scoped to the logical thread and work
generation. Successful inference resets fallback timing. Usage-limit rejections
without timing remain paused until human input or interruption supersedes them.

| Host event | Executing, no committed stop | Committed stop / stopped waiting | Queued or host-owned wake |
| --- | --- | --- | --- |
| Successful host completion | Defer normal completion while active descendants remain; otherwise complete with that turn's ordinary answers. | Checkpoint waiting and keep the reservation, subscription and answers; do not publish a final result. | A matching committed stop still checkpoints; a host-owned wake keeps its dispatch identity through terminal settlement. |
| Terminal host failure | Fail work and cancel an undispatched wait. Confirmed recoverable failures instead retain recovery waiting while active descendants remain. | Same failure policy; a committed stop does not turn failure into success. | Recovery queues behind a host-owned wake; terminal failure retains that wake's reconciliation identity. |
| Host interruption | Settle interrupted and retire an undispatched subscription. | Same terminal outcome, retaining the actual interruption. | Retain a dispatched wake's reconciliation identity. |
| Manual compaction | Auxiliary turns do not acquire or settle logical work. Without an exact committed stop, no wait checkpoint is taken. | Settle only the exact committed stop's host prerequisite; preserve work, wait, answers and queued events. | Hold continuation delivery until auxiliary compaction settles. |
| Addressed input injection | Retain the notification without starting or steering the receiver, or consuming it on acknowledgement alone. | Retain it for ordered wait delivery after host settlement and any compaction barrier. | Do not replace the existing dispatch or claim a notification twice. |
| Explicit human input or steering | Supersede an undispatched subscription and local stop so ordinary work can process input. | Same supersession; queued composer input alone does not perform it. | Release an undispatched wake's claims; require reconciliation before superseding a host-owned wake. |
| Human interruption / interrupt_agent | Retire the wait and interrupt the actual running turn; its observed outcome settles work. | Without a running turn, settle interrupted once. | Cancel an undispatched wake; require reconciliation for a host-owned wake. |
| Enclosing execution yields or succeeds | Yield retains the wait without arming a stop; authenticated success makes the exact wait eligible for one local stop. | Repeated evidence reuses that stop. | Evidence for an execution that no longer owns a registered wait has no effect. |
| Enclosing execution fails | Retire its exact registered wait so another execution may retry; do not settle logical work. | Retire that wait and local stop, returning waiting work to working. | Do not retire a queued or dispatched wake using stale execution evidence. |
| Enclosing-cell re-execution | Reuse the same subscription for the same turn, item and work generation; reject a different enclosing execution while pending. | Reuse only while the authenticated enclosing host turn still runs; stopped work has no executing caller. | A pending wake retains its identity; new input must supersede it before another execution can wait. |

Unrelated or duplicate terminal observations do not settle work again. Normal
host starts keep their existing admission controls: a new root turn supersedes a
registered or queued wait; a child turn requires its retained work reservation.
Observed attributed items in admitted model input consume only their exact
unclaimed notifications, so reading input does not generate a later duplicate wake.

Each wait snapshots local descendants with unsettled work or unread outcomes once.
Already-settled, read history is not selected, reported or displayed as awaited work.
Unread addressed messages retain their authority independently of that selection;
without selected work or a pending message, registration rejects.
In `any` mode, a queued descendant outcome returns immediately, leaving others queued.
In `all` mode, every selected agent must settle before completion; failures and
interruptions count as settled. Pending result deliveries finish before the wait
returns all selected outcomes in `events`. An addressed message wakes either
mode immediately as a single `event`, leaving completion notifications queued.
Stopped selected targets settle with their terminal status, including when their
notification was already read or their runtime was paused. Pending result delivery
keeps its existing order. In `any` mode, mixed selections settle on an eligible selected outcome;
descendants admitted after registration do not join that wait.
One pending wait per caller is retained; calls in the same enclosing execution
report that wait instead of replacing it. A different enclosing execution rejects
while that subscription remains pending. Human interruption, `interrupt_agent`, and explicit
steering supersede pending waits through their existing work-lifetime controls.
Queued composer input waits for logical work to resume and finish; it does not cancel a wait.

The default is stop-wait with no timeout: logical work blocks until an eligible
outcome or addressed message wakes the caller. Explicit timeout values above the captured host maximum
reject and lower values clamp to its minimum. Retain requested/effective duration
and the deadline captured when waiting begins. Zero effective duration times out immediately when
no selected event exists. Later timeout delivery waits for host settlement.

Optional `yield_time_ms` checks progress at intervals of at least 300,000 milliseconds;
omission disables yielding. An interval without new captured changes keeps the same
logical wait pending, without inference or a finished-wait publication. The interval
starts at registration. Completion, addressed
messages and an elapsed timeout take precedence. Host settlement and compaction retain
their ordinary barriers. A yield resumes the caller with `state:"yielded"`, not a task
completion; another wait call waits again. It neither consumes completion notifications
nor creates a second subscription or timer. Its targets remain the actual wait
selection; progress rows include only agents with new captures.

Progress reports available captured changes not delivered by that caller's earlier yields,
with `mchanges` IDs and current observed activity/status. Agents without new captures are
omitted. Late writer intervals remain eligible even under an already-reported change ID.
The progress payload is bounded to approximately 12 KiB, including at most eight file
capture rows per record; clipped paths and omitted rows remain readable through the exact
`mchanges` command shown in each report. Git effects retain diagnostic provenance and use
`--history`, not authored diff counts. `more:true` leaves omitted captures eligible for later
yields. Capture gaps remain explicit. Payload and cursor freeze before dispatch, and only
confirmed delivery advances the cursor in that recipient context; uncertain delivery and cold
reconciliation reuse the same retained output. Reset, rewind and resumed contexts do not inherit
consumption of earlier progress.

The retained wait acknowledges `{state:"waiting"}` with delivery instructions at
the MCP boundary. Agents let that enclosing execution finish; the host turn ends
and the outcome arrives as a later `wait_agent` output. Completion waits omit the
timeout; explicit timeouts serve actual deadlines. Only
successful completion of that enclosing stock execution admits
a persisted local-stop disposition. A failed enclosing execution retires its
wait so a later execution can retry. Yielded, incomplete or unrelated calls
cannot arm it. Explicit steering after a successful enclosing result retires its
subscription and any local stop so ordinary inference can process that input.
Request Execution then supplies an empty completed continuation
locally, without provider inference or usage. New input or admitted wake invalidates
a stale stop. Stop disposition and terminal reduction serialize per work generation.

A local host-turn completion checkpoints logical waiting, not task completion:
no final answer, completion event/notice, idle transition or released reservation.
Agent-requested compaction registers its intent and optional wait atomically,
including when an outcome is already ready. Its `compacting` acknowledgement requires
the same authenticated successful enclosing execution and settled host turn as a
local wait. Failed execution retires the checkpoint without claiming events; yielded
execution remains pending. Results and addressed messages stay retained behind the
compaction dispatch barrier. Standalone success continues once. Compound success
releases only the original wait: an eligible outcome is delivered once, otherwise
the caller stays waiting without inference. Wait deadlines run from registration
and may expire during compaction.

At measured context usage of 40% or below, Codex self-checkpoints preflight sources
through the admitted stock reader and
compare locally estimated uncached reset input cost with the latest observed input
cost, using its model, service tier and pinned provider pricing. Source contents
are assumed unchanged across reset. Unavailable cost evidence permits reset with a notice;
cost rejection skips only reset and preserves the original wait and deadline.
Above 40%, or when context usage/window is unavailable, checkpoints bypass cost
preflight and its veto. The boundary uses the caller's observed context and window,
not provider input counts or the reset estimate.
Attempted preflight without a usable capture also skips reset, preserving the wait
without automatically retrying denied or canceled acquisition.
Rejection records a `skipped` compaction outcome and immediately displays its reason,
including that context was not reset and selected sources were not attached.
Preflight reads supply only the estimate, never attachment presentation or model
reload context. The rejection reaches the model in the eventual wait output, not an immediate
inference. Manual, automatic and Claude compaction are unchanged. Future hook and
outcome text is unmeasured.

Compound native compaction failure preserves the wait and events, surfaces an immediate
notice, and includes the failed compaction outcome in eventual delivery. Failure
alone does not start inference. Human input and interruption supersede undispatched
continuations without changing an already-settled reset outcome. Uncertain native dispatch pauses for reconciliation and is never
repeated automatically. These semantics apply to Codex roots/delegates and Claude
delegates.

Manual compaction preserves the pending wait and logical work. Its auxiliary
host turn neither completes the task nor starts a competing continuation; addressed
messages and wakes queue until compaction settles.
Selected logical child completion/failure/interruption or an addressed message may
wake. Child questions, approvals and their human replies do not. Nested coordinators
obey the same state machine. Events retain arrival order and are claimed once.
Exact attributed item identities observed in admitted model input consume their
unclaimed notifications, so a later wait does not replay already-read messages or
results. Injection acknowledgement alone does not consume an event. Message wakes
are presented as received messages, separately from assignment completion.
After host settlement, Session Control starts empty input with retained
`toolOutput:{namespace:"threadcontrol",name:"wait_agent",output:...,retain:true}`.
Retained wake output includes its dispatch identity for exact host confirmation;
provider input omits that identity without changing the frozen metrics or saved
history. Direct public wait results omit the internal wait identity.
The result identifies event/source task/thread/turn/kind/status, without fake
human text or composer insertion. A running host turn never receives a second start.

Normal completion remains blocked while any transitive descendant is working,
waiting, or holding an admitted pending-start reservation. One retained continuation
per attempted completed turn names the active descendants and asks the agent to wait.
A descendant that settles before dispatch is removed from that hint; if none remain,
the original completion proceeds without another model turn. Paused and uncertain
retained agents do not block completion. Explicit interruption and terminal failures
keep their ordinary outcomes and supersede undispatched completion continuations.

Confirmed overload, internal-server and response-stream failures retain logical
waiting while descendants remain active. The native failure stays visible. One
unread descendant result or addressed message can resume the parent through its
ordinary retained wait delivery. Fresh terminal outcomes of captured transitive
work also wake through a terminal coordinator without consuming that coordinator's
input. Already-stopped targets alone cannot trigger recovery, and each notification
is claimed once. An in-flight preceding wake retains its dispatch identity until
settlement before the recovery wait can dispatch. Authentication, invalid
requests, interruption and unknown errors remain terminal. Recognized provider
usage-limit and capacity rejections use the dedicated wait above; other quota
failures remain terminal. Descendant recovery adds neither
timer retries nor model switching; host-owned request retries remain unchanged.

Ordinary successful terminal work first retains its completed nonempty ordinary answers
from that turn in order, including an empty payload when none exist, then injects one attributed
`FINAL_ANSWER` through `thread/inject_items`. Failure/interruption retains its actual
outcome. Injection alone neither starts nor steers its receiver. A completion wake
follows confirmed delivery and carries identity/status rather than duplicate text.
Delegates may select explicit `mchanges` IDs/ranges in the existing reload block
at the end of their ordinary final answer, submitting answer and selection together.
After successful logical settlement, the receiving parent's input worker acquires
the selected retained evidence before injecting the answer and completed reload
reader/result together. Automatic completion attachments share a 4,000-token
budget, including labels, diagnostics and an exact `mread` continuation for omitted
evidence. Captured intervals already reported by confirmed yields in the receiving context
are omitted, without dropping later intervals under the same change ID or unreported captures.
Yield summaries are not full patches: explicit `mchanges`, preload and compaction reloads retain
their full-content behavior. Context replacement makes earlier intervals eligible again.
Replacement after preparation also restores unfiltered captured evidence at receiver
projection. Both retained variants obey the same output bound.
The next inference consumes the retained snapshot without another acquisition turn.
When the answer satisfies a selected wait, its reply, attachment and finished-wait
notice publish together after host-confirmed wake delivery. Existing reload history
retains the captured results; the original selection remains available for reconciliation.
Reload metadata stays out of displayed live, restored and replayed answers.
Ordinary text and quoted examples remain unchanged. Selection neither completes
work nor bypasses descendant guards. Failed or interrupted work never acquires a
successful attachment; later work inherits no pending selection.
Confirmed initial assignments, follow-ups, messages and answers appear in the receiving
runtime's Main transcript with their original logical sender and terminal outcome.
Inherited traffic keeps its original sender and recipient. An interruption request
settles when its target outcome arrives, retaining the original request evidence.
Failed injections do not publish delivered traffic.
Rejected delivery remains visible and may satisfy a wait as delivery failure.

Stable work/event/delivery identities persist intent before dispatch and acknowledgement
after confirmation. Duplicate calls/observations reuse state. Lost acknowledgement
is uncertain, with host-history reconciliation before any retry; restart cannot
blindly redispatch. Human input or coordinator interruption supersedes unsent wakes.
Already dispatched continuations remain host-owned and require reconciliation.

## Lifetime, presentation and recovery

Host lifecycle and logical working/waiting state remain separate in admission,
elapsed timing, roster/composer publication, idle guards and cancellation. Existing
prompt handling remains independent and responsive to unrelated storage waits.
Activity and Session Presentation consume detached owner state and reuse existing
terminal painting. Waits display actual descendant names in a short preview with an omitted-target
count and complete detail access.
Transcript progress
shows `Waiting for …`, adds a muted explicit timeout when present, and records
`Finished waiting for …: reason` when the wait settles, including timeout,
addressed messages, completion, failure and cancellation. View switches do not transfer or stop coordination.
The child composer shows its admitted role, actual model and reasoning effort;
follow-ups, settings changes and resumed facts remain scoped to that child. Missing
facts never borrow parent settings or model defaults. Human input to an admitted
idle child starts a tracked assignment with the same work-generation admission and
parent-result delivery as a follow-up; running child input steers its actual turn.
Human input keeps its native user role and serializes with agent follow-ups.
Known refusal releases a new reservation; uncertain dispatch remains retained
until the host's exact input identity confirms it.

Exit stops owned live work through its native backend and retains known outcomes, pending delivery,
wait selections and dispatch facts. Fresh resume restores paused state without child
restart, wait rearming, deadline expiry delivery or message redispatch. Explicit work
or a new wait may proceed after reconciliation. Forks inherit authorized history,
not live subscriptions, foreign-root authority or dispatch eligibility. Manual rewind replaces conversation history within the same native thread and
logical member. Parent/task identity, descendant sessions and their live work
remain unchanged. The caller's native runtime reload does not establish process
handle continuity. Existing retained root branches remain historical delivery
endpoints when another saved root is selected; pending addressed messages follow
the selected root while dispatched delivery keeps its reconciliation identity.
Resume remains product-local: Mekugi checkpoints are consumed by Mekugi, with no
cross-product session-resume compatibility requirement for stock Codex.

Startup restores the selected root's checkpoint before admitting new input, without
requiring a model request. Retained run payloads identify their exact operation;
reconciliation cannot confirm a repeated assignment from an older identical message.
An exact terminal host turn settles an uncertain interruption. Outcomes without
sufficient retained identity remain visibly uncertain and are never redispatched.
Explicit follow-up can finish a confirmed child's paused, non-executing admission
phases; the superseded initial task stays canceled and only the new assignment runs.

The descriptor owns record codecs and typed MCP contributions. Session History
owns publication, dependencies, leases and retention. The binding owns compatibility,
native decoding and preparation; Session Control alone dispatches host effects.
The invocation-local MCP transport owns connections, not coordination policy.
Native renderer and remote-human JSON-RPC connections retain their existing
authority and subscriptions; neither supplies agent identity or carries an MCP
request. Prepared child startup is an internal typed server operation, not a new
human-control method. Children borrow the captured provider selection and use
their own control, approval and observation owners.

Main and attachable delegates use the same runtime attachment and control path;
role-declared threads additionally show their role in the composer. Detail links
to another live runtime select it and open its ordinary detail view.
Explicit selection of a
retained admitted child can resume its prepared runtime without starting a work
turn; historical and replay-only children remain read-only. A
child’s native runtime identity is distinct from its logical task path. The
logical tree shares an admitted retained namespace, while changes keep their
actual author and selected workspace. Child admission establishes that namespace
before publishing execution evidence. Client disconnection releases only the
view; explicit root end cancels its owned tree, and server shutdown joins all
owned runtimes before draining client connections.

## Feature use

The service counts model-facing uses per thread for the
[session Features view](../../apps/mekugi/internal/sessionpresentation/SPEC.md#session-metrics).
Totals and their subsets:

- **Compactions:** completed live native context resets, whoever initiated them;
  newly registered `compact` or `wait_agent` compactions; those registrations with
  nonempty acquired-source selections. Self-initiated requests count at registration,
  so requests whose reset fails can exceed the completed total. Empty and host-loaded
  instruction-only selections count no preload.
- **Spawns:** admitted children; those with nonempty preload sources; spawn calls
  whose preload selection is rejected.
- **Delegate finals:** completed final answers; those selecting captured changes.
- **Messages:** dispatched `send_message` calls; those with `btw:true`; `btw` calls an
  authenticated caller makes that are rejected before dispatch; dispatched `btw` calls
  whose answer fails or whose target generation changes.
- **Waits:** newly registered `wait_agent` waits; those with `wait_mode:"all"`.

Spawns and final answers count when their effect is confirmed, immediately or by
reconciliation, so each counts once. Repeated calls that reuse a registration, spawn
or action count once, and empty selections count no preload. Each use counts for the
thread that made it; a descendant sum covers admitted descendants, including settled
ones. Counts are invocation-local and never checkpointed, resumed or inherited by forks.

## Acceptance

Focused composed checks and deterministic installed-host fixtures establish native
suppression, sole replacement discovery, all operations, role/config/guidance
differentials, cwd and complete permissions, active-boundary full-history sanitation,
descendant nested stop/wake, ordered delivery, failures/uncertainty and cold recovery.
Actual-renderer snapshots cover waiting across host stops, resumption, cancellation
and paused restoration. Runtime checks separately cover responsiveness and idle
guards. Source/schema checks alone establish no runtime guarantee.
