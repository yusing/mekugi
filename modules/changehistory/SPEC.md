# Change history

## Requirements

- Passively capture observed edits from stock `apply_patch` and shell writes under durable short
  change IDs, supporting cross-agent review across each record's authored workspace.
- Support surgical reversal (`revert`) and reapplication (`apply`) of selected captured edits
  (including path-scoped and `/rewind` reversals) without discarding unrelated or uncommitted work.
- Keep model-authored rewind scoped to the caller's own captured edits.

## Current approach (replaceable)

### Capture and storage

- Works with [Change evidence](../../docs/contracts/change-evidence.md) and
  [Session history](../sessionhistory/SPEC.md) to acquire bounded pre-edit baselines, reconcile
  endpoint differences (plus private Git workspace checkpoints), allocate stream-scoped IDs (e.g.
  `amber1`), and persist per-file chunked evidence before exposing IDs.
- Diagnostic VCS operations (e.g. `git checkout`/`merge`) are retained under `--history` only and
  excluded from authored counts, saved Diff, rewind, and child handoffs.

### `mchanges` command

```text
mchanges --list [ID[..ID] ...] [--max-tokens N]
mchanges [--mine | ID[..ID] ...] [--summary|--history] [--max-tokens N] [PATH ...]
mchanges revert|apply ID[..ID] ... [--max-tokens N] [PATH ...]
```

- Bare `mchanges` / `--mine` selects the calling thread's IDs; explicit IDs/ranges (including
  abbreviated `amber1..3`) read across agents in the shared namespace without reading the live
  workspace. Default view outputs one composed patch; `--summary` outputs composed status/counts;
  `--history` outputs per-attempt inputs, host outcomes, and diagnostics. Incomplete, pending, or
  retired IDs remain explicit (never silently empty diffs), and `--max-tokens` freezes a
  continuation for `mread`.
- `revert` (latest-first) and `apply` (capture-order) require explicit IDs and all dependencies,
  locate hunks via anchored context, three-way merge drifted regions (emitting git-style conflict
  markers rather than guessing), write in-memory results cleanly, and are themselves captured as new
  change records with an `undo:` hint.

### Saved review presentation

- Publishes an immutable composed review snapshot per attachment for the Diff pane, caller filters,
  and Activity exact-operation dialogs (omitting pure `/tmp` captures outside the workspace and
  net-empty files while keeping moves, empty-file ops, binaries, and incomplete evidence visible).
- See [Terminal UI](../../docs/guide/terminal-ui.md#live-diff-pane) and
  [Agent tools](../../docs/guide/agent-tools.md#captured-changes) for user controls and behavior.
