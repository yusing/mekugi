# Command segments

## Purpose

Receive per-command shell observations, retain completed reports, and restore
them against the host's completed command. Reports are evidence, never execution receipts or authority.

## Scope

This module owns invocation-private report transport, retained reports, their
compatibility reader, and adoption by a viewing thread. Shell instrumentation is
shared. The application supplies exact live host correlation and authenticated
instrumentation scope; presentation owns output buffers, notices, and rendering.

## Behavior

A report binds the exact workspace and host turn/item identity to the command,
aggregate output digest, terminal exit, and ordered source segments. Restoration
requires agreement with those host facts. Missing, corrupt, conflicting, or
unmatched evidence leaves the host's combined result available. Skipped segments
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
normal exit; cancellation reaches storage waits.

## Codex connection

The executable helper sends shell reports through invocation-private FIFOs. The
application supplies host start/completion facts and uniquely correlates exact
scripts before a report is accepted. Protocol validation uses the same shell
splitter as the helper. It cannot manufacture host identity or execution authority.
The application still owns the Codex observation binding during extraction.

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
original thread independently of the currently displayed thread.

## Acceptance

Checks exercise publication through the composed module, real retained files,
historical mixed records, equal retries, old/new disagreement, corrupt neighbors,
host/source mismatches, ownership failure, cancellation, readonly inspection,
and fork survival after parent expiry. Router acceptance preserves asynchronous
publication, real shell output, combined-output fallback, and restored activity.
Transport acceptance exercises handshake validation, claim cancellation,
concurrent shutdown, private-resource cleanup, and resource-free invocations.
