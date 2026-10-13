# Native orchestration

## REQ-ORCHESTRATE-001 — Isolated batch preparation

Orchestration uses separate Codex threads and checkouts under one Mekugi session.
Native collaboration tools retain their existing behavior and workspace. The
invocation-local `orchestrate` MCP server uses host caller metadata and retained
workspace ownership. Tool arguments cannot select another caller's workspace or
run. User configuration remains unchanged.

A retained empty-workspace journal does not override a unique retained workspace.
Multiple retained nonempty workspaces remain ambiguous and reject dispatch.

Only an independent coordinator prepares batches. An orchestration child or its
native descendants cannot start a nested run, including after fresh resume without
a live parent. Retained run identity governs coordinator admission.

Git, SVN and unversioned shadow preparation and listing are available
through `tools.mcp__orchestrate__prepare({task_name:"batch"})` and
`tools.mcp__orchestrate__list_agents({})`. Preparation creates no Codex thread and
starts no model turn. Main can copy required ignored inputs into the returned
checkout before a later spawn. Task names contain lowercase letters, digits and
underscores, begin with a letter, and contain at most 64 characters.

Git batches start at the source's committed baseline, with their own branch.
Source index, uncommitted files and ignored inputs stay
in the source checkout. A selected
subdirectory remains the child's working directory inside the new checkout.
Preparation returns the task name, branch, checkout, cwd, baseline and observed
preparation state. Repeating a successfully prepared task returns its retained
record; it never resets the branch or replaces files copied afterward.

Optional `evidence: [{name, source}]` on `prepare` copies regular files from
absolute source paths into run-owned storage outside both checkouts. Names are
unique single filenames. The result returns their retained paths, and the child's
first input lists them before its assignment. Copies preserve bytes without
changing source files. Repeats keep the original copies, even after the source
changes or disappears; reusing a name with another source rejects. Copy failures
remain visible and prevent launch. Missing or changed retained copies also prevent
launch. Main still copies inputs required at checkout-relative paths itself.

The durable run record is scoped to the selected workspace and coordinating
thread. Preparation intent is saved before checkout creation. A failed or
interrupted preparation remains visible; retry does not blindly repeat its
filesystem effects. Listing reads retained facts without starting processes or
claiming that historical preparation state proves current filesystem contents.
Prepared checkouts remain until integration is accepted or Main requests cleanup
of an accepted batch. Branches remain after cleanup; ordinary replay retention
does not remove orchestration resources.

Git preparation includes locally initialized submodules, recursively, at the
gitlink commits recorded in the baseline. Each is an independent local clone
on the batch branch, with its path, source repository and baseline retained in
the run. Source submodule edits and newer commits do not follow. Uninitialized
submodules remain uninitialized; submodule preparation never fetches remote URLs.
A missing local baseline fails preparation. Main imports and integrates any changed
submodule commits with native Git before the superproject.

Acceptance:

1. Two batches have distinct branches and checkouts at the committed baseline;
   source dirty files and index are unchanged.
2. Repeating preparation and reopening the store preserve the same checkout,
   including later copied ignored inputs.
3. Missing or invalid caller identity, invalid task names and unsupported sources
   reject without creating an unowned checkout.
4. Preparation failure retains its intent and failure state; a repeat does not
   retry the effect. Listing another thread cannot reveal the first run.
5. Registration is invocation-local and does not change existing journal tools.

## REQ-ORCHESTRATE-002 — Fresh batch threads

The MCP server exposes `spawn_agent` for a prepared batch and `interrupt_agent`
for its live turn. For example, after preparation:

```js
await tools.mcp__orchestrate__spawn_agent({task_name:"batch",message:"Complete the assigned batch and report its checks."});
```

`kind` defaults to `implementation`; `diagnosis` selects a read-only assignment
without implementation review. For implementation, `required_checks` lists the
validation commands Main selects for the scope. The runtime runs each command itself
in the batch cwd before review. Checks cannot be supplied for diagnosis.

