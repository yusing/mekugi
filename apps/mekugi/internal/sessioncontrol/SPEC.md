# Session control

The application coordinates Main's submissions, interruption, native shell,
settings and session replacement. It issues app-server requests and correlates
their results; Codex owns request identities, transport, execution, permissions
and process lifetime.

Admission follows outstanding operations, not displayed status or elapsed time.
Request acknowledgement, committed user input and host turn completion are separate
observations. Unknown, duplicate or retired responses cannot settle a newer
operation. Notifications and answers to host requests preserve their original
method, payload and identity. Transport failures are returned without retry.

[Conversation](../../../../modules/conversation/SPEC.md) owns composer payloads,
queues and provisional transcript rows; control decides which submissions settle,
return to the composer or requeue. [Session presentation](../sessionpresentation/SPEC.md)
owns attachment lifetime, displayed-thread and history reconstruction. Control
issues its observational reads without changing execution or input identity.

## Interactive operations

Semantic interruption validates the observed thread, turn and pending-start identity
before changing control state. Restoration and expedition are explicit dispositions;
keyboard locks and contextual dismissal stay with the renderer.

Terminal keys and contextual dismissal belong to
[terminal session](../terminalsession/SPEC.md#composer-controls).

### Submission and interruption

For conversation input, Enter steers an active turn; Tab queues for the next turn
and submits while idle. Shell commands use their immediate-submission behavior below.
Alt-Up/Shift-Left restores only locally waiting input; transmitted steers remain
host-owned. Starting input appears provisionally and reconciles with the host echo
without duplication. Conversation matches text only when the host omits client
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

Prepared input dispatch uses the same request ledger as other control operations.
Its captured turn identity selects start versus steer and only starts include the
selected service tier. No second input-request map or transport retry is created.

### Compaction

Session control owns manual compaction's prerequisite interruption, cancellation
and continuation; displayed progress does not determine its outcome.
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

The native client borrows observation, accounting, review and capture owners
independently of provider request preparation. Shell observations share the ordinary
capture admission and completion path, retaining the submission's original workspace
and thread through later presentation switches. Replay starts no observation window.

### Session replacement and exit

Confirmed replacement retires Main's source operations together, preserving queued
input and the target's restoration intent. Rejection leaves source operations
intact. Independent side cleanup and title saves retain their owners; forgetting
observations never cancels work, claims completion or restores live resources.

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

Invocation-local observations bridge provider generation and interactive control.
Session control owns authenticated ancestry and the attached audience used for
auxiliary notices and Activity supplements. Conflicting identity remains invalid
for the invocation. Activity owns supplement retention and deduplication; terminal
consumers receive detached facts rather than access to the ancestry registry.
Question answers wait for matching tool-input generation to finish; ordinary
steers remain available. Generation completion is independent of host execution,
preview availability and command completion. Request cleanup removes only its own
generation observations. Side-thread membership survives pending cleanup, never
resume or replay.

Successful approval-answer delivery publishes its explanatory feedback before a
subsequent provider request can read it. Failed delivery publishes no feedback;
turn completion retires only that turn's feedback. These observations carry no
execution authority and do not replace the host request ledger.

Live approval response handles and exact-command grant settlement belong to
Session Control. Decisions retain the admitted prompt and its original host
request; terminal layout, cursor state and painted readiness remain separate.
The response uses the existing request transport, and failed delivery leaves the
prompt pending. Session grants apply only to the same guard kind and exact
command identity, including its workdir.
Question handles likewise retain their original host or local authentication
request. Secret answers are supplied only for delivery and never retained in
control state or published views. Cancellation and expiry settle that original
authentication request without affecting another prompt or command.

The invocation's shell-approval transport belongs to Session Control, independently
of provider requests and command reports. It delivers guarded commands to the
existing approval and secret-entry workflows. Withdrawal, timeout and shutdown
retire pending questions; replies retain their original command identity. Guard
policy and grants stay with the composed approval modules. Shutdown closes report
observation and approval transport before draining retained request facts.

## Thread names

Manual names entered before host identity wait for the admitted thread. Once
identified, each thread serializes its name saves and retains only the latest
queued manual name. Manual input suppresses optional generation before persistence,
including a failed save. A generated candidate cannot pass a pending manual name.

The request ledger owns wire correlation; name control retains semantic admission
and settlement only. Acknowledgement releases the next queued name. Failure keeps
the confirmed name and produces nonfatal feedback unless a newer name is pending.
Responses and host name notifications remain scoped to their original thread and
cannot rename a displayed replacement. Name saves remain independent of Main's
turn, input settlement and session replacement.

## Applied settings

`/model`, `/effort` (alias `/reasoning`) and `/tier` accept explicit values or show
model-advertised choices. Pickers initially select the confirmed value when present;
loading, unavailable choices and errors remain visible. `/tier default` clears the
requested tier. Explicit confirmed choices override per-model defaults for this
thread/model throughout the invocation, including after compaction. Children and
persistent configuration are unchanged.

Settings wait for authoritative host confirmation before publication or dependent
input. An unchanged default does not wait for a notification the host suppresses.
Control classifies each admitted intent as unchanged, a thread update or an
active-turn repair before transport issuance. The adapter commits that decision
only after successful submission; transport failure cannot leave a new pending
operation. Confirmed notifications settle the captured target and explicit
default-restoration intent independently of picker or display state.
An active-turn update targets only the turn that received the intent; if that turn
is unavailable, the result reports next-turn-only success. Applied settings affect
only subsequent captures. Applied values and recovery evidence belong to
[session preferences](../../../../modules/sessionpreferences/SPEC.md).
Codex alone authors configuration history items when supported.

Resume recovers settings from metadata, preferences and the identity-validated host
rollout before the host can checkpoint invocation defaults. Explicit launch
overrides remain authoritative, and restoration preserves an unset reasoning
effort rather than substituting defaults. Codex validates the effective settings.
Input and snapshot replacement wait for confirmation. Rejection returns waiting
input to the composer; explicit reasoning selection can repair the gate. Host
feature overrides remain invocation-local, and no operation writes user
configuration.
