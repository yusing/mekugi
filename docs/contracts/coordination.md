# Native agent coordination

## Requirements

Inherited from [product requirements](../product.md#user-requirements):
- Keep stock Codex V2 as the owner of execution, agent membership, scheduling, messages,
  interruption, completion, restoration, and delivery. Do not maintain a second assignment ledger,
  wait state machine, or conversation store.
- Preserve one interactive UI, the stock JavaScript execution interface, stock non-wait tool
  schemas/names, and product-local resume.
- Fresh delegates can start preloaded with selected sources under their own permissions; assignments
  and messages can snapshot explicit source references at send time.
- Parents can wait for any or all current descendant outcomes without polling; addressed messages
  wake waits; opt-in progress yields pair changed-agent summaries with exact captured changes.
- Side questions (`btw`) answer from cloned context without mutating or steering primary work.

## Boundary contract

- **Authority & identity**: native identity resolution governs access; arguments and labels never
  grant authority. Address agents by relative/canonical task path or exact thread ID where
  supported. Foreign cross-session access grants no spawn, follow-up, interruption, reset, or
  cold-restore authority, and local failures never fall back to foreign targets.
- **Prepared children & source snapshots**: malformed top-level preload references reject spawn
  before starting a child; individual read failures remain visible alongside acquired sources.
  Message/assignment file references freeze under recipient authority at send time so later edits or
  retries do not alter them. Automatic message/final attachments omit changes authored by the
  recipient.
- **Waits & reset**: `wait_agent` accepts optional `wait_mode` (`any`/`all`) and `yield_time_ms`
  (no `timeout_ms`, `compact`, or `reload` fields). Omitting `yield_time_ms` disables timed yields.
  `new_context` owns summary-free reset and source reload separately (wait-specific reset
  composition is deferred; a `new_context` acknowledgement is not completion proof).
- **Capture-aware progress & final delivery**: no new captured changes means no progress summary or
  wake. Progress selects exact retained change ranges (never copies diffs) and uses the native
  restricted auxiliary executor with tools disabled, collecting all changed targets into one
  combined yield without mutating primary history. Final delivery attaches unseen retained ranges
  and advances tracking only on confirmed delivery.
- **Side questions**: branch before ordinary delivery via the restricted auxiliary executor (no
  tools, no primary mutation, no fallback to messaging/steering on failure).
- **Observability & feature use**: passive native inspection reads settings/context/compaction
  counts without starting or restoring unloaded threads. Native feature-use counters are demand-read
  and never gate execution.

## Current approach (replaceable)

- Native `thread_manager` and `app-server/src/agent_host.rs` own trees, forks, passive inspection,
  and connection-bound host resources; Go `sessionapi/coordination.go` and `executionobservation`
  supply direct foreign routing and retained capture-range selection.
- Full-history children use native history forking (inheriting settings/cwd without inheriting
  execution, approval, or process handles).
- Progress `yield_time_ms` enforces a 5-minute minimum interval.
- Cross-session communication reuses stock Codex local/remote message boards alongside direct
  foreign message/BTW/list/inspect routing over the destination's authenticated resource connection.
