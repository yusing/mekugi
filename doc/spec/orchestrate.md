# Native orchestration

## REQ-ORCHESTRATE-001 — Isolated batch preparation

Orchestration uses separate Codex threads and checkouts under one Mekugi session.
Native collaboration tools retain their existing behavior and workspace. The
invocation-local `orchestrate` MCP server uses host caller metadata and retained
workspace ownership. Tool arguments cannot select another caller's workspace or
run. User configuration remains unchanged.

Only an independent coordinator prepares batches. An orchestration child or its
native descendants cannot start a nested run, including after fresh resume without
a live parent. Retained run identity governs coordinator admission.

Git and unversioned shadow preparation and listing are available
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
Prepared branches and checkouts remain until explicit cleanup; ordinary replay
retention does not remove them.

Git preparation includes locally initialized submodules, recursively, at the
gitlink commits recorded in the baseline. Each is an independent local clone
on the batch branch, with its path, source repository and baseline retained in
the run. Source submodule edits and newer commits do not follow. Uninitialized
submodules remain uninitialized; submodule preparation never fetches remote URLs.
A missing local baseline fails preparation. Main imports and integrates any changed
submodule commits with native Git before integrating the superproject.

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

Children start as independent Codex threads with fresh context. `message` carries
the complete assignment. Main can select model, reasoning effort and service tier;
omitted settings inherit its current effective values. Host-confirmed settings
are authoritative. Native subagents stay inside the child's checkout. Host thread
identity selects their journal workspace. Presentation keeps identical native agent paths in
different thread trees separate, without changing host identifiers. Live native
descendants also prevent coordinator departure.

A launch reserves its prepared checkout before requesting a host thread. Repeats
return retained progress; a changed handoff or launch configuration rejects.
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
profiles and approval-review routing remain intact. Missing profile provenance
rejects launch rather than approximating a custom policy with a sandbox label.

## REQ-ORCHESTRATE-003 — Batch lifecycle waits

Main calls `tools.mcp__orchestrate__wait_agent({timeout_ms:10000})` to receive
the next observed batch completion, journal blocker, failure, question or approval. A wait returns
the task, available host thread and turn identity, event kind and host status. Prompt events
identify their host request or asynchronous question item. Cross-thread answering
remains part of the accepted navigation capability below. Events describe
observations, not a claim that a prompt is still pending when the result is read.

Events observed during this session remain queued until a wait receives them.
An authored task newly becoming `blocked` in a confirmed child's journal, including
its native descendants, returns `kind: "blocked"` with the owning thread, journal
path and reason. Publication follows successful journal persistence and uses the
run's durable ancestry, without requiring a Main task binding. It does not change
the child's host lifecycle. Duplicate mutation receipts, reason-only edits and
restored history do not create another event.
Terminal outcomes become available only after their run record is saved.
Persistence failure returns `kind: "failure", status: "storage_failed"`.
Waiting leaves input and host events responsive. Timeout returns `timed_out: true`;
cancellation ends that wait without interrupting a child. Timeout defaults to
ten seconds and accepts one millisecond through twenty-five minutes. Replay
does not recreate wait handles or live prompt requests.

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
saves input without waking or steering the batch. Queued messages accompany its
next idle follow-up turn, in arrival order before the new assignment. A running
follow-up steers only its own input and leaves deferred messages queued.
Acknowledgement records which turn received the queued input. A rejected or
undispatched canceled follow-up leaves that input queued; an uncertain dispatch
requires inspection before resending. Repeating a message call never queues it twice.

Confirmed child roots can also use `followup_task` with `target: "main"`.
Main receives it through the ordinary composer lifecycle: an idle coordinator
starts a turn and a running coordinator receives a steer. Settings, restoration,
questions, compaction and pending input keep their existing admission gates.
User drafts remain in their editor, and already queued user input takes priority.
The run records intent and acknowledgement just as for batch follow-ups. Child
messages are literal input, including text that resembles a slash command.
Interrupted or rejected child input stays child-owned; it does not enter the
user's draft or automatic composer resend. Local input rejection returns a
delivery failure. Canceled waiting follow-ups release their departure gate.

Confirmed children can use `send_message` with `target: "main"` without waking or
steering it. Messages accompany Main's next ordinary input turn or idle follow-up,
in arrival order before that input. Steers and context compaction leave them queued.
Main's user attachments and draft remain intact. Rejection before delivery releases
the messages back to the queue; uncertain dispatch remains retained for inspection.
Restart restores queued input from the run, without reviving a turn.

## REQ-ORCHESTRATE-005 — Cross-checkout journals