Children start as independent Codex threads with fresh context. `message` carries
the complete assignment. Main can select model, reasoning effort and service tier;
omitted settings inherit its current effective values. Host-confirmed settings
are authoritative; resume reads current settings and history from Codex rather
than retaining the initial thread-start response. Native subagents stay inside the child's checkout. Host thread
identity selects their journal workspace. Presentation keeps identical native agent paths in
different thread trees separate, without changing host identifiers. Live native
descendants also prevent coordinator departure.

A launch reserves its prepared checkout before requesting a host thread. Repeats
return retained intent and confirmed identities; a changed handoff or configuration
rejects. New launch records retain checkout state, without separate starting,
started, or launched labels. Thread and turn IDs show acknowledged progress.
Confirmed thread identity is saved before the first turn, and confirmed turn
identity is saved before success is returned. Uncertain outcomes require
inspection rather than an automatic retry. Launch checks the checkout location,
branch and baseline without resetting prepared inputs.

Cancellation prevents the first turn when possible. Late host acknowledgements
remain useful; an already-started turn is interrupted even if saving its result
fails. An interrupt acknowledgement does not claim the turn has finished: host
lifecycle events supply that outcome. Checkout preparation and orchestration
run-storage waits leave input and host events responsive. Running batches require
exit confirmation and prevent replacing the coordinator. Viewing another thread
leaves its work subscribed and does not replace the coordinator.

The prepared checkout replaces the source repository in runtime workspace roots;
explicit external roots and an empty root list remain inherited. Named permission
profiles and approval-review routing remain intact. When Codex reports no active
profile, the complete host-confirmed sandbox policy is inherited, including
network access, writable roots and temporary-directory exclusions. Source-local
writable roots map to the prepared checkout; external roots remain unchanged.
The first turn applies this retained policy before any model or tool work.
Fresh-process batch resume restores the batch's latest host-confirmed custom
sandbox policy before accepting input, including changes made after launch.
Unavailable permission settings reject launch rather than guessing a profile.

## REQ-ORCHESTRATE-003 — Batch lifecycle waits

Main calls `tools.mcp__orchestrate__wait_agent({targets:"all"})` to observe
the next batch completion, message, journal blocker, failure, question or approval.
`targets` accepts `"all"` (the default) or a nonempty list of task names or
`/root/task_name` paths in this run. A targeted wait returns the next event from
any selected batch; events from other batches remain queued. Invalid or unknown
targets reject the wait. An available event returns immediately with
the task, available host thread and turn identity, event kind and host status.
Otherwise the call registers a logical wait and promptly returns its identity.
The logical wait continues independently of the MCP request. Its result is
delivered through the existing host notification path, without polling model turns.
Repeating the same host call does not register or deliver the wait twice.
The reply contains a wait `id` and `state`. An immediate completed reply carries
the selected `event`; `registered` means the subscription remains pending.
Later results arrive as attributed `WAIT_RESULT` notifications with the same wait
identity. A canceled subscription returns `canceled`.

To cancel a subscription, call
`tools.mcp__orchestrate__wait_agent({cancel_id:"WAIT_ID"})`.
`cancel_id` cannot be combined with `targets` or `timeout_ms`.
A successfully completed turn delivers its last completed agent message to Main
as a separate `FINAL_ANSWER` notification through Codex's thread history. Delivery
preserves the message text; a turn without a message has an empty payload. Delivery
does not require a wait, start or steer Main, or enter the human composer.
An active Main receives it through the host mailbox; an idle Main retains it for
its next turn and resume. The completion wait returns identity and status after
host acknowledgement, without repeating the answer. Messages from earlier turns,
native descendants and asynchronous questions do not supply that result.
Delivery failures are visible and are not retried automatically. Restored history
does not deliver the answer again. Failed turns retain their host error message. Prompt events
identify their host request or asynchronous question item. Cross-thread answering
remains part of the accepted navigation capability below. Events describe
observations, not a claim that a prompt is still pending when the result is read.

