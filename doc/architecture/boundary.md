# Filesystem, history, and output boundary

## CTR-BOUNDARY-001 — Host effects and durable observation

Codex owns editing, filesystem permissions, sandboxing, command execution, and
live process continuation. Mekugi receives the selected metadata directory
from the request. Relative observation paths require that directory; the
router never substitutes its own cwd or imposes an unrelated root-library
confinement policy. Observation cannot authorize a host effect. Mekugi-owned reads
and evidence copies must obey the originating caller's read authorization, rather
than inherit broader router access. Workspace selection and caller-supplied metadata
do not by themselves establish authority. Cross-thread input must retain authenticated
source provenance; a user-role transport carrier does not make agent text human input.

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
Completed host results first visible during compaction still publish change
evidence. Observation uses the selected workspace or the selected thread's
retained call ownership and leaves the provider compaction payload unchanged.
Observation failure reports missing evidence without blocking provider compaction.
Unsupported local context references fail before provider forwarding.

The shared persistence primitive closes a private same-directory temporary file
before rename; successful application-write byte counts are invocation-owned.
The kernel owns flushing. Store locks and dependency-first publication preserve
reader visibility, not power-loss durability. Replay and roster usage retain their
existing paths below the state root; generated debug bundles use its debug subtree
with process leases and age cleanup owned by storage maintenance. Diagnostic bytes
do not consume the correctness-state quota.

[Session history](../../modules/sessionhistory/SPEC.md) owns publication, catalogs,
namespace allocation, leases, and retention. Feature owners contribute record
recognition, dependency enumeration, and semantic retirement without exposing
private schemas to retention. A rejected preparation leaves the owner unchanged;
partial publication advances only the successfully written owner records.

New call envelopes retain file evidence inline and create no shared filesystem
snapshots. Workspace comparison snapshots are outside record retention.
Historical version-2 object dependencies remain readable and are retired only
when their final owner expires, after referencing envelopes. Cross-owner fork
initialization retains inherited records before publishing the new scope.
Neither retirement nor a retained reference grants execution authority.

The frozen call-envelope codec in `libraries/legacycall` validates the complete
historical record, including mixed feature fields and referenced snapshots.
Reading one feature's projection does not bypass validation of the other retained
facts. The codec reads retained files only; publication and retention remain with
session history, and current feature semantics remain with their owners.

The live-diff renderer consumes provisional inputs and completed review files
without owning workspace effects or replay locks. Commentary and capture are
auxiliary consumers of observed facts; they cannot replace a stock result or
provider response. The
[duplicate-output projection](../spec/execution.md#duplicate-output-projection)
is a model-input-only exception: it runs after all evidence consumers and leaves
the original request input, host result, rollout, replay evidence, and UI intact.
Caller-owned base instructions pass through except for
explicit omission blocks and pinned inherited conflicts. XML-framed session-helper guidance is
embedded from a checked-in standalone Markdown file generated from tool-source descriptions and
an out-of-router template. Guidance remains call-local and is stored with the
pinned catalog snapshot.
