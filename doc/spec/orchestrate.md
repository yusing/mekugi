# Native orchestration

## REQ-ORCHESTRATE-001 — Isolated batch preparation

Orchestration uses separate Codex threads and checkouts under one Mekugi session.
Native collaboration tools retain their existing behavior and workspace. The
invocation-local `orchestrate` MCP server uses host caller metadata and retained
workspace ownership. Tool arguments cannot select another caller's workspace or
run. User configuration remains unchanged.

A retained empty-workspace journal does not override a unique retained workspace.
Multiple retained nonempty workspaces remain ambiguous and reject dispatch.

Only the active session's independent coordinator prepares batches. A proven
journal root alone does not grant this authority: reviewers, side threads and
unselected forks cannot prepare batches. Selecting an independent thread as Main
allows its own run. An orchestration child or its
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

`list_agents` defaults to a compact summary of every retained batch, including
prepared, failed and removed batches. Each row identifies the task and checkout
state, available host thread/turn identity and last observed host status. Workflow
rows include kind, phase, generation, candidate revision, reason, current-generation
dispatches, older unsettled dispatches and outcome delivery state. Resuming a workflow
does not hide an older unconfirmed host effect that still needs reconciliation.
`phase: "ready"` identifies recorded workflow
readiness, not verified integration. Missing workflow or host observations remain
unknown. Unconfirmed launch identities, dispatches, evidence failures and shadow
writeback state remain visible. Summaries omit assignment text, launch settings,
reports, check output, review findings and settled historical dispatches.

`targets` accepts `"all"` (default) or a nonempty list of task names or
`/root/task_name` paths in this coordinator's run. Unknown or invalid names reject
the call; repeated names return one row. Results retain run order. `detail` selects
`"summary"` (default), `"checks"` (summary plus required commands and exact retained
check results), `"reviews"` (summary plus exact retained verdicts and findings), or
`"full"` (complete batch records). For example,
`tools.mcp__orchestrate__list_agents({targets:["batch"],detail:"reviews"})` retrieves
one batch's review evidence. Checks and reviews name their candidate revision;
an empty result is not approval. Explicit detail is not truncated. Full records
retain all available prompts, settings, reports and dispatch history. Unconfirmed
wait deliveries remain under `unsettled_waits` for the whole run, even when batch
targets are selected. Summary, checks and reviews include wait identity, state,
target/deadline metadata, event identity and reconciliation error, but omit event
payloads. `detail:"full"` also returns their exact retained event payloads, which
can contain reports, check output and review findings. All views read retained facts
without resuming work or changing checkout, workflow or wait state.

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

A confirmed thread whose first turn was retained as canceled before dispatch
stays idle across recovery and repeated spawn calls. After the user requests
resume, Main can send `followup_task` to start that thread with its original
assignment, evidence and new input, using current host permissions rather than
reapplying launch defaults. Recovery admits the retained thread and exact
cwd through the normal host settings/history checks. A missing turn acknowledgement
without proof of unsent input remains uncertain and cannot authorize this retry.
In Main's orchestration view, the canceled batch's composer stays disabled until
Main's first handoff is acknowledged.

The prepared checkout replaces the source repository in runtime workspace roots;
explicit external roots and an empty root list remain inherited. Named permission
profiles and approval-review routing remain intact. When Codex reports no active
profile, the complete host-confirmed sandbox policy is inherited, including
network access, writable roots and temporary-directory exclusions. Source-local
writable roots map to the prepared checkout; external roots remain unchanged.
Initial spawn dispatch applies this retained policy before any model or tool work.
Batch resume restores the latest host-confirmed named permission profile or custom
sandbox policy before accepting input, including changes made after launch.
These settings remain retained when the batch is opened through ordinary `/resume`.
Missing saved batch permissions reject resume instead of inheriting invocation
defaults. This also applies to older snapshots that did not retain permissions.
Unavailable permission settings reject launch rather than guessing a profile.

## REQ-ORCHESTRATE-003 — Batch lifecycle waits