Wait registrations and selected results are retained before success is exposed.
A disconnected observer cannot consume a later event. Delivery acknowledgement
settles the selected result; repeating that call then reports delivery without the
event. An uncertain or rejected delivery, including one interrupted by restart, is
never dispatched again automatically. `list_agents` reports it under
`unsettled_waits`; canceling it states that the result is absent from Main's history
and returns its event to the queue. Canceling a registered wait ends its subscription
without interrupting a child or consuming an undelivered event. Recovery drops
unclaimed question and approval events because replay does not revive their host
requests.
An authored task newly becoming `blocked` in a confirmed child's journal, including
its native descendants, returns `kind: "blocked"` with the owning thread, journal
path and reason. Publication follows successful journal persistence and uses the
run's durable ancestry, without requiring a Main task binding. It does not change
the child's host lifecycle. Duplicate mutation receipts, reason-only edits and
restored history do not create another event.
Terminal outcomes become available only after their run record is saved.
Persistence failure returns `kind: "failure", status: "storage_failed"`.
Waiting leaves input and host events responsive. An omitted `timeout_ms` means no
logical deadline, not an indefinitely open MCP request. An explicit timeout accepts
one millisecond through twenty-five minutes; expiry delivers `timed_out: true`.
Recovery reconciles retained logical waits and deadlines without reviving host
processes, transport requests, or live prompt handles.

## REQ-ORCHESTRATE-011 — Runtime review workflow

New implementation batches enter a runtime-owned workflow. Diagnosis assignments
remain read-only and do not enter implementation review. The assignment retains
its accepted scope and required validation commands. Main supplies this assignment;
neither Main nor the implementer decides whether to dispatch or skip its reviews.

The workflow advances through implementation, candidate validation, queued review,
review, correction, and readiness for integration. A candidate identifies an exact
clean, committed revision. Mekugi runs each required check through host command
execution in the batch checkout with the implementer's permissions; only the host
exit status is validation evidence, never an agent's completion message. A failed
check returns to the implementer for correction with its output tail. A check without
a confirmed exit status requires a decision. An implementation or correction result
that is not a clean, committed checkout also returns for correction. A changed
candidate invalidates prior check and review approvals.

Every implementation candidate requires independent correctness and simplification
reviews. Mekugi starts fresh review threads with host-enforced read-only
permissions and schema-constrained verdicts. Mekugi's optional
`[orchestration.reviewers]` settings map `correctness` and `simplification` to
arbitrary Codex role names. No named role is selected by default. Empty or omitted
selections use built-in review instructions and inherited execution settings.
Selected roles supply instructions, model, reasoning effort, and service tier from
the source workspace's enabled Codex configuration layers and role files. Missing
fields retain the built-in rubric or inherited execution setting. Mekugi does not
change user configuration. Explicit per-role service tier takes precedence over
the coordinator's tier. A missing, unreadable, or malformed selected role is a
visible blocker. Correctness reviews check the accepted scope and
retained behavior for concrete defects and validation gaps. Simplification reviews
check whether the added mechanisms are necessary and identify smaller approaches
that preserve the required behavior. The host remains the execution
and permissions authority. Missing required host capabilities become explicit
blockers rather than weaker reviews or silent approval. Both reviews of a candidate
run concurrently, and each batch's workflow progresses independently of the others.

Each verdict names the candidate and review purpose, with a result of clear,
findings, or blocked. Findings have stable identifiers. The reason field contains
only a blocker and is empty for clear or findings verdicts; clear and blocked
verdicts have no findings. The reviewer's schema-constrained final answer remains
the verdict even when the reviewer completes journal work in the same turn.
Missing, malformed,
wrong-revision, or failed review results do not count as approval. Mekugi collects
both results before delivering one correction assignment directly to the implementer.
The implementer can fix or dispute findings but cannot approve its own candidate.
Each changed candidate receives validation and both reviews again. Two correction
rounds are permitted for failed checks, unclean results, and findings combined; a
further rejection then requires a decision.

