# Activity

## Requirements

- Present host operations, tool invocations, agent traffic, and captured edits clearly without
  altering stock tool inputs, results, batch/parallel execution, or model context.
- Identify supported tools in the roster while their input is still streaming.
- Show accurate sender/recipient direction for agent traffic; hide internal Mekugi bookkeeping
  without losing inseparable ordinary command output; never duplicate an earlier reply on
  interruption.

## Current approach (replaceable)

- **Observation authority**: consumes host items, turn events, Conversation transcript publications,
  and captured-edit receipts across live sessions, restored history, and replay. Host outcomes and
  retained captures remain authoritative over source classification or transient streaming labels.
  `presentation` owns renderer-neutral feeds, roster derivation, active-work timing (paused during
  logical waits), and detail membership; `terminalview` owns layout, disclosure, pacing, and
  painting.
- **Source & edit presentation**: groups operations under the reasoning that preceded them;
  hides internal `mreload` invocations without dropping inseparable ordinary command output;
  classifies common shell reads/searches/listings/edits (`Read`, `Search`, `List`, `Inspect`,
  `Skill`, `Run`) without executing source or reading the filesystem; separates user shell commands
  (`You running`/`You ran`) from agent batches; replaces edit intent with confirmed captured counts
  and links to [Change history](../changehistory/SPEC.md) for exact-operation diff details (with
  tool-managed Git effects kept separate from authored edit totals, and ambiguous Git stat rows
  omitting file status when creation/deletion cannot be distinguished from an edit).
- **Retained output & bounds**: retains normalized command/segment output in memory (up to 1 MiB
  per output / 16 MiB shared per session, protecting running commands; newest 2,000 feed records)
  and replaces streamed output with the host aggregate on completion. Retains historical
  wait-target snapshots through [Session history](../sessionhistory/SPEC.md) (presentation-only;
  never restores live waits).
- See [Terminal UI guide](../../docs/guide/terminal-ui.md) for user-visible layout and dialogs.
