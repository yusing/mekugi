# Change evidence

## Requirements

- Retain live diffs and durable captured-change review for stock `apply_patch` and shell writes
  without replacing native execution, batching, tool arguments, or host results.
- Preserve durable evidence and retention across optimizations; missing or incomplete evidence must
  be labeled explicitly, never presented as complete or as a no-op.

## Boundary contract

- **Observation only**: Codex owns editing, execution, permissions, and continuations. Capture never
  replays effects, alters tool arguments/results, or completes a yielded call before its terminal
  continuation. Failed commands keep any bytes they actually wrote.
- **Publication before reference**: immutable per-file evidence is persisted before change IDs or
  completed receipts become usable. Storage failure never advertises durable evidence; missing or
  corrupt records fail explicitly rather than becoming empty diffs.
- **Coverage honesty**: captured evidence describes observed file differences within witnessed
  windows, not a live Git diff or proof that unobserved effects did not occur. Concurrent external
  writers cannot be separated without host per-write facts; missing baselines, expired reads, or
  unavailable checkpoints retain explicit incomplete/partial coverage.
- **Provenance separation**: VCS operations that import, discard, or write repository state are
  diagnostic history (`mchanges --history`), excluded from authored edit counts, saved Diff, rewind,
  and child handoffs. Named interpreter writes in mixed windows remain authored.
- **Delivery**: change IDs are appended only after persistence without modifying original host text;
  output deduplication runs only after observation/retention consume originals. Provisional live
  previews never establish durable evidence.

## Current approach (replaceable)

- **Named pre-execution capture** reads bounded baselines for stock patches (literal or via
  synchronous native pre-tool hooks for dynamic patches in `exec` cells) and statically recognized
  shell writers (`cat`/`tee`/redirections, `mv`/`rm`, `sed -i`/`perl`, named formatters, literal
  Python/JS writes, `mchanges apply`/`revert`, plus tracked `mktemp` paths), including named ignored
  files.
- **Bounded workspace fallback** snapshots non-read-only commands using private Git state (never
  touching user Git config/hooks/filters) with ignore rules, per-directory checkpoints for
  `cd`/worktrees/symlinks, a 1s acquisition/comparison budget, 20k-file cap without Git ignore, and
  512 MiB private object reset.
- **Overlap & interval tracking**: parent/child runtimes share claim tables so an unnamed diff is
  claimed once; intervening named edits split open writers into ordered intervals under one change
  ID. Per-file chunked evidence spills past an 8 MiB inline-text budget; synchronous pre-tool
  acquisition shares a 500 ms deadline.
- See [Change history](../../modules/changehistory/SPEC.md) for `mchanges` and saved review, and
  [Live preview](../../modules/livepreview/SPEC.md) for provisional streaming diffs.
