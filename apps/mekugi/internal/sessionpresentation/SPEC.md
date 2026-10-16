# Session presentation

The application owns displayed-thread identity, Main's input destination,
read-only admission and child-history pagination. [Activity](../../../../modules/activity/SPEC.md)
owns transcript content, [session control](../sessioncontrol/SPEC.md) issues backend
requests, and [terminal session](../terminalsession/SPEC.md) owns focus, navigation,
geometry and rendering.

Displaying a child changes no execution identity and cancels no host work. Main
remains the input, question and approval destination. Child display makes the
composer read-only; returning to Main restores its existing draft.
[Replay](../../../../modules/sessionreplay/SPEC.md) is read-only even while Main is
displayed. Display and pagination intents name their view version and target
identity, so stale actions cannot affect a replacement session. View values are
detached data without terminal or backend types. A highlighted roster row is not
the displayed thread. Metadata can rename a caller without changing its identity.

## History

Child history opens at the newest turn's recent items. Older pages continue from
the last retained item. Pages deduplicate turn and item identities and reject
repeated cursors or foreign items.
Deferred evidence joins its original item when that page arrives, preserving
entry, output and navigation identities. Older pages cannot replace newer live
metadata or promote old commentary to a final answer. Archived children use the
same observation flow.

Only one child-history read is active at a time. Other threads continue normally.
Cancellation retires that observation and releases buffered live events without
interrupting the child. Saturated buffering gives live observations precedence.
Late responses cannot revive a cancelled read or cross an attachment boundary.
History distinguishes intentionally unloaded content, loading, failure and
exhaustion. Failed reads preserve loaded content and remain retryable. Reading
history never resumes a child or revives commands, previews or delivery receipts.

### Codex history binding

Codex supplies lineage, including inherited fork items and archived descendants.
Fresh resume rediscovers history instead of reviving continuation handles. Child
discovery is bounded to 128 descendants and eight list pages, with a partial-history
notice at the limit. Paginated children read at most eight pages of 100 turns,
then at most 100 items from one turn per page. Turn identity, status, failure and
timing come from host metadata rather than item text. Legacy children require full
history reads because Codex's item-list API does not support their rollouts.
Errors never trigger an unbounded fallback. Limits bound item counts rather than
bytes, so one large item can still require a large frame.

Active skills reconstruct from event history independently of lazy transcript
pages, including inherited history since the latest completed compaction.
Incomplete or failed scans leave counts unknown with a notice. Live resets take
precedence over history. [Activity's loaded-skill presentation](../../../../modules/activity/CONTRACT.md#agents-roster-and-navigation)
owns the display contract.

## Resume and switching

Startup `resume THREAD_ID` uses `thread/resume`; the returned identity must match.
Failure exits without creating a conversation. `resume --last` chooses the most
recently updated non-archived thread in the launch workspace, across providers and
CLI, VS Code and app-server sources, excluding exec and child sessions. Lookup
failure or an empty result exits without a thread.

Bare startup `resume` and in-session `/resume` replace Main with a picker. It lists
25-row pages, newest first, initially in the active workspace. Tab toggles all
workspaces and restarts loading. Rows show relative update time, branch and title
or first-message preview, plus workspace-relative directories in the all-workspace
view. Visible rows set widths. Search matches title, branch, directory and ID,
loading until a match or 1,000 scanned sessions; approaching the loaded end or an
unfilled viewport requests another page. Stale requests cannot mix results.
Loading, empty, no-match and failure states stay visible; failed paging waits for
a Tab restart. Enter selects; Escape first clears search. Startup Escape then
starts a new thread and Ctrl-C exits; in-session dismissal keeps the current thread.

`/resume THREAD_ID` and picker selection obey session control's idle gate. The
current thread is marked and selecting it only reports that it is already open.
The old session stays live until host acceptance, buffering events and locally
stacking input. Success unsubscribes it and retires its and its descendants' late
events, then restores the selected workspace, Main, roster, children, preferences
and retained Diff. Returning to a left thread readmits it. Failure keeps the old
session, replays its buffered events and restores waiting input. Drafts are not
stored per thread.

Main hydrates host messages and command/edit items after child histories, retaining
their original placement and reconciling buffered items by identity. Failed turns
retain a visible error; an active snapshot retains its steer/interrupt target.
Main reads full host resume history before trimming to its retained transcript
window. Descendant streaming deltas are omitted during restoration; completed
items provide their content.

Roster restoration never treats historical unfinished turns as live work. Only
completed collaboration items claim delivered assignments/messages. The
host-selected rollout supplements missing cell failures and received task/message
bodies, after identity validation,
within its last 64 MiB. Evidence retains its item/time placement and deduplicates
against later live requests under the [history contract](#history).

Restoration reports progress and preserves typed input as an unsent draft;
`/quit` or Ctrl-C on an empty draft can exit. Missing
child history remains incomplete without blocking the parent. Ctrl-B o loads one
older page for the selected child, or the first eligible child while Main is shown.
Escape cancels that observational read without interrupting the child.

Saved Diff restores only durable changes for the root and discovered descendants
in their workspaces before new input. Later observations join only admitted lineage,
not children of a previous session. Retention gaps cannot become successful recapture.
Applied [settings](../../../../modules/sessionpreferences/SPEC.md) and
[terminal preferences](../terminalsession/SPEC.md#preferences) restore independently;
missing settings show defaults with a notice, and save failures do not undo applied
host state. Provider selection stays invocation-local. No restoration issues model
requests or manufactures host-resumable history.

## Session title and progress

`/title NAME` immediately names Main without interrupting work or submitting model
input, then saves through `thread/name/set`. Empty names only show usage. Before
host identity exists the latest name waits; during replacement the command keeps
its draft for the new session. Manual names suppress automatic naming immediately
and survive resume/fork. Ordered saves prevent pending generated names from
overwriting them. Failure restores the last confirmed name with nonfatal feedback.
[Session naming](../../../../modules/sessionnaming/SPEC.md) owns automatic eligibility
and generation; candidates remain hidden until host confirmation.

Main's composer shows its turn state and user-only feedback, or a read-only label
for a displayed child. Its caption uses the displayed thread's model, effort,
effective tier, context and output rate; missing child settings never borrow Main's.
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

Agent waits update only the caller's latest roster status, never transcript or Main
composer progress. [Activity](../../../../modules/activity/SPEC.md#records-and-codex-connection)
owns the immutable target snapshots used for live display and restoration.

Router errors remain composer feedback, not transcript or provider-response edits.
Editing/dismissal clears actionable feedback. Elided feedback opens complete retained
details, including an entire router-error batch. Model-visible errors retain their
own transcript presentation. Unsupported server requests remain visibly pending and
are never auto-approved.

## Session status

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
until reclaimed. Scope is the router invocation across all threads, never historical
launch metrics reconstructed by resume/replay. Unavailable capture and empty history
have explicit states. Debug launch shows its actual artifact directory even without
capture, using shared workspace-relative formatting; it stays invocation-local and
out of sanitized metrics. Closing releases only the view. The JSON metrics endpoint
remains independent; there is no HTML dashboard or startup dashboard announcement.

## Acceptance

Display, read-only admission and history controls remain usable without a terminal
consumer. Acceptance includes retry, cancellation, replacement and replay isolation,
live history reconciliation and retained terminal presentation. Offline replay
does not establish live host coverage.
