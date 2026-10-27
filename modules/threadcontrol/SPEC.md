# Independent thread coordination

## Ownership

Thread control owns admitted logical trees, work generations, admission reservations,
attributed messages/results, targeted waits and their durable dispositions. Codex
owns independent thread and turn execution, tools, processes, permissions, approvals
and interruption. The Mekugi server owns each native runtime. Its Session Control retains that
runtime’s sole host connection and request ledger. Launch supplies the selected
execution owners; the server admits children and joins their lifetimes.

One durable checkpoint records logical members, attributed operations and selected
waits. A spawn advances through fixed creation, assignment retention, admission and
execution phases. Each acknowledgement is retained before the next phase; an
uncertain operation never permits a blind retry. Session Control owns host request
correlation, independently of the logical checkpoint.

Each runtime exposes one authenticated MCP server, `threadcontrol`, through stock
JavaScript execution. Coordination calls the Mekugi server's typed native-thread
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
history and delivery reconciliation; provider input omits those IDs while preserving
the attributed message content, including when replaying retained sessions.
Projected agent instructions identify the canonical logical task and a child's
parent task, including for independent root-shaped runtimes.
Observation uses that admitted membership for child ancestry and attribution;
native request metadata remains unchanged on the provider wire.

## Operations and admission

- `spawn_agent` preserves task name, assignment, role, context, model and effort
  conventions and adds optional `cwd`. It returns confirmed independent host
  identity and canonical logical task path. Task names are nonempty and contain
  only lowercase ASCII letters, digits, underscores and hyphens; each name forms
  one segment of its parent's logical task path.
- `followup_task` controls an admitted non-root target: start when idle, steer its
  actual running turn, or supersede an unsent wait/wake before reconciling host
  state. Original caller attribution remains attached. Action results identify
  the resolved target thread and canonical task, including when input used an alias.
- `send_message` queues attributed input without starting an idle thread. A
  matching message can satisfy an existing selected wait.
- `interrupt_agent` interrupts the actual running turn. A waiting task without
  one cancels its wait and settles interrupted, retaining the thread for later work.
  Request acknowledgement alone is not completion.
- `list_agents` reads admitted tree state, including pending, paused and uncertain
  work/delivery identities, with the existing `path_prefix` filter.
- `inspect` reads an admitted agent by `target` path, including `/root` and idle
  or completed agents. It reports applied model, reasoning effort and service tier, context
  usage/window, and completed compactions. Unavailable observations are `null`,
  distinct from a measured zero. Inspection uses live session facts or retained
  observations without starting work or restoring an inactive runtime.
- `wait_agent` accepts `target?: string[] | "all"`, `timeout_ms?: integer`.
  Omitted target means `"all"`.

Follow-up acknowledgements include the target's current context and compaction
metrics. Immediate wait results and resumed wait outputs include those same metrics
for each selected target, keyed by canonical task path. Unavailable observations
are `null` without failing an otherwise successful coordination operation.
Dispatched wait metrics freeze with that wait's existing checkpoint so a resumed
runtime can confirm the exact retained continuation without replacing its facts.

Host MCP thread/session/turn/call/item metadata binds each call to admitted ownership.
Task labels and tool arguments confer no authority. Members communicate with
admitted peers/ancestors and control non-root targets in their tree. Human/session
control retains root authority. Foreign, retired or conflicting callers reject.

Codex retains execution admission and resource failures; Mekugi imposes no
thread-count cap. Effective Codex V2 configuration supplies timeout bounds.
Absent overrides use explicit waits from ten seconds to one hour.
Delegation depth is unrestricted, matching V2; the legacy `agents.max_depth`
setting does not constrain its logical tree. Admission records work before dispatch
and releases that identity only on confirmed logical settlement; restarting idle
work records a new generation. Stop-wait retains its reservation.
Unknown role/settings and unavailable authority fail explicitly.

## Prepared children and guidance

The Codex binding captures configuration provenance plus actual invoking
thread/turn settings, permissions, runtime roots, model/provider, effort/summary,
tier, base/developer instructions and cwd. `config/read` alone is not live state.
Configured subagent model and effort defaults apply to fresh and full-history
children before role overrides. Without those defaults, children inherit the
invoking turn's model and effort. Configured roles precede built-ins; role files
and their resolved values are frozen before dispatch. Fresh children apply the
default role; full-history children apply an explicitly selected role or preserve
stock inheritance. Explicit model/effort arguments still require a fresh child.
Stock tier precedence remains unchanged. Roles may reduce supported
capabilities, not widen authority or replace provider credentials.