Host lifecycle events drive dispatch and progress, not model polling or long MCP
waits. Main receives a single readiness notification with the implementer's final
report, or a blocker naming its reason, failed check output, review findings, and the
next actions, instead of intermediate review and correction relays. The UI retains
phase progress. While Mekugi drives validation, review, or correction, the implementer
accepts no manual input. New implementer input after a blocker, cancellation, or
readiness, through a follow-up or the implementer's view, starts a new
implementation round with a fresh correction budget; late host results from the
stopped round are not adopted.
Dispatch intent and confirmed thread/turn identities are retained before dependent
actions. Recovery discovery reads retained and host state without resuming child
threads or starting turns. Further dispatch that needs a resumed child waits for
explicit resume through the existing navigation or messaging path. Uncertain
dispatch is not repeated blindly. Cancellation stops undispatched transitions and
uses the existing host interruption path for active work. It never means successful
completion.

Integration remains Main's responsibility within the accepted destination and
authorization. A reviewed candidate must still match its revision when integration
is admitted. Workflow readiness does not replace Git ancestry or shadow-writeback
verification. Earlier retained batches without workflow records keep their existing
completion semantics; new implementation batches cannot omit the review gate.

## REQ-ORCHESTRATE-004 — Batch follow-ups

`tools.mcp__orchestrate__followup_task({target:"batch",message:"Continue the batch."})`
delivers input through Codex to a confirmed live batch thread. An idle batch
starts a turn; a running batch receives a steer with the active turn identity as
its precondition. Main and confirmed child roots can target a sibling by task
name or `/root/task_name`. Native subagents are not orchestration run members.
`main` names the coordinator; a batch named `main` uses `/root/main`.

The run saves delivery intent before host dispatch and saves acknowledgement
before returning success. Repeating the same host call returns its retained
delivery state without resending. Changed input under the same call identity
rejects. Unconfirmed dispatches require inspection, not an automatic retry.
Canceling a follow-up stops undispatched work; a late acknowledgement is retained
without interrupting the recipient's existing work. Host rejection is reported
without replacing the batch's launch or turn-completion facts.

`tools.mcp__orchestrate__send_message({target:"batch",message:"Use the revised input."})`
delivers input promptly to a running batch without starting a new turn. For an idle
batch it queues input without waking it. Queued messages accompany its
next idle follow-up turn, in arrival order before the new assignment. A running
follow-up steers only its own input and leaves deferred messages queued.
Acknowledgement records which turn received the queued input. A rejected or
undispatched canceled follow-up leaves that input queued; an uncertain dispatch
requires inspection before resending. Repeating a message call never queues it twice.

Confirmed child roots can use either messaging tool with `target: "main"`.
The run retains the report and Main receives a `kind: "message"` event through
`wait_agent`, identifying the sending batch and thread. Repeated calls return
retained progress without another event. Child text never starts or steers a
coordinator turn, enters the human composer, or accompanies user input.
Previously queued child-to-Main reports remain inspectable without becoming
user input. Human decision questions use Codex's native question tools and
appear in the shared question UI with their source thread.

## REQ-ORCHESTRATE-005 — Cross-checkout journals

Main binds its journal task to `/root/task_name`. Confirmed orchestration threads
mount read-only under that binding across the run's checkouts; native descendants
retain their nested paths. Run records authorize these views after restart, without
changing the child's own independent journal root. Child reads can select Main
with `agent: "main"`, while other agent selectors retain their native local paths,
but cannot reveal sibling results. Child completion leaves Main's integration task
unchanged.

