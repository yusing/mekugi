# Session control

The application coordinates Main's submissions, interruption, native shell,
settings and session replacement. It issues app-server requests and correlates
their results. Its live client binds the actual app-server connection to one
request ledger, startup admission, Main operations and independent title saves.
Thread retirement filters late notifications, including newly observed descendants;
readmission restores that root and its descendants without reviving host work.
Codex owns request identities, transport, execution, permissions and process lifetime
for its runtimes. The Claude adapter uses the [native SDK backend](../../../../backends/claude/SPEC.md)
with its own correlated request receipts rather than Codex protocol emulation.

Admission follows outstanding operations, not displayed status or elapsed time.
Request acknowledgement, committed user input and host turn completion are separate
observations. Unknown, duplicate or retired responses cannot settle a newer
operation. Notifications and answers to host requests preserve their original
method, payload and identity. Transport failures are returned without retry.

Server-owned thread coordination submits asynchronous host calls to that same
event loop and ledger. Cancellation before dispatch prevents execution; after
dispatch it releases observation with an uncertain outcome. Shutdown releases
waiting callers without replaying their operations.
Coordination inspection uses the same event-loop handoff to read current applied
settings and session metrics, without issuing a host request or copying a UI frame.
Prepared-child resume preserves retained host identity and checks canonical
authority before execution. History confirmation can read the retained source
without a live host connection. Ordinary child input waits for logical admission
and uses the same prepared-authority checks as agent assignments. Human starts
reserve child capacity through Thread Control; human steers name the actual
running child turn. The existing request ledger reports acceptance, rejection or
uncertainty without changing the human input's role. Prompt answers, approval
decisions and cancellation keep their existing authority.

[Conversation](../../../../modules/conversation/SPEC.md) owns composer payloads,
queues and provisional transcript rows; control decides which submissions settle,
return to the composer or requeue. [Session presentation](../sessionpresentation/SPEC.md)
owns attachment lifetime, displayed-thread and history reconstruction. Control
issues its observational reads without changing execution or input identity.
Reusable presets use the same host request ledger for configuration reads and
per-slot, version-checked writes to Codex's active user configuration. The host
retains file formatting, configuration validation and persistence authority.
Child metadata is read once per attached request scope. Its response uses the
same request ledger; missing metadata does not block admitted Activity, and
retiring the scope clears both correlation and read admission.

## Claude execution receipts

Claude's request ledger retains the exact native session, frozen preparation,
unconsumed attributed inputs and dispatch receipts through the selected Session
History owner. Idle messages do not start native work. Native input start confirms
dispatch acceptance; the exact terminal input UUID confirms consumption and allows
the retained payload to retire. A queued acknowledgement establishes neither.
Unknown dispatch remains visible and is never automatically repeated.

Runtime event processing remains independent of callers awaiting input acceptance.
Native completion and logical settlement share an admission boundary: a follow-up
cannot start a new native turn while the preceding logical generation still owns it.
Transport loss releases those callers as uncertain; interruption targets the actual
generation. Resume preserves exact native identity and reconciles retained receipts.
The ledger does not copy the native transcript or own the logical agent tree.

Delegate approvals use ordinary Conversation prompts with their original child
identity and one live response authority. Cancellation withdraws that request;
an answer neither starts work nor wakes a logical wait.

## Interactive operations

Semantic interruption validates the observed thread, turn and pending-start identity
before changing control state. Restoration and expedition are explicit dispositions;
keyboard locks and contextual dismissal stay with the renderer.
The admitted start keeps its identity when the host assigns a turn and commits
input. A delayed cancellation of conclusively completed original work settles
without interrupting, restoring input from, or canceling compaction for a successor.
Logical waits retain their own observed identity after the host turn ends. They
remain busy for replacement/exit admission and cancellable through Thread Control;
no host turn identity is fabricated for cancellation. Human input reconciles and
supersedes unsent logical continuations before dispatch. Host-turn completion does
not settle logical work; cancellation retains its captured wait and stop identity.

