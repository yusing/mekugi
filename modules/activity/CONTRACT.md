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
feed records, roster derivation, detail-page membership and neutral values;
`terminalview` owns transcript and roster state, disclosure, pacing, geometry,
painting and navigation targets. Application adapters supply typed observations,
host reconciliation and detached displayed-thread admission; the
[module specification](SPEC.md#transcript-observations) owns transcript interpretation.
Display intents carry their admission version; detail and edit
targets preserve source identity, and skill intents name their agent. Application
owners resolve host identities and retain output handles. Group members retain
entry, operation and reasoning-section identity through operation grouping, reasoning compaction, batch
flattening and stage folding. Dialogs consume that same flat membership.
Contributed retained pages keep their provider's stable identity and frozen source;
live command refresh cannot replace them with another invocation's content.

Initial disclosure comes from published content and observed item completion.
Terminal settlement remains separate from that completion fact. The renderer
retains later disclosure choices and applies them after source replacement;
source reconciliation does not read terminal annotations.
Publication reconciles entry identities and revisions before terminal consumers
read them. Replacement invalidates affected render caches; retirement releases
disclosure and pacing state. Unchanged publications retain cached runs and disclosure.

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
Large consecutive captured-file groups under `/tmp` show a bounded prefix and
an omitted-file count. The count opens that invocation’s retained changes;
expanded all restores every file row. Source, outcome and command-directory
boundaries remain separate, and ordinary project paths stay visible.
Tool-managed files remain rows in their source group. Output-not-returned notes
align with command text and stay muted in either theme.

Consecutive same-action operations by one agent share a verb cell and branch
within and across calls. Each operation retains its source, output, timing and
outcome. Running, skipped, requested, unknown and approval states remain distinct
groups; failures keep their own exit status. Aggregate batch outcomes stay separate.
Different command directories start separate groups. Consecutive settled target-only
operations also merge targets. Read ranges join by file, with comma-separated spans
and an observed line count for each attributable span; unassigned output keeps its
aggregate count rather than guessing counts from requested ranges. Target-only
actions retain their targets. A group that cannot fit uses separate target rows. Collapsed reads show each target's line count,
shortening its path before that count, and preserve one dialog page per invocation.
Streaming, failed or otherwise detail-bearing operations retain individual rows.
Timed operations retain individual rows, except successful file and Skill reads may
group with total elapsed time and individual dialog timing.

Semantic symbol queries retain quoted source: definitions display Read,
references display Search, malformed calls remain Run. Read spans use `L25–46`.
Native web queries display Web Search; local searches remain Search. Opening a
web page remains Open, and finding a pattern within a page remains Search.
Search patterns and web queries stay literal, with emphasized local targets.
Grouped path targets abbreviate a shared directory as `prefix/{first,second}`
when they fit on one row. Ranges and per-target counts stay attached to their
own operands; literal braces or commas keep separate targets. This is display-only:
retained source, output, dialog pages, copies and navigation keep the original paths.
One live invocation's multiple operations reveal at 80 ms intervals in Main, children and
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
Host-identified user shell commands show `You running` and `You ran`, retaining
their command preview and output excerpt even after successful completion or
history restoration. They never join counted operation batches or share their
verb with agent commands. Source classification does not replace their command
preview; captured edits remain available alongside it. Agent commands retain
their ordinary classification and disclosure.
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
frame, at least one, with only the latest 64 lines queued. Displayed completion
waits for rolling output; canonical outcome and retained aggregate output settle
on host observation independently of that animation. Command completion reaches
the neutral feed before terminal settlement. Tracked reports enrich that same
completed identity when their attribution settles; waiting for a report neither
keeps the host operation running nor delays its aggregate result. Instant
reads/searches/inspections/listings and Skill reads reveal immediately and never
hold completion; skill programs still roll.

Running output advances detached terminal frames without replacing its neutral
activity record. Segment lifecycle and attribution changes update that record
independently of tail reveal. Retained output remains available by its existing
source and output identity.
Without retained output, live dialogs use the same bounded terminal fallback;
host completion still replaces that fallback immediately.

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
the initial directory, so enrichment then requires an absolute `-C`. Offline replay
uses [retained output only](SPEC.md#source-interpretation). Output naming no commit
claims none.

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

Large Create previews targeting `/tmp` collapse their source in the default
Activity view. Expanded all and original dialog pages retain the complete source.

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
Completed attributable searches and listings show `(N results)`, and structural
inspections show `(N entries)`, when complete output establishes the count.
Collapsed successful Search, List and Inspect output omits the hidden-line hint,
including when its count is unknown; expanded output and dialogs retain the
original rows. Headers and diagnostics are not results. Missing,
truncated, failed or ambiguous output supplies no count; known empty output shows
zero. Tracked commands use their own output, and restored commands use the same
interpretation. Successful one-line command output
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
Completed timed headings and short summaries lead with `Thought for Ns`, followed
by ` • ` and their title or short text when present. No duration is invented when
timing is absent.

For providers streaming untitled reasoning, an observed request start supplies
Thinking progress until public text takes over. Other output or turn end removes
an empty placeholder; a previous item's late completion cannot remove a newer one.
Timing prefers request start, then item start, then first visible summary. Turn end
settles that turn's unfinished summaries; an older turn's completion preserves a
known newer turn's thinking and final-answer selection. Raw/encrypted reasoning
is never a substitute.
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
display source, output and outcome. New commands join while open, and completed
pages remain until closing. Empty state is
explicit. Yielded commands remain running until host completion; restored history
never revives them. Session switching closes the dialog. Reading or closing it
neither interrupts work nor submits model input.

## Content dialog

Operation and attachment dialogs include a Model-visible tab alongside original
details. It shows the latest retained Mekugi-prepared content, after Mekugi
transformations, without claiming provider consumption. A nested command or patch
exposes only its explicitly identified whole execution-cell output, never an inferred
per-command share. Missing or oversized observations show unavailable content.
Observations are bounded and invocation-local: switching preserves admitted
content, while eviction and fresh resume may leave it unavailable until another
real request supplies it. Captured-edit pages keep their frozen original source
and a separate link to the observed host item for prepared-content lookup.
Original details and copies remain unchanged.

Detail pages preserve original operation identity through feed grouping. New tracked
segments appear while open. Missing boundaries label combined output explicitly,
preserving requested source, actual edits and invocation outcome independently.
Classified edit summaries supplement the complete, uninstrumented shell source
in combined pages. Tracked edit pages retain their original segment source,
including silent captured edits. Grouping does not replace source with edit labels.
The header shows target, result position, ranges, retained line count and known
exit. Source has a solid numbered gutter; output a dashed one. Single-range reads
start at the requested line. File reads use file syntax, numbered search matches
color path/line/source separately, and search titles retain pattern/path colors.
Skill reads render retained Markdown without line numbers; skill programs remain
literal command output. Copies preserve retained source, not painting.

Live output is visibly marked. Narrative pages refresh from their source without
changing page identity; feed aging does not invalidate an open page. The terminal owns
[dialog controls](../../apps/mekugi/internal/terminalsession/SPEC.md#content-dialogs)
and source-selection actions.

Inline diagnostics use a bounded first-line preview. Elided errors open full
retained literal text, including restored JavaScript failures. A complete
single-line diagnostic may wrap without a details link.
Display/copy sanitize controls without changing retained host evidence.

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
keep totals; later work resumes them. An older turn's completion leaves a known
newer turn's responding state and work timer intact. History uses known turn
endpoints, never thread metadata or invented duration. Response age advances
independently of late usage. Available timestamps fit beside narrative headings;
unknown ones stay absent.

Metrics show context, active time/age, attributed captured edits, tokens, estimated
cost and provider roundtrips. Tokens/cost use [canonical usage](../usage/SPEC.md),
including unknown/lower-bound rules; explicit zero remains valid. Live visible
output estimates about four bytes/token at one-second refresh, replaced by reported
usage. Hidden reasoning is not estimated; input changes on reported usage only.
Unknown historical accounting stays blank until consumption is observed.

[Change history](../changehistory/SPEC.md#saved-review-presentation) supplies
captured caller totals and composed project totals from one admitted publication.
Diff visibility, selection and caller filtering cannot alter roster counts. No
captures omit the metric; an exact cancellation shows zero; unavailable net counts
remain unknown. Neither total reads current Git or workspace state.

Changed token/cost/context/edit metrics ease from displayed values over half a
second; first observations appear immediately. Usage totals follow eased rows;
composed net edits update immediately, and unknown counts never interpolate.
Visible metric values determine column widths; separator halves align across
visible rows, all metrics stay dim, edit colors included. Focus changes preserve
name and activity column alignment. Narrow rows omit metrics
before losing usable activity. No model label is added. Observed roles color the
existing status glyph; unknown/conflicting roles retain ordinary colors.

Roster placement and resize belong to native layout. Rows compact before hiding
agents; visible rows alone determine widths. Edge counts distinguish hidden,
responding and error agents, with overall header counts. The header includes the
number of finished children and known aggregate token usage. Finished means an
observed final answer without a newer open response or error, not verified task
completion. Finished children retain separate selectable rows and their captured
edit and usage metrics, even when the roster is unfocused; names never collapse
into a comma-joined summary. The viewport moves only when selection leaves it.
Hover emphasizes the name; selection shades the row. Returning to Main brings
its roster row into view and clears the previous child highlight. Remembering a
child for toggling does not present it as the displayed transcript.
Clicking a subagent selects its read-only transcript without focusing the roster;
Main’s draft stays Main’s. [Terminal pane controls](../../apps/mekugi/internal/terminalsession/SPEC.md#panes)
own explicit roster focus.
The invocation-local Side destination remains available through thread navigation;
its tab lives beside Main in the shared tab bar, outside the roster counts.
Its editable destination follows
[Conversation’s side contract](../conversation/SPEC.md#side-questions).
Arrows/k/j stop at either end; n/Tab/p also navigate, a toggles Main/current child.
Roster wheel scrolling changes neither selection nor transcript follow. Transcript
page/wheel scrolling pauses follow and Esc resumes it; frozen selections retain
their own navigation. Inserting older history preserves a paused viewport and
existing output/navigation identities; [session presentation](../../apps/mekugi/internal/sessionpresentation/SPEC.md#history)
owns page admission and evidence placement. Running headers scroll normally; the composer process count
and `/ps` dialog follow [running processes](#running-processes).

Statuses mean only observed open response, latest error, plaintext FINAL_ANSWER,
or none. Final summaries do not repeat their marker, and no glyph asserts verified
task completion. Operations/source/output retain five-row bounds without clipping
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
