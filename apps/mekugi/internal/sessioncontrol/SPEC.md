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

Live control owns thread/turn identity, submission admission and settlement,
interruption, native shell, settings operations, and session replacement.
Admission derives from these operation records, not displayed status, elapsed
time, or activity items. Request acknowledgement, committed user input, and host
turn completion remain separate observations.

[Conversation](../../../../modules/conversation/SPEC.md) retains draft queues,
attachments, neutral question/approval models, input history and provisional
transcript rows. Sent payloads remain indexed by submission
identity while control retains an outstanding acknowledgement or commitment.
Control returns synchronous restoration or requeue decisions naming those
identities; it never stores composer payloads or invokes presentation callbacks.
The application adapter maps observations and semantic decisions to admission,
response and settlement operations; rendering and live handles stay outside
Conversation's models.

[Session presentation](../sessionpresentation/SPEC.md) owns displayed-thread and
child-history intent state. This owner issues the resulting observational reads
without changing execution or input identity.

## Interactive operations

Terminal keys and contextual dismissal belong to
[terminal session](../terminalsession/SPEC.md#composer-controls).

### Submission and interruption

For conversation input, Enter steers an active turn; Tab queues for the next turn
and submits while idle. Shell commands use their immediate-submission behavior below.
Alt-Up/Shift-Left restores only locally waiting input; transmitted steers remain
host-owned. Starting input appears provisionally and reconciles with the host echo
without duplication. The adapter matches text only when the host omits client
identity. Acknowledgement/completion races retain submission order. Rejection
restores input before later drafts, preserving attachments. An interrupted,
uncommitted first start returns to Ready with its draft restored. Interruption
waits for the host turn ID when unavailable and never repeats for that turn.

Escape on an active turn expedites pending or unsent steers: after interrupt and
steer acknowledgements and turn completion settle, only uncommitted steers may
start a new turn. The draft stays unchanged and Tab-queued input stays queued.
Rejected interruption resends nothing; if every steer commits, ordinary queue
restoration applies. During a starting turn or without pending steers, interruption
only restores input. Ctrl-C likewise restores uncommitted and queued input before
the draft without automatic resubmission. Committed input is never restored or resent.
`instant_interrupt` remains Codex-owned: launch forwards its toggle, and ordinary
steering uses the same host path without aborting turns or replaying tools.

### Compaction

Manual compaction separates live workflow from displayed progress. Queued intent
accompanies composer payloads; session control owns its host correlation,
prerequisite interruption, cancellation and continuation eligibility.
`/compact` with Enter interrupts busy Main before compaction; Tab waits for it.
Both prerequisite-interrupt acknowledgement and interrupted-turn completion precede
compaction. Both compact acknowledgement and successful compact-turn completion
precede continuation. The command separates text batches and never becomes model
input. An intent admitted while busy continues with waiting input, otherwise one
visible continuation message; idle compaction stays idle.

With an empty draft, Ctrl-C restores queued compaction and waiting input without
interrupting Main. Cancelling a prerequisite interrupt cannot undo its issued
request or discard its acknowledgement/completion gates, but prevents compaction.
Cancellation immediately removes continuation eligibility, including races with
success. Starting/running cancellation restores waiting input and never targets a
later Main turn. A cancelled start targets only its eventual compact turn, and
completion removes that target. Rejected interruption restores the command and
waiting input for retry. Confirmed replacement discards compaction state; switching or
replay cannot revive it. Codex owns compaction/execution; progress and errors remain
visible.

### Native shell

With an initial `!`, Enter or Tab immediately submits literal shell syntax, minus
the marker and surrounding whitespace, through `thread/shellCommand`, including
while Main is busy. It never becomes model input or local execution.
Empty commands remain editable; attachments must be removed first. Codex uses the
thread shell/workspace with full access and owns output, interruption and durable
history. During Main work it injects command/output into that turn; while idle it
does not start a model reply, and ordinary model input waits for standalone shell
completion. Acknowledgement settles submission, not execution. Compaction blocks
shell submission for retry. Rejected commands return to an empty composer or, when
newer prose exists, local history instead. Observed edits use the ordinary recorder.

### Session replacement and exit

Confirmed replacement retires Main's source operations and correlation scope
together, preserving queued input and the target's restoration intent. Rejection
leaves source operations intact. Resume metadata, configuration, preference evidence
and buffered history remain adapter inputs. Independent side cleanup and title
saves retain their owners; forgetting observations never cancels work, claims
completion or restores live resources.

`/clear` starts a fresh host session with current workspace/settings only when no
task or submission is pending. Existing saved sessions and files remain. Failure
keeps the old presentation usable; success retires its late events. Settings, shell
and side questions retain drafts for retry during replacement. `/resume` has the
same idle admission requirement, including stacked input; restoration is specified
by [session presentation](../sessionpresentation/SPEC.md#resume-and-switching).

`/archive` confirms with No selected initially. Success archives Main through Codex,
stops its turn and exits only after host success; failure keeps the session usable.
It requires an established session outside restoration/replacement. New submissions
and switches wait while it is pending; displayed children never change its target.
`/quit` exits only while idle. Exit prints unsent drafts and stacked input; unresolved
or uncommitted transmitted input says outcome unknown and is never resent.

Side-thread correlation outlives a closed dock or Main replacement until delayed
fork/start responses and cleanup settle. A later start acknowledgement cannot revive
a completed turn. Interruption/unsubscription targets only that side thread;
its notifications never become Main observations. Closing the dock drops its
visible exchange, not pending host correlation.

## Applied settings

`/model`, `/effort` (alias `/reasoning`) and `/tier` accept explicit values or show
model-advertised choices. Pickers initially select the confirmed value when present;
loading, unavailable choices and errors remain visible. `/tier default` clears the
requested tier. Explicit confirmed choices override per-model defaults for this
thread/model throughout the invocation, including after compaction. Children and
persistent configuration are unchanged.

The app-server invocation enables `step_model_switching` and
`reasoning_effort_override` locally. `thread/settings/update` waits for the scoped
authoritative notification before publishing settings or admitting dependent input.
An unchanged default does not wait for a notification the host suppresses. Updates
to the same active turn use `turn/settings/update`; `targetUnavailable` reports
next-turn-only success. Applied publication affects only subsequent captures.
The update never targets a replacement turn. Applied values and recovery evidence
belong to [session preferences](../../../../modules/sessionpreferences/SPEC.md).
Codex alone authors capability-gated configuration history items.

Resume reads metadata, preferences and the identity-validated host rollout before
`thread/resume` can checkpoint invocation defaults. Explicit launch overrides remain
authoritative and Codex validates the effective settings. Nullable reasoning copies
the returned collaboration mode and changes only its effort to null; plain RPC
`effort:null` means no change. Input and snapshot replacement wait for authoritative
confirmation. Rejection returns waiting input to the composer; explicit reasoning
selection can repair the gate. No operation writes user configuration.
