---
name: mekugi-native-first
description: Design, implement, refactor, or review Mekugi native-runtime features and Go-to-Codex migrations without porting host workarounds.
---

# Native-first feature work

Deliver requested behavior through existing native owners. Go code (and Rust already ported from
it) is evidence of current means, not requirements.

## Establish the native delta

Compare required behavior against what the native owner already provides, what it lacks, and why
the gap belongs in this change. Inspect plausible native producers/consumers (even under different
names) before inventing a mechanism; do not make former Go schemas the acceptance contract.

- A new owner needs a distinct responsibility, not just a different trigger, template, or delivery
  destination. Extend existing owners and preserve stock schemas/lifecycles except for required
  observable changes.
- Separate stock facilities from fork workarounds using the pre-change baseline. Go-era
  registrations, acks, synthetic continuations, polling, duplicate stores, and callback protocols are
  removal candidates, not things to translate into Rust.
- Prove composition at the real boundary: an acknowledgement is not completion. Check pending and
  immediately ready results, interruption, and retained evidence; fix ordering gaps at the existing
  owner rather than wrapping another coordinator around it.

## Build the smallest complete behavior

Make the smallest real consumer path pass before adding supporting machinery. Derive tests from
required behavior, not legacy workaround fixtures. Keep entry points, consumers, and generated
contracts aligned in each step.

When a port is already entangled, compare in-place refactoring against rebuilding from a clean native
baseline, accounting for unrelated later fixes, active protocol consumers, and retained data,
before recommending either (never rewrite history or create branches without authorization).

Report required behavior delivered, native facilities reused, justified deltas, and validation
limits. Source inspection alone proves neither runtime composition nor cost reduction.
