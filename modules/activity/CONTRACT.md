# Activity presentation

## Scope

Activity presents supplied host observations, source content and retained output.
It does not execute tools, infer missing outcomes or write commentary into model
responses. The [module specification](SPEC.md) owns parsing and output retention;
[classification](../../doc/spec/activity.md) owns tool interpretation. The
[native session](../../doc/spec/native_ui.md) owns observation intake, history,
pane layout, input routing and dialog interaction. `presentation` owns the retained
feed records, roster derivation, neutral values and canonical group membership;
`terminalview` owns disclosure, blocks and painting. Application adapters supply
typed observations and host reconciliation without retaining a second transcript. Group members retain entry, operation
and reasoning-section identity through read merges, reasoning compaction, batch
flattening and stage folding. Dialogs consume that same flat membership.

## Operation rows

Operations use flat, colored verb rows. Adjacent verbs share the widest visible
column, except verbs wider than seven cells do not widen their neighbors.
Reasoning heads a tree of following operations, with branch connectors and a
continuation rail beneath its bullet. Operations without reasoning use the same
tree. Main separates successive trees with one blank row. Tools continuing a
reasoning section after agent traffic name it on a continuation row.

Adjacent confirmed edits from the same source and outcome share a verb cell and
branch. Past-tense verbs distinguish observed effects from intent. The first row
names non-stock sources after `via`, with an invocation count when grouped;
comma-separated sources retain independent shell highlighting. Requested edits
remain amber and say `requested`, without treating that marker as source.
Observed differences replace intent even while later commands run, but do not
complete the host invocation or publish a change reference. Later observations
replace earlier counts, including removing counts after a return to baseline;
comparison failure remains retryable, not a no-op.

| Evidence | Presentation |
| --- | --- |
| Successful tracked writer, no observed differences | `Edit PATH via TOOL · ran` |
| Running or untracked intent | Requested verb and `· requested` |
| Patch started, including awaiting approval | `· pending` |
| Failed or declined patch | Red verb and one group-level outcome |
| Confirmed capture | `Edited`, `Created`, `Deleted` or `Moved`, with observed counts |

Grouped captures appear once on the owning invocation. They replace successful
sibling intent in live and restored feeds, including timed shell batches, while
preserving command output, duration, skipped work and failures. Successful edit
command output belongs beneath its edited paths; its source remains in the dialog.
Whole-directory deletion uses one directory row and known deleted-file count,
including files with unavailable contents. Separate capture warnings disclose
those gaps. Opening the row selects retained changes within that directory.

Multi-row edit groups align nonzero counts; edited groups use an eight-cell bar
scaled against the largest changed-line total. Source/outcome text moves to its
own verb-column row when needed. Paths give way before status and counts: remove
whole leading directories, then elide an oversized filename's middle, and wrap
below a readable width. Main combines repeated confirmed paths and sums their
counts; Activity keeps invocation rows for exact cross-pane navigation.
Tool-managed files remain rows in their source group. Output-not-returned notes
align with command text and stay muted in either theme.

Consecutive settled same-action targets by one agent merge within and across
calls. Read ranges join by file; target-only actions retain their targets. A group
that cannot fit uses separate target rows. Collapsed reads show each target's
line count, shortening its path before that count, and preserve one dialog page
per invocation. Streaming, failed or otherwise detail-bearing operations remain
separate. Timed operations remain distinct, except successful Skill reads may
group with total elapsed time and individual dialog timing.

Semantic symbol queries retain quoted source: definitions display Read,
references display Search, malformed calls remain Run. Read spans use `L25–46`.
Search patterns are literal patterns, with emphasized targets. One live
invocation's multiple operations reveal at 80 ms intervals in Main, children and
roster summaries; restored history appears immediately. A completed child
compaction is an event and latest activity; attempts and failures do not imply it.

## Command state, output and timing

Run changes to Running on host start and Ran on host completion. A yielded
process stays Running across turns until exit. Replay does not revive it.
Known nonzero exits make the verb red and append `exit N`, moving the suffix to
its own row when needed; roster summaries retain the failure. Unknown exit is
distinct from success and does not suppress observed aggregate output.

