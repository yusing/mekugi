# Native agent coordination

## Status

This contract governs the native correction. Runtime validation and release status
are separate from its requirements. Wait-specific reset composition remains deferred.

## Required outcomes

Stock Codex V2 owns execution, membership, scheduling, messages, interruption,
completion, restoration and delivery. Extend those owners, not the former Go
coordinator's lifecycles. Do not add another assignment ledger, wait state machine
or conversation store.

Retain one interactive UI and the stock JavaScript execution interface. Stock
non-wait schemas and names are accepted. Address agents by canonical task path or exact
thread ID; relative task-name addressing is unsupported. The wait interface changes
explicitly; parity with the former Go schema is not a goal. Resume is product-local.
Parents can wait for any or all selected descendant outcomes. Addressed messages
retain native wake behavior. Progress is opt-in and change-driven; one combined
yield follows all requested summaries. Side questions leave primary work unchanged.

## Ownership

Reuse the native registry, scheduler, delivery, history, auxiliary executor and reset
owner. Native configuration/admission limits remain authoritative. Renderers,
connectors and resource callbacks do not mirror those owners.

Capture storage already owns retained changes. Coordination selects exact ranges,
not another snapshot, copied diff or journal. Reuse native request identities,
cancellation and existing recipient-context capture tracking. Local aggregation
inside a pending operation does not justify a durable coordination lifecycle.

Native observations feed terminal, browser, headless and replay consumers. Resource
acquisition remains cancellable and independent of a departing renderer.

## Operations and admission

- Keep stock spawn, ordinary message, follow-up and interruption semantics and
  schemas except for the required additive capabilities below.
- Use native child preparation and history forking, including effective settings,
  role precedence, cwd, permissions and provider authority.
- Extend passive native inspection with supported settings, context/window and
  completed-compaction observations. Use stock loaded/stored thread lookup and spawn-subtree
  discovery, not collaboration-registry membership. Fresh and resumed roots are inspectable
  before any agent operation; inspection leaves stock startup and registration unchanged.
  Return native thread records, not a separate agent-observation payload. Persist successful
  compaction counts at completion so observing the count needs no rollout scan.
  Metadata-only thread reads and roster reads do not load retained history. Explicit inspection
  may read history for settings and context; requesting turns retains its separate history cost.
  Do not start or restore work merely to inspect; unavailable measurements remain unavailable.
- `new_context` owns summary-free reset and source reload.
- `wait_agent` owns waiting, optional yielding and any/all outcome aggregation.
  Its target parameters are optional `wait_mode` and `yield_time_ms`. Remove
  `timeout_ms`, `compact` and `reload` rather than retaining aliases.

Arguments and labels never grant authority. Reuse native identity resolution and
reject unauthorized operations before their effects.

### Active-session coordination

Stock Codex already supports cross-session communication through shared local
message boards and remote HTTP boards. Hosts attach independent runtimes with
explicit access. Posts, replies, reads and subscriptions do not merge agent trees.
Local notifications reach connected runtimes through their owning thread manager;
shared SQLite alone does not route live notifications across processes. Remote
boards provide their own notification transport.

Reuse those facilities before adding a transport. Board discussion threads are not
agent-tree inspection, and board messaging is not independent BTW inference. Direct
foreign message/BTW/list/inspect routing also exists in the current host/native
integration. Where a supported direct operation still needs that route, adapt it to
existing destination operations instead of adding another registry, delivery ledger
or foreign actor tree.

Keep exact foreign identity and authenticated attribution. Foreign access grants
no spawn, follow-up, interruption, reset or cold-restore authority. A local failure
does not become a foreign fallback.

Direct send and inspection accept an exact foreign thread ID. The existing
`list_agents.path_prefix` field also accepts that ID to list its active tree;
foreign list results use exact IDs, not ambiguous tree-local paths. The original
source host authenticates the sender, and only the destination's original resource
connection can deliver the operation. Native destination acquisition freezes message
sources under recipient authority. Foreign operations never restore an unloaded target.

### File references in assignments and messages