Git batches use linear native Git integration, preserving unrelated source edits.
Child and source history added after each recorded baseline must contain no merge
commits, including recorded submodules. Existing merges at or before the baseline
remain unchanged. Main integrates changed submodule commits locally and records
their results in the source's committed gitlinks before accepting the superproject.
Each recorded source submodule repository must contain its committed gitlink object,
including nested submodules. Acceptance reads these objects locally without fetching
remote objects; an object present only in the batch clone keeps acceptance open.
Cleanup cannot supply the first import needed for acceptance.
Shadow batches use `integrate`. Main marks its bound journal
task `accepted` only after integration is verified. For Git,
acceptance verifies that the batch's commits are reachable from the source's
committed revision or have patch-equivalent commits there. Unmatched commits or
new merge commits keep acceptance open. For shadow, it verifies
confirmed writeback and containment of its changes in the current source. It records
the batch tip and source revision or merged tree before changing the task. The child checkout must retain its recorded location
and branch and have
no tracked or untracked changes; ignored prepared inputs may remain. Checkouts
with assume-unchanged or skip-worktree paths require inspection before acceptance,
including recorded submodules, because those flags can conceal unfinished edits.
Source edits and index are preserved. Acceptance verifies integration, not review.

Only the run coordinator can accept its existing task bound to a confirmed batch.
Active, unresolved or failed child lifecycles, including native descendants, keep
acceptance open. A batch task uses `accepted`, rather than `done`, to release its
parent's completion gate. An abandoned task uses `dropped` with a reason under the
ordinary lifecycle rules. Reopening an accepted task uses `working` and requires
new integration evidence before acceptance. Retained acceptance remains available
after restart without checking the VCS again or reviving processes.

## REQ-ORCHESTRATE-006 — Restored run discovery

Resuming Main restores its retained run into the Agents roster. Preparation
failures, uncertain launch records and confirmed child identities remain visible.
A recorded running turn displays as interrupted after restart; its stored last
observation remains unchanged. Other recorded outcomes retain their lifecycle.
These rows are unsubscribed facts, with no live timers or usage inferred from them.

Recovery reads the selected Main's manifest without checkout effects, child
resume requests, turn dispatch or process revival. Empty runs leave ordinary
session navigation unchanged. Read failures remain visible rather than appearing
as an empty run. Selecting a confirmed retained child resumes it through Codex
and restores its settings and history before switching the view. Messaging a
retained recipient uses the same admission before dispatch. Checkout identity
and host thread/cwd must match the run. Dirty files and later commits remain
intact; resume neither repeats launch nor revives prior processes. Uncertain
launches without confirmed thread identity remain inspectable. Subscribed rows
keep their live state and replace the matching retained row.

## REQ-ORCHESTRATE-008 — Unversioned shadow batches

Unversioned sources can prepare a shadow batch in a run-owned private Git
repository. The complete source snapshot honors ignore files, excludes VCS
metadata and preserves on-disk bytes, executable state and symlinks. Incomplete snapshots fail
preparation. The child starts in a branch worktree with fresh context; source
files remain unchanged. Prepared inputs and private branch history survive
reopening, independently of observation-snapshot retention. Shadow integration
and acceptance use the source-preserving writeback below.

### SVN committed baselines

SVN batches use the same private Git checkout and integration path. Preparation
reconstructs the working copy's committed per-path baselines from a temporary
copy of its local metadata, including mixed revisions. Local file and property
edits, scheduled additions, replacements, moves and deletions stay in the source.
Versioned files remain in the baseline even when Git ignore files match them.
The selected subdirectory remains the child's cwd inside the reconstructed
working-copy root. A directory absent from the baseline cannot launch.

Preparation uses the installed SVN client without fetching remote content or
changing source metadata. Missing pristine content fails preparation. File externals
recorded in the local metadata keep their committed baseline. Unversioned inputs
and separate directory externals do not follow; Main prepares required inputs explicitly.
Keep source SVN operations idle while its metadata is copied. Integration writes
only merged source paths and leaves SVN scheduling and metadata unchanged; Main
owns any subsequent SVN add, delete or commit.