Main calls `tools.mcp__orchestrate__wait_agent({targets:"all"})` to observe
the next batch completion, message, journal blocker, failure, question or approval.
`targets` accepts `"all"` (the default) or a nonempty list of task names or
`/root/task_name` paths in this run. A targeted wait returns the next event from
any selected batch; events from other batches remain queued. Invalid or unknown
targets reject the wait. An available event returns immediately with
the task, available host thread and turn identity, event kind and host status.
Otherwise the call registers a wait and returns its identity. After the enclosing
exec succeeds, Main's host turn ends locally, without another model request or
journal auto resume. Main remains suspended until a selected event or explicit
timeout yields. No sleep, polling call or finish mutation is needed.
The yield uses Codex's retained tool-output input to start or continue Main,
without adding human input. The host still owns turn execution and interruption.
Repeating the same host call does not register or deliver the wait twice.
The reply contains a wait `id` and `state`. An immediate completed reply carries
the selected `event`; `registered` means the subscription remains pending.
Later results arrive as attributed `WAIT_RESULT` tool output with the same wait
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
without interrupting a child or consuming an undelivered event. Human input or
interruption also cancels unsent waits. An already dispatched yield remains
host-owned until its outcome is confirmed. Recovery drops
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
deadline and no automatic wake. An explicit timeout accepts one millisecond
through twenty-five minutes and stays visible in the active wait row; only its
expiry delivers `timed_out: true`. The MCP request need not stay open.
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
that is not a clean, committed checkout also returns for correction. Current blocked
tasks in the implementer's authenticated owned journal require a decision before
candidate admission, even when a successful host turn has no final message. The
outcome retains the blocked path and reason. Missing or unreadable journal state
also requires a decision. Resolved historical blockers and tasks owned by other
journals do not prevent admission. A changed
candidate invalidates prior check and review approvals. The batch view shows the
running validation command and streamed host output through the ordinary command
presentation. Completion uses the host exit status; missing status stays uncertain,
while canceled checks show interruption.
Command checks do not create model threads, turns, or token usage.
Each candidate retains its queued checks before dispatch. Recovery can continue a
known-unsent check after explicit resume, but a dispatch without a confirmed result
requires host reconciliation before another check, including after follow-up input
starts a new generation. A missing result alone does
not prove that a command never executed. Older unfinished validation state without dispatch
evidence requires a decision. Completed check receipts remain tied to their exact
candidate, command and checkout and are not rerun during recovery.

By default, every implementation candidate requires independent correctness and
simplification reviews. Mekugi starts fresh review threads with host-enforced
read-only source access and schema-constrained verdicts. Each reviewer can write
only to its own temporary/build storage outside the candidate checkout. Temporary
directories and Go's build cache use that storage; network access and permission
escalation remain disabled. This uses a thread-local host permission profile, not
user configuration. Storage ownership is retained across recovery, and cleanup
waits for confirmed host shutdown of the reviewer. Uncertain shutdown retains the
storage. Unsupported host permissions produce a blocker rather than writable source.

The review assignment includes retained host validation receipts for the exact
candidate and generation: commands, checkout, exit status, outcome and bounded
output tails, including passed checks. Reviewers can reuse these results and run
focused checks for uncovered behavior. The implementer's report is separately
labeled as claims, not host validation evidence. Mekugi's optional
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

Confirmed reviewers appear as independent threads in Agents and the orchestration
picker, identified by batch, review purpose and round. Selecting one shows its own
activity and transcript. Runtime ownership remains unchanged: review views do not
accept manual turns, shell commands, or compaction. Verdicts remain private workflow input
rather than batch completion messages to Main. Full host thread identity owns
routing, even when concurrent thread IDs share a prefix. Each reviewer contributes
its own provider-observed usage and statistics once through canonical accounting;
host context usage is not added to provider totals. Retained review identities and
available usage survive restart. Selecting retained history reads it without
resuming review execution or reviving timers, including recorded in-progress turns.
The `/session` dialog uses the same invocation capture from every thread view; it
does not add per-view snapshots together.

