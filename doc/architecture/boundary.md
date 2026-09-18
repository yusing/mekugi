# Filesystem, history, and output boundary

## CTR-BOUNDARY-001 — Filesystem, history, and output boundary

The root library boundary owns authorized workspace reads, evaluation diagnostics,
completed results, direct application, rollback coordination, and translation. Its
confined form receives a caller-authorized root and relative working directory. It
validates regular UTF-8 inputs, prevents capability escape, and preserves one path identity
from evaluation through commit.

Normal router translation is intentionally outside that library confinement boundary. It
uses the optional canonical metadata directory supplied by the request and ordinary host
path resolution. It never substitutes router cwd, and without a selected directory it
accepts only absolute operands. Codex remains responsible for authorization and filesystem
effects when it executes the translated carrier.

Durable replay is scoped to the selected metadata directory or explicit no-directory state.
It persists each completed model-call-to-carrier mapping before exposure, validates exact
identity and payload on replay, and builds an immutable request-visible history view.
Forks and resumes inherit only visible durable facts; compaction removes ancestry from that
request view without deleting records needed elsewhere. Replay never retranslates,
reexecutes, or revives expired process capabilities.

The replay boundary also owns session retention. Durable stable-thread catalogs retain shared
file dependencies, while process leases protect running work and snapshot adoption. Cleanup
operates only on exact managed record names under the store lock; it never traverses user
workspaces or Codex transcripts. Age and storage-pressure policy is defined by
[the router interface](../spec/router.md). Retiring a session does not transfer execution authority
or recycle change IDs.

The same durable boundary coordinates change records and execution receipts. Change review
renders from completed engine results, stays separate from executor patches and final-state
previews, and becomes visible only after required durable facts exist. Offline session
inspection reads and validates those facts without opening writable recovery state or
inferring execution from carrier source.

Application stages the complete engine result and performs ordered external operations with
rollback attempts. Translation renders the complete carrier without mutation. Neither an
authorized root nor a rendered patch serializes outside writers, supplies a cross-file
snapshot, or makes installation crash-atomic. Callers retain writer coordination through
the final external effect, and failures report known effects and rollback outcomes honestly.

Persistent agent guidance is rendered from one shared owner with a model-specific workflow
selection. Tool descriptions remain call-local. Instruction rewriting replaces only
complete recognized conflicts and preserves caller-owned policy; transport selection does
not change those semantics. Capture and commentary observe this boundary but cannot replace
a successful tool or provider result.