## REQ-ORCHESTRATE-010 — Shadow integration

Main calls `tools.mcp__orchestrate__integrate({target:"batch"})` for a completed,
idle shadow batch. It plans a merge before applying only changed paths to the
source. Main and the child retain their checkouts and branches. Git batches
continue to use native Git integration.

The plan merges the child's committed tip with a fresh source snapshot using
the batch's original baseline, or its last integrated tip for a follow-up commit.
Nonoverlapping source edits survive. Text and binary
merges use snapshot bytes; configured filters and custom merge drivers do not run.
Conflicts return their exact paths with `state: "conflicted"`; a clean merge returns
`state: "applied"`, its source revision, merged tree and changed paths after
writeback. The run retains its plan and per-path progress across restart. A
conflicted call writes nothing; Main can repeat after resolving the conflict.

The child checkout must retain its recorded branch and location and be clean.
Assume-unchanged or skip-worktree paths require inspection before integration.
Pending turns, native descendants, questions, approvals and unsettled deliveries
prevent integration. Main alone can request it. Snapshot, merge or persistence
failure before writeback is reported without changing the source. Every changed
path must still match its planned source preimage before any write. Writeback
preserves unrelated and ignored files, VCS metadata, executable state and links.
External writers must leave affected paths idle during writeback; checks and
replacement are not an atomic filesystem transaction.

Intent precedes each path effect. Cancellation or a failed write returns retained
partial progress. A repeat verifies applied paths and reconciles an uncertain path
only if it already matches the merged result; it never blindly repeats uncertain
replacement. Other uncertain paths require inspection. Temporary paths from an
interrupted replacement remain identified in the result for inspection. Pending
paths can continue when their preimages still match. Source preimages and merged
blobs remain in the private repository for recovery. Integration evidence becomes
available only after every changed path is confirmed. Main then marks
its bound task `accepted`; writeback alone does not accept it.

After confirmed completion, acceptance and repeated integration verify that the
source still contains the writeback changes. Later nonoverlapping edits, including
sibling changes to the same file, remain intact and do not invalidate the proof.
Removed or overwritten changes fail verification. Verification changes neither
source files nor the retained plan; incomplete or uncertain writeback still uses
the exact preimage and merged-result checks above.

## REQ-ORCHESTRATE-009 — Accepted checkout cleanup

Newly saved journal acceptance requests cleanup of the integrated, idle Git or
shadow batch checkout, including Git's run-owned submodule clones. Cleanup starts
only after acceptance and integration evidence are durable. A cleanup failure stays
visible and preserves the accepted integration; Main can inspect and retry through
`tools.mcp__orchestrate__cleanup({target:"batch"})`. Restoring accepted journal
history does not start cleanup or repeat an uncertain removal.

After checkout removal, Mekugi archives the accepted child batch through
Codex's native thread archive. Main and independent reviewer threads are not
archive targets. Native spawned descendants follow Codex's archive semantics.
Archive intent is durable before dispatch and keeps Main's accepted binding
fixed. Host confirmation is retained separately from checkout removal. Archive
failure preserves acceptance and history and remains visible; an unconfirmed
request requires inspection and is never automatically repeated on recovery.

Shadow cleanup retains the private repository and its history and leaves
the source's files and VCS metadata unchanged. The bound journal task must still
be `accepted` with the run's integration tips. The checkout must retain its
exact recorded location, repository, branch and accepted tip and be clean. Pending
input, questions, approvals, native descendants or queued and uncertain deliveries
preserve it. Cleanup resumes an accepted retained child when needed to confirm
current idle state. This restores its settings, history and saved Diff without starting or
steering a model turn or changing the viewed thread.

