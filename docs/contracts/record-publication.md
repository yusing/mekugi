# Retained authority and publication

## Requirements

- Optimizations preserve durable session evidence and dependency-safe retention.
- Saved Mekugi sessions remain resumable after execution stops; resume restores retained facts,
  never live processes, grants, or execution authority.
- Resume is product-local (Mekugi and stock Codex resume only their own sessions).

## Boundary contract

- **Authority & provenance**: filesystem operands resolve from admitted request workspace metadata,
  never application cwd. Evidence reads keep caller authority; cross-thread inputs preserve
  authenticated source provenance (agent text never becomes human input).
- **Ordered publication**: dependencies are written and validated before referencing records or
  handles become usable. Cross-owner operations (`recordtransactions`) borrow one non-nested
  transaction scoped to evidence publication, input adoption, or fork initialization, never
  spanning terminal or network I/O. Failed writes may leave conservative retention but never expose
  missing evidence. This guarantees ordered reader visibility across restarts, not crash-atomic
  multi-file or power-loss durability.
- **Lease safety**: snapshot leases bridge validation to publication locks without deadlock; session
  leases protect retained evidence until consumer delivery completes. Compaction removes ancestry
  from one request view without retiring records needed by another branch.
- **Owner boundaries**: [Session history](../../modules/sessionhistory/SPEC.md) owns namespaces,
  catalogs, leases, and retention; feature modules own their record codecs and retirement policies;
  [Change evidence](change-evidence.md) owns capture reconciliation;
  [Executable frontends](../../apps/mekugi/internal/frontends/SPEC.md) owns worker delivery;
  [Diagnostics](../../modules/diagnostics/SPEC.md) owns private debug bundles separately.

## Current approach (replaceable)

- Historical mixed-call records use the frozen `libraries/legacycall` codec validated whole before
  feature projection; new features publish owner-typed records with neutral dependency graphs.
- Fork initialization retains inherited records before cloning indexes and publishes the new
  namespace last so interrupted setup is retryable.