Each verdict names the candidate and review purpose, with a result of clear,
findings, or blocked. Findings have stable identifiers. The reason field contains
only a blocker and is empty for clear or findings verdicts; clear and blocked
verdicts have no findings. The reviewer's schema-constrained final answer remains
the verdict even when the reviewer completes journal work in the same turn.
Missing, malformed,
wrong-revision, or failed review results do not count as approval. Mekugi collects
both results before delivering one correction assignment directly to the implementer.
The implementer can fix or dispute findings but cannot approve its own candidate.
Each changed candidate receives validation and, unless explicitly waived below,
fresh verdicts from both purposes. Correction reviews compare each purpose's last
reviewed candidate with the current candidate, addressing unresolved findings,
disputes and affected existing behavior. They reuse unchanged clear scope, reopening
it only for an evidenced interaction with the correction. Prior verdicts retain
their candidate and host-dispatch provenance across correction and restart; they
never approve a new candidate. A purpose without a completed review, including a
blocked review, reviews the full baseline diff. New implementation input starts a
full review round, so changed accepted scope does not inherit delta-only coverage.
Two correction rounds are permitted for failed checks, unclean results, and findings combined; a
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

Main can honor an explicit user request to finish corrections without more reviews
by sending an idle, stopped or ready implementation batch a `followup_task` with
`review_waiver` containing the user's reason. All earlier host work must be settled.
This coordinator-only control is retained for that batch across fresh resume; it
does not change other batches or user configuration. It keeps required validation
and the clean exact-candidate integration gate. A passing candidate becomes ready
with the waiver identified separately from review clearance. Existing verdicts and
findings remain available with their original revisions; no clear verdict is
created. Failed or uncertain checks retain their normal correction/blocker behavior.

Completed corrections remain eligible for lifecycle reconciliation after review
readiness. Fresh discovery and explicit admission of an already subscribed batch
check authenticated host history for the exact implementer thread and correction
turn. A matching terminal result can repair the launch observation and independent
journal lifecycle, including a partially saved repair. Journal repair requires
the same turn; a journal on a proven earlier turn does not suppress ordinary
workflow completion or failure handling. A newer active turn prevents adoption.
This observation starts no child turn,
repeats no review or result delivery, and does not replace integration acceptance.

After authenticated terminal review completion, Mekugi archives that independent
reviewer through Codex to release its private runtime and owned helpers. Host
completion, verdict or failure evidence, and available usage are retained first.
Failed and interrupted reviews can also release their resources after confirmed
completion. Active or unresolved work stays intact. Main and the implementer are
not targets of this trigger. Archive intent and its separate outcome remain in
the review dispatch record. Failures preserve the review outcome and remain
inspectable; recovery never repeats an unconfirmed archive request. Archival keeps
private verdicts, workflow receipts, usage and read-only host history available.

Integration remains Main's responsibility within the accepted destination and
authorization. A reviewed candidate must still match its revision when integration
is admitted. Workflow readiness does not replace Git ancestry or shadow-writeback
verification. Earlier retained batches without workflow records keep their existing
completion semantics; new implementation batches use the default review gate.

## REQ-ORCHESTRATE-004 — Batch follow-ups

`tools.mcp__orchestrate__followup_task({target:"batch",message:"Continue the batch."})`
delivers input through Codex to a confirmed live batch thread. An idle batch
starts a turn; a running batch receives a steer with the active turn identity as
its precondition. Main and confirmed child roots can target a sibling by task
name or `/root/task_name`. Native subagents are not orchestration run members.
`main` names the coordinator; a batch named `main` uses `/root/main`.

For the explicit correction-only control described above, Main uses
`tools.mcp__orchestrate__followup_task({target:"batch",message:"Finish the requested corrections and commit.",review_waiver:"User requested no further reviews"})`.
The reason and delivery intent are saved together before a turn starts. Omission
preserves the batch's current policy. Children and `send_message` cannot select it.

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

