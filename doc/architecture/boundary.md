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
that scope writes through. Non-declared commands additionally receive a bounded,
stateless sweep rooted at the selected metadata directory, or the absolute call
workdir when metadata is absent, never router cwd. Paths outside that root require
a named scope. The in-memory window registry is not durable authorization or process
state. Replay validates retained identity without rerunning an edit, command, or
completed sweep.
Last-seen content and running preview state are bounded, process-local auxiliary
state. Last-seen lookups are isolated by durable record namespace; previews use
the captured scope and the broker's viewer lifetime, not the request lifetime.
Neither is restored as a process or execution authority during replay.
Fork and resume views use visible durable facts. Compaction removes invisible
ancestry from one request view without deleting records needed by other
branches. Expired output references and Codex process handles are not revived.

Session retention keeps shared durable dependencies and protects running work.
Cleanup operates only on exact managed record names under the store lock; it
never traverses user workspaces or Codex transcripts. The age and
storage-pressure policy belongs to [REQ-ROUTER-001](../spec/router.md).
Retirement neither transfers execution authority nor recycles change IDs.

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
