# Retained session history

## Requirements

- Preserve durable session evidence, namespace identity, and dependency-safe retention across
  compaction, forks, and restart without ever modifying or deleting Codex transcripts or workspace
  files.
- Retained records restore facts only, never live processes, approval grants, or execution
  authority.

## Current approach (replaceable)

- Stores replay/evidence records under `$XDG_STATE_HOME/mekugi/replay` (default
  `~/.local/state/mekugi/replay`) behind one process-independent publication boundary with
  dependency-before-reference ordering and cross-process leases protecting active turns, workers,
  and yielded calls.
- **Budgets & retention**: 4 GiB default shared cap (32 MiB/record, 256 MiB retained-output
  sub-quota, 16 MiB commentary sub-quota), 14-day inactive expiry, LRU eviction of inactive
  sessions under pressure via a background maintenance worker (never evicting leased/active work).
  Durable per-kind byte totals avoid scanning the store on admission; unrecognized record kinds
  keep dependencies conservatively intact.
- **Namespaces & forks**: subagents share the root namespace; ordinary forks freeze inherited
  ranges and clone indexes safely; allocation high-water marks outlive reclaimed payloads so
  handles never collide.
- Provides read-only `mekugi inspect-storage` validation (no writes, locks, or permission changes)
  and single-file atomic preference publication outside replay quotas.
- See [Record publication contract](../../docs/contracts/record-publication.md) for cross-owner
  transaction rules.
