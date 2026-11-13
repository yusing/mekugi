---
name: mekugi-native-first
description: Design, implement, refactor, or review Mekugi native-runtime features and Go-to-Codex migrations without porting host workarounds.
---

# Native-first feature work

Deliver the requested behavior through existing native owners. Go's implementation,
and Rust code already ported from it, are evidence of current means, not requirements.

## Establish the native delta

Use the existing task ledger to distinguish the required outcome, the native
capability that already serves it, and the concrete missing behavior. Do not create
another planning artifact or make former Go schemas the acceptance contract.

Inspect the actual native producer and consumer before designing an extension.
Distinguish stock Codex facilities from fork-added machinery: consult the relevant
pre-change baseline when HEAD already contains the workaround being reconsidered.
Check differently named facilities and composition of existing operations, not just
matching tool names. Do not infer a host limitation from the Go adapter's design.

Examples of distinctions worth checking, not automatic implementation recipes:

- Reset may compose with waiting instead of requiring reset fields on the wait tool.
- Context-only inference may reuse BTW rather than another model-client path.
- Cross-session messaging may already exist through message boards; verify whether
  they also satisfy the requested addressing, notification and auxiliary operations.
- Retained changes may need frozen IDs/ranges, not another snapshot of their content.

## Extend the owner, not the workaround

Reuse native execution, scheduling, cancellation, message delivery, history,
permissions and auxiliary inference. Share their implementation, not merely copy its
design into a new helper. Preserve stock schemas and lifecycles unless a required
observable change needs a specific delta.

Treat Go-era registrations, acknowledgements, dispatch/consumption states, synthetic
continuations, polling, stores and callback protocols as candidates for removal, not
items to translate into Rust. For proposed new state, persistence, timers, transport
or snapshots, identify the concrete gap in existing native ownership first. Ordinary
aggregation of outcomes does not by itself require a new lifecycle.

Prove composition at its real boundary. A request acknowledgement is not completion;
check pending and immediately ready results, interruption and retained evidence where
those affect the promised behavior. Fix an ordering gap at the existing owner rather
than assuming composition works or rebuilding a coordinator around it.

## Build the smallest complete behavior

Make the smallest real consumer path work before adding supporting machinery. Derive
checks from required behavior, not old workaround fixtures. Keep each change usable
through its entry point, implementation, consumers and generated contracts; avoid a
large speculative foundation followed by commits that only expose its switches.

When a port is already entangled, compare an in-place refactor with reconstruction
from a known native baseline. Account for unrelated later fixes, current protocol
consumers and retained data before recommending either. A safer baseline is not a
drop-in rollback or authorization to create branches or rewrite history.

Report required behavior delivered, native facilities reused, remaining justified
deltas and concrete validation limits. Source inspection alone does not establish
runtime composition, compatibility or a reduction in cost.