Removal intent is retained with Main's current acceptance before the native Git
effect. The accepted binding stays fixed while removal is pending. A repeat
reconciles a missing checkout only when its Git registration is also absent and its branch
still retains the accepted tip. A surviving checkout after uncertain removal
requires inspection; cleanup never repeats that effect automatically. Branches,
manifests and journals remain. The subscribed transcript stays
viewable, with new turns disabled after removal.
Archived batches remain selectable in the retained run. Reopening their history
reads it without resuming or unarchiving the thread, changing host settings, or
starting a turn. Saved Diff and journal history remain available.

After checkout removal, cleanup removes unchanged evidence copies at their exact
recorded run-owned paths. Changed, redirected, shared, failed or uncertain copies
remain, as do unknown files and directories. Each copy's removal intent and outcome
are retained. Independent copies continue after a failure, and the result reports
partial cleanup. A repeat can reconcile a missing copy with retained removal intent;
it never repeats uncertain deletion of a surviving copy or removes a recreated file.
Copies without retained filesystem identity remain for inspection. Main's accepted
binding stays fixed throughout evidence cleanup, including repeat calls. Source
evidence files remain unchanged. External writers must leave copies idle during
cleanup; filesystem checks cannot make hashing and deletion atomic.

Populated submodules must remain at their committed gitlinks, on their recorded
batch branches, with clean worktrees and no stashes or unique branch/tag work.
Checkouts with assume-unchanged or skip-worktree paths require inspection before
cleanup; those flags can conceal unfinished edits.
Removed, deinitialized or unregistered repositories preserve the checkout.
Changed nested commits are retained in the recorded local source repositories
before removal, without switching source branches or changing source edits and
index. Cleanup returns the retained paths, tips and refs. It may add a retention
ref when no source ref already reaches a changed tip. Retention failure preserves
the checkout; successfully retained commits remain available after a later failure.

## Accepted delivery scope

`/orchestrate ISSUES` enters the workflow through the ordinary composer. One
user input carries the issues and the division of work: children implement and
validate requested changes in isolated checkouts; Mekugi drives reviews and
corrections; Main coordinates and verifies integration. For change requests, local
commits and linear Git integration are part of the invoked workflow, subject to
explicit user restrictions.
Diagnosis-only assignments remain read-only. Tool descriptions and parameter help belong
only to MCP metadata, not composer input or recovery instructions. Later ordinary
input does not repeat the instruction or change tool exposure. Attachments, busy
input, rejection and cancellation use the composer lifecycle. Fresh children
receive their coordinator identity, journal access, implementation and delivery
responsibility, and assignment. Codex sets their
working directory when it starts the thread.
Caller guidance and the explicit assignment retain their own policy.
Context resets restore these responsibilities once from durable records;
native descendants retain their existing guidance.
Bare `/orchestrate` opens a picker of this Main's retained batches and coordinator.
It shows concise task names, lifecycle and available live timer, token and cost metrics.
Resumed batch roots appear only in this group, never as UUID-named native agents.
Selecting a subscribed thread switches the viewed shell without starting a turn
or reloading history. Prepared batches remain visible before launch. Retained
threads that are not subscribed remain visible and resume when selected.
Fresh and resumed child views admit the current Diff scope when they open,
including scope observed before the view registered. Resume restores saved Diff
before accepting input. Native descendant admission also reapplies current scope
through the same per-root filter, so changes observed before ancestry was known
become visible without admitting foreign or Main changes.
An empty run offers `/orchestrate ISSUES`; Escape closes the picker.

Orchestration calls use the shared subagent event presentation in the caller's
Main or Activity feed. Italic headers and accent-colored rails distinguish
orchestration from native subagent events without changing their wording.
Confirmed launches use start events; delivered messages
and follow-ups use attributed message events. Waiting uses the shared wait
presentation and names all or the selected batches. Live transcript waits use the
same breathing style as roster waits; completed, failed, interrupted, and restored
waits remain static. Consecutive wait calls remain
peer progress rows, without nested wait bullets. Other calls show concise action labels, supplied targets and observed
call states. Queued input remains queued, and call completion does not claim
that a batch's work or integration finished. Errors remain visible in the feed.
Raw results, including delivery IDs, stay in the existing output dialog rather
than appearing as feed tails. Host item identity replaces a started row with its
result. Restored calls retain their recorded outcomes without reviving work.

