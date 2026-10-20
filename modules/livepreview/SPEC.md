# Live preview

## Purpose and boundary

Show provisional filesystem changes while edit source arrives, before host
completion. A preview never writes or executes source, authorizes a read or write,
finalizes a host call, allocates a change ID, or establishes durable evidence.

The application supplies admitted workspace/thread scope and host-turn facts.
[Change evidence](../../docs/contracts/change-evidence.md) owns capture, attribution
and publication; [change history](../changehistory/SPEC.md#saved-review-presentation)
owns retained review. Preview consumes those facts without deciding their authority
or replacing their outcomes. Terminal behavior follows the
[terminal session contract](../../apps/mekugi/internal/terminalsession/SPEC.md).

## Source projection

Only arriving source with a producible hunk opens a live preview. Stock patches
and literal `cat`/`tee` heredoc writes can stream, including nested stock commands.
Supported Python, JavaScript and TypeScript writes use known literal paths and
bounded source values. Independent writes within a call remain visible.

Copies, moves, removals, formatter-only effects, generic commands, VCS effects,
non-file targets, binary content and metadata-only changes do not open source
previews. This limits prediction, not capture: their observed effects remain
available through Activity and saved Diff under the change-evidence contract.

Literal working directories and preceding literal `cd` steps resolve targets.
Relative operands require an explicitly selected absolute directory; computed
directories cannot supply scope. An `a && b` chain is projected as if each step
succeeds. Unsupported preceding effects or dependencies on an earlier file
operation prevent an invented result. Literal assignments and supported non-writing
setup do not prevent projection.

Each call preserves its first successful bounded source read for a path and reader.
Pre-execution capture may supply the baseline without replacing an earlier read.
When only captured baselines are allowed, uncaptured paths are rejected rather
than acquiring before-state after execution. Failed reads remain errors and may
be retried while input arrives.

### Literal interpreter writes

Python projection supports literal file and path-based text operations, replacement,
concatenation, bounded repetition, string-buffer append, trimming, ASCII case
conversion, splitting and joining. Known string lists, tuples and insertion-ordered
literal dictionaries support bounded synchronous iteration, including replacement
pairs and dictionary entries, keys or values. Nested loops share the iteration
bound. Top-level helpers accept plain/default parameters and collections, preserving
local/global binding and definition-time defaults.

Predicted writes compose in source order: later reads see earlier predicted writes,
and reassigned paths affect only later statements. Writes through one open handle
append in order; reopening in write mode truncates, including empty contexts.
Closed handles and overlapping handles with a writer are not projected. Collection
mutation, computed dictionary entries, dynamic loops, loop `else`, `break`/`continue`,
regex replacement, unsupported buffer mutations and Unicode case conversion are
not predicted. Stop-only guards, assertions and printing do not invent effects.

A Python edit script arriving through a heredoc previews its explicit target writes,
not the transport script. Reads and in-memory transformations alone do not establish
edit intent. An unfinished script containing only imports, docstrings, `sys.path`
setup and literal/path assignments stays hidden. Other source statements may reveal
an ordinary script; a completed read-only script creation previews the script itself.

### Source boundaries and completion

Only exact ordered source matches establish a projected patch result. An
unprojectable completed patch withdraws its earlier provisional preview; the host
result remains authoritative for execution failure. An unfinished patch or header
retains the preceding established projection until source can replace it.

Target content reveals complete decoded lines and supported statement boundaries.
Go, JavaScript, TypeScript, Python and Bash/sh buffer unfinished syntax while allowing
complete statements inside open blocks to appear independently. Delimiters inside
strings cannot release partial statements; open object, array and call literals
remain one unit. Unknown formats use complete lines. Final input releases an
unterminated or malformed tail without implying success. Full source and input
completion arrive together, so coalescing cannot leave a truncated final view.
Interrupted input never fabricates a final script.

Patch representation omits carriage returns and missing-final-newline differences.
A path that cannot be expressed in a patch header has no projected preview. Clipping
retains a bounded window of the actual diff around added source, or its tail when
there are no additions, with the source headers needed for syntax highlighting.
Following prioritizes arriving additions over a still-deleted baseline suffix.
Large deletion blocks show only nearby rows while following additions; scrolling
can inspect the remaining published source.
Neither clipping nor following changes retained evidence or transports command text.
Arrival-paced reveals bound lag and drain completed input within the display's
settling interval. Projection first received at completion cannot invent an active
stream. Finished input remains labelled preview, not success.

## Scope, visibility and continuity

Only observed or authorized-restored threads in the selected attachment are eligible.
Session switching invalidates the old scope; later observations require admitted
lineage. Root host-turn identity controls live/saved transitions. Child turns and
stale completions cannot change the current root view. Thread/turn retirement removes
only matching previews and rejects late updates, including queued completed updates.

Preview visibility belongs to the displayed thread. Child edits cannot take over
Main, and previews never replace saved review. Hiding presentation leaves updates,
capture and execution active; showing it reveals only current previews. Omitting
the feature leaves retained change publication, Activity receipts and saved review
available.

Active calls retain first-observation order; replaceable updates coalesce per call
without waiting for a viewer. Scope/membership and retained changes precede sampled
previews, so a thread's preview cannot overtake its membership or delay a retained
receipt. Overflow requires resynchronization rather than silent loss. A replacement
view receives current admitted scope, root-turn state and provisional snapshots
distinguished from newly arriving source. Retiring an old view cannot detach its
replacement.

Resynchronization preserves surviving pinned files, source windows and original
completion deadlines. It cannot reveal previously unseen completed work or reopen
expired calls. Already displayed completions may finish their original settling
interval. Scope withdrawal and absent unfinished calls retire from view. Each call
settles independently of running siblings and newer edits; clipping never clips
retained edit receipts.

Running observations use captured scope, not workspace sweeps. Recognized edits may
settle when observed endpoints match the projection or uniquely matched
[writer segments](../commandsegments/SPEC.md#live-correlation) finish, independently
of later tests. Their `observed` label describes differences, not exclusive
attribution, shell success or test success. Unknown additional targets remain
distinct from displayed files. Failed or partial host outcomes retain their observed
effects under the change-evidence contract.

The feature owns source-card admission, captured-baseline prediction and endpoint
comparison state. Its composed capture dependency supplies the bounded endpoint
reader. Application coordination supplies detached writer-settlement facts and
continues intermediate evidence comparison after a source card settles; closing
the card never closes a host observation window or its report subscription.

### Terminal live views

The [terminal session contract](../../apps/mekugi/internal/terminalsession/SPEC.md#live-changes)
defines live-view controls, pane precedence, geometry, scrolling and settling.
The feature's terminal view owns reveal admission, pending bursts, per-call
settling and resynchronization pruning. Observation and frame time come from the
attached presentation clock, including replay; rendering does not consult a
second wall clock. Calls admitted at the same instant retain observation order.

## Lifecycle and failure

Preview state is bounded and attachment-local. Switching sessions or detaching
retires the old attachment's projection and observation work independently of host
commands and retained capture. Source baselines, active previews and completed
display state are process-local. Resume restores authorized review scope and
retained identities, never transient edit bursts or polling. Previewing preserves
stock inputs/results, continuations, cancellation and failed or partial outcomes
without replaying or rewriting a tool call.

The module owns each arriving source stream, its baseline cache, projection and
paced publication. The application supplies admitted identity, the bounded patch
reader and display timing, then forwards deltas and authoritative final input.
Unfinished request cleanup withdraws its streams. Accepted final input can finish
its bounded projection after transport cancellation, but attachment retirement
cancels that projection and rejects later publication. Completed literal-call
recognition is shared with capture through the Codex binding, while unfinished
recognition remains display-only.

Unsupported source and failed reads cannot fabricate a diff or suppress captured
evidence. Exhausted scope remains visible without affecting edits or retained
inspection. Slow viewers resynchronize; source evaluation is independently bounded
and cannot stall input, cancellation or unrelated lifecycle delivery. Missing
retained evidence remains unavailable under the change-history contract, never an
empty change.
