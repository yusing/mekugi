# Mekugi product specification

## Problem

Stock Codex is the execution authority, but repeated source reads, large tool
outputs, and unclear progress can raise model cost and make long-running work
harder to follow. Provider and client behavior also needs evidence from the real
request path before optimization claims are credible.

## Outcome

Mekugi adds bounded, recoverable tools and visible progress around stock Codex.
Codex still owns editing, command execution, permissions, sandboxing, native
agents, patch review, and live command sessions. Mekugi observes completed work
for review and measures actual traffic without replaying effects.

Success is measured in this order:

1. Reduce model round trips and payload size.
2. Make agent and user workflows clearer and more recoverable.
3. Improve agent performance without weakening correctness or host authority.

Controlled comparisons live in [codex-setup-ab](https://github.com/yusing/codex-setup-ab).
Provider usage and complete captured results take precedence over local estimates
or model prose.

## Product principles

- **Codex remains in charge.** Mekugi may project tools, persist evidence,
  observe, and display; Codex executes effects and owns their lifecycle.
- **Evidence must be usable.** Readers retain omitted output, and observed
  edits support focused review only after their result and workspace outcome
  are known.
- **Durability precedes dependent reads.** Resumes and branches recover
  completed visible history from durable records, not one live router process.
- **Auxiliary features stay non-invasive.** Progress, metrics, and diagnostics
  do not decide whether an edit, command, or provider response succeeds.
- **One component owns each meaning.** The router, executable frontends,
  capturer, and live viewer share observed facts instead of independently
  approximating execution outcomes.

The [interface requirements](spec/index.md) define observable behavior and
acceptance criteria. The [architecture contracts](architecture/index.md)
define ownership and boundaries.

## User journeys

### Edit and review without replacing Codex

An agent reads only the source needed for a decision, calls stock
`apply_patch`, and sees a provisional live preview while the call streams.
After the host result and workspace outcome are known, a compact change ID
supports focused review and handoff. Failed or partial outcomes are not called
successful edits.

### Execute and resume work through Codex

An agent uses stock `exec_command`, directly or in Code Mode JavaScript, and
can batch independent calls. Session IDs returned by Codex continue through
`write_stdin`; Mekugi does not create a competing process handle.

### Follow long-running work

Agents keep durable, addressable milestones. Users see live updates, child
activity, final grouped results, and provider-authoritative usage without
adding that display text to later model context.

### Evaluate optimizations with real evidence

Operators compare isolated control and treatment runs using hidden correctness
graders and captured provider traffic. Model handoff is judged separately from
task correctness and transport-only byte changes.

## Scope

Mekugi includes:

- bounded UTF-8 source reads, symbol lookup, structural inspection, and
  retained output continuation through authenticated executable frontends;
- observation of stock edits, change review, and provisional live previews;
- stock Code Mode JavaScript and host-owned execution with batching and
  yielded-session continuation;
- router-local executable plugin declarations;
- durable replay, journals, session inspection, commentary, diagnostics, and
  provider-authoritative usage reporting;
- optional model handoff and authenticated third-party model routing; and
- content-safe captured metrics for evaluating real traffic.

User installation, configuration, and commands belong in the
[README](../README.md). Detailed behavior belongs to the corresponding file
in the specification index.

## Non-goals

Mekugi does not:

- replace Codex's sandbox, permission flow, patch UI, native agent lifecycle,
  or process control;
- provide an interactive editor, a hosted benchmark service, or a second
  conversation store;
- make binary or non-UTF-8 content editable through source readers;
- promise crash-atomic multi-file filesystem updates;
- infer missing evidence, equate local token estimates with provider billing,
  or judge correctness from a model's final prose;
- make generated commentary, diagnostics, or metrics part of task execution
  semantics; or
- promise that an optimization lowers latency, cost, or error rates without a
  matching controlled measurement.
