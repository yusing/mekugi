# Live preview

## Purpose

Project provisional filesystem changes and maintain bounded live-view state before
host completion. A preview never executes source, authorizes a read or write,
finalizes a host call, allocates a change ID, or establishes durable evidence.

## Scope

`projection` owns literal shell/interpreter evaluation, patch projection,
target-language boundaries, and per-call source baselines. `preview` owns plain
snapshot values, consumption of admitted workspace/thread scope, root-turn transitions,
thread/turn retirement, and the attached renderer's bounded mailbox.

The application adapts authenticated backend observations into these APIs. It
retains streaming-tool argument decoding, input pacing, bounded observation
workers, command-segment matching, captured endpoint reconciliation, and durable
receipt loading. The always-on application review join owns scope admission and
retained-change delivery, independently of this optional feature. It supplies an
immutable admitted scope to preview and uses the existing change-history and
command-segment owners. The module imports neither feature.

`terminalview` owns terminal layout, source following, file windows,
selection, reveal animation, and per-call settling timers. The
[terminal session](../../apps/mekugi/internal/terminalsession/SPEC.md) owns pane composition and input routing. Saved
review composition and durable changes remain outside this module.

## Behavior

### Eligible source and baselines

Only arriving source content with a producible hunk opens a live card. Stock
patches and literal `cat`/`tee` heredoc writes can stream, including nested stock
commands. Independent writes in one call remain visible. Copies, moves, removals,
formatter-only effects, generic commands, VCS effects, non-file targets, binary
content, and metadata-only changes do not open source cards. Their observed
effects remain available in Activity and saved Diff.

Literal working directories and preceding literal `cd` steps resolve targets.
Relative operands require an explicitly selected absolute directory. A computed
directory cannot supply preview scope. Steps of an `a && b` chain are projected
as if each succeeds; an unsupported preceding effect or a dependent earlier
file operation prevents an invented result. Literal assignments and supported
non-writing setup do not prevent projection.

A call keeps its first successful bounded source read per path and reader.
Pre-execution capture can prime that baseline without replacing an earlier read.
Captured-only projection rejects every uncaptured path instead of learning a
baseline after execution. Read errors remain errors and can be retried while
input arrives. Projection performs no write and never executes a script.

### Literal interpreter writes

Supported Python, JavaScript, and TypeScript writes resolve only known literal
paths and bounded source values. Python supports literal file reads/writes,
path-based text methods, literal replacement, concatenation, bounded repetition,
string-buffer `+=`, trimming, ASCII case conversion, splitting and joining.
Known string lists, tuples, and insertion-ordered literal dictionaries support
bounded synchronous iteration, including flat replacement-pair unpacking and
`items`/`keys`/`values`. Nested loops share the same iteration budget. Collections
can be passed to helpers; mutation and computed dictionary entries are excluded.

Top-level helpers with plain/default parameters preserve Python local/global
binding and definition-time defaults. Ordered writes compose predicted content;
a later read sees an earlier predicted write, and path reassignment affects only
later statements. One open handle appends in order; reopening in write mode
truncates, including empty contexts. Closed handles and overlapping handles with
a writer are not projected. Stop-only guards, assertions, and printing do not
invent effects. Dynamic loops, loop `else`, `break`/`continue`, regex replacement,
unsupported buffer mutations, and Unicode case conversion are not predicted.

A Python edit script arriving through a heredoc previews its explicit target
writes, not the transport script. Reads and in-memory transformations alone do
not establish edit intent. While an unfinished script contains only imports,
docstrings, `sys.path` setup, and literal/path assignments, its source stays
hidden. Other source statements can reveal an ordinary script. A completed
read-only script creation previews the script itself.

### Source boundaries and completion

Interpreter predictions use the same in-memory patch projection as stock patches.
Only exact ordered source matches establish a patch result. An unprojectable
completed patch withdraws its earlier provisional card; the host result reports
execution failure. An unfinished patch/header retains the preceding established
projection until it has source to replace it.

Arriving target content reveals complete decoded lines and supported statement
boundaries. Go, JavaScript, TypeScript, Python, and Bash/sh buffer unfinished
syntax; complete statements inside open blocks can appear independently.
Delimiters inside strings do not release partial statements. Open object, array,
and call literals remain one unit. Unknown formats use complete lines. Final
input releases an unterminated or malformed tail without implying success.
Completed input publishes its authoritative full source and completion state
together, so coalescing cannot leave a truncated final frame. Interrupted input
never fabricates a final script.
Patch representation omits carriage returns and missing-final-newline differences;
a path that cannot be expressed in a patch header has no projected card.

Input pacing belongs to the application worker. It follows recent arrival rate,
bounds lag, preserves distinct reveals, and drains a completed call within its
existing bounded settling window. Completion-only shell/JavaScript projections
never manufacture an active stream. Source-card clipping preserves a bounded
suffix of the actual diff, never the transporting command text.