Retain explicit [source references](../../modules/agentguidance/SPEC.md#source-reference-syntax)
in assignments, follow-ups, messages and BTW questions. Reuse bounded native
acquisition under recipient cwd and authority before acceptance. Freeze acquired
file/skill content with original text and attribution so later edits cannot change
an assignment. Preserve individual failures and literal escaping; retries do not reread.

Share parsing/acquisition with preload/reload. Composer attachments and preload keep
their separate meanings. Freezing source content is distinct from selecting already
retained change ranges for progress or final replies; those need no second copy.

## Prepared children and guidance

Use the invoking native step's actual settings, environments, cwd, provider,
permissions and role restrictions. Fix demonstrated inheritance gaps at native
preparation, not in parallel prepared-agent records or Go-built transcript imports.

Fresh children may acquire `preload:{sources:[...]}` before first inference through
the shared reload reader. Apply child authority and preserve partial failures and
native history retention. Full-history children use native inheritance; do not
replace stock parameter forms merely to match the old Go implementation.

Keep native model/role/mode guidance assembly. JavaScript exposure must not erase
role guidance or require a replacement collaboration skill. Remove obsolete Go
catalog/prompt rewriting when its consumers use the native interface.

## Full-history children and workspace

Use native forking for the complete authorized calling prefix, including context
replacements and attributed conversation/tool items. Do not reconstruct a transcript
or inherit execution, approval or process handles.

Omitted cwd inherits the admitted caller cwd; relative cwd resolves there. A child
workspace does not change its parent's workspace or expand permissions. Reuse native
environment preparation for fresh and retained recipients.

## Logical waits and ordered delivery

Keep stock V2's pending call, event subscription, timing and interruption handling.
Replace the finite-return `timeout_ms` control with optional `yield_time_ms`.
Omission disables timed yields and waits for an ordinary outcome, message or
interruption. No second deadline, polling scheduler or persisted wait-phase protocol.

Any mode continues on the first eligible selected outcome. All mode accumulates
selected outcomes before continuing; failure and interruption settle their slots.
Messages retain wake authority in either mode. Use native identities/consumption
rules so already-read completions are not selected again. Aggregation adds no
assignment or delivery states.

### Reset barriers

This correction completes without wait-specific `compact` or `reload` fields.
Standalone `functions.new_context` and reload remain supported. Special reset/wait
composition and its ready/pending ordering proof are deferred, not completion gates
for this correction. The following composition direction applies when that work resumes.

Compose `functions.new_context` and ordinary `wait_agent`, rather than adding
`compact` or `reload` to wait. Reload belongs to the reset operation. For a
reset-before-wait checkpoint, settle reset before starting the ordinary wait.
Resetting an already pending call uses native reset/execution ownership and preserves
its result for observation in the resulting context.

Ordering remains an implementation acceptance gate. Current `new_context`
acknowledges a request before reset settles; arbitrary same-cell sequencing is not
established as equivalent. Do not deliver a result and then silently discard it in
reset, or treat acknowledgement as completion. Any necessary ordering fix belongs
to the native reset/execution boundary, not compound-wait machinery. Prefer safe
native composition over a model round trip solely to coordinate the tools.

Retain summary-free reset, omitted-selection reuse, explicit-empty clearing,
authorized reload and visible failures. Test ready and pending results, cancellation,
manual reset and reload failure before claiming parity with removed wait fields.

### Completion and recovery

Keep native final delivery, interruption, restoration, shutdown and provider recovery.
Extra transitive-completion guards, generalized descendant recovery and concurrency
policy changes are not prerequisites. Establish separately required missing behavior
before changing those owners.

Attach selected captured changes through native successful final delivery. Freeze
exact retained ranges, then materialize through existing readers/output owners.
Preserve attribution, answer text, bounds and recovery handles without a new
completion-checkpoint ledger.

Reuse recipient-context capture tracking to omit intervals already delivered by
progress. Preserve unseen extensions and bounded-out evidence; failed/uncertain
delivery acknowledges nothing. Reset/context replacement restores needed evidence.
Explicit reads remain independent.

### Capture-aware progress

Optional `yield_time_ms` has a five-minute minimum. A due interval begins collection,
not a partial-result deadline. Completion, messages and interruption retain native
precedence. No new changes means no summary inference or wake; keep waiting.

Freeze exact newly reportable retained change ranges for changed selected agents,
including new intervals under an existing ID. Do not snapshot or copy changes.
Changes beyond those ranges belong to the next collection.

Reuse the existing BTW auxiliary executor internally for summaries. Use its cloned
history and request cancellation at completed-tool boundaries, not a separate
model-client sampling path. Pass range identifiers as summary metadata, not source
read requests. Use existing context and completed tool results with tools disabled.
Do not steer primary work, append summary traffic to primary history, replay tools,
discard continuation handles or wait for unrelated background processes to exit.

Collect every requested BTW result before one combined yield. A stopped/failed
target resolves with its actual unavailable reason. Reuse request correlation and
local aggregation: no collection timeout, public summary tool or durable wait/work
state machine. Higher-priority native events may cancel collection; late replies
cannot revive it.

Entries contain agent identity, substantive summary and exact change ranges.
Brevity is advisory, not a truncation/retry condition. Normal aggregate bounds retain
exact recovery. Only confirmed delivered intervals advance existing context-scoped
capture tracking. A yield is not task completion; another wait continues waiting.
Auxiliary metrics never gate successful delivery.

## Independent side questions

Branch before ordinary message delivery and reuse the native restricted auxiliary
executor. Answer from stable cloned recipient context, without earlier side-question
history or tools. Do not mutate primary history, assignment, waits or the human
side dock. Cancellation affects only that request; failure never falls back to
messaging or steering.

Progress shares this executor, not merely its design. Keep recipient-authorized
source acquisition for ordinary BTW questions separate from progress range metadata.

## Lifetime, presentation and recovery

Project native facts into terminal/browser/headless consumers. Preserve passive
retained visibility, actual child settings, unavailable measurements and independence
from view disconnection. Do not recreate Go lifecycle states to preserve display labels.

Human input, interruption, restore, rewind and shutdown stay at native owners.
Rewind does not imply continuous process handles; observation does not imply execution.
Product-local history remains readable without replaying effects. Retained-session
support is a cutover obligation, not a reason to retain a second coordinator.

## Feature use

Keep one launch-local native reducer fed by actual semantic events. Adapt emission
sites to stock owners, not port-specific lifecycle transitions. Count reset,
spawn/preload, delivered final changes, message/BTW/source sends, all-mode waits,
actually delivered progress and native model-input deduplication savings. Unchanged probes and failed/uncertain yields do not count
as delivered progress.

Expose snapshots on demand, without host-reflected events, duplicate counters or
per-use round trips. Measurement failure cannot retry, roll back or fail successful
work. General usage accounting stays separate.

## Correction strategy

Prefer a new implementation line from `de1b856b9d`, immediately after native source
reload and its generated artifacts, before `49b1bb95a9` introduced compound waits.
The parent of `3637d1a189` already contains that earlier workaround. Starting from
stock instead would unnecessarily discard the preceding Mekugi foundation.

This is a source-based safety recommendation, not an executed branch or runtime
validation result. The base keeps app-server/workspace, model-guidance, plugin-policy
and reset/reload work. It still disables native tools for hosted collaboration:
restore native JavaScript exposure narrowly, without copying unrelated concurrency
changes.

Keep HEAD as a capability reference, not a template. Deliver complete behavior-sized
commits. Do not cherry-pick compound waits or the large coordination port. Later small
feature commits are not independent: much of their machinery is inside the port.
Reuse native BTW/passive inspection, source acquisition and capture readers. Preserve
later unrelated personality cleanup, effective role guidance and demand-driven native
feature accounting. Regenerate protocol artifacts instead of importing removed APIs.

In-place refactoring has higher coupling risk across custom wait, delivery, recovery,
completion and SQL paths. A clean baseline makes stock deviations reviewable, but is
not a drop-in rollback: current host RPC consumers and stored coordination data depend
on the port. Adapt consumers and establish retained-session behavior before changing
the runtime pin/default. This record authorizes no implementation, branch deletion,
history rewrite or installation.

## Acceptance

- Prove stock native tool exposure, schemas, admission, scheduling, message/follow-up
  distinctions, interruption, completion and shutdown first.
- Exercise indefinite/any/all waits, messages, failures and interruption without a
  replacement work/wait ledger.
- Preserve standalone reset/reload and context-scoped capture behavior. Wait has no
  reset fields; special reset/wait composition and its validation are deferred.
- Check unchanged-agent silence, exact ranges, shared BTW execution, safe boundaries,
  out-of-order replies, unavailable targets, cancellation, one combined yield and
  output recovery. No duplicate change snapshots.
- Cover child preload/history, recipient authority/cwd, source failures, independent
  BTW and passive observations through actual consumers.
- Exercise existing local/remote cross-session communication and preserved direct
  operations without foreign control authority or local tree enrollment.
- Verify progress/final overlap, new intervals, bounded-out evidence and context reset
  against existing capture/context owners.
- Check regenerated native/host contracts, retained sessions, terminal/browser/headless
  publication and auxiliary feature accounting before cutover.

Source/documentation checks establish the design, not runtime parity. Report focused
native/host tests, live-provider behavior and rendered coverage separately.
