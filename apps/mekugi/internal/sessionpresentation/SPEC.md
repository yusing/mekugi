# Session presentation

The application owns attachment lifetime, displayed-thread identity, Main's input
destination, read-only admission, history restoration, Main's status and displayed-thread
caption values. [Conversation](../../../../modules/conversation/SPEC.md#transcript-observations)
owns transcript interpretation, Activity owns feeds, and [session control](../sessioncontrol/SPEC.md) issues backend
requests, and [terminal session](../terminalsession/SPEC.md) owns focus, navigation,
geometry and rendering.

Status, captions, question and approval docks, and pending-input previews are
published together as detached, renderer-neutral facts. Prompt identity and input
ordering come from Conversation; pending-input reasons come from session control.
Main's workspace, confirmed or pending title, active skills, backend version and approval caller
are part of that publication. Frames read the published values rather than live
application records.
The session retains the latest complete publication after each observation or
semantic intent, including failed operations and restoration. Reading it neither
refreshes owner state nor advances its version. Repeated publication of unchanged
facts keeps the version; nested values returned to consumers are detached copies.
Menu identity, active kind and preferred choice belong to this attachment;
completion targets and choices remain Conversation values.
The current side answer and its running state share this publication. Side and BTW dismissal
names its captured conversation and exchange, so unrelated progress cannot prevent
closing it or redirect a delayed dismissal to a replacement conversation. Closing
the side removes its visible exchange immediately while Control retains any
pending host acknowledgement and cleanup. Scrolling and copy geometry remain
renderer-local.
Clock-driven changes settle on explicit maintenance or replay transitions.
Feedback publication includes its replacement revision. Publication and prompt
admission do not acknowledge display. Only successful
terminal delivery makes a newly opened dock eligible for keyboard input.
The terminal paints those values without choosing semantic queue states or
reconstructing prompt content. Approval intents retain their view version, request
identity, offered choice and ordinary denial reason. Admission resolves the current Conversation owner before
any host response; stale identities and replay produce feedback without answering.
Ordinary question answers and call submissions use the same version and replay
admission before resolving Conversation identities. Submission of a hidden call
preserves an unrelated open editor. Secret editing remains renderer-local.
Nonsecret answer text and approval reasons remain with their originating editor
until admission is known. Resolution by another client preserves unadmitted input
in local history. An uncertain answer submission retains its payload and unknown
disposition without automatic retry; secret contents stay outside that recovery.
Prompt choice, note and editing actions carry the same view version and stable
target identities. Admission precedes changing the Conversation record; replay
and stale targets leave it unchanged. Ordinary editor publications are detached
from parked editor history. Secret editor contents never enter publication.

Session control
remains the operation authority; Conversation, Notifications and Usage retain their
drafts, prompts, feedback delivery and accounting. Presentation selects status
precedence and retains progress timing; the terminal chooses styling, animation
and geometry using the same presentation clock as replay.

Descriptor-composed status contributions project admitted thread facts into
detached items for composer captions and agent metrics. Publication selects the
displayed thread, independently of Main and the highlighted roster row. It reads
no storage and keeps no second accounting state. Zero contributions preserve the
remaining caption and roster values; replacement and backward replay use the
new attachment's facts.

Displaying a live admitted child attaches its actual runtime without starting or
canceling work. Its composer, questions, approvals and settings belong to that
child. Main and child drafts remain isolated; returning to either restores its
existing draft. Explicit selection can resume an admitted cold child runtime without starting
work; historical and replay-only views remain read-only.
The single side
conversation is a separately admitted editable destination with its own draft and
captured settings. Independent native children use their runtime's detached feed,
caption, roster lifecycle and output/detail owner. Displayed paths resolve in that
child's workspace; Main's workspace remains the authority for parent input.
Closing or changing the child view releases observation without ending execution.
The side header target uses the same display intent and transcript
as Main and subagents; closing returns display to Main without changing Main work.
[Replay](../../../../modules/sessionreplay/SPEC.md) is read-only even while Main is
displayed. Display intents name their view version and target identity, so stale actions
cannot affect a replacement session. Interruption names Main’s observed
turn or pending start and explicitly chooses restoration or expedition of
uncommitted steers. Ctrl-C carries that captured identity and cancels queued
compaction before interrupting Main. The same admission applies to every consumer, and replay
rejects host operations. View values are
detached data without terminal or backend types. A highlighted roster row is not
the displayed thread. Metadata can rename a caller without changing its identity.

## Attachment lifetime

Feature state follows the shared [scoped-state contract](../../../../docs/contracts/composition.md#construction-and-lifetime).
Successful replacement retires prior scopes and observations together; rejected
replacement preserves them. Late observations cannot repopulate a retired attachment.
Backward replay reconstructs into a fresh attachment, and shutdown releases it.
These boundaries restore no processes, handles, approval grants or dispatched operations.

The application binds the attachment's existing Presentation and Conversation
models, source feeds, transcript and feedback independently of the host connection
and terminal. Startup identity discovery keeps those scoped models and parked
input; accepted replacement constructs new bindings. History leases, physical
file reads, host request correlation and terminal delivery retain their own lifetimes.

Native client views borrow one execution attachment and its authoritative queues,
prompts, transcript and host ledger. Each view owns its display selection, menus,
chooser, detail acquisition and delivery receipts. Client editors remain local.
Input, prompt, dialog, display and history actions remain admissible across
unrelated status, output and feedback publications. Their owners check the
originating attachment and the bound destination, work, editor or overlay identity.
Navigation does not retarget an action already bound to Main, Side or an Activity
source. Pending acquisition and editor consequences remain with that original owner.
Reference insertion retains its original input thread and editor; opening details
uses the captured feed and source even after display navigation.
Actions without an explicit destination bind to the uninterrupted displayed/input
destination observed by their originating publication. Returning to an earlier
destination does not revive those actions. Caller-label changes preserve identity.
Future revisions and replaced targets remain invalid; unavailable targets produce
feedback without asking the user to repeat selection or silently redirecting input.
History cancellation belongs to its observed loading operation, not a later read
of the same thread.
Waiting-input restoration captures Main's turn or pending start, restores only
locally waiting input to its originating editors, and leaves transmitted steers alone.
Pending compaction acknowledgement and continuation remain cancellable after the
compaction turn completes.
An attached client keeps its source interaction alive while admitted input or editor
consequences are outstanding, even after navigating to another session. Independent
observation, attachment preparation and receipt work cannot block cancellation or
navigation. Admitted execution and shared history outlive client disconnection.
Activity catch-up uses complete source state and resets only transient reveal state;
semantic editor and clipboard effects retain immutable identities until receipt.
Capacity for those consequences is reserved before the originating action is admitted.
Clients acknowledge applied effects independently
of output hydration; notice receipts name the captured notice claim and acknowledge only frames actually delivered,
even when unrelated publications have since advanced.
Menu actions and panel controls likewise recheck their current surface identity
after unrelated status or output progress. A replaced menu, changed completion
editor or closed panel refuses the action. Confirmed replacement releases the view's old
acquisitions while preserving its client editor and undelivered restorations.

Turn observations join Conversation's transcript and Activity's wait and agent
models through these same scopes. Completion settles its own turn's presentation;
an older turn cannot stop a newer response or consume its pending transcript.
Main's live selected turn remains owned by session control, including an active
resume snapshot received before history. Offline playback selects its recorded
turn without constructing live control state. The same publisher consumes either
selection; absent control exposes no pending operations or interrupt target.
Main status completes only for that selected turn.
Closing a turn retires its text/wait correlation without ending yielded commands.

Live observation adapters perform metadata requests, command tracking, preview
control and usage enrichment separately from presentation dispatch. Offline
playback admits recorded metadata directly and constructs no backend client or
request tracker. Both sources use the same observation reduction through their attached Activity,
Conversation and session models. Reduction admits canonical source facts before
returning ordered, detached text, command and caller-name deliveries. Live effects
and terminal delivery remain separate; no renderer or backend handle enters the
reducer. The terminal attachment retains its own pacing and navigation.

The application's source binding borrows the existing attached Activity feed pair
for source records, semantic revisions and enrichment. Activity owns the single
ordered roster and shared feed sequence; the binding adds admitted metadata to
that owner rather than maintaining an application registry. Conversation remains the
transcript interpreter; its backend binding supplies neutral submitted-text spans
and question-reply identities. Shared shell-source decoding supplies command facts. Audience admission and coverage belong to that binding,
independently of painter configuration. Private host evidence and retained output
handles stay in the binding; terminal consumers receive detached publications,
identity-addressed details and output windows. Command reveal frames and animation
cursors never become semantic source state.
Public reasoning enters the source feed with its complete admitted text and
completion immediately. Terminal reveal may lag that publication, but subsequent
animation changes only the terminal overlay, not source content or sequence.
The attachment's command-operation index references Activity's canonical
invocations and pending report evidence. Completion is visible there before a
terminal finishes revealing its buffered output; process lists use canonical
running state rather than index membership.

The application binds source feeds and terminal views separately. Semantic
transitions publish detached values before subsequent input or animation; painting
does not republish source state. Idle maintenance reuses the detached Activity
publication while its feed revisions, retained output versions, roster facts,
status contributions and saved-review totals are unchanged. Display admission and
terminal reveal and pacing still settle before the next intent. Output demands are
resolved by application integration, outside the painter. Older-page insertion
preserves the terminal's anchor around the source transition without transferring
navigation to the feed.

An open detail selection retains its original output references in the application,
independently of feed aging. It publishes detached page identities, membership and
output versions; only the selected output is hydrated. Close and replacement retire
that selection, and stale output requests cannot resolve a later dialog. Terminal
layout and navigation retain no source or output handle.

## History

Each independent runtime restores its own retained host history. Root restoration
preserves original item identity, timing, retained output and saved-review scope.
Thread Control restores logical member identities without starting child runtimes.
A cold child remains paused in the roster; its transcript is published when its
runtime is available. Reading a publication never resumes work.

## Resume and switching

Startup `resume THREAD_ID` first attaches to that server-owned thread when active.
Saved history must belong to a durably enrolled Mekugi session before any host
resume request; the returned identity must match. Stock Codex sessions are excluded.
Failure exits without creating a conversation. `resume --last` chooses the most
recently updated non-archived Mekugi thread in the launch workspace, across
providers, excluding exec and child sessions. Lookup
failure or an empty result exits without a thread. The resolved thread uses the
same existing-owner attachment path as an explicit ID before saved-thread resume.

Bare startup `resume` and in-session `/resume` replace Main with a picker. It lists
Mekugi sessions from 25-row host pages, newest first, initially in the active workspace. Tab toggles all
workspaces and restarts loading. Rows show relative update time, branch and title
or first-message preview, plus workspace-relative directories in the all-workspace
view. Visible rows set widths. Search matches title, branch, directory and ID,
loading until a match or 1,000 scanned sessions; approaching the loaded end or an
unfilled viewport requests another page. Stale requests cannot mix results.
Loading, empty, no-match and failure states stay visible; failed paging waits for
a Tab restart. Enter selects; Escape first clears search. Startup Escape then
starts a new thread and Ctrl-C exits; in-session dismissal keeps the current thread.

The attached presentation model owns chooser identity, rows, query, workspace
filter and paging state without terminal geometry. Application integration keeps
only the in-flight host request and supplies decoded rows. Search, selection,
dismissal and page requests carry the
originating session version and chooser identity. A selection names a thread,
not a row index. The application owns list requests and bounded scanning; the
terminal owns highlight, scrolling and viewport-driven page demand. Replacing
the chooser or changing its query/filter resets terminal navigation.

`/resume THREAD_ID` and picker selection obey session control's idle gate. The
current thread is marked and selecting it only reports that it is already open.
The old session stays live until host acceptance, buffering events and locally
stacking input. Success unsubscribes it and replaces the attachment, then restores
the selected workspace, Main, roster, children, preferences and retained Diff.
Returning to a left thread readmits it. Failure replays buffered events into the
preserved attachment and restores waiting input. Native clients retain their local
editors by original thread while attached; drafts are not durably stored.

For a native target already running in the server, the same idle admission instead
attaches the renderer to that existing execution owner. The source view remains
usable until the destination supplies its initial snapshot. Pending input and
consequences keep that source interaction alive after the visible switch. An unavailable target
reports failure without issuing a duplicate host resume. `/quit` detaches a native
view; confirmed `/archive` retires the archived execution and its owned resources.
Closing a startup resume chooser before a thread is selected ends that unconfirmed
startup. Closing a view after startup selection leaves admitted execution running.

`/fork` switches to the host-created branch through this same attachment and
history restoration flow. It has no selection dialog and never submits a prompt.

### Restoration

Each runtime hydrates its host messages and command/edit items, retaining
their original placement and reconciling buffered items by identity. Failed turns
retain a visible error; an active snapshot retains its steer/interrupt target.
Each runtime reads full host resume history before trimming to its retained
transcript window.

Roster restoration never treats historical unfinished turns as live work. Only
confirmed Thread Control outcomes claim delivered assignments/messages. The
host-selected rollout supplements missing cell failures, after identity validation,
within its last 64 MiB. Evidence joins its recorded item anchor, or its recorded
time when no anchor is available, without reordering existing entries. Later
live requests deduplicate under the [history contract](#history).

Restoration reports progress and preserves typed input as an unsent draft;
`/quit` or Ctrl-C on an empty draft can exit. Child runtime availability does not block root restoration.

Restoration releases new input only after retained review has consumed the admitted
runtime identity in its own workspace. Missing evidence follows
[saved-review loading](../terminalsession/SPEC.md#boundary); it cannot become successful
recapture. Later observations join only the admitted lineage.
Applied [settings](../../../../modules/sessionpreferences/SPEC.md) and
[terminal preferences](../terminalsession/SPEC.md#preferences) restore independently;
missing settings show defaults with a notice, and save failures do not undo applied
host state. Provider selection stays invocation-local. No restoration issues model
requests or manufactures host-resumable history.

## Ordinary input

Interruption availability is published from control and pending-input state,
independently of the visible queue payloads.

Ordinary draft edits and submission name the attached view and draft revision.
Editor actions name the ordinary draft or active prompt identity and revision.
Admission precedes text, caret or history mutation; the same source-coordinate
actions are usable without terminal keys. Composer operation intents use the same
version and editor target before submission, cancellation, waiting-input restore,
clipboard acquisition, external editing or effort changes. Acquisition results
retain their original attachment and editor target across unrelated publication
updates. Secret editor actions remain local to
the terminal after prompt admission.
Completed paste admission checks the originating editor and menu before text or
attachment acquisition. A changed destination cannot redirect a paste that began
in another draft, prompt, menu or attachment.
Referencing selected source admits its attachment, displayed thread and editor target
as one operation, including return from a displayed child to Main. Unrelated status
or output progress does not invalidate the reference. Replay rejects it. Rejected
admission preserves both the displayed thread and input.
Admission precedes taking the draft or reading attachment sources, so a stale
intent cannot clear, capture or submit newer input. Main, live admitted children
and the selected side own separate input destinations. Observational child views,
replay and prompt-owned editors cannot admit ordinary input.
Restoration and archival preserve the editable draft until submission is admitted.
Pending session replacement accepts locally stacked input without sending it to the
old thread; successful replacement carries that input into the new attachment.

Submission explicitly selects send/steer or next-turn queueing. Acceptance into a
local queue is separate from immediate host readiness. Attachment preparation and
the existing control gates run before transport issuance, preserving rejection
restoration, pending prompts, skill acquisition and provisional echo ordering.

The session workflow selects eligible batches, stages their control identity and
settles acknowledgement, commitment and turn completion separately. Conversation
owns payload consequences; Session Control keeps the single host-request ledger.
Effects returned to the renderer identify pending input and changed prompt records,
without embedding cursor positions, painted sequences or host response handles.
Acquisition and transport remain outside the serialized presentation transition.

Prepared native input rechecks its captured thread, workspace, attachment and
current operation gates after acquisition. Output or clock progress alone cannot
invalidate the prepared snapshot. Delayed command cleanup and insertions validate
their originating editor even when another editor has become selected.
Rejected input returns only to its originating live editor. Explicit withdrawal
for editing transfers locally waiting input to the requesting editor; submitted
input remains host-owned. A disconnected editor receives no restoration, automatic
retry or replacement server draft; existing history and failure facts remain.

Draft token annotations are shared between submission projection and terminal
editing. Session Presentation maps Conversation's existing atomic spans to source
annotations; wrapping, painting and cursor geometry remain terminal-owned.

Compaction, interruption and settings acknowledgements apply their control
dispositions to the same Conversation queues without a renderer. Rejected compact
starts restore the command before waiting input; rejected prerequisite interrupts
restore waiting input and retain the active turn's status. Rejected expedited
steers restore waiting input only after the interrupted turn has ended. A failed
default-effort repair restores waiting input while keeping the repair gate visible.
The terminal receives only editor-restoration effects and user feedback.

## Session title and progress

`/title NAME` immediately names Main without interrupting work or submitting model
input, then saves through [session control](../sessioncontrol/SPEC.md#thread-names).
Empty names only show usage; during replacement the command keeps its draft for
the new session. The header shows the latest pending manual name, falling back
to the confirmed name on failure. Confirmed names survive resume/fork.
[Session naming](../../../../modules/sessionnaming/SPEC.md) owns automatic eligibility
and generation; candidates remain hidden until host confirmation.

The live composer's runtime supplies its turn state and user-only feedback;
observational child views retain a read-only label. Its caption uses the displayed thread's model, effort,
effective tier, context and output rate; missing child settings never borrow Main's.
Side and One-off use that same caption projection. Their confirmed fork settings,
live settings updates, effective per-thread tier and usage stay scoped to their
own identity; switching back to Main restores Main's metrics. Closing a side
conversation retires its displayed metrics.
Child captions additionally show the admitted role alongside that child's
host-confirmed model and reasoning effort. Settings changes, follow-ups and resumed
facts update the same caption; no parent setting or inferred default fills a missing
child fact.
Known context uses the latest host last-turn total and context window independently
of cumulative usage. Before usage arrives it shows 0% used; an absent window shows
only the known count. Host reports, including compaction reductions, replace the
snapshot. Identity-validated rollout context cannot overwrite newer live usage.
Accounting and rates remain with [usage](../../../../modules/usage/SPEC.md).

Compaction replaces Working with `Compacting context`, then emits `Context compacted`
on success. Turn termination clears unfinished progress without claiming success;
child compaction does not replace Main status. Empty-stdin polling uses `Still running`
without repeated transcript rows; completion, other activity or turn end clears it.
Compaction takes precedence. Steering returns to the active state after success.
Public reasoning stays in Activity rather than replacing composer status. Live
progress animates; restored/completed progress does not restart. Completion freezes
elapsed time, using Activity's shared compact duration grammar.

Server-coordinated logical waits update the caller's roster through
[Activity](../../../../modules/activity/SPEC.md#records-and-codex-connection)
and keep Main busy and interruptible after the
host turn stops. The composer shows waiting, elapsed work continues across the
continuation, and the local stop emits no task-completion notice. Cold restoration
shows paused work without restarting its clock or subscription.

Router errors remain composer feedback, not transcript or provider-response edits.
Editing/dismissal clears actionable feedback. Elided feedback opens complete retained
details, including an entire router-error batch. Model-visible errors retain their
own transcript presentation. Unsupported server requests remain visibly pending and
are never auto-approved.

## Session status

The attached presentation model owns the identified panel, structured fields and
detached capture-owned metrics snapshot. Refresh and close intents name that panel and the
originating session version. Late actions cannot affect a replacement panel.
Application integration retains account-request correlation and refresh timing;
terminal layout, scrolling, metrics tabs and retained-exchange selection stay with
the terminal attachment.

`/status` opens a temporary responsive, scrollable, selectable panel without model
input, configuration changes or transcript entries. It shows known model, effort,
effective tier, provider, directory, identity, approval/sandbox policy, instruction
sources and context remaining. Missing values are omitted. Themed remaining-usage
gauges retain numeric percentages for readability without relying on color.
Selection excludes borders/gauges; Escape clears selection before closing.
Enter, q or Ctrl-C also closes without
interrupting work; arrows, wheel and PgUp/PgDn scroll independently of Main.

`account/read` refreshes account information without refreshing credentials.
ChatGPT additionally reads rate limits, showing account/plan, reported quota windows,
remaining percentages, credits and usage link. Resets show local expiry and the
system timezone's UTC offset at that instant, including daylight saving. Available
reset counts remain authoritative even when fewer details are supplied; missing
details never imply zero. Non-ChatGPT sessions show observed input/output tokens.
Failures stay in the panel; closing retires late responses. Billing controls and
reset redemption are outside the panel's scope.

## Session metrics

`/session` uses the same temporary panel, consuming a detached
[transport-diagnostics snapshot](../../../../modules/transportdiagnostics/SPEC.md)
without its own counters. Left/Right and Tab/Shift-Tab cycle Overview, Transport
and Exchanges; ordinary panel controls scroll, select and close. Overview groups
settled requests/retries, provider-usage coverage, cache rate and capture health.
Transport includes all four control-message directions. Exchanges retains each
attempt's telemetry, returned service tier, timing, status/errors, cache-prefix
diagnostics and validated request kind. Missing usage differs from zero; local
deduplication estimates remain separate from provider evidence.

Snapshots refresh once per second only while open, or immediately with `r`.
`[`/`]` selects retained exchanges; newest follows, older selection stays fixed
until reclaimed. Scope is the invocation across all threads, never historical
launch metrics reconstructed by resume/replay. Unavailable capture and empty history
have explicit states. Debug launch shows its actual artifact directory even without
capture, using shared workspace-relative formatting; it stays invocation-local and
out of sanitized metrics. Closing releases only the view. The JSON metrics endpoint
remains independent; there is no HTML dashboard or startup dashboard announcement.

## Acceptance

Display and read-only admission remain usable without a terminal
consumer. Acceptance includes retry, cancellation, replacement and replay isolation,
live history reconciliation and retained terminal presentation. Offline replay
does not establish live host coverage.
