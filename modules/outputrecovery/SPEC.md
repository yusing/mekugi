# Output recovery

## Requirements

Inherited from [product requirements](../../docs/product.md#user-requirements): allow agents to
page omitted command, file-read, symbol, outline, and change-review output without re-running the
producer, scoped strictly to the owning session family across compaction, forks, and restart.

## Current approach (replaceable)

- Exposes the authenticated `mread` helper (`mread REF [REF ...] [--stdout|--stderr] [--max-tokens N]`)
  through [Executable frontends](../../apps/mekugi/internal/frontends/SPEC.md).
- Allocates short lowercase handles (e.g. `maple`) in one durable namespace shared by a root thread
  and its subagents (snapshotted on fork/side thread; isolated across unrelated sessions; expired
  handles are never recycled).
- Default page ceiling is 8,000 tokens (max 15,500, framing included), admitting complete rows or
  JSON array elements (byte continuation for oversized rows). Multi-handle reads share one budget
  and emit one combined `next_call: mread ...` for unfinished handles; failed handles report
  qualified stderr without suppressing valid pages.
- Persists records through [Session history](../sessionhistory/SPEC.md) (16 MiB per omitted
  payload, 256 MiB aggregate read quota) before exposing any reference; continuation cursors store
  positions/fingerprints without duplicating payload bytes. Also backs compaction source snapshots
  and attachment recall hints.
