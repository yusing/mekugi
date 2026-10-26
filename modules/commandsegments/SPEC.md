# Command segments

## Purpose

Receive per-command shell observations, retain completed reports, and restore
them against the host's completed command. Reports are evidence, never execution receipts or authority.

## Scope

This module owns invocation-private report transport, live host/report correlation,
observation subscriptions, retained reports and adoption by a viewing thread.
Shell instrumentation is
owned by the [shared library](../../libraries/shellinstrumentation/SPEC.md), including
supported shells, source boundaries and untracked cases. The backend supplies
decoded host facts; the application supplies authenticated instrumentation and
captured-window scope. Presentation owns output buffers, notices and rendering.

## Behavior

### Shell observations

Supported lists expose each segment's output, exit state, start/end timestamps
and elapsed duration while the host command runs. Opening a segment uses its
retained stdout/stderr in the shared output dialog. Recognized edits retain Edit
rows and hidden edit bodies. Single commands and pipelines report their duration
without replacing combined host output. Terminal commands preserve their terminal
and report statuses rather than redirecting output through pipes.

The helper relays nonterminal list output unchanged to Codex's original
descriptors. Acknowledged begin/end boundaries prevent output preceding its
segment identity or spilling across a completed boundary. Unstarted segments remain
pending until an observed start or evidence that the shell passed them. A skipped branch has
no exit or timing and becomes visibly skipped when a later segment starts or the
shell finishes. EXIT closes an active segment whose ordinary end was bypassed.
Background descendant output belongs to the segment running when it arrives.

Boundary observations record wall time and monotonic elapsed duration after
setup/matching and before final output drainage. Single-command timing spans
begin/EXIT and includes their small acknowledgement overhead. Missing boundaries
never borrow invocation duration. The helper reports final closure after relayed
output drains. It never starts, signals or continues the observed command, and
never blocks output relay on the router. Falling behind or exceeding the 4 MiB
report-output budget disables separate output reporting, not host output or status.

Completed presentation requires final report closure and agreement with the
host's terminal exit. Incomplete reports, including process replacement or a
killed shell, fall back to the combined host result. Terminal, single-command and
lossy reports keep observed states/timing without separate output. Truncated or
released retained output also stays combined instead of claiming a complete stream.
The UI adds measured elapsed suffixes, not extra timing metadata rows.

### Live correlation

Reports match a unique live host item by thread and script within 350 ms. Matching
admits only the current invocation's exact, reversible guard PATH prefixes added
after host observation; item-bound prefixes must name that host item. Authored
source, helper wrappers and other-session instrumentation remain distinct.
Ambiguous items, redacted command differences and missing or late host
notifications keep the untracked fallback. Start observations reach correlation
before waiting on presentation, so a queued UI update does not itself lose a match.

Writer previews subscribe independently of Activity consumption. They require
exact thread, turn and authored-script identity after removing only the current
session's exact authenticated guard PATH prefixes. Item-bound instrumentation
must match that command item. Helper wrappers, other-session paths and unrecognized
source stay intact. Reports retain the executing script even when admitted
against a host script without guard prefixes.

Only host items started after a preview window opened qualify, even if an older
report arrives late. A preview retains at most one match per captured script until
closure, independently of Activity retirement, so sequential `exec` batches keep
earlier reports. Additional matches and overlapping windows claiming the same
script remain ambiguous rather than borrowing reports. Segment completion can
settle edit intent or a provisional card before later non-edit commands finish;
it never finalizes host execution or publishes durable change evidence.

### Retained reports

A report binds the exact workspace and host turn/item identity to the command,
aggregate output digest, terminal exit, and ordered source segments. Restoration
requires agreement with those host facts. Guard-prefix admission does not rewrite
the retained host command or relax restoration identity. Missing, corrupt,
conflicting, or unmatched evidence leaves the host's combined result available. Skipped segments
have no output, exit, or timing; absent per-segment output means unavailable, not
empty output. Incomplete output is never presented as a complete stream.

Reports are immutable. An equal retry reuses existing evidence. Historical mixed
call records remain readable in place, including their filesystem dependencies;
the complete historical record is validated before its segment projection is
used. When both formats exist, disagreement or corruption in either rejects the
report. Equal historical-only retries neither rewrite nor duplicate the record.

Restoration adopts accepted records and dependencies into the requesting thread's
catalog before exposing them. Independent candidates are attempted independently;
a failed combined admission still permits smaller candidates that fit. A fork
keeps inherited reports after the parent expires. Restoration never revives live
processes, timers, approval grants, or continuation handles.

Explicit storage inspection validates the selected logical identity without
creating storage, locks, or catalogs. It selects the actual new envelope when
present, otherwise the genuine historical record. It never fabricates a mixed
call record from a new report.

## Lifecycle

Retained operations use the invocation's session-history owner. The record kind
is registered in live, worker, headless, and offline invocations. Only an enabled
interactive invocation acquires transport resources during module startup.
One application consumer claims reports. Closing observation or the invocation
cancels and joins transport work and removes its private resources. Transport
shutdown never cancels host execution or shell approval requests.
The application bounds asynchronous publication and drains accepted writes on
normal exit; cancellation reaches storage waits. Publication runs outside the UI
event loop with at most 32 pending reports and a 30-second storage wait. Each
pending report retains its original workspace/thread after session switching.

## Codex connection

The executable helper sends shell reports through invocation-private FIFOs. The
application delivers backend-decoded command facts before presentation queuing;
the module correlates exact scripts before accepting a report. Host completion
remains distinct from segment and report closure. Protocol validation uses the
same shell splitter as the helper and grants no execution authority.

## Records

New reports have a feature-owned record kind under the shared retained directory.
Existing mixed call records and their names remain unchanged. Publication and
retention use [session history](../sessionhistory/SPEC.md); report meaning belongs
here. Disabling the record kind preserves its records under that owner's policy.

## Failure

Unavailable report transport leaves commands running with their original output
and status. Startup records optional transport acquisition failure, which the
application reports without disabling the independent approval channel. Report
attachment failure has the same fallback. Failure of the approval channel itself
remains subject to the application's guarded-startup policy. Cancellation remains
cancellation. Malformed or unmatched reports are declined; loss of an accepted
stream ends observation without claiming host completion.

Publication fails on conflicting immutable facts, invalid evidence, storage
errors, cancellation, or insufficient capacity. Successful writes may remain
after a later failure, but unavailable evidence is never returned as restored.
The application preserves live output and reports publication failures to the
original thread independently of the currently displayed thread, including a full
pending queue. The notice names the host turn/item and complete underlying error;
distinct causes remain separate. [Notifications](../notifications/SPEC.md) owns
full details, dismissal, delivery and launcher recovery. Failed retention promises
no restoration. Old history without reports keeps host timing and combined output.

## Acceptance

Checks exercise publication through the composed module, real retained files,
historical mixed records, equal retries, old/new disagreement, corrupt neighbors,
host/source mismatches, ownership failure, cancellation, readonly inspection,
and fork survival after parent expiry. Router acceptance preserves asynchronous
publication, real shell output, combined-output fallback, and restored activity.
Transport acceptance exercises handshake validation, claim cancellation,
concurrent shutdown, private-resource cleanup, and resource-free invocations.
Shell/application acceptance requires per-segment output and exit, skipped and
early-exit states, measured single-command timing, terminal/lossy fallback,
notification-before-presentation matching, preview-window isolation and exact
host-exit validation. Fresh resume and inherited fork history restore only
matching workspace, turn, item, command, aggregate and exit facts. Installed-Codex
sandbox checks remain separate from offline transport and shell fixtures.
