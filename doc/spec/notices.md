# Native session notices

## REQ-NOTICES-001 — Actionable router diagnostics

Router failures that block work or require action produce user-only error feedback
in Main's composer, not transcript entries, generated assistant commentary or
provider-response items. Success and ordinary cancellation are silent.
Request failures with an identified Codex thread and turn remain hidden while
Codex can retry. A host-confirmed failed turn releases its request notices;
completion or interruption discards them. Attempt diagnostics remain available
by reference even when recovery makes a notice unnecessary. Auxiliary notices
do not wait for a turn outcome.
The original HTTP failure, tool error, exit code, and
substantive provider result remain unchanged.

### Content and scope

Notices identify the request phase, an opaque per-process diagnostic reference, and
the complete error string when available. Classified failures supply actionable
context. Error strings can contain provider-controlled text; native terminal rendering
sanitizes control sequences. No request snapshots or separate operational event logs
are added to notices.

Main receives its own notices and those of proven descendants. Child notices name
the originating agent. Unrelated roots and unknown session identities do not borrow
another thread's view. Router-wide auxiliary degradation notices can appear in Main;
session-scoped storage, cleanup, progress-retention, and usage-file notices carry
their authoritative thread identity when known. Truly unscoped session failures
remain available to the launcher.

Deduplication uses the originating session/category and, when present, thread/turn
and diagnostic identity. Distinct causes remain separate. Repeated causes retain
counts without repeatedly interrupting the native view.

The composer shows an error-batch preview and an ellipsis when details are omitted.
Clicking an elided error opens the complete batch in the shared error dialog;
the check button (`[✓]`) dismisses it. These controls follow the
[composer feedback behavior](native_ui.md#req-native-ui-001--ui).
Cleanup planning and reclaimed-storage progress remain transcript content.

### Delivery and recovery

The bounded queue reserves a notice for one native delivery at a time and acknowledges
it only after a successful terminal write paints its visible surface. Composer errors
and transcript progress acknowledge independently: a visible composer can deliver its
errors while transcript progress is hidden. Failed writes, hidden or replaced composer
feedback, covering dialogs and frozen selection leave the affected notices pending.
Retrying restores the complete error batch without duplicating transcript progress.
No model turn or writable provider response is needed to display a notice.

At most 256 session/category entries are retained until shutdown. Excess distinct
entries become an overflow count. The launcher reports undelivered notices and repeat
counts after Codex exits, including when no UI was attached. Unresolved request
notices remain available for launcher recovery if no host turn outcome was
observed. Host-confirmed recovered or interrupted turns produce no launcher error
summary. UI display
never inserts notice IDs into model-visible history. Existing durable exact-ID replay
cleanup still removes previously retained router-authored messages; it does not infer
provenance from text or prefixes.

Acceptance:

1. JSON and SSE provider output contains no generated critical-error commentary.
2. Main's composer displays root, proven-child and router-wide errors, but not
   unrelated-thread errors. Child errors name their origin; omitted details open
   in full, and the check button dismisses the batch.
3. Concurrent claims, failed writes, and retries preserve single delivery and pending
   recovery. Successful display acknowledges only the captured repeat count.
   Composer errors and cleanup progress acknowledge only their own visible surface;
   hidden, replaced, dialog-covered or frozen-selection feedback remains pending.
4. With no usable native view, pending notices remain reportable after shutdown; queue
   overflow does not change request success or failure.
5. Request-attempt failures do not interrupt retrying turns. Host completion or
   interruption clears their notices; terminal failure releases the complete
   diagnostic batch for that thread and turn, including proven children. A late
   request finalization uses a retained recent host turn outcome. Without a
   retained outcome, its notice remains available for launcher recovery.