Command previews preserve source while wrapping top-level `;`, `&&` and `||`
statements, then unquoted blanks or pipes with an indented shell continuation.
Oversized words break without an invented continuation. Multiline programs use
a code gutter; JavaScript source always sits below its heading. Main limits
wrapped previews to five rows with a hidden-line count. Tabs expand to four spaces.
Opening source uses the dialog, not transcript expansion. Interpreter previews
use their own language colors.

Output has a dashed gutter distinct from source. The tail shows five nonblank
lines, or three in panes shorter than 40 rows, and counts omitted lines in the
verb column or a separate earlier-lines row. Streaming includes unfinished lines
and carriage-return replacement. Bursts reveal a quarter of pending lines per
frame, at least one, with only the latest 64 lines queued. Pending completion
waits for rolling output. Instant reads/searches/inspections/listings and Skill
reads reveal immediately and never hold completion; skill programs still roll.

An unsegmented invocation has one combined stream after its final operation,
without guessed command attribution. [Tracked commands](../commandsegments/SPEC.md)
have individual operations, starts, output and outcomes. Unreached commands say
`skipped`; terminal completion also retains the combined host tail. Whole setup
scripts remain one Run event, while recognized edit scripts retain classified
rows. Missing segment boundaries produce one explicit shell-batch outcome and
combined output rather than failures assigned to individual operations.

Completion replaces streaming content with the host aggregate. Failure stays
open. Successful output stays open until its agent's next standalone event,
then collapses after five seconds without further events, measured from the later
completion/event. Eligible outputs share that deadline. Successful file and Skill
reads collapse immediately into their target line count; restored successful
output starts collapsed. Each successful tracked segment collapses independently.
Late output cannot change completed content. Released output discloses release
and falls back to its bounded display tail; attribution loss or between-frame
overflow uses combined host output. Dialogs see retained completion even while
the visible feed tail is still rolling.

Elapsed time appears only above 3 ms, truncated to milliseconds below one second
and whole seconds thereafter, omitting trailing zero units (`4ms`, `1m10s`, `1h`).
Negative duration displays zero. Live elapsed starts at the observed command
boundary and freezes at measured completion. Supported single commands and
pipelines use the tracking helper's duration, including an EXIT boundary after
`exit` or `errexit`; an untracked invocation uses host duration. Missing older
segment timing stays absent. A disconnected report invents no end, and skipped
commands have no timing. Resume/fork retain completed evidence. A batch total
appears once on its batch row and combined-output dialog, never on every command.

After 500 ms, a timed row may show a literal shell `timeout` bound or explicit
`exec_command.timeout_ms`, using the same duration format. Fast/skipped rows or
rows without individual timing omit it. Parameter-derived bounds need the
original input; shell-derived bounds survive history. `yield_time_ms` is not a
timeout. Ordinary previews hide recognized timeout prefixes; setup/heredoc
scripts retain source and bounds. Long suffixes wrap. Native status timers, ages
and countdowns share this grammar; replay position keeps `MM:SS`.

## Hooks and version control

Hooks specialize Run as Running, Ran, Failed, Blocked or Stopped; unfinished
restored hooks say outcome unknown. Rows name the event, configured status
message, configuration source and asynchronous status, never an unavailable
configured command. Session-flag configuration displays as builtin. Hook
completion is independent of turn completion, and never implies a shell exit.
Structured output retains warning, stop, feedback, context and error labels;
context bodies receive content highlighting. The dialog also names handler type.
Default feeds omit hooks; expanded events/all show them. Successful output
collapses normally; failed, blocked, stopped or error-bearing output stays open.
Approval/question prompts remain independent. Only observed structured data is
available, not inferred source, raw streams or missing rollout observations.

Version-control rows retain verb, heading and muted source. Commit/Stage use gold;
Diff/Status/Check use read blue. Commit subjects preserve amend/fixup/squash markers
in amber. Headings elide before source, wrapping below 16 cells. Paths after `in`
remain emphasized. Untimed, successful staging immediately before the same
invocation's commit folds into it; timed, failed, skipped stages and stages before
skipped commits remain separate. Committed requires completion plus observed
commit output, not merely requested source.

