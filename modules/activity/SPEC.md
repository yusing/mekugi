# Activity

## Purpose and scope

Own host-operation feeds, agent roster derivation and bounded output, with
renderer-neutral publications and terminal feeds and dialogs. Application adapters
supply typed source facts and reconcile host items; the host retains authority over
identity, execution, outcomes and captured changes. Parsing neither evaluates
source nor establishes that an operation ran.

The descriptor contributes its terminal roster constructor. Removing that
contribution removes the roster placement and its input targets; independent
Conversation transcripts remain available through their shared painter.

## Behavior

### Observation authority

Main and child feeds consume typed host command, edit, collaboration, web, image,
MCP and other tool observations. Host items and turn events establish execution
state; source recognition for previews and capture does not. Unknown tools retain
their host identity and available arguments, including MCP server/tool identity.
Observation preserves original inputs, results, parallel execution and host-owned
continuations. It adds no conversation commentary or model turns and does not
reconstruct a second lifecycle from provider requests or arbitrary printed output.
MCP starts and completions share one row per host item. Its tool details retain
the supplied JSON arguments, result content and error, including structured or
non-text results. Failed calls remain visibly failed. Restored history and offline
replay use the same details without restarting the operation.

Attribution requires validated stable thread relationships and canonical agent
identity, never routing-session IDs or message text. Request preparation precedes
identity admission. Deferred observations keep their originating thread.
Ambiguous ancestry suppresses attribution, not execution
or substantive child output. Authenticated recipient-input observations supplement
assignment and directed-message bodies as display data, without waiting for a root
provider response. Source identity deduplicates live and restored observations.

Supplemental observations use invocation-scoped queues and thread-scoped source
deduplication, independent of attached feed replacement. The application supplies
authenticated ancestry and audience snapshots. Queued observations retain their
source thread until delivery, so unresolved ancestry can become known and invalidated
ancestry cannot leak into another root. Queue expiry uses arrival time without
changing an older authored timestamp. Capacity loss produces an auxiliary notice.
Thinking-request starts and unreturned-output notes share this boundary; they do
not reconstruct a provider-derived roster lifecycle. Conversation can consume
these observations without optional Activity output retention or roster placement.
Mekugi-prepared content is retained separately from host evidence in the same
authenticated invocation audience. It is keyed by observed call or submitted
attachment identity, bounded independently of diagnostics, and never regenerated
by rerunning projections. Detail readers receive detached content; unavailable
observations do not fall back to original output.
Captured-edit supplements annotate their original host items through the same
receipt reconciliation as retained review, including items loaded from history.
An early receipt waits for its host identity instead of creating a second tool row.

Activity projects admitted collaboration completions into ordered spawn,
assignment and directed-message facts. Live and replay completions with a completed
or unspecified outcome retain delivery; restored history requires an explicit
completed outcome and otherwise shows unconfirmed delivery. Blank assignments produce no message;
a successful spawn still records its supplied settings. Adapters resolve caller
labels, allocate feed sequence numbers and retain host-side effects.

An `exec` cell has no host activity item of its own. A newly observed host
`Script failed` result supplies one error row, including failures before any nested
tool runs. Its first-line preview is bounded; the full error remains available to
the content dialog and restores from retained rollout evidence. Repeated requests
do not repeat the error, and the model receives its original result unchanged.
If a finished cell returned only its host header and optional script error, its
trace-correlated command rows with output say `output not returned to the model`.
Yielded cells and cells that printed content receive no note. These notes remain
for the live router session and are not reconstructed on resume. Reclassifying
an operation or replacing/withdrawing its capture receipt preserves its existing
repetition and unreturned-output notes without duplicating them.

Wait/input observations retain typed host command/item identity; an uncorrelated
poll cannot name a guessed command. A turn ending before its wait item completes
shows `Wait ended`, without claiming any child finished. A later host wait result
replaces it; restoring a stopped turn applies the same rule without reviving waits.
Activity owns active waits in the admitted turn scope. Turn completion closes
them in observation order without consulting rendered feeds. Publications remain
detached from active state, including their target names and statuses.

### Source interpretation

Activity interprets command source using shared shell facts and command argument
grammar. Application adapters remove authenticated wrappers and supply the selected
command directory.

