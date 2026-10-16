# Session control

The application owns app-server request issuance and response correlation here.
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

Submission, steering, shell, settings, and conversation presentation still live
in the router integration. Their extraction is incomplete; the request ledger
does not substitute for their lifecycle records or payload owners.
