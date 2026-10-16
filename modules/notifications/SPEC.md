# Notifications

## Purpose

Deliver bounded user-visible notices and complete failure details without changing
execution, provider results or model-visible history.

## Scope

The module owns pending notices, repeat counts, delivery claims, recovery summaries
and attached-view feedback. Producers supply complete text, stable notice identity,
proven scope and an error or progress surface. The application interprets host and
provider facts, supplies descendant/display-name snapshots, and routes optional
direct producer callbacks. Private failure records belong to diagnostics.

Terminal painting, geometry, input, control-sequence sanitization, desktop signal
encoding and lifecycle titles remain renderer-owned. A desktop event carries only
generic content; prompt, answer, tool and secret content never enters its message.

## Behavior

### Pending notices and outcomes

Request failures wait while their identified host turn can retry. A failed turn
releases them; completed or interrupted turns discard them. Late request failures
use retained recent outcomes. Unknown outcomes remain recoverable at shutdown.
Auxiliary degradation and cleanup progress do not wait for a turn result. Success
and ordinary cancellation produce no error notice.

Main receives its own notices, proven descendant notices and invocation-wide
auxiliary notices. Other roots and unknown session identities remain pending.
Descendant notices retain their supplied display name. Distinct causes remain
separate; repeated session/category causes retain counts without interrupting a
view again after delivery. Identified request retries deduplicate by thread, turn
and diagnostic reference.

The invocation retains at most 256 entries and 256 recent turn outcomes. Excess
distinct entries become a recovery overflow count. Pending notices and later
repeat counts remain available after shutdown, including when no UI attaches.

### Delivery and feedback

Each notice has one delivery claim at a time. Claims retain the complete batch
and the repeat count captured at reservation. Error and progress surfaces receive
independent acknowledgements. Success acknowledges only the captured count;
failed or hidden delivery releases the claim for retry. Retrying never duplicates
progress items in the attached view.

A protocol write or host outcome is not a display receipt. The actual renderer
acknowledges a surface only after successfully painting it visibly. Replaced
feedback, covering dialogs, hidden panes and frozen selections stay pending.
Full error text remains available independently of preview truncation.

Non-error feedback expires after three seconds. Actionable feedback remains until
editing or dismissal. Replacing feedback does not acknowledge a displaced batch.
Feedback is user-only; model-visible error evidence retains its own presentation.

### Desktop eligibility

Normalized desktop policy defaults to enabled and unfocused-only. An explicit
always condition permits focused delivery; an event allowlist narrows eligibility.
The application supplies only eligible live events after host identity, completion,
question-priority and duplicate/history checks. Configuration and output failures
remain auxiliary and never change host work. Lifecycle titles are independent of
desktop enablement.

## Lifecycle

The composed service is invocation-scoped: notices can precede attachment and
must remain available after detach or shutdown for launcher recovery. Each attached
view owns its feedback and progress-delivery state. The module starts no goroutine,
opens no file and restores no live operation. Producer callbacks remain optional.

## Codex connection

The application supplies decoded host turn outcomes and normalized configuration.
The module receives no Codex payload and issues no backend request. Request
classification, opaque diagnostic references, native/headless host decoding and
launcher printing remain application integration responsibilities.

## Records

None. Notice identifiers never enter model input. Historical router-message cleanup
uses retained exact-ID provenance at the replay owner, not text resemblance.

## Failure

Missing scope or display evidence remains pending, rather than being assigned to
another thread or claimed delivered. Notification failure cannot replace a tool
result, acknowledge execution, retry a request or alter a substantive provider error.

## Acceptance

Module checks cover deduplication, bounds, exclusive claims, captured repeat counts,
independent surface results, late outcomes, scope, recovery and feedback expiry.
Application acceptance covers provider/notice separation, classified complete errors,
proven child attribution, native and headless outcome input, visible terminal receipts,
retry without duplicate progress, composer details/dismissal and launcher recovery.
Terminal acceptance owns generic desktop content, focus, signal formats and titles.