Tracked segments use this same source interpretation and writer classification.
Their original script remains available for literal formatter and copy recognition;
authenticated display source supplies operation text and fallback labels.
Classification describes the visible operation, not its effects:

| Label | Recognized source |
| --- | --- |
| Read | Simple `cat`, valid `mcat`, bounded `sed -n` print programs, `nl -ba FILE`, single-file `cat`/`nl` piped to those print ranges, or `cat` piped to bounded `head` |
| Search | Literal `rg` and `grep`, showing query and target rather than execution flags |
| List | Simple listings, with `ls` path operands or `.` when absent |
| Inspect | `inspect_file`, including options and multiple paths |
| Skill | `skills-mgr get` and `skills-mgr run`, including references |
| Run | Unrecognized or unsafe-to-simplify source, retaining its original preview |

Globs and simple shell parameter references stay literal. Parameter operators and
executable substitutions normally remain Run; bounded `sed -n` is classified by
its literal flags/program even with dynamic input operands. Their source remains
visible and unevaluated, and Read does not describe embedded substitution effects.
In-place edits and non-print programs are not reads.

Source classification does not inspect the filesystem. Ambiguous `mcat PATH:N`
operands retain Run source; the [reader](../fileread/SPEC.md#authenticated-raw-row-reader)
owns execution-time disambiguation of literal filenames and range mistakes.

Literal absolute executables, command-resolution-preserving `env` assignments and
unsets, and literal `bash`/`sh`/`dash -c` or `-lc` scripts without extra arguments
or redirections retain classification. Unknown wrapper options, dynamic arguments
and PATH changes remain Run. Plain `timeout DURATION` also preserves classification
for decimal durations with optional `s`, `m`, `h` or `d`; options and dynamic bounds
remain Run. Display never changes the executed source.

Mixed scripts and `&&` chains retain classified operations and Run neighbors in
source order, without claiming they ran. Other unsupported compounds remain Run.
Recognized output-only search pipelines and discarded stderr for `find`, `rg`,
`grep` and `ls` do not obscure the operation. Effectful/unknown pipeline stages and
ambiguous abbreviated listing options remain Run. Whole setup programs retain
their source and known timeout metadata. Literal `mread` recovery calls add no row.

Blank lines separate operations except inside fenced source. Each operation keeps
its verb, label, source language/body, explicit timeout annotations and read
targets. Unknown labels remain available rather than being discarded. Read ranges
belong to file reads; target-only operations retain their literal operands. Skill
program invocations retain program source rather than becoming skill reads.

The same content values can carry authored messages and reasoning supplied by host
adapters. Their kind does not establish host execution state.

The presentation owner interprets admitted item facts into renderer-neutral source
values, combining observed timing, outcomes, identities, questions, hooks and bounded
output. Public reasoning section detection belongs to that same source projection;
terminal disclosure and geometry are applied only after publication. Grouping
preserves source identities for dialogs and cross-pane navigation under the
[presentation contract](CONTRACT.md#operation-rows). Shared fence and inline-code
recognition belongs to `sourceanalysis`.

Activity owns literal Git, SVN and change-read source recognition and interpretation
of their observed output into neutral change rows. Recognition neither evaluates source nor grants
execution or filesystem authority. Change-history reads retain their existing
evidence owner.

Observed Git commit output may request read-only enrichment of the named immutable
commit in its proven directory. The attached Activity session owns these bounded,
asynchronous reads and their cache. Main and children share that owner; replacement
and shutdown retire it without blocking input. Pending or failed reads preserve the
original output evidence. Offline replay interprets retained output without starting
Git or inheriting live enrichment caches. Rendering and disclosure remain terminal-owned.

### Transcript observations

Activity consumes [Conversation's transcript publications](../conversation/SPEC.md#transcript-observations)
for Main and child messages and public reasoning across live observations, restored
history and simulated replay. Conversation owns correlation and historical
interpretation; the application retains input settlement. The
[presentation contract](CONTRACT.md#authored-content) owns child-answer selection,
public-summary disclosure and terminal pacing.

### Edit evidence

Statically scoped `sed -i`, `cat` redirections, `rm`, `mv`, and supported Python or
JavaScript writes show requested Edit/Delete/Move intent without applied counts.
Variable redirection targets stay unevaluated; heredoc bodies stay out of Run cards.
Moves into explicit directories name each requested destination; other destinations
remain as requested until captured evidence establishes the resulting paths.
Unresolved writes retain Run source, alongside any resolved targets. A successful
tracked writer, including a no-op, establishes only that its command ran.

Stock `apply_patch`, including transparent literal-bound nested calls, produces no
generic Run row, bare Edit row or patch body. Authenticated completed receipts use
the same review files as [change history](../changehistory/SPEC.md) to identify
Create/Edit/Delete/Move effects and counts. A transparent cell's completed receipt
describes its workspace effects without asserting nested patch success. Incomplete
capture has unavailable counts; failed or unfinished patches have no successful
summary. Repeated receipts deduplicate and replace intent without removing sibling
failures, skipped work or output. Combined output stays with the final operation.

Edit receipts retain their source across tool-managed-file summaries. An edit with
no resolved target says paths are unavailable. Generated edit operations omit diff
bodies from the feed; authored Markdown and retained evidence remain unchanged.
Activity hosts composed detail contributions, preferring an exact retained match
from [change history](../changehistory/SPEC.md#saved-review-presentation). With no
matching contribution, captured receipts and completed native file-change items
retain fallback details, including before an outer batch exits. Activity projects
those supplied file changes into operation summaries and completed detail pages;
live, restored and replay consumers share that projection.
Pending, failed and declined items supply no completed-host diff fallback. This
navigation neither snapshots the workspace nor publishes change evidence;
[live preview](../livepreview/SPEC.md) owns provisional diffs and
[capture](../../docs/contracts/change-evidence.md) owns observed effects.

## Retained output

Output preserves bounded logical lines independently of the terminal's animated
tail. Carriage returns replace the unfinished line; CRLF completes it. Shared
[text normalization](../../libraries/textsanitize/SPEC.md) removes controls and
expands tabs. Internal blank lines remain available; trailing padding is omitted.

Each output retains up to 1 MiB including line slots, with a 16 KiB line cap.
Dropped lines and line truncation remain distinguishable. The session shares a
16 MiB budget across Main and children, releasing the oldest completed output
first. Running output is protected and may temporarily exceed the shared budget.
Loss of segment attribution releases its separate output rather than fabricating
per-command evidence.

Host completion immediately records the observed outcome and replaces streamed
content with the supplied aggregate, including an explicitly empty aggregate.
Absent exit status remains unknown. Later deltas cannot change completed output.
Truncation records loss within the current stream or replacement aggregate, even
when that line has subsequently been dropped.

An invocation owns its admitted command source, host timing, completion and
optional retained output. Live observations, restored history and offline replay
share that owner. Its detached snapshot reports host completion immediately,
independently of pending segment evidence or terminal output animation. The
neutral feed admits the completed aggregate at the same boundary. Valid segment
reports enrich that existing identity; terminal frames can retain provisional
segment targets until attribution settles without delaying the neutral outcome.
The application's existing live-operation index supplies identity and lifetime;
yielded invocations survive the originating turn. Completed operations remain in
that index only while admitted report evidence is unsettled, never for animation.
Completed feed and dialog
references retain the same invocation after it leaves that index.

The invocation also owns admitted segment facts, retained segment output and
attribution reduction. Application integration supplies ordered report facts and
drained bytes once; transport correlation and durable report publication remain
outside Activity. Terminal reveal cursors consume detached segment snapshots.
Held command frames reproject detached command content, including its admitted
annotations. Rendering never reopens the original host record or delays semantic
completion while output is being revealed.

Read-only snapshots keep their content and outcome after later append, replacement
or release. Consumers can use the output version to avoid rebuilding unchanged
views. Restored output comes from retained host aggregates or validated segment
reports, never reconstructed unrecorded streams.

Output requests are renderer-neutral values naming source, output, version and a
byte bound, or the complete retained snapshot. Application integration resolves
them against the attached retention owner and returns detached snapshots; the
renderer chooses its syntax-context bound and has no retained handle.

## Lifecycle and integration

The presentation session keeps feed models, their shared sequence and one
observation-ordered roster in the application's attached session store, with
agent lifecycle state in its thread stores. Caller labels can change while
thread identity and roster order remain stable. Adapters publish detached
roster values; those values do not own lifecycle transitions. The optional composed
service supplies output retention and asynchronous enrichment. Main and children
have distinct feeds and share output retention; separate attachments and offline
replay resets receive distinct stores. Confirmed replacement retires the old store;
a failed replacement keeps it. Child display changes neither work scopes nor host
operations. The application serializes access and retires thread/turn correlation
with the attachment on replacement, replay reset or shutdown. Replacement
drops output state with the associated feeds and dialogs;
feed trimming alone does not invalidate an open dialog's output. Detached command
reports retain timing and outcomes, but discarded segment output remains
unavailable. Returning to those commands uses the host's combined output.
Terminal pacing, selection and fallback to a released output's display tail remain
consumer-owned.

## Records and Codex connection

Activity owns immutable start-time wait-target snapshots and their retained codec.
The application binds proven workspace, caller, turn and item identity;
[session history](../sessionhistory/SPEC.md) supplies publication, ordinary retention
and active leases.

Explicit wait targets and reported states remain authoritative. When receivers
are absent at start, Activity snapshots the supplied running descendants and
retains those names through completion; absent state never claims success or
continued work. Resume may restore recorded names but cannot infer old targets
from today's roster or revive waits. Storage failures report a notice without
blocking live display. Offline replay supplies the roster at the recorded start
instant, freezes the same targets through completion and rebuilds them on seek;
it neither opens the live wait store nor consults today's roster.

Feeds retain the newest 2,000 records while protecting live invocations.
Reordering preserves source identity independently of publication order.

The model derives canonical roster order, observed status, latest activity and
active skills. Missing parents promote their children without reordering siblings.
Activity also owns admitted agent lifecycle transitions and active-work timing.
Live observations, offline replay and restored history use that same policy;
the application admits thread/turn identities before applying transitions and
retains host operation authority. Historical timing never resumes a live clock.
Main and child identities stay distinct; Main's roster summary reads its existing
feed rather than copying it. Loaded skills come from confirmed reads or submitted
attachments. Failed/declined segments load nothing; independently successful
segments still count after aggregate failure. Feed trimming retains context loads,
compaction clears them, and bounded history restoration cannot overwrite a newer
live reset. Unknown context is not an empty confirmed set.

Roster tokens and cost consume [per-thread usage](../usage/SPEC.md), not a second
ledger. Edit totals consume the same admitted review publication as retained
details, independent of Diff visibility or filters. Presentation cannot finalize
captured changes, reconstruct missing provider usage or revive replayed processes.

The [presentation contract](CONTRACT.md) defines terminal behavior and the boundary
between retained observations, terminal state and application navigation.

Retained streams stay with their invocation or segment. The presentation contract
owns [output disclosure and release fallback](CONTRACT.md#command-state-output-and-timing)
and [dialog lifetime](CONTRACT.md#content-dialog); display pacing never delays
access to completed evidence.

## Failure and absence

Output bounds disclose truncation or release. Omitting the composition entry stops
attached output retention; host execution, command outcomes and bounded display
tails continue. Retention does not introduce a second transcript or execution path.
Admitted lifecycle observations still supply native wait-target selection and
Conversation integration when the optional roster and retention are absent.

## Acceptance

Parser cases cover reads, literal operands, timeouts, fenced programs, edit-source
inheritance and unavailable targets. Live/restored feed and output-dialog checks
exercise the same parser, preserving invocation membership, actual rendered rows
and source-copy behavior. Output cases cover chunked CRLF, bounds, shared retention,
aggregate replacement, unknown exits and snapshot stability. Real stream/segment
and dialog consumers cover paced completion, release fallback and source numbering.
Application checks cover unchanged host inputs/results and parallel execution,
authenticated attribution, repeated script errors, live-only output notes, waits
ending without completion and intent replaced by observed evidence. Source parsing
and renderer checks alone do not establish execution or capture coverage.
VCS checks exercise literal classification, output parsing and rendered rows through
live/restored consumers, plus directory/hash isolation, bounded reads, cancellation,
cache reuse and attachment retirement.
