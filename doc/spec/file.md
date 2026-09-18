# File baselines

## REQ-FILE-001 — File baselines

Each script is bound to an existing regular UTF-8 file by its outer invocation.
An invocation has one immutable baseline for each resolved path. Multiple scripts
for the same path reuse its baseline and retain pending disjoint edits. No command makes pending content
available as a new target baseline inside the script.

Hpatch does not create, move, or remove files or directories. Those are outer-shell
operations and are not part of the hpatch transaction. An empty file created by the
shell can receive content through `append VALUE`.

All content changes remain in memory until the complete invocation crosses the
apply or translation boundary. A failure in any file prevents evaluation success
for every file. Application and rollback guarantees remain in
[REQ-OUTPUT-001](output.md).

Acceptance:

1. An invocation can supply scripts for several paths without shifting their targets
   or losing pending disjoint edits.
2. Every operation rejects a missing path before external mutation or patch output.
3. Empty existing files accept append; introduced content is not targetable in the
   same invocation.
4. Failure or cancellation during evaluation exposes no intermediate change.
