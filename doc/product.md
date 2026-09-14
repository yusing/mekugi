# Mekugi product specification

## Problem

Stock Codex is the execution authority, but its ordinary editing and execution
interfaces can make agents repeat source context, wrapper syntax, and recovery work.
That raises model cost and makes long-running work harder for users to follow and
resume. Provider and client behavior can also be difficult to evaluate without
content-safe evidence from the real request path.

## Outcome

Mekugi adds a compact, recoverable workflow around stock Codex without replacing its
authority. Agents can edit from verified source references, run native scripts through
Codex-owned execution, recover rejected edits, and report durable milestones. Users
retain Codex permissions, sandboxing, process lifecycle, and patch review while gaining
clearer progress, session evidence, and optional model or representation optimizations.

Success is measured in this order:

1. Reduce model round trips and payload size.
2. Make agent and user workflows clearer and more recoverable.
3. Improve agent performance without weakening correctness or host authority.

The [benchmark methodology](benchmarks.md) defines how performance claims are tested.
Provider usage and complete captured results take precedence over local estimates or
model prose.

## Product principles

- **Codex remains in charge.** Mekugi may translate, persist, observe, and display,
  but Codex executes effects and owns permissions, sandboxing, native agents, and live
  command continuation.
- **Evidence must be usable.** Read results can become edit targets, edit results can
  support follow-up work, and failures identify what can safely be retried or repaired.
- **Durability precedes exposure.** Resumes and branches recover visible history from
  durable records rather than depending on one live router process.
- **Auxiliary features stay non-invasive.** Progress, metrics, diagnostics, and
  compression cannot decide whether an edit, command, or provider response succeeds.
- **One component owns each meaning.** The engine, portable core, router, capturer,
  and carrier renderer share results instead of independently approximating them.

The [interface requirements](spec/index.md) define observable behavior and acceptance
criteria. The [architecture contracts](architecture/index.md) define ownership and
boundaries.

## User journeys

### Edit and review with less repeated context

An agent reads or searches only the source needed for the next decision, copies verified
references into one atomic edit, and receives compact current references plus a reviewable
Codex patch. If the script is rejected, the agent can repair the retained input without
re-emitting unrelated edits.

### Execute and resume work through Codex

An agent submits Bash, POSIX shell, or another interpreter's native source without a
second wrapper language. Codex still authorizes and runs the carrier. Yielded work is
continued through the host-provided session rather than replayed.

### Follow long-running work

Agents keep durable, addressable milestones. Users see meaningful live updates, child
activity, final grouped results, and provider-authoritative usage without adding that
display text to later model context.

### Evaluate optimizations with real evidence

Operators compare isolated control and treatment runs using hidden correctness graders
and captured provider traffic. Optional compact representation and model handoff features
are judged separately from task correctness and from transport-only byte changes.

## Scope

Mekugi includes:

- verified UTF-8 source reading, search, symbol lookup, structural inspection, and
  atomic multi-file editing;
- edit validation, formatting-aware result projection, change review, and rejected-input
  recovery;
- a free-form script interface with batching, retained source, and host-owned continuation;
- router-local tool declarations translated to Codex-executed carriers;
- durable replay, journals, session inspection, commentary, diagnostics, and
  provider-authoritative usage reporting;
- optional compact provider representation, model handoff, and authenticated third-party
  subagent routing; and
- reproducible historical-workspace benchmarks with correctness gates and content-safe
  metrics.

User installation, configuration, and commands belong in the [README](../README.md).
Detailed behavior belongs to the corresponding file in the specification index.

## Non-goals

Mekugi does not:

- replace Codex's sandbox, permission flow, patch UI, native agent lifecycle, or process
  control;
- provide an interactive editor, a hosted benchmark service, or a second conversation
  store;
- make binary or non-UTF-8 content editable through the verified-row workflow;
- treat hashes as writer locks or promise crash-atomic multi-file filesystem updates;
- infer missing evidence, equate local token estimates with provider billing, or judge
  correctness from a model's final prose;
- make generated commentary, diagnostics, or metrics part of task execution semantics; or
- promise that an optimization lowers latency, cost, or error rates without a matching
  controlled measurement.
