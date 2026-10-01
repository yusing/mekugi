# Filesystem, history, and output boundary

## CTR-BOUNDARY-001 — Host effects and durable observation

Codex owns editing, filesystem permissions, sandboxing, command execution, and
live process continuation. Mekugi receives the selected metadata directory
from the request. Relative observation paths require that directory; the
router never substitutes its own cwd or imposes an unrelated root-library
confinement policy. Observation cannot authorize a host effect.

The replay store retains completed stock-call identity, bounded patch and
command-scope baselines, confirmed workspace outcomes, change IDs, and managed
omitted output. Command observation captures its derived scope and the link targets
that scope writes through. A call with unknown write scope also retains one
inventory of the selected metadata directory, separate from that scope: strong file-change
stamps, byte-verified Git blob IDs, and bounded file content,
including ordinary ignored files and non-Git workspaces. VCS metadata is excluded;
dependency trees retain aggregate metadata only. Directory summaries carry no
content or descendant listing and are non-replayable. Inventory endpoint reads
pin directory ancestors without following symlinks. Its comparison is window
evidence, never authorship, and skips explicit, same-cell patch, and overlapping-writer paths. Dynamic Code Mode
resolves native host inputs against the same inventory; fully scoped,
remote-only, and reader-only cells do not compare it. Missing inventory or host
evidence remains incomplete. The in-memory window registry is not
durable authorization or process state. Replay validates retained identity
without rerunning an edit or command.
Last-seen content and running preview state are bounded, process-local auxiliary
state. Last-seen lookups are isolated by durable record namespace; previews use
the captured scope and the broker's viewer lifetime, not the request lifetime.
Neither is restored as a process or execution authority during replay.
Fork and resume views use visible durable facts. Compaction removes invisible
ancestry from one request view without deleting records needed by other
branches. Expired output references and Codex process handles are not revived.

Session retention keeps shared durable dependencies and protects running work.
Version-2 call envelopes reference immutable filesystem-evidence collections by
SHA-256 of their deterministic, uncompressed JSON. The shared objects use gzip
and exact `snapshot-<64 lowercase hex>.json.gz` names in the replay store.
Patch baselines, command file snapshots, workspace recovery inventories and
completed review files share identical collections across calls.
Large text content is interned separately from collection metadata, so unchanged
file contents remain shared when another file or path in an inventory changes.
Manifests use explicit text-slot references; envelopes enumerate their complete
object dependencies for retention without decompressing content during cleanup.
Call identity, host outcomes and ordering remain in their own envelopes. Readers
validate hashes and bounded expansion before restoring evidence; version-1
inline records remain readable without bulk migration. An envelope and its
missing objects are retained in one catalog update and admitted as one write;
the objects and one directory sync precede the envelope. Ownership adoption includes their
dependencies, and cleanup protects shared and in-flight objects and removes
envelopes before their last-owned snapshots.
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
provider response. Caller-owned base instructions pass through except for
explicit omission blocks and pinned inherited conflicts. Additive journal
guidance belongs to the journal tool projection. XML-framed session-helper guidance is
embedded from a checked-in standalone Markdown file generated from tool-source descriptions and
an out-of-router template. Configured plugin entries are derived from the authenticated frontend
registry. Both projections remain call-local; only the frontend section is stored with the pinned
registry snapshot.
