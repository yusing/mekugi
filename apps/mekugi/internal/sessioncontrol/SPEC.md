# Session control

The application owns app-server request issuance, response correlation and live
Main operation control here.
Codex owns request identities, transport, execution, permissions, and process
lifetime. Live presentation supplies intent and consumes correlated results;
it does not issue operations directly through the backend client.

One typed ledger retains each successfully issued request and its consumer
context. Responses consume that identity once. Unknown, duplicate, and retired
responses cannot settle a newer operation. Notifications and answers to host
requests preserve their original method, payload, and identity without becoming
outbound requests. Transport failure is returned to the caller without retry.

Confirmed session replacement retires Main's correlation scope. Rejected
replacement leaves it intact. Independent side-conversation cleanup and title
saves retain their owners across Main's replacement. Forgetting observation
never cancels host work or claims completion.

Live control owns thread/turn identity, submission admission and settlement,
interruption, native shell, settings operations, and session replacement.
Admission derives from these operation records, not displayed status, elapsed
time, or activity items. Request acknowledgement, committed user input, and host
turn completion remain separate observations.

Conversation retains draft queues, attachments, neutral question identities, input history,
and provisional transcript rows. Sent payloads remain indexed by submission
identity while control retains an outstanding acknowledgement or commitment.
Control returns synchronous restoration or requeue decisions naming those
identities; it never stores composer payloads or invokes presentation callbacks.
The adapter resolves text matching only when a host user message omits its client
identity. Acknowledgement and completion races retain submission order, and
committed input is never restored or resent. Explicit Escape delivery waits for
both interruption acknowledgement and turn completion, plus any unresolved steer
acknowledgement, before making uncommitted input eligible for a new turn.
Ordinary cancellation restores it instead. Deferred cancellation uses the
eventual host turn identity, and repeated interruption does not issue another
request for that turn.

Native shell acknowledgement settles submission, not execution. Standalone shell
execution gates model input until completion; shell execution admitted during a
model turn remains Codex-owned within that turn. The composer retains a rejected
shell payload separately from later prose input.

Settings intent remains pending until the appropriate authoritative host update.
A thread update may be forwarded to the same active turn that received the intent,
never a replacement turn. Nullable-reasoning restoration remains gated after a
failed request until confirmed or explicitly repaired. Applied values and recovery
evidence belong to [session preferences](../../../../modules/sessionpreferences/SPEC.md).

Confirmed replacement retires source operations together while preserving queued
composer input and the target's restoration intent. Rejected replacement leaves
source operations intact. Resume metadata, configuration, preference evidence and
buffered history remain adapter inputs; replay cannot revive live resources.

Manual compaction has one live workflow owner, separate from displayed progress.
Its queued intent accompanies composer payloads, while acknowledgement, matching
turn identity, prerequisite interruption, cancellation, and continuation eligibility
belong to session control. Both prerequisite-interrupt acknowledgement and the
interrupted turn's completion precede compaction. Both compact acknowledgement
and successful compact-turn completion precede continuation. Waiting user input
takes precedence over the single automatic continuation message.
Only an intent admitted while Main was busy can continue automatically.

Cancellation immediately removes continuation eligibility, including when
successful completion races acknowledgement. A cancelled start can interrupt
only its eventual compact turn; completion removes that target. Queued-command
cancellation preserves an already-issued prerequisite interrupt's acknowledgement
and completion gates. Confirmed replacement discards live compaction state;
replay and session switching never revive it. Codex still owns compaction and
execution. Composer command and cancellation behavior is specified in the
[native UI contract](../../../../doc/spec/native_ui.md).

[Session presentation](../sessionpresentation/SPEC.md) owns displayed-thread and
child-history intent state. This owner issues the resulting observational reads
without changing execution or input identity.

[Conversation](../../../../modules/conversation/SPEC.md) owns composer payloads and
question/approval presentation models. The application adapter maps observations and
semantic decisions to this owner's admission, response, and settlement operations;
rendering and live request handles remain outside those models.

Side-thread request correlation outlives a closed dock or Main replacement until
its delayed fork/start response and cleanup settle. A completed turn cannot be
revived by its later start acknowledgement. Interruption and unsubscription target
only that side thread; its notifications never become Main observations. Closing
presentation drops the visible exchange, not the pending host correlation.