Main can inspect explicit child change IDs and ranges through
[`mchanges`](changes.md#bounded-read-command), including after fresh recovery.
Independent batch namespaces can reuse an ID; `--workspace` selects the retained
owner when a read is ambiguous. This read authority does not grant cross-checkout
apply/revert or expose sibling evidence to another child.

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
acceptance open. An implementation batch task uses `accepted`, rather than `done`, to release its
parent's completion gate. An abandoned task uses `dropped` with a reason under the
ordinary lifecycle rules. Reopening an accepted task uses `working` and requires
new integration evidence before acceptance. Retained acceptance remains available
after restart without checking the VCS again or reviving processes.

For a confirmed `kind: "diagnosis"` batch, Main reviews the report and sets its
bound task to `done`. The same mounted host lifecycle checks apply, including
native descendants. This completes the assignment without an implementation
candidate, review clearance or integration proof, including for retained diagnosis
batches whose workflow phase remains `implementation`. Completion stays closed
after restart and requires no child turn. Diagnosis completion retains the checkout
and thread; accepted-only cleanup and archive remain unavailable.

## REQ-ORCHESTRATE-006 — Restored run discovery

Confirmed `/clear` or `/resume` replacement detaches the previous coordinator's
batch subscriptions, views and live wait state. Failed replacement keeps them
usable. Checkouts, batches and durable waits remain retained. Returning to Main
uses ordinary discovery and batch admission again. A host-confirmed resume can
receive settings, lifecycle and native-descendant events through ordinary
restoration, including pending approval requests, while input remains gated; late
results cannot bypass admission or restore detached subscriptions.

Resuming Main restores its retained run into the Agents roster. Preparation
failures, uncertain launch records and confirmed child identities remain visible.
A recorded running turn displays as interrupted after restart until authenticated
host evidence reconciles its lifecycle. Other recorded outcomes retain their lifecycle.
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
metadata and preserves on-disk bytes, Git executable state (owner execute) and
symlinks. Incomplete snapshots fail preparation. The child starts in a branch
worktree with fresh context; source
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
path must still match its planned source preimage before any write. Preimage and
uncertain-write checks use the same Git executable state as snapshots; group and
other execute bits do not change that state. Writeback preserves unrelated and
ignored files, VCS metadata, executable state and links.
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
targets of accepted-checkout cleanup; terminal reviewers use the review lifecycle
above. Native spawned descendants follow Codex's archive semantics.
Archive intent is durable before dispatch and keeps Main's accepted binding
fixed. Host confirmation is retained separately from checkout removal. Archive
failure preserves acceptance and history and remains visible; an unconfirmed
request requires inspection and is never automatically repeated on recovery.
If dispatch was confirmed not to have started, an explicit cleanup retry can
archive the child after acceptance and idle input are checked again.

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
user input carries the issues. Authenticated runtime guidance supplies the
division of work: Main looks up evidence,
holds overall intent, current state and issues, delegates work, and verifies and
integrates results. Children own planning, documentation, implementation,
validation and correction in isolated checkouts. Mekugi owns review dispatch and
correction routing. For change requests, local
commits and linear Git integration are part of the invoked workflow, subject to
explicit user restrictions.
Diagnosis-only assignments remain read-only. Tool descriptions and parameter help belong
only to MCP metadata, not composer input or recovery instructions. Later ordinary
input does not repeat the instruction or change tool exposure. Attachments, busy
input, rejection and cancellation use the composer lifecycle. Batch views reject
`/orchestrate ISSUES` without changing the draft or attachments; bare
`/orchestrate` still opens run navigation. Fresh children
receive their coordinator identity, journal access, implementation and delivery
responsibility, and assignment. Codex sets their
working directory when it starts the thread.
The native command queues its intent in the original composer position. After
input validation, one asynchronous admission initializes run storage and persists
membership without creating a checkout. The entry stays queued until admission
completes; input rejected by local validation creates no run storage. Storage lock waits leave host events,
rendering and keyboard cancellation responsive. Startup failure restores the whole
pending stack in original order. Cancellation releases a newly created unused
binding before further input can dispatch. A definitive host rejection also releases
a newly created unused binding; existing or prepared runs remain intact.
Submitting the command resumes ordinary transcript following.
A successful authenticated batch preparation also establishes run identity.
Before provider forwarding, the instruction projection uses the authenticated
retained cwd and journal/run membership to mask pinned incompatible
global workflow rules. Ordinary turns, prewarm and recovery use that authority when
workspace metadata is empty or names a containing Git root. Fresh native descendants
use their authenticated parent until their own identity is retained. Conflicting
identity and unrelated selected workspaces do not grant membership. It removes the normal Main/review workflow read triggers
and Main-only authorship/delegation restrictions, preserving adjacent safety,
authorization, scope and quality policy. Global instruction files stay unchanged.
The current role policy also governs previously read and recovered workflow text;
tool results remain exact evidence rather than being rewritten.

The projection covers instruction carriers, including wrapped AGENTS.md context,
and refreshes one runtime policy section. It preserves ordinary user messages,
assistant history, tool results, non-text parts, fenced examples and caller-qualified
nonmatching rules. Runtime section markers inside fenced examples remain literal;
only an unfenced owned section is refreshed. Without one, policy is inserted before
the caller text so an open example fence cannot absorb it. Projection is idempotent.
Quoted workflow text cannot select a run;
ordinary non-orchestration requests retain their existing policy.
Non-orchestration history does not impose orchestration identity checks on context
recovery; compaction retains its own continuity authorization and unavailable-reset notices.
Fresh children, resumed Main/children and context resets use the same retained authority without
live parents. Native descendants retain their assigned roles within that division
of work. Explicit assignments and compatible caller policy remain effective.
Bare `/orchestrate` opens a picker of this Main's retained batches, reviewers and coordinator.
It shows concise task names, lifecycle and available live timer, token and cost metrics.
Resumed batch roots appear only in this group, never as UUID-named native agents.
Selecting a subscribed thread switches the viewed shell without starting a turn
or reloading history. Prepared batches remain visible before launch. Retained
batch threads that are not subscribed remain visible and resume when selected.
Retained reviewers open read-only history without resuming execution.
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
and follow-ups use attributed message events. Active waits name all or the
selected batches and show an explicit timeout when supplied. A returned event replaces that
selection with only its source batch and reason: message, done, failure, blocker,
question or approval, with its reported status when relevant. A message does not
claim that its sender or any other selected batch finished. Completion, failure and
blocker labels identify recorded observations, not the batch's current state. An
event can belong to an earlier turn or workflow generation; delivery and restoration
preserve that event without inferring its freshness. Timeout, cancellation,
unconfirmed or failed delivery, acknowledged delivery without a repeated event,
and replies without an event have distinct labels. Event payloads remain in the
output dialog; the row does not expose message or review text. Wait registration and later host yields retain their identities.
A delayed yield updates the waiting row after
acknowledgement, retaining its host item identity and position. Selected results stay in the run's
presentation history after delivery; repeated tool calls still omit the delivered
event. Resume restores these rows without delivering notifications again. Older
records that no longer contain their event show delivery without an inferred source.
Live transcript waits use the same breathing style as roster waits;
completed, failed, interrupted, and restored waits remain static. Consecutive wait calls remain
peer progress rows, without nested wait bullets. Other calls show concise action labels, supplied targets and observed
call states. Queued input remains queued, and call completion does not claim
that a batch's work or integration finished. Errors remain visible in the feed.
Raw results, including delivery IDs, stay in the existing output dialog rather
than appearing as feed tails. Host item identity replaces a started row with its
result. Restored calls retain their recorded outcomes without reviving work.

The Agents pane (5) lists the coordinator, batch threads and runtime reviewers together with
the viewed thread's native subagents. Independent thread rows use italic and
accent styling; native subagents retain their identity and role colors. The
panel has one focus, selection, collapse state and height. Runtime reviewers
nest beneath their owning batch and show only their review purpose and round,
without the batch-name prefix. Groups are collapsed while the parent is idle
and expanded while its authoritative activity state is working. Existing pane
collapse and thread navigation controls remain unchanged. Main stays visible.
Within available terminal space, the roster expands for Main and working rows;
when idle/history rows need scrolling, Main and working rows stay pinned above
them. Independent parent rows retain that order even when all rows fit; switching
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
Collapsing a reviewer group or switching the viewed thread does not change these counts or totals, including
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