The Agents pane (5) lists the coordinator and its batch threads together with
the viewed thread's native subagents. Independent thread rows use italic and
accent styling; native subagents retain their identity and role colors. The
panel has one focus, selection, collapse state and height. Main stays visible.
Within available terminal space, the roster expands for Main and working rows;
when idle/history rows need scrolling, Main and working rows stay pinned above
them. Independent rows retain that order even when all rows fit; switching
threads does not reorder unchanged rows. Actual lifecycle changes can move a
row into or out of the working group. A very short terminal prioritizes Main. Manual collapse and resize remain
available. Subscribed threads use
the same current-activity summaries and metric columns as native subagents,
including captured edit counts from their own workspace and caller identity.
The viewed root keeps its context usage and output rate in the composer only.
The roster rule counts agents and working/error states across the whole run.
The Agents tab badge uses the same run-wide working count.
Token and cost totals likewise cover the coordinator, batch roots, native
descendants, and runtime reviewers, counting each authenticated thread once.
Switching the viewed thread does not change these counts or totals, including
while updated metrics ease into view. The net edit metric remains the composed
change evidence for the viewed workspace, not a sum of per-agent counters.
Unavailable threads retain their observed lifecycle state. Displayed lifecycle
labels use sentence case with spaces, including `Queued review` and `Needs decision`,
consistently in the roster and picker. Live working labels use the shared
breathing animation even when Activity is hidden; restored and stopped labels
remain static. Clicking or pressing Enter on a thread row switches the viewed shell; `Ctrl-B [` and `]`
cycle these threads, resuming a retained confirmed thread when needed. Activity excludes
the viewed root and shows only its native subagents. Switching keeps the currently
visible Activity, Diff or Journal pane and keyboard focus, with the destination
thread's local native-agent filter. Without orchestration navigation, these shortcuts resize
the Diff navigator. The Main pane title shows `main` or `main › batch`.
Transcript, Activity, native Agents, Journal, Diff, settings and unsent drafts
belong to the viewed thread. Switching uses the subscribed in-memory state,
without resuming a thread or reloading history. Background threads continue to
receive events and process their existing input lifecycle. Native events that
precede readable host metadata stay with their originating thread until complete,
unconflicted authenticated ancestry admits them to an already subscribed root.
They then enter that root's Activity in arrival order. Failed observational reads
recheck known identity, and later identity can resolve waiting events without
another host notification. Resolution clears only that thread's ancestry notice;
unrelated notices and unresolved or conflicting ancestry remain intact. Pending questions and
approvals appear on the viewed shell with their source thread. Their answer
editors stay with the source across switching; hiding restores the viewed draft.
Replies, denial feedback and interruption target the originating thread.
Resolution during answer paste discards that paste instead of editing another draft.

`/quit` with active orchestration asks for confirmation on the composer notice.
Enter confirms exit and interruption of all threads; Escape keeps working with
drafts and subscriptions intact. Ordinary Main Ctrl-C keeps its existing
clear/interrupt behavior. Confirmed exit stops new orchestration dispatch and
uses Codex's existing shutdown to stop its running turns. Resume shows interrupted
work without reviving processes.

Native orchestration replaces the home `batch-agent-sessions` skill and helper.
Existing helper manifests, branches and unfinished checkouts remain available for
inspection; orchestration does not import or remove those resources.

Third-party threads retain their current stable schema exposure during initial
delivery. Child-authored journal roots remain unchanged. Nested orchestration
is unavailable.

Supported sources are Git, SVN and unversioned shadow. Other VCS sources reject
preparation. Retained runs from a removed adapter remain inspectable, with their
files preserved. New batch launches, orchestration messages and integration
acceptance reject for those runs.