Fresh children accept optional `preload:{"sources":[...]}` with the same source
selection as [compaction reload](../agentguidance/SPEC.md#compaction-context):
file `path`, optional inclusive `ranges`, or `skill` with an optional native
`SKILL.md` path. A preload requires explicit `fork_turns:"none"`; an empty sources
array is valid. Relative file paths resolve in the selected child cwd. Codex
supplies `AGENTS.md`, which is excluded from acquisition.
The prepared assignment freezes the selection before creation. Before the child's
first provider inference, the shared native reload reader acquires selected
sources under that child's permissions and approvals. Successful bodies and
individual failures become reusable context alongside the attributed assignment;
one failed source does not discard the others. Completed capture survives resume
and inherited history without rereading. Compaction retires the initial preload
when its assignment leaves context and selects subsequent reloads normally.

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
admitted identity; tool descriptions own each operation's arguments and behavior.
No copied stock policy catalog or host-version profile is required.

Native content-kind provenance authenticates each generated role/mode fragment by
thread, response-item ID, content index and exact bytes. Raw host observations or
identity-validated rollout history supply it; text resemblance never does.
Threadcontrol retains those facts before exposing references. Agent Guidance only
projects immutable admitted fragments and logical caller values.

Independent children receive the selected model’s child baseline, with one Mekugi
namespace calling reference, canonical logical identity and selected cwd/shared-filesystem
wording. The effective mode
fragment stays byte-for-byte unchanged. Custom text stays verbatim outside proven
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

Selection resolves once to immutable visible descendant identities. `"all"`
snapshots that set; empty, unknown, ambiguous, foreign, self and ancestor selections
reject. A selected queued event returns immediately, leaving others queued.
Stopped selected targets settle with their terminal status, including when their
notification was already read or their runtime was paused. Pending result delivery
keeps its existing order. Mixed selections settle on an eligible selected outcome;
unselected stopped targets do not wake the caller.
One pending wait per caller is retained; distinct calls report the existing
wait instead of replacing it. Human interruption, `interrupt_agent`, and new input
supersede pending waits through their existing work-lifetime controls.

The default is stop-wait with no timeout: logical work blocks until an eligible
outcome or addressed message wakes the caller. Explicit timeout values above the captured host maximum
reject and lower values clamp to its minimum. Retain requested/effective duration
and the deadline captured when waiting begins. Zero effective duration times out immediately when
no selected event exists. Later timeout delivery waits for host settlement.

The retained wait acknowledges `{state:"waiting"}` at the MCP boundary. Only
successful completion of that enclosing stock execution admits
a persisted local-stop disposition. A failed enclosing execution retires its
wait so a later execution can retry. Yielded, incomplete or unrelated calls
cannot arm it. Request Execution then supplies an empty completed continuation
locally, without provider inference or usage. New input or admitted wake invalidates
a stale stop. Stop disposition and terminal reduction serialize per work generation.

A local host-turn completion checkpoints logical waiting, not task completion:
no final answer, completion event/notice, idle transition or released reservation.
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
retained agents do not block completion. Explicit interruption and failure keep their
ordinary outcomes and supersede undispatched completion continuations.

Ordinary successful terminal work first retains its last completed ordinary answer
from that turn, including an empty payload when absent, then injects one attributed
`FINAL_ANSWER` through `thread/inject_items`. Failure/interruption retains its actual
outcome. Injection alone neither starts nor steers its receiver. A completion wake
follows confirmed delivery and carries identity/status rather than duplicate text.
Confirmed messages and answers appear in the receiving runtime's Main transcript
with their original logical sender and terminal outcome. An interruption request
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
terminal painting. Omitted or explicit all-target selection displays `all`; named
selections show a short preview with an omitted-target count and complete detail access.
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

Exit stops owned live work through Codex and retains known outcomes, pending delivery,
wait selections and dispatch facts. Fresh resume restores paused state without child
restart, wait rearming, deadline expiry delivery or message redispatch. Explicit work
or a new wait may proceed after reconciliation. Forks inherit authorized history,
not live subscriptions, foreign-root authority or dispatch eligibility.
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

Main and delegate display use the same runtime attachment and control path;
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

## Acceptance

Focused composed checks and deterministic installed-host fixtures establish native
suppression, sole replacement discovery, all operations, role/config/guidance
differentials, cwd and complete permissions, active-boundary full-history sanitation,
targeted nested stop/wake, ordered delivery, failures/uncertainty and cold recovery.
Actual-renderer snapshots cover waiting across host stops, resumption, cancellation
and paused restoration. Runtime checks separately cover responsiveness and idle
guards. Source/schema checks alone establish no runtime guarantee.
