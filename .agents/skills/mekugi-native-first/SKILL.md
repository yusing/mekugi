---
name: mekugi-native-first
description: Design, implement, refactor, or review Mekugi native-runtime features and Go-to-Codex migrations without porting host workarounds.
---

# Native-first feature work

Deliver the requested behavior through existing native owners. Go's implementation,
and Rust code already ported from it, are evidence of current means, not requirements.

## Establish the native delta

Use the existing task ledger to compare the required behavior with what the native owner
already provides, what it lacks, and why the gap belongs in the proposed change. Inspect
plausible native producers and consumers, including differently named facilities, before
choosing a new mechanism. Do not make former Go schemas the acceptance contract.

Reusing generic native infrastructure does not establish that the feature owner is missing.
A new owner is justified by a distinct responsibility, not a different trigger, template, or
delivery destination. Extend the existing implementation where the responsibility remains
the same, preserving stock schemas and lifecycles except for required observable changes.

Distinguish stock facilities from fork-added workarounds using the relevant pre-change
baseline. Go-era registrations, acknowledgements, dispatch states, synthetic continuations,
polling, stores and callback protocols are candidates for removal, not translation into
Rust. Ordinary aggregation of outcomes does not itself require a new lifecycle.

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