Terminal keys and contextual dismissal belong to
[terminal session](../terminalsession/SPEC.md#composer-controls).

### Submission and interruption

For conversation input, Enter steers an active turn; Tab queues for the next turn
and submits while idle. Shell commands use their immediate-submission behavior below.
Alt-Up/Shift-Left restores only locally waiting input into the requesting editor,
including input queued in another attached view; transmitted steers remain
host-owned. Explicit queue steering promotes ordinary queued messages through
the existing steer admission path without interrupting the turn. Starting input appears provisionally and reconciles with the host echo
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
native empty-input turn. Continuation adds no user message; idle compaction stays idle.
Compaction during a logical wait preserves that wait rather than starting an empty
continuation. User input and attributed agent deliveries queue until both compaction
acknowledgement and its host turn settle.

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

A shell submitted during a logical wait runs before Main resumes. After shell
acknowledgement and successful host-turn settlement, Main continues once with
empty input and Codex's retained command/output, including nonzero command exits.
This supersedes the logical wait through ordinary input admission. A selected wake
that already owns continuation instead resumes Main after the shell settles.
Standalone shell turns do not complete logical model work or announce Main done;
addressed messages and child wakes wait for actual shell settlement.

The native client borrows observation, accounting, review and capture owners
independently of provider request preparation. Shell observations share the ordinary
capture admission and completion path, retaining the submission's original workspace
and thread through later presentation switches. Replay starts no observation window.

### Session replacement and exit

Startup selection and resume preparation belong to Control, including initialized
notification ordering, latest-session lookup, identity validation, invocation
overrides and the target's nullable-effort repair. Preference and rollout owners
supply saved evidence before resume dispatch. Accepted identity is validated before
retiring the old attachment; preference recovery is retained before corrective
settings dispatch. A picker remains an application presentation choice, and
startup failure never silently starts a fresh session.

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

`/fork` takes no arguments and creates a saved branch of the latest completed
conversation, then switches to it. It uses the same idle admission as `/resume`,
inherits current settings and carries pending input through the existing
replacement flow. The original remains resumable and workspace files are unchanged.
Failure preserves the original attachment and restores waiting input. Fork history
uses the same durable authorization and evidence restoration as resumed history.
Inherited retained records are published before switching; storage waits leave
input and host delivery responsive.

`/rewind` is disabled until the session has a user turn. Its dialog shows readable
user messages, including decoded question answers, and selects a turn to fork
immediately before it, excluding that
turn and all later conversation. It shares `/fork` admission, current settings,
pending-input preservation and automatic switching. After choosing a turn, a
second dialog offers Keep edits (the default) or Revert captured edits when there
are changes to choose between. If no captured changes fall at or after the selected
turn, rewind skips that dialog and forks directly. Revert
targets retained changes from that turn onward, including admitted descendant
agents, using validated host call times. Unrelated edits remain subject to the
ordinary mchanges merge behavior. Missing history or ambiguous change membership
disables Revert with a reason; Keep remains available.
Newly admitted changes or membership invalidate a prepared reversal; reopen the
dialog to select against the updated evidence.

Revert runs mchanges through the host's native shell before forking. The original
session remains selected until successful execution completes. Conflicts,
incomplete reversal or rejection leave that session usable, with the command's
output and recovery instructions; no fork or archive follows failure. Cancellation
before dispatch changes no files. Once dispatched, interruption follows native
shell behavior and cannot promise to undo completed effects.
Saved change and output evidence remains available in the new namespace,
including excluded turns and any captured reversal;
conversation restoration uses only the host's truncated branch. Inheritance restores
no running resources or approval grants.

After the branch is usable, a separate confirmation offers to archive the exact
original thread, initially selecting No. Cancellation before fork dispatch changes
no session. Fork failure leaves the original usable; failure to switch preserves
both saved threads. Archive failure leaves the new thread usable and reports that
the original still exists. Resume and replay never repeat these operations.

`/archive` confirms with No selected initially. Success archives Main through Codex,
stops its turn and exits only after host success; failure keeps the session usable.
It requires an established session outside restoration/replacement. New submissions
and switches wait while it is pending; displayed children never change its target.
`/quit` exits only while idle. Exit prints unsent drafts and stacked input; unresolved
or uncommitted transmitted input says outcome unknown and is never resent.

Side-thread correlation outlives a closed side conversation or Main replacement until delayed
fork/start responses and cleanup settle. A later start acknowledgement cannot revive
a completed turn. Interruption/unsubscription targets only that side thread;
its notifications never become Main observations. Closing Side drops its
visible exchange, not pending host correlation. Once explicit close cleanup
completes, root and descendant notification mappings release their operation;
outstanding requests retain only their own acknowledgement correlation.
An unexpected host close stops submissions but retains root identity until the
visible slot is dismissed, so its prompts and descendant activity retire together.
The temporary `/btw` answer has a separate operation and exchange from the
Side/One-off slot. Both can run concurrently, with independent follow-ups and
cancellation. Main replacement closes both; closing either leaves the other intact.
The existing request ledger correlates side replies and notifications to the
side operation, independently of any renderer. An empty `/side` forks without
starting a turn; `/oneoff TASK` creates the same owned ephemeral fork and starts
its task. Answer-only forks retain their restricted settings; one-off forks inherit
permissions and environments. Each new side turn carries its scoped instruction.
One-off tool events and prompt responses retain their original thread, turn and
request identities. Host parent identities admit descendants into the same isolated
audience. Activity with unresolved ancestry remains outside Main until metadata
settles its audience. Closing the slot retires their visible activity and prompts without
answering them or claiming that their host processes completed. Queued answers wait
for their own thread's tool-input generation, independently of Main.
Side and One-off forks initially preserve Main’s effective settings without changing Main.
While either is selected, model and reasoning commands, pickers and shifted-arrow
reasoning controls target that fork. Host confirmation and active-turn repair stay
with the original fork through view switches; Main's settings remain independent.
That operation borrows
Conversation's current exchange and owns fork/start validation, effective fork
settings, side classification before sampling, and delayed cleanup. Acquisition
prepares attachments before dispatch; restoration returns the existing input
value to the application without retaining another draft or side registry.

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

## Composer predictions

Eligible completed Main turns and restored idle conversations may request a
next-message suggestion using OpenAI's remote prompt and sampling configuration.
Plan mode, unsupported models and third-party providers are ineligible. The host
owns the ephemeral prediction fork, inherited context and inference. Predictions
use its prediction mode without configuration overrides or fresh-thread fallback.
The host must support prediction forks (Codex 0.162.0-alpha.20 or newer).
Their messages, prompts and cleanup stay separate from Main and side questions.
An attached, focused, enabled and empty Main editor permits acquisition. Losing
focus alone preserves an existing suggestion request. New input, a new turn,
disablement and replacement retire eligibility immediately;
late host acknowledgements retain only the correlation needed to unload the fork.
Remote moderation policy suppresses matching source-turn suggestions. Incomplete
moderation evidence suppresses that source rather than treating omitted data as safe. Unavailable
configuration or auxiliary failure leaves ordinary conversation usable.

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

`/model [MODEL] --reasoning=LEVEL --speed=TIER` combines settings in one update.
Both `--name=value` and `--name value` are accepted, with quoted values. Omitted
fields stay unchanged; omitting all arguments opens the picker. `--speed standard`
clears the requested tier, while named tiers are validated by Codex. Speed controls
remain Main-only. Invalid arguments retain the draft and issue no host request.

`/permissions` lists Codex's named permission profiles for Main. An explicit
`/permissions PROFILE [--approval-policy=POLICY] [--reviewer=user|auto_review]`
selects a profile without opening the picker. Built-in profiles use their standard
approval defaults; custom profiles preserve unspecified approval settings. Codex
validates effective requirements. Changes apply to subsequent turns, preserving
already running commands and pending approvals. Selection, discovery and host
confirmation hold queued input; cancellation or a closed view releases selection.
Failed updates remain visible without changing confirmed permissions. Permission
choices are thread-local and do not write persistent configuration.

Settings wait for authoritative host confirmation before publication or dependent
input. An unchanged default does not wait for a notification the host suppresses.
Control classifies each admitted intent as unchanged, a thread update or an
active-turn repair before transport issuance. Control dispatches the plan through
the existing request ledger and commits it only after successful submission;
transport failure cannot leave a new pending operation. Thread acknowledgements
leave confirmation pending; turn acknowledgements report whether the captured
live target received the update. Confirmed notifications settle that target and
explicit default-restoration intent independently of picker or display state.
An active-turn update targets only the turn that received the intent; if that turn
is unavailable, the result reports next-turn-only success. Applied settings affect
only subsequent captures. Confirmed values and nullable-effort recovery evidence
are retained before dispatching a dependent live update. Applied values and recovery
evidence belong to
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
