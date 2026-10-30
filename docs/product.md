# Mekugi product

## Problem

Long agent sessions degrade through their context. Repeated reads and large
outputs crowd the window, compaction summaries drift from the sources they
describe, and fresh delegates spend their first turns rediscovering what their
parent already knew. Meanwhile users cannot see what agents are doing or why
until after the turn. Each of these raises model cost and makes work harder to
follow, correct, and recover.

## Outcome

Codex still owns editing, command execution, permissions, sandboxing, native
agent execution, patch review, and live command sessions. Mekugi manages what
enters the model's context around those stock tools, observes completed work for
review, and measures actual traffic without replaying effects.

Success is measured in this order:

1. Reduce model cost, including unnecessary round trips and payload size.
2. Make agent and user workflows clearer and more recoverable.
3. Improve agent performance without weakening correctness or host authority.

Controlled comparisons live in [codex-setup-ab](https://github.com/yusing/codex-setup-ab).
Provider usage and complete captured results take precedence over local estimates
or model prose.

## Product principles

### Execution authority

- **Codex remains in charge.** Stock execution and editing tools keep their
  names, schemas, results, and authority. Mekugi adds executable helpers that run
  through stock command execution instead of custom editing tools, edit
  languages, or rewritten transports. A replacement interface has to be taught,
  guarded, and repaired in every request; the stock interface already is. Agent
  coordination is the one replaced interface: the `threadcontrol` tools replace
  native collaboration while thread execution stays with the backend.
- **Auxiliary features stay non-invasive.** Progress, metrics, and diagnostics
  do not decide whether an edit, command, or provider response succeeds.
- **One component owns each meaning.** Features share observed facts instead of
  independently approximating execution outcomes.

### Context management

- **Context comes from current sources.** When context is rebuilt, after
  compaction or for a fresh delegate, the sources the work needs are read again.
  A summary carries the task; it never stands in for source text, and neither do
  agent-maintained notes or generated digests.
- **Reduce output tokens by reference, never by hiding.** Bounded output always
  has an exact continuation, and repeated text is replaced only by a reference to
  text still visible to the model. Output is not filtered by guessed relevance.
  These projections change model input only; host results, retained evidence,
  and the UI keep the complete text. Instruction text is removed only where its
  author marked it or it duplicates or conflicts with supplied guidance.
- **Supply known facts before they are requested.** Codex agents receive
  workspace and Git state and measured context usage, and attached files and
  skills are labeled as reusable, so agents do not spend turns rediscovering them.
- **Hand off exact handles, not paraphrase.** Read references and captured-change
  IDs or ranges address the same evidence across agents, compaction, resume, and
  forks. A reviewer handed a change range inspects that work, not a worktree diff
  that mixes in other agents' and the user's edits.
- **Corrections act on exactly what was done.** A rejected change is undone by
  reverting the captured edits that made it, not by restoring files from version
  control, so unrelated work, including the user's uncommitted edits, survives
  and the correction itself stays reversible.
- **Ride existing generations.** Compaction selections, child reply summaries,
  and similar structure are authored within a reply the model already makes, with
  no separate summarization, selection, or repair request.
- **Agents choose their checkpoints.** Agents compact at points they judge safe,
  informed by measured usage. Mekugi adds no automatic compaction threshold and
  leaves provider and host compaction policy intact.
- **Model-facing text stays minimal.** Guidance, tool descriptions, and results
  preserve meaning in the fewest tokens, without decoration.

### Observability

- **Show what the agent is doing, will do, and has done.** Running commands,
  child activity, and edits streaming into the Diff pane show current work.
  Reasoning, delegate assignments, pending approvals, and queued input show what
  comes next. Saved changes, retained output, transcripts, metrics, and replay
  show what happened.
- **Catch problems while they can still be stopped.** Edits are previewed
  while they stream, command output is visible per command, and sensitive
  commands wait for approval, so the user can steer, interrupt, stop one
  process, or deny a command instead of discovering the result after the turn.
- **Keep the why beside the what.** Operations are grouped under the reasoning
  that preceded them, agent start rows carry their assignments, child replies
  keep supporting details behind their summary, compaction summaries stay
  readable, and incomplete edit captures keep their reasons.
- **Label uncertainty.** Provisional previews, unknown counts, missing evidence,
  and lower bounds are marked as such rather than shown as confirmed results.
- **Watching costs the model nothing.** Display-only activity, previews, and UI
  metrics are not added to model context.

### Evidence

- **Evidence must be usable.** Readers retain omitted output, and observed
  edits support focused review only after their result and workspace outcome
  are known.
- **Durability precedes dependent reads.** Resumes and branches recover
  completed visible history from durable records, not one live process.
- **Savings claims require measurement.** Local estimates guide projection
  decisions; only controlled comparisons establish lower cost or better results.

Feature specifications own detailed behavior and acceptance. The
[documentation index](index.md) links their owners and the shared contracts.

## User journeys

### Keep a long session grounded

An agent works through a long task while measured usage notices report how full
its context is. At a safe checkpoint, or when the window fills, compaction writes
a handover summary and selects the files, skills, and captured changes the
unfinished work needs. Mekugi reads those sources again before the conversation
continues, so the agent resumes from current text rather than its own summary.

### Delegate with prepared context

A parent starts a fresh delegate with selected sources read under the
delegate's permissions and supplied before its first inference. The delegate
begins work without rereading them, replies
with a concise summary and supporting details, and its edits carry
captured-change IDs. The parent hands those exact ranges to reviewers or reloads
them, instead of describing the change in prose. Delegates can coordinate
their own children and message their parent while working.

### Undo what the user rejects

The user objects to part of an agent's work, such as an unwanted dependency
switch or a compatibility path. The agent reverts the captured edits behind that
part, scoped to the affected paths, while the rest of the task, other agents'
edits, and the user's own changes stay in place. Overlapping edits are merged,
conflicts are marked rather than guessed, and the revert can be reapplied.
`/rewind` offers the same
captured-edit reversal when branching before an earlier turn.

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
provider-authoritative usage without adding that display text to later model
context. Terminal and browser clients attach to the same server-owned threads.

### Automate and inspect without an interactive session

The headless client streams host messages and frontend events as JSONL. Offline
inspection and replay use retained evidence without executing recorded effects.

### Evaluate optimizations with real evidence

Operators compare isolated control and treatment runs using hidden correctness
graders and captured provider traffic. Task correctness is judged separately from
transport-only byte changes.

## Scope

Codex is the root execution backend. Claude Code runs only as an optional
delegate backend under the same thread coordination. Headless execution and
offline replay retain their own command entrypoints.

Mekugi includes:

- compaction that reloads selected sources, agent-chosen compaction checkpoints,
  measured context usage notices, and workspace facts in base instructions;
- fresh delegates with preloaded sources, coordinated waits, and structured
  child replies;
- bounded UTF-8 source reads, symbol lookup, structural inspection, bounded
  command output, and retained output continuation through authenticated
  executable frontends;
- duplicate-output references in model input and explicit instruction omission;
- observation of stock edits, change review, exact revert and reapply of
  captured edits, and provisional live previews;
- server-owned threads with terminal and browser clients;
- durable replay, session inspection, retained host facts, diagnostics, and
  provider-authoritative usage reporting;
- authenticated third-party model routing; and
- content-safe captured metrics for evaluating real traffic.

Installation and a quick start belong in the [README](../README.md);
configuration and commands belong in the [user guides](../README.md#guides)
under `docs/guide/`.

## Non-goals

Mekugi does not:

- replace Codex's sandbox, permission flow, patch UI, native agent execution,
  or process control, or replace stock tools with custom editing or transport
  protocols;
- provide direct execution, runtime plugin loading, or a root agent backend
  other than Codex;
- filter tool output by inferred relevance, or replace source reads with
  generated summaries;
- add its own cross-session memory, or share resumable sessions with stock
  Codex in either direction;
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
