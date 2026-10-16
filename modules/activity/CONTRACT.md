# Activity presentation

## Scope

Activity presents supplied host observations, source content and retained output.
It does not execute tools, infer missing outcomes or write commentary into model
responses. The [module specification](SPEC.md) owns observation authority,
classification, parsing and output retention.
[Session presentation](../../apps/mekugi/internal/sessionpresentation/SPEC.md) owns
displayed-thread identity, input admission and bounded child history; the
[terminal session](../../apps/mekugi/internal/terminalsession/SPEC.md) owns pane layout, input routing and
dialog interaction. `presentation` owns the retained
feed records, roster derivation, neutral values and canonical group membership;
`terminalview` owns transcript and roster state, disclosure, pacing, geometry,
painting and navigation targets. Application adapters supply typed observations,
host reconciliation and detached displayed-thread admission; the
[module specification](SPEC.md#transcript-observations) owns transcript interpretation.
Display intents carry their admission version; detail and edit
targets preserve source identity, and skill intents name their agent. Application
owners resolve host identities and retain output handles. Group members retain
entry, operation and reasoning-section identity through read merges, reasoning compaction, batch
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
Captured edit paths use the shared workspace path display: workspace paths are
relative and external paths remain absolute, including stock patch rows without a
command directory.

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

Skill rows emphasize the skill name; reference paths and run arguments keep normal
weight. Per-command Run excerpts omit terminating semicolons, preserving quoted
semicolons and other executable syntax. Literal `echo`/`printf` section headings
beside other operations are decoration: colon-ended or bordered headings, and
newline-framed `printf` titles using capitalized multi-word text or one uppercase
word, with only letters, spaces, hyphens and slashes. Literal newline-terminated
`printf` confirmations ending in `checks passed.` or `evidence remains available.`
also qualify with that capitalized text grammar. Data-bearing formats, standalone,
redirected, escaped or dynamic headings remain Run. Live/successful tracked
headings are omitted; failed ones stay visible.

A known command directory different from the displayed workspace adds muted
`· in DIR` to the invocation's first row. Workspace subdirectories display
relatively, outside directories absolutely; operation paths are relative to DIR.
Workspace commands and unknown directories/workspaces have no suffix. Live and
restored rows use the same host evidence; reads from different directories never
merge.

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

Run previews omit literal Bash/Sh `-c`/`-lc` and PowerShell `-Command`/`-c` launch
wrappers, including literal `-NoLogo`/`-NoProfile`. Outer redirects, assignments,
extra arguments or dynamic words keep the wrapper intact. The inner source keeps
shell highlighting; live and restored display never changes execution.

Literal interpreter wrappers such as `python -c`, `python - <<'PY'` and `node -e`
show multiline programs in their own language, with source indentation intact.
Their heading names the interpreter and replaces the program with `…`; one-line
programs keep the literal command. A lone `-` without redirected stdin says
`· program read from stdin`. Provisional and completed views share this projection,
which never evaluates source or implies success.

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

Literal VCS commands specialize Run: `git commit` and `svn commit`/`ci` are Commit;
`git add` with paths, `-A` or `-u` is Stage; git diff/show, svn diff and `mchanges`
patch/net/summary reads are Diff; `git diff --check` is Check; git status (long,
short or porcelain v1) and svn status are Status. Rows retain scope paths and
source flags that affect the displayed result, including Git `-C` and stat forms.
Commit headings use the first nonblank line of literal `-m` text or quoted stdin
message forms (heredoc, here-string or `$(cat <<'EOF' … EOF)`); file/existing
messages have no invented heading. Diff headings name working tree, staged,
literal revisions, SVN `rN`, or change IDs (`mine` when absent).

Dynamic words, expanding globs/braces, environment assignments, `git -c`,
interactive/editor/external-diff/output-writing options and unsupported output
forms remain Run. This includes dry-run, porcelain v2/NUL output, remote/verbose
SVN status, `REV:PATH`, custom Git pretty formats/prefixes, mixed file-summary formats,
and `mchanges` list/history/apply/revert. Redirection qualifies only for commit
message stdin or discarded/merged stderr. Copy-detecting stat/numstat needs summary
evidence to distinguish copies from moves; literal revision tildes are not expansion.

Version-control rows retain verb, heading and muted source. Commit/Stage use gold;
Diff/Status/Check use read blue. Commit subjects preserve amend/fixup/squash markers
in amber. Headings elide before source, wrapping below 16 cells. Paths after `in`
remain emphasized. Untimed, successful staging immediately before the same
invocation's commit folds into it; timed, failed, skipped stages and stages before
skipped commits remain separate. Committed requires completion plus observed
commit output, not merely requested source.

A successful command's own output supplies parsed patch/stat/numstat/shortstat,
name-status/name-only, SVN diff-summary, status and commit rows. Hunk counts stay
within declared extents; copies create the destination with a `copy of` note,
not a moved source. A scaled stat graph supplies only a total, not an invented
added/removed split. Unreadable or unattributable output keeps its ordinary tail.
Silent successful Stage/Check commands may precede a commit in combined output;
other mixed lists do not guess attribution. Tracked segments use their own output.

Observed Git commits may enrich file counts from a read-only lookup of the printed
hash in the proven command directory. It runs independently of UI input, at most
two reads at once, bounded to two seconds and 4 MiB. Pending, unavailable or
unreadable objects leave output-observed created/deleted/moved files without counts.
Consecutive `-C` options resolve in order. A prior non-VCS shell segment invalidates
the initial directory, so enrichment then requires an absolute `-C`, also on replay.
Output naming no commit claims none.

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

Main and children share parsing, grouping and viewport behavior. User messages
have a distinct band; assistant text, operations and final-answer cards retain
their identities rather than becoming a second transcript. Image-view tools show
View with their workspace path; attached user images claim no tool inspection.
Completed attributable searches may show a result count; missing, truncated,
failed or ambiguous output supplies none. Successful one-line command output
stays visible rather than becoming a line-count toggle.

Asked rows retain full questions and observed answers with shared Markdown and
source copying. Adjacent question records share their Main heading. Approval facts
decorate the associated host item, never a separate Approval/Session event:
Pending Approval, Approved, Denied and Auto Denied remain distinct from execution
success. External resolution and turn end say Resolved elsewhere or Turn ended
before an answer. Command dialogs retain decision scope/denial reason; compound
guard decisions stay on their aggregate shell row.

Attachments show one Attached or Attach failed receipt per submitted file/skill
snapshot before agent activity, including after resume. Chunked content has one
receipt; stacked snapshots stay separate. Paths use shared workspace formatting
without transport quotes. Successful receipts open the exact submitted content;
failures name omissions and open no invented content. Rejected submissions claim
no success. Transport envelopes remain hidden from transcripts and recalled input.

Adjacent traffic with one agent forms a thread. Assignments/messages retain exact
entry links; off-screen old entries shrink to excerpts only when that shortens
them. Omitted-row links open the retained entry, never nearby similar text. The
latest exchange keeps the larger excerpt; narrative gaps do not consume excerpt
rows. Reasoning between traffic appears after its rail closes; later Main tools
retain their own narrative heading or a continued heading. Settled off-screen
operation groups collapse to counted verbs and failure counts; running, skipped,
declined, unconfirmed, unanswered and outcome-unknown work stays visible. Resume
starts settled groups collapsed. Shrinking preserves a scrolled-up anchor.

Replies link to their own retained assignment or user input. An answer to the
latest adjacent assignment omits a redundant link; older assignments keep theirs.
Repeated replies quote input again only after another message intervenes, not
after tools/reasoning/progress. Question replies link to the Asked record as
`re: your answer`, without a duplicate user band. Missing target identity leaves
plain text, never fabricated navigation. Hover/click identifies and highlights
the exact destination. Live, restored and replayed child turns preserve explicit
final answers; otherwise only the turn's last nonblank message is promoted.
Authenticated recipient-input observations supply bodies
missing from host notifications exactly once, bounded to 64 KiB with explicit
clipping; legacy envelopes never enter provider output.

### Public reasoning and disclosure

Only host public summaries appear, including restored or completion-only summaries.
Empty items create no row; late deltas cannot reopen completed items. Short one-row
bodies and heading-only summaries stay directly visible. Longer summaries split
at Markdown/bold titles or short plain titles, never inside fences; rendering,
section detection and source copy share fence boundaries. Body text retains
Markdown and uniform faint/italic styling. Untitled sections use Thinking/Thought;
titled sections keep their titles. The latest three body rows and omitted-row count
preview longer streams, paced like command output.

Earlier sections fold as a new one starts. Completion first drains queued text,
then leaves the latest completed item open until later activity and a five-second
quiet period shared with successful command output across Main and children.
Restored long summaries start folded. Collapsed sections share one row and one
item duration; dialogs retain each section's full body as pages. Observed durations
appear only from one second onward, on the final section rather than per section.
No duration is invented when timing is absent.

For providers streaming untitled reasoning, an observed request start supplies
Thinking progress until public text takes over. Other output or turn end removes
an empty placeholder; a previous item's late completion cannot remove a newer one.
Timing prefers request start, then item start, then first visible summary. Turn end
settles unfinished summaries. Raw/encrypted reasoning is never a substitute.
Roster status uses collapsed section labels without body or duration, retaining
short text when it fits and Thinking/Thought otherwise. Only live waiting/reasoning
animates; completion and history do not restart it.

Ctrl-E cycles the displayed transcript through default, expanded events, and
expanded all. Events expands narrative, public reasoning and hooks; all additionally
opens retained operation source/output. User prompts are unchanged. The choice is
pane-local, applies to arriving content, preserves follow/scroll anchors and names
the next choice in its hint. Default restores ordinary automatic folding.
Manual scrollback and earlier-content jumps pause following; Live/Diff toggles,
resize and dialogs do not. Reaching the last full viewport or Escape resumes follow
and clears unread counts; paused views offer Back to bottom above the composer.

## Running processes

The composer shows a live count only while commands from Main or admitted children
are running. `/ps` opens the shared dialog with one tab per whole invocation in start
order, including a single-command tab. Each page retains owning agent, original
display source, output and outcome. Live pages follow until scrolled away; new
commands join while open, and completed pages remain until closing. Empty state is
explicit. Yielded commands remain running until host completion; restored history
never revives them. Session switching closes the dialog. Reading or closing it
neither interrupts work nor submits model input.

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
and `/ps` dialog follow [running processes](#running-processes).

Statuses mean only observed open response, latest error, plaintext FINAL_ANSWER,
or none. Final summaries do not repeat their marker, and no glyph asserts that an
agent finished. Operations/source/output retain five-row bounds without clipping
status. Hidden-line counts underline on hover; only retained extra detail makes
content clickable. Full authored messages use the shared transcript renderer.

Loaded-skill counts represent the current context under the [module specification](SPEC.md#records-and-codex-connection).
Empty or unknown counts stay hidden. Labels use `1 loaded skill` or `N loaded skills`;
hover underlines the label and clicking opens sorted names in the shared dialog
without changing selection. Exit/resume restores confirmed loads, including inherited
history, under [bounded restoration](../../apps/mekugi/internal/sessionpresentation/SPEC.md#codex-history-binding).

## Acceptance

Actual-renderer snapshots cover operation groups, colors, wrapping, output,
questions, reasoning and dialogs. Native/replay consumers cover source identity,
host outcomes, output pacing, grouping, selection/copy, roster sizing and history.
Replacement and release preserve honest unknown/unavailable states. Rendering
checks do not establish live host execution, capture or provider accounting.