Successful history-list/summary and version-control output uses grouped change
rows with shared counts and bars. History reads describe retained changes, not new
edits. At most 12 rows show before an omitted-row count. Unreadable lines remain
notes; output with no readable rows retains its ordinary tail. Summary heads its
rows with Diff; list rows group by change ID and collapse like ordinary output.
Git file summaries and unified patches use short statuses; SVN keeps status verbs.
Working-tree status preserves two columns, staged green and unstaged/untracked/
conflicted red. Tool-managed counts, reasons and binary/line notes stay muted.
Closing result rows retain commit hash/branch/totals, status branch/ahead/behind/
clean, SVN revision, or omitted-file diff totals, even when output collapses.
Roster summaries keep verb, heading, changed-line counts and source.

Unified diffs reuse shared hunk colors and show path sections with changed-line
counts. Added/deleted files name the real path; renames retain both. Hunk content
and copied raw output stay unchanged. Partial or unrecognized diffs never claim
complete file totals. Output syntax detection stops above 8 KiB of retained
context; explicit file/search/diff hints retain a 256 KiB limit. Tails use retained
header/context before selecting rows. Unknown or oversized content stays plain.

## Authored content

Messages retain authored Markdown and sender-colored rails. Outgoing/incoming
headings name the other peer, using main or the child display name, with two spaces
after the envelope. Starts indent their assignment without an extra label.
Assignments retain original host creation times; arrival time alone governs queue
retention. Leading/trailing blank streaming lines add no rows. Main completion
previews retain response and linked assignment excerpts, each up to two wrapped
rows with truncation indication; children retain full content. Only exact retained
replacement provenance can combine enriched replies; text similarity is not proof.

Shared [Markdown, tables, links and Mermaid](../../libraries/terminalui/SPEC.md)
preserve source-aware copy. Child text is sanitized before layout. Agent names
remain identifiers and fenced code literal. Reasoning headings stay plain bold,
without authored-heading rules. Public app-server summaries are displayable;
raw/encrypted reasoning is not. Completed collapsed sections keep their full
source in dialog pages. Fully shown short content has no redundant detail target.

## Content dialog

The dialog captures input above both panes and leaves transcript follow unchanged.
It uses at most 90% of each dimension, or available width below 60 columns, over
faint uncolored panes. A theme-aware fill and accent frame retain content syntax,
diff and selection colors. Close uses the top-right button, Esc/q or outside click.
Arrows/j/k, page keys, b/space, g/G and Home/End scroll; the wheel affects only it.
Left/Right or clickable tabs choose results/commands. Tab paths retain nearby
directories and filename rather than an indistinguishable prefix.

Each page keeps its own scroll, search, output/status and copy target. New tracked
segments appear while open. Missing boundaries label combined output explicitly,
preserving requested source, actual edits and invocation outcome independently.
The header shows target, result position, ranges, retained line count and known
exit. Source has a solid numbered gutter; output a dashed one. Single-range reads
start at the requested line. File reads use file syntax, numbered search matches
color path/line/source separately, and search titles retain pattern/path colors.
Skill reads render retained Markdown without line numbers; skill programs remain
literal command output. Copies preserve retained source, not painting.

Live output is visibly marked. Live pages follow until scrolled away; End or
returning to bottom resumes.
Narrative pages refresh from their source while preserving navigation. `/` starts
case-insensitive substring search, Enter finds and n/N advances. `y` copies current
retained output, or source when no output exists. Drag selection excludes gutters,
numbers and frame padding and freezes content while streams continue. Off-screen
selection survives scrolling. Search, resize or page change clears selection.
Selection pauses follow; its visible complete footer hints offer Reference (r/R),
Copy (Ctrl-C, c/C, y) and Clear (Esc). Reference closes the dialog and inserts one
undoable token at the composer caret, including active question answers. Copy and
Clear leave it open; Esc clears before closing. There is no drag-selection hint.
Feed aging does not invalidate an open page.

Inline diagnostics use a bounded first-line preview. Elided errors open full
retained literal text, including restored JavaScript failures. A complete
single-line diagnostic may wrap without a details link.
Ctrl-B ! opens the newest displayed error; arrows page its retained errors.
Display/copy sanitize controls without changing retained host evidence.

Recognized existing regular local links open current UTF-8 contents, up to 8 MiB.
Relative paths require session workspace metadata; absolute paths and local file
URLs are accepted. Workspace paths display relatively, external paths absolutely.
Line/range suffixes remain in titles, reveal the first line and highlight its
number including left padding but not gutter. Markdown defaults to rendered view
with a File tab; tabs keep independent navigation and copy original bytes. Other
files and errors have one File view. A selectable path precedes syntax-colored
content. Binary, oversized and unreadable content shows copyable errors. Reopening
rereads, without monitoring. Missing links retain labels without click targets;
HTTP(S) links copy destinations in transcripts and do nothing on dialog click.

