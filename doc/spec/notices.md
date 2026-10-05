# Native session notices

## REQ-NOTICES-001 — Actionable router diagnostics

Router failures that block work or require action produce native Main error entries,
not generated assistant commentary or provider-response items. Success and ordinary
cancellation are silent. The original HTTP failure, tool error, exit code, and
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

### Delivery and recovery

The bounded queue reserves a notice for one native delivery at a time and acknowledges
it only after a successful terminal write with Main content painted. Failed or hidden
writes release the reservation and leave the notice pending. Retrying presentation
does not duplicate the retained Main entry. No model turn or writable provider response
is needed to display a notice.

At most 256 session/category entries are retained until shutdown. Excess distinct
entries become an overflow count. The launcher reports undelivered notices and repeat
counts after Codex exits, including when no UI was attached. UI display
never inserts notice IDs into model-visible history. Existing durable exact-ID replay
cleanup still removes previously retained router-authored messages; it does not infer
provenance from text or prefixes.

Acceptance:

1. JSON and SSE provider output contains no generated critical-error commentary.
2. Native Main displays root and proven-child notices, but not unrelated-thread notices.
3. Concurrent claims, failed writes, and retries preserve single delivery and pending
   recovery. Successful display acknowledges only the captured repeat count.
4. With no usable native view, pending notices remain reportable after shutdown; queue
   overflow does not change request success or failure.
