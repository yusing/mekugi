# Claude delegate backend

## Scope and authority

Provide Claude Code delegate execution through the official TypeScript Agent SDK.
The Codex parent and its JavaScript execution interface remain unchanged. Thread
Control owns logical identity, assignments, waits and delivery; the application owns
the runtime ledger and integrates the backend's typed process and tool events.
Claude owns its native execution under the captured approval policy and role
capability limits. Claude delegates run without filesystem or network sandboxing.
Unavailable approval semantics, models, runtime dependencies and authentication
fail explicitly rather than falling back to another backend.

The backend transport, approval policy and session-control integration are
independent of delegate admission. A future standalone Claude entrypoint can reuse
those owners; this integration exposes Claude only through Thread Control.

## Runtime

Delegates load native user, project and local Claude settings. Claude owns instruction
discovery and exclusions, memory preferences, and commit/PR attribution. Mekugi
appends captured custom/role instructions, coordination guidance and the authenticated
shell frontend catalog; Codex JavaScript execution guidance stays with Codex.
Captured approvals, role reductions and Thread Control coordination remain authoritative.
Native delegation and team tools are disabled. Native main-agent selection cannot
replace the captured Codex role or model, and native role catalogs stay out of
provider requests. Claude receives the captured Codex agent catalog for Thread
Control admission.

On-demand model discovery reads the SDK initialization catalog without submitting
input, with transcript persistence disabled. The application closes and joins the
discovery process before returning qualified model identifiers. Ordinary Codex
startup does not acquire the optional Claude runtime. Discovery returns native
advertised choices; provider authentication and entitlement still govern execution.

A subprocess worker uses a persistent streaming-input query. Its stdout carries
only correlated protocol messages; diagnostics remain separate. The worker supplies
native session, input and tool-use identities. Readiness requires successful SDK
initialization and approval-policy admission, with the selected session identity
frozen before dispatch and checked against native events. Ordinary Codex
operation does not require the optional Bun/Claude runtime.

The `never` approval policy admits a captured `auto_review` reviewer without
starting a reviewer. Other automatic-review policy combinations remain
unsupported, and unknown reviewer values reject before startup.

Release builds bundle the SDK worker with Bun and embed it in the executable.
Claude delegates require Bun and Claude Code on PATH. Source builds without
the worker bundle report the missing build step only when admitting a Claude
delegate; they keep ordinary Codex execution available.

The host retains attributed inputs before acknowledgement and dispatches them at
admitted lifecycle boundaries. Idle messages do not start work. Interruption stops
the actual active generation and does not release queued assignments. A native
result ends a turn; closing the owned runtime waits for process cleanup. Resume
uses the exact retained SDK session ID and reconciles uncertain input acceptance.
Unconsumed attributed inputs remain in the host request ledger until native
consumption is confirmed; non-querying SDK input alone does not prove durability.
Forks use the backend's session-fork operation without replaying tools.

## Tools and coordination

Authenticated MCP calls bind identity to the admitted runtime and current turn,
never to model-supplied arguments. Coordination uses the existing logical tree and
operation handlers. A successful waiting tool batch stops through `PostToolBatch`
before further inference. The actual `hook_stopped` terminal result settles the
host turn while preserving logical waiting. Wakes use synthetic non-user input
with explicit original tool and sender attribution.

Agent context checkpoints use the same successful batch stop. After its exact
hook-stopped terminal settles, Session Control sends synthetic coordinator input
containing exactly `/compact`, with its own UUID and no attributed-text prefix.
Native success requires an explicit successful compaction result, a manual compact
boundary, and terminal settlement matching that input. A failed compaction result
overrides generic terminal success. The auxiliary command never completes the
logical assignment or consumes retained messages; Thread Control releases the
original continuation or wait after settlement.

Private executable frontends share the parent's retained namespace under a distinct
child identity. Native tool hooks acquire edit baselines before execution and
publish actual terminal effects through Change History. Post-tool context carries
durable receipts separately from the original native result, including failures.
Pending/background tools retain their capture until actual settlement.
If joined runtime shutdown leaves a tool without a terminal hook, observed file
effects remain capturable with an unknown outcome and no invented exit status.

## Presentation and failure

Claude members use the ordinary roster but do not expose a navigable attachment.
Roster metrics include observed model, turn counts, logical-work timing, captured
edits, SDK-reported cumulative tokens and estimated cost, and reported model roundtrips.
Repeated SDK totals do not add consumption, and forks exclude their source's
consumption. Context uses the SDK's lightweight summary after generating turns,
independently of native event delivery. Completed compaction boundaries contribute
to inspection. Live and retained
inspection share the latest observations without starting work. Missing facts
stay absent, and incomplete accounting stays explicit. SDK turn durations do not
establish provider-request throughput.
Transport loss, authentication failure, policy rejection and interruption remain
distinct outcomes; uncertain dispatch is never retried as if it had not happened.

## Acceptance

Exercise spawn, retained-only input, steering/follow-up, interruption, exact-ID
resume, nested waits, ordered final delivery and no provider continuation during
waiting through the real adapter. Verify private-PATH parent-range reads, paged
recovery, child attribution, native successful/failed/interrupted edit receipts,
unavailable metrics and disabled navigation. Local mock-provider checks and live
authenticated acceptance are reported separately.