### Publication, scope, and retirement

The application admits only observed or authorized-restored threads in the
selected attachment. A session switch invalidates the review mailbox and requires
admitted lineage for later observations. The module consumes that scope without
performing a second admission decision. Root host-turn identity alone controls live/saved
view transitions; child turns and stale completions cannot change the current
root view. Thread/turn retirement removes only matching previews and rejects
late frames, including completed frames already queued for display.

The service publishes without waiting for the renderer. Active calls keep
first-observation order; replaceable updates coalesce in place per call. The
application drains retained change notifications and scope barriers before sampled previews, so a
new thread's preview cannot overtake its membership or delay a retained receipt.
An overflow invalidates the subscription rather than silently losing evidence.
A replacement subscription starts with a complete scope/membership barrier,
retained root-turn status, and current provisional snapshots with restoration
provenance. Cleanup of an old subscriber cannot detach its replacement.

Renderer resynchronization preserves surviving pinned files, source windows,
and original completion deadlines. It cannot reveal previously unseen completed
work or reopen expired calls. Already displayed completions can finish their
original settling interval. Scope withdrawal and absent unfinished calls retire
from the live view. Preview clipping never clips retained edit receipts.

The application supplies running observations from captured scope, not a workspace
sweep. Recognized edits can finish their cards when observed endpoints match the
projection or uniquely matched writer segments finish, independently of following
tests. Such cards say `observed`; they do not claim the shell or tests succeeded.
Unknown additional targets remain distinct from known displayed files. Terminal
host outcomes and the capture owner remain authoritative for effects and status.

### Terminal live views

`/live` toggles presentation; `on`/`off` apply explicitly. It defaults on at every
launch, including resume, and lasts only for that frontend. Hiding reserves no
space and does not stop updates or capture; showing it reveals only current cards.
Eligible source replaces only its owner's selected transcript above Main's composer,
never saved Diff. Child edits cannot take over Main. Prompt/picker/help docks take
precedence over live source.

Each file gets at least 15 rows including its heading, plus one shared Live heading;
short terminals show a file-count summary instead. At capacity, new files advance
the window without shrinking existing splits. Ctrl-B e cycles and pins the displayed
thread's file window; later arrivals cannot steal it. Wheel and Ctrl-B PgUp/PgDn
scroll live source independently of transcript and saved Diff. Reaching the source
tip resumes following; Ctrl-B r resumes both source following and the newest window.

Each completed call expires after its own 1.5-second settling interval, independently
of running siblings or newer edits. A surviving pinned file keeps its window;
expiry of the selected call follows the newest remaining file. With no cards,
the transcript returns. History never reconstructs transient bursts, and projection
first received at completion opens none. Finished input remains labelled preview,
not success; observed changes describe differences without exclusive attribution
or successful command completion. Following tests retain their own running state,
not the edit's dock. Shared captured baselines avoid rereading already-edited source
from app-server file-change notifications.

## Lifecycle

The composed handle contains only a private typed state key. The application
resolves preview state from its attachment store and replaces that store when the
selected session changes or the view detaches. Each attachment retains bounded
state and one in-process renderer subscription, with no mailbox goroutine,
filesystem store, HTTP endpoint, or second transcript. Retiring an attachment
cancels its input-projection and running-observation workers, independently of
host commands and retained capture. Disabling live presentation does not disable capture or host
execution. Removing the composition entry omits preview observation and leaves
retained change publication and saved review available.

## Codex connection

No backend binding is registered. The application validates request kind, selected
workspace, caller identity, and root/subagent metadata before supplying neutral
scope and turn facts. It preserves exact stock arguments and results, PTY/yield
continuations, cancellation, and failed or partial outcomes. Previewing never
replays or rewrites a tool call.

## Records

None. Source baselines, active snapshots, completed display snapshots, mailboxes,
and turn state are process-local. Application resume restores authorized review
scope and retained change identities; it never reconstructs transient edit bursts
or polling.

## Failure

Unsupported source or failed reads cannot fabricate a diff or suppress captured
evidence. The application reports exhausted review scope without affecting edits
or retained change inspection. Slow viewers resynchronize; slow source evaluation stays outside the
UI owner and remains independently bounded. Missing retained evidence is reported
by the application's saved-review loader, not interpreted as an empty change.

## Acceptance

Module tests cover literal projection without effects, prediction/runtime agreement,
source-boundary gating, baseline reuse, bounded evaluation, immutable admitted
scope, mailbox coalescing, overflow, and subscriber replacement. Application tests
cover scope admission, receipt ordering, stock-byte preservation, pre-completion
streaming, source/capture/segment joins, attachment replacement, and late-turn retirement. Saved-review
restore, updates, Activity receipts and root switching remain available with the
preview feature omitted.
Renderer snapshots and PTY tests cover source following, independent scrolling,
resize, pinned windows, per-call expiry, and resynchronization. Installed-Codex
acceptance remains a separate host check.