## Agents roster and navigation

Canonical-path trees follow observation order and stable identity. Each row has
status, name, latest activity and metrics; continuation guides show ancestry,
or a relative path when parents are off-screen. Unknown parents do not hide their
children. Responding, latest-error and answered states describe observations, not
task completion. Root activity may be its latest
observation or a newer directed message, otherwise a dim dash. Explicit labels
for wait/start/resume/interrupt/input/MCP describe observed requests, never inferred
completion. Wait previews name only supplied targets; batches name their latest
operation and remaining count. Agent colors derive solely from canonical paths.

Active-work timers start on the first turn, not registration, accumulate observed
work intervals and exclude idle gaps. Stopped/error/interrupted/unloaded agents
keep totals; later work resumes them. History uses known turn endpoints, never
thread metadata or invented duration. Response age advances independently of late
usage. Available timestamps fit beside narrative headings; unknown ones stay absent.

Metrics show context, active time/age, attributed captured edits, tokens, estimated
cost and provider roundtrips. Tokens/cost use [canonical usage](../usage/SPEC.md),
including unknown/lower-bound rules; explicit zero remains valid. Live visible
output estimates about four bytes/token at one-second refresh, replaced by reported
usage. Hidden reasoning is not estimated; input changes on reported usage only.
Unknown historical accounting stays blank until consumption is observed.

Per-agent edits are cumulative captures within their capturing workspace,
including tool-managed files, independent of caller filter. Unknown captures add
no counts; scratch/outside-workspace edits remain in Diff but not totals. The
roster rule instead uses composed retained project changes across agents: exact
cancellation is zero, no captures omit the metric, incomplete/binary/inconsistent
history is unknown. It is neither a sum of agent counters nor Git/workspace state.

Changed token/cost/context/edit metrics ease from displayed values over half a
second; first observations appear immediately. Usage totals follow eased rows;
composed net edits update immediately, and unknown counts never interpolate.
Slots reserve fixed widths before values arrive; separator halves align across
visible rows, all metrics stay dim, edit colors included. Narrow rows omit metrics
before losing usable activity. No model label is added. Observed roles color the
existing status glyph; unknown/conflicting roles retain ordinary colors.

Roster placement and resize belong to native layout. Rows compact before hiding
agents; visible rows alone determine widths. Edge counts distinguish hidden,
responding and error agents, with overall header counts. The viewport moves only
when selection leaves it. Hover emphasizes the name; selection shades the row.
Click selects the transcript, not input authority; Main's composer stays Main's.
Arrows/k/j stop at either end; n/Tab/p also navigate, a toggles Main/current child.
Roster wheel scrolling changes neither selection nor transcript follow. Transcript
page/wheel scrolling pauses follow and Esc resumes it; frozen selections retain
their own navigation. Running headers scroll normally; the composer process count
and `/ps` dialog follow the [running-process contract](../../doc/spec/native_ui.md#running-processes).

Statuses mean only observed open response, latest error, plaintext FINAL_ANSWER,
or none. Final summaries do not repeat their marker, and no glyph asserts that an
agent finished. Operations/source/output retain five-row bounds without clipping
status. Hidden-line counts underline on hover; only retained extra detail makes
content clickable. Full authored messages use the shared transcript renderer.

Loaded-skill counts represent the current context under the [module specification](SPEC.md#records-and-codex-connection).
Empty or unknown counts stay hidden. Labels use `1 loaded skill` or `N loaded skills`;
hover underlines the label and clicking opens sorted names in the shared dialog
without changing selection. Exit/resume restores confirmed loads, including inherited
history, under [bounded restoration](../../doc/architecture/activity.md).

## Acceptance

Actual-renderer snapshots cover operation groups, colors, wrapping, output,
questions, reasoning and dialogs. Native/replay consumers cover source identity,
host outcomes, output pacing, grouping, selection/copy, roster sizing and history.
Replacement and release preserve honest unknown/unavailable states. Rendering
checks do not establish live host execution, capture or provider accounting.
