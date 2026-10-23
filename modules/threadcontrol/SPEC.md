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
Session Control translates those semantic operations into backend requests. Its six operations replace native collaboration; no native alias or
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
without becoming human composer input.
Projected agent instructions explicitly identify root versus child membership
and a child's parent logical task, including for independent root-shaped runtimes.
Observation uses that admitted membership for child ancestry and attribution;
native request metadata remains unchanged on the provider wire.

## Operations and admission

- `spawn_agent` preserves task name, assignment, role, context, model and effort
  conventions and adds optional `cwd`. It returns confirmed independent host
  identity and canonical logical task path.
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
- `wait_agent` accepts `target?: string[] | "all"`, `timeout_ms?: integer`, or
  exclusive `cancel_id?: string`. Omitted target means `"all"`.

Host MCP thread/session/turn/call/item metadata binds each call to admitted ownership.
Task labels and tool arguments confer no authority. Members communicate with
admitted peers/ancestors and control non-root targets in their tree. Human/session
control retains root authority. Foreign, retired or conflicting callers reject.

Effective Codex V2 configuration supplies total concurrency and timeout bounds.
Absent overrides use four slots and explicit waits from ten seconds to one hour.
Delegation depth is unrestricted, matching V2; the legacy `agents.max_depth`
setting does not constrain its logical tree. Admission reserves before dispatch,
releases only on confirmed logical settlement, and reacquires before restarting
idle work. Stop-wait retains its
reservation. Unknown role/settings and unavailable authority fail explicitly.

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
Generated child defaults do not become configuration overrides. Named role
settings resolve from the invoking workspace’s effective configuration layers and
are frozen before child creation. Mekugi supplies its collaboration tool guidance;
no copied stock policy catalog or host-version profile is required.

Native content-kind provenance authenticates each generated role/mode fragment by
thread, response-item ID, content index and exact bytes. Raw host observations or
identity-validated rollout history supply it; text resemblance never does.
Threadcontrol retains those facts before exposing references. Agent Guidance only
projects immutable admitted fragments and logical caller values.

Independent children receive the selected model’s child baseline, with Mekugi
calling references, canonical logical identity and selected cwd/shared-filesystem
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

Stock V2 sanitation annotates inherited user/assistant envelopes, clears parent
input ordering and local authorization/review evidence, preserves incomplete
authority and retained root provenance, and keeps tool declarations with their
comparison baseline. Generated parent context is replaced only by content
provenance. Ordinary conversation/unrelated developer text survives; current child
identity/role/cwd context appears once. No process, approval or dispatch handle is
inherited. Preserve-fork-prefix and legacy-compaction behavior remain supported.

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
reject. A selected queued event returns immediately, leaving others queued. One
pending wait per caller is retained; distinct registrations report the existing
wait instead of replacing it. Cancellation affects only the subscription.

Omitted timeout has no deadline. Explicit values above the captured host maximum
reject and lower values clamp to its minimum. Retain requested/effective duration
and registration-time deadline. Zero effective duration times out immediately when
no selected event exists. Later timeout delivery waits for host settlement.

Registration persists and returns `{id,state:"registered"}` immediately, closing
the MCP call. Only successful completion of that enclosing stock execution admits
a persisted local-stop disposition. Failed, yielded, incomplete or unrelated calls
cannot arm it. Request Execution then supplies an empty completed continuation
locally, without provider inference or usage. New input or admitted wake invalidates
a stale stop. Stop disposition and terminal reduction serialize per work generation.

A local host-turn completion checkpoints logical waiting, not task completion:
no final answer, completion event/notice, idle transition or released reservation.
Selected logical child completion/failure/interruption or an addressed message may
wake. Child questions, approvals and their human replies do not. Nested coordinators
obey the same state machine. Events retain arrival order and are claimed once.
After host settlement, Session Control starts empty input with retained
`toolOutput:{namespace:"threadcontrol",name:"wait_agent",output:...,retain:true}`.
The result identifies wait/event/source task/thread/turn/kind/status, without fake
human text or composer insertion. A running host turn never receives a second start.

Ordinary successful terminal work first retains its last completed ordinary answer
from that turn, including an empty payload when absent, then injects one attributed
`FINAL_ANSWER` through `thread/inject_items`. Failure/interruption retains its actual
outcome. Injection alone neither starts nor steers its receiver. A completion wake
follows confirmed delivery and carries identity/status rather than duplicate text.
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
terminal painting. View switches do not transfer or stop coordination.
The child composer shows its admitted role, actual model and reasoning effort;
follow-ups, settings changes and resumed facts remain scoped to that child. Missing
facts never borrow parent settings or model defaults. Existing child input/read-only
behavior is unchanged.

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

Parent child-display remains read-only and consumes detached publications. A
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
