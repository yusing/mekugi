# Mekugi product

## Problem

Repeated source reads, large tool outputs, and unclear progress can raise model
cost and make long-running work harder to follow. Mekugi adds bounded,
recoverable tools and visible progress around stock Codex.

## Outcome

Codex still owns editing, command execution, permissions, sandboxing, native
agents, patch review, and live command sessions. Mekugi observes completed work
for review and measures actual traffic without replaying effects.

Success is measured in this order:

1. Reduce model cost, including unnecessary round trips and payload size.
2. Make agent and user workflows clearer and more recoverable.
3. Improve agent performance without weakening correctness or host authority.

Controlled comparisons live in [codex-setup-ab](https://github.com/yusing/codex-setup-ab).
Provider usage and complete captured results take precedence over local estimates
or model prose.

## Product principles

- **Codex remains in charge.** Mekugi may expose executable frontends, persist evidence,
  observe, and display; Codex executes effects and owns their lifecycle.
- **Evidence must be usable.** Readers retain omitted output, and observed
  edits support focused review only after their result and workspace outcome
  are known.
- **Durability precedes dependent reads.** Resumes and branches recover
  completed visible history from durable records, not one live router process.
- **Auxiliary features stay non-invasive.** Progress, metrics, and diagnostics
  do not decide whether an edit, command, or provider response succeeds.
- **One component owns each meaning.** Features share observed facts instead of
  independently approximating execution outcomes.

Feature specifications own detailed behavior and acceptance. Shared
[interface](../doc/spec/index.md) and [ownership](../doc/architecture/index.md)
indexes link those owners rather than defining another copy of their contracts.

## User journeys

### Edit and review without replacing Codex

An agent reads only the source needed for a decision, calls stock
`apply_patch`, and sees a provisional live preview while the call streams.
After the host result and workspace outcome are known, a compact change ID
supports focused review and handoff. Failed or partial outcomes are not called
successful edits.

### Execute and resume work through Codex

An agent uses stock `tools.exec_command` through JavaScript `exec` and
can batch independent calls. Session IDs returned by Codex continue through
`write_stdin`; Mekugi does not create a competing process handle.

### Follow long-running work

Users see host progress, child activity, command outcomes, and
provider-authoritative usage without adding that display text to later model context.

### Automate and inspect without an interactive session

The headless client streams host messages and frontend events as JSONL. Offline
inspection and replay use retained evidence without executing recorded effects.

### Evaluate optimizations with real evidence

Operators compare isolated control and treatment runs using hidden correctness
graders and captured provider traffic. Task correctness is judged separately from
transport-only byte changes.

## Scope

Codex is the agent backend and the terminal is the interactive renderer.
Headless execution and offline replay retain their own command entrypoints.

Mekugi includes:

- bounded UTF-8 source reads, symbol lookup, structural inspection, and
  retained output continuation through authenticated executable frontends;
- observation of stock edits, change review, and provisional live previews;
- bounded foreground command output alongside stock JavaScript tool execution,
  batching, and yielded-session continuation;
- durable replay, session inspection, retained host facts, diagnostics, and
  provider-authoritative usage reporting;
- authenticated third-party model routing; and
- content-safe captured metrics for evaluating real traffic.

User installation, configuration, and commands belong in the
[README](../README.md).

## Non-goals

Mekugi does not:

- replace Codex's sandbox, permission flow, patch UI, native agent lifecycle,
  or process control;
- provide direct execution, runtime plugin loading, a browser renderer, or a
  second agent backend;
- provide an interactive editor, a hosted benchmark service, or a second
  conversation store;
- make binary or non-UTF-8 content editable through source readers;
- promise crash-atomic multi-file filesystem updates;
- infer missing evidence, equate local token estimates with provider billing,
  or judge correctness from a model's final prose;
- make activity presentation, diagnostics, or metrics part of task execution
  semantics; or
- promise that an optimization lowers latency, cost, or error rates without a
  matching controlled measurement.
