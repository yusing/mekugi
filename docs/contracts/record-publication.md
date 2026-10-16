# Retained authority and publication

## Authority and continuity

[Stock execution](../../backends/codex/SPEC.md#execution-authority) remains Codex-owned.
Observation resolves filesystem operands from selected request metadata, never
the application's cwd. Relative operands require that selected directory.
Workspace selection does not establish read authorization: evidence reads and
copies retain the originating caller's authority. Cross-thread input retains its
authenticated source provenance; a user-role carrier cannot make agent text
human input.

Durable namespace, thread and workspace identity remain separate from routing
and the currently displayed view. Forks and resume restore authorized retained
facts, never processes, grants or continuation handles. Compaction can remove
ancestry from one request view without retiring evidence needed by another
branch. Unsupported local context references fail before provider forwarding.
[Change evidence](change-evidence.md) owns capture scope and reconciliation of
completed results, including results first visible during compaction.

## Cross-owner publication

The application's `recordtransactions` boundary owns two ordered operations:

- Evidence publication writes validated host observations before their change
  membership. Attribution uses host thread and call identity.
- Validated-input adoption gathers feature-owned dependencies within one retained
  view. Fork initialization retains inherited records before publishing cloned
  indexes, then publishes the new namespace last. Interrupted setup is retryable.

Participants borrow one operation-scoped session transaction. Handles cannot
escape the operation, and participants cannot open nested transactions. Record
encoding and validation stay with their owners; single-owner operations remain
there. No transaction spans terminal or network delivery.

Dependencies precede referencing records, and references become usable only
after successful publication. Completed writes may survive a later failure and
leave conservative retention; failure cannot claim missing evidence is available.
This provides ordered reader visibility, not crash-atomic multi-file or
power-loss durability.

A snapshot lease protects validation until the publication lock bridges it to
ownership. That lease is released under the lock before ownership publication;
retention cannot wait for a reader lease while holding the transaction the
reader needs. A separate session lease protects retained-evidence delivery until
the consumer confirms it.

## Semantic owners

[Session history](../../modules/sessionhistory/SPEC.md) owns namespaces, catalogs,
allocation, leases and retention, using contributed record dependencies and
retirement policy. [Change history](../../modules/changehistory/SPEC.md),
[output recovery](../../modules/outputrecovery/SPEC.md) and
[command segments](../../modules/commandsegments/SPEC.md) own their records and
recovery meanings. The frozen [historical codec](../../libraries/legacycall)
validates entire mixed envelopes and bounded regular-file dependencies before
any feature projection is used. Inspection never manufactures a replacement
record or grants execution authority.

[Frontend delivery](../../apps/mekugi/internal/frontends/SPEC.md) owns authenticated
execution and output delivery. [Live preview](../../modules/livepreview/SPEC.md)
owns provisional state, and [diagnostics](../../modules/diagnostics/SPEC.md) owns
private bundles and their independent retention. These consumers neither replace
host results nor create another retained-session store.