Main binds its journal task to `/root/task_name`. Confirmed orchestration threads
mount read-only under that binding across the run's checkouts; native descendants
retain their nested paths. Run records authorize these views after restart, without
changing the child's own independent journal root. Child reads can select Main
with `agent: "main"`, while other agent selectors retain their native local paths,
but cannot reveal sibling results. Child completion leaves Main's integration task
unchanged.

Main reviews and integrates Git batches with native VCS commands, or shadow
batches with `integrate`, then marks its bound journal task `accepted`. For Git,
acceptance verifies that the batch's committed tip is reachable from the source's
committed revision. For shadow, it verifies confirmed writeback and the current
merged paths. It records the batch tip and source revision or merged tree
before changing the task. The child checkout must retain its recorded location
and branch and have
no tracked or untracked changes; ignored prepared inputs may remain. Checkouts
with assume-unchanged or skip-worktree paths require inspection before acceptance,
including recorded submodules, because those flags can conceal unfinished edits.
Source edits and index are preserved. This verifies integration, while Main owns review.

Only the run coordinator can accept its existing task bound to a confirmed batch.
Active, unresolved or failed child lifecycles, including native descendants, keep
acceptance open. A batch task uses `accepted`, rather than `done`, to release its
parent's completion gate. An abandoned task uses `dropped` with a reason under the
ordinary lifecycle rules. Reopening an accepted task uses `working` and requires
new integration evidence before acceptance. Retained acceptance remains available
after restart without checking the VCS again or reviving processes.

## REQ-ORCHESTRATE-006 — Restored run discovery

Resuming Main restores its retained run into the Orchestration roster. Preparation
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
available only after every changed path is confirmed. Main then reviews and marks
its bound task `accepted`; writeback alone does not accept it.

## REQ-ORCHESTRATE-009 — Accepted Git checkout cleanup

Main calls `tools.mcp__orchestrate__cleanup({target:"batch"})` to remove an
accepted, idle Git batch checkout, including its run-owned submodule clones. The bound journal task must
still be `accepted` with the run's integration tips. The checkout must retain its
exact recorded location, repository, branch and accepted tip and be clean. Pending
input, questions, approvals, native descendants or queued and uncertain deliveries
preserve it. Cleanup requires a subscribed child so current idle state is known.

Removal intent is retained with Main's current acceptance before the native Git
effect. The accepted binding stays fixed while removal is pending. A repeat
reconciles a missing checkout only when its Git registration is also absent and its branch
still retains the accepted tip. A surviving checkout after uncertain removal
requires inspection; cleanup never repeats that effect automatically. Branches,
manifests and journals remain. The subscribed transcript stays
viewable, with new turns disabled after removal.

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
user input carries the issues and workflow instructions; later ordinary input
does not repeat them or change tool exposure. Attachments, busy input, rejection
and cancellation use the composer lifecycle. Fresh children receive their
checkout, assignment and orchestration-tool instructions in their first input.
Context resets carry the run's workflow instructions once in journal recovery.
Main receives coordinator instructions after preparation; confirmed child roots
receive their checkout and coordination instructions. Native descendants retain
their existing guidance. Fresh resume restores the retained recovery content,
without repeating instructions on ordinary requests or changing tool exposure.
Bare `/orchestrate` opens a picker of this Main's retained batches and coordinator.
It shows branch, lifecycle and available live timer, token and cost metrics.
Selecting a subscribed thread switches the viewed shell without starting a turn
or reloading history. Prepared batches remain visible before launch. Retained
threads that are not subscribed remain visible and resume when selected.
An empty run offers `/orchestrate ISSUES`; Escape closes the picker.

The Orchestration group in Agents lists the coordinator and its live batch
threads. Enter on a thread row switches the viewed shell; `Ctrl-B [` and `]`
cycle these threads. Without orchestration navigation, these shortcuts resize
the Diff navigator. The Main pane title shows `main` or `main › batch`.
Transcript, Activity, native Agents, Journal, Diff, settings and unsent drafts
belong to the viewed thread. Switching uses the subscribed in-memory state,
without resuming a thread or reloading history. Background threads continue to
receive events and process their existing input lifecycle. Pending questions and
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

The remaining capabilities are accepted but not yet delivered:

- SVN children start from their VCS-recorded committed baseline, excluding local
  edits, in run-owned shadow Git repositories. SVN preparation remains staged.
- Accepted shadow checkout cleanup uses the same ownership and settled-work
  requirements as Git cleanup and remains staged.
- The home `batch-agent-sessions` skill/helper retires only after parity.

Third-party threads retain their current stable schema exposure during initial
delivery. Child-authored journal roots remain unchanged. Nested orchestration
is unavailable.

Supported sources are Git, SVN and unversioned shadow. Other VCS sources reject
preparation. Retained runs from a removed adapter remain inspectable, with their
files preserved. New batch launches, orchestration messages and integration
acceptance reject for those runs.
