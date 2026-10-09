# Filesystem, history, and output boundary

## CTR-BOUNDARY-001 — Host effects and durable observation

Codex owns editing, filesystem permissions, sandboxing, command execution, and
live process continuation. Mekugi receives the selected metadata directory
from the request. Relative observation paths require that directory; the
router never substitutes its own cwd or imposes an unrelated root-library
confinement policy. Observation cannot authorize a host effect.

The replay store retains completed stock-call identity, bounded known-edit
baselines, endpoint differences, change IDs, host results, and managed omitted
output. Hook observations use the same managed publication and retention owner.
Records are scoped by metadata workspace, thread, and hook run identity; resume
and offline replay read published observations without restoring process resources.
Recording uses source-resolved edit operands and their link targets,
plus differences between private workspace snapshots taken around writer
commands. Ignore rules decide only which unnamed paths a snapshot covers; they
never exclude a source-named target. Snapshots are a private Git directory and
index per workspace under the replay store, serialized across router processes
by a file lock. They read the user's repository objects, index, and ignore
files but never write its state or run its hooks, filters, or configuration.
Durable call observations keep only a snapshot tree name; the snapshot itself
is comparison state, not record evidence, and its size and idle lifetime are
bounded by the snapshot owner rather than record retention. The in-memory window
registry is not durable authorization or process state. Replay validates
retained identity without rerunning an edit or command.
Last-seen content and running preview state are bounded, process-local auxiliary
state. Last-seen lookups are isolated by durable record namespace; previews use
the captured scope and the broker's viewer lifetime, not the request lifetime.
Neither is restored as a process or execution authority during replay.
Fork and resume views use visible durable facts. Compaction removes invisible
ancestry from one request view without deleting records needed by other
branches. Expired output references and Codex process handles are not revived.

The shared persistence primitive closes a private same-directory temporary file
before rename; successful application-write byte counts are invocation-owned.
The kernel owns flushing. Store locks and dependency-first publication preserve
reader visibility, not power-loss durability. Replay and roster usage retain their
existing paths below the state root; generated debug bundles use its debug subtree
with process leases and age cleanup owned by storage maintenance. Diagnostic bytes
do not consume the correctness-state quota.

Session retention keeps shared durable dependencies and protects running work.
New call envelopes retain file evidence inline and create no shared
filesystem snapshots; workspace snapshots are not retention dependencies.
Readers still validate historical version-2 snapshot references, hashes, and
bounded expansion. Historical object dependencies remain
protected across branches until their last owner expires. Cleanup removes an
old envelope before its last-owned objects; no migration deletes retained evidence.
Cleanup operates only on exact managed record names under the store lock; it
never traverses user workspaces or Codex transcripts. The age and
storage-pressure policy belongs to [REQ-ROUTER-001](../spec/router.md).
Retirement neither transfers execution authority nor recycles change IDs.
Background planners validate a store-wide publication revision before each
bounded removal commit. The revision is coordination metadata, not authorization
or replay state. Catalog and change-index reads occur outside the store lock;
unchanged plans reuse their size/ownership view instead of rescanning after each
session. Catalogs remain intact during partial retirement and are removed only
after the final batch; a restarted planner safely revisits already-removed names
without rewriting the remaining catalog for each batch. Concurrent publication
invalidates the plan. The maintenance lease
permits one worker per store, while active-turn and inherited-read leases remain
independent protections. Pressure requests carry only a managed record name and
byte growth, never a thread view or host continuation.

The live-diff renderer consumes provisional inputs and completed review files
without owning workspace effects or replay locks. Commentary and capture are
auxiliary consumers of observed facts; they cannot replace a stock result or
provider response. The
[duplicate-output projection](../spec/execution.md#duplicate-output-projection)
is a model-input-only exception: it runs after all evidence consumers and leaves
the original request input, host result, rollout, replay evidence, and UI intact.
Caller-owned base instructions pass through except for
explicit omission blocks and pinned inherited conflicts. Additive journal
guidance belongs to the journal tool projection. XML-framed session-helper guidance is
embedded from a checked-in standalone Markdown file generated from tool-source descriptions and
an out-of-router template. Configured plugin entries are derived from the authenticated frontend
registry. Both projections remain call-local; only the frontend section is stored with the pinned
registry snapshot.
