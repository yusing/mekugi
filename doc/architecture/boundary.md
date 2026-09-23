# Filesystem, history, and output boundary

## CTR-BOUNDARY-001 — Host effects and durable observation

Codex owns editing, filesystem permissions, sandboxing, command execution, and
live process continuation. Mekugi receives the selected metadata directory
from the request. Relative observation paths require that directory; the
router never substitutes its own cwd or imposes an unrelated root-library
confinement policy. Observation cannot authorize a host effect.

The replay store retains completed stock-call identity, bounded patch
baselines, confirmed workspace outcomes, change IDs, and managed omitted
output. It validates identity on replay, but never reruns an edit or command.
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
provider response. Caller-owned base instructions pass through unchanged
except for explicit omission blocks. Additive journal guidance belongs to the
journal tool projection and remains call-local.
