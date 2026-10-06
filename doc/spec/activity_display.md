# Native activity presentation

## REQ-ACTIVITY-DISPLAY-001 — Operation rows and agent feeds

The [UI](native_ui.md) owns app-server event intake, lifecycle, history
hydration, and pane layout. This contract owns operation-row formatting and feed
navigation; [activity observation](activity.md) owns tool classification.
Activity is native presentation, not router-generated conversation commentary.

### Operation rows

Operations are flat rows, each led by a colored verb. Adjacent operations pad
their verbs to one column as wide as the widest verb among them, not to verbs
elsewhere in the feed; a verb wider than seven cells takes its own width rather
than widening its neighbors' column. A reasoning summary is a `•` row that heads
the operations after it: they branch from it as one tree with `├`, `└`, and a `│`
rail beside continuation rows, with no blank row between the reasoning and its
tree. The connectors sit beneath the reasoning bullet; in Main one blank row
separates each reasoning and its tree from the next. Operations with no reasoning above them form the same tree.
Tools that continue a reasoning after agent traffic name it on a continuation row.
Confirmed edit events group file rows for adjacent invocations from the same
editing source; the rows share one verb cell and one tree branch. The first row
names the group's verb, and later rows leave the cell blank unless their verb
differs from `Edit`. Confirmed rows use past tense (`Edited`, `Created`,
`Deleted`, `Moved`). The first row names a source other than stock `apply_patch`
after `via`, with `×N` when the group spans N invocations, without repeating it
as a trailing file label; each comma-separated source uses its own Bash syntax
highlighting, including shell commands such as `git stash push`, rather than
Markdown styling. Requested edit intent keeps the requested verb, such as `Edit`,
in amber and never reads `Edited`; the first row ends with `· requested`, and its
`(requested)` marker is not shown as source text. A successful tracked edit segment
instead shows `Edit PATH via TOOL · ran` as soon as its writer command completes,
even before following commands end. This includes successful no-ops and reports
command completion, not confirmed file changes or counts. Running and untracked
edit intent keeps `· requested`. A started patch that has not
completed, including one awaiting approval, ends with `· pending`; a `failed` or
`declined` patch uses a red verb and ends with `· failed` or `· declined`, named
once for the group rather than on every row. A different source or outcome
starts a new group. A source and outcome that do not fit after the first file
take their own row in the verb column.
File rows' line counts share one column within a group of more than one row when
the row fits, and zero counts are omitted. In an `Edited` group of more than one
row, an eight-cell bar after the counts scales each row's changed lines against
the group's largest. Confirmed capture counts and past-tense verbs replace successful edit intent
rows even when the shell invocation has tracked segments. A grouped capture
appears once on its owning invocation, not once per segment; neighboring command
output, failed/skipped edits, and per-command exit statuses remain visible.
A confirmed whole-directory removal with complete captured file evidence shows
one `Deleted PATH/ • N files` row, using `file` for one, instead of descendant file
rows. Clicking it opens the invocation's retained file changes, starting within
that directory. Detailed captured evidence remains available through `mchanges`.
A path too wide for its row gives way before the row's other parts: it drops
whole leading directories behind `…/`, keeping its nearest directories and file
name, or elides the middle of the name itself when that alone is too wide; below
a readable width it wraps instead. A receipt's tool-managed files are one row in the
receipt source's `Edited` group. Unresolved edit targets remain an explicit
`paths unavailable` row in the same group. Main also folds repeated confirmed edits of one path within
a group into one row with summed counts; Activity keeps each invocation's rows,
since cross-pane navigation targets them.
They retain paths and line counts but omit diff bodies in the
pane; durable change evidence and inline receipts are unchanged. This omission
applies only to generated tool activity, not authored text. Notes about output
not returned to the model align with command text on their operation's branch
and use muted, dimmed styling in either theme.
Consecutive same-action target events by one agent collapse into one row, both
within a call and across calls. Reads join ranges of the same file; Inspect,
List, Search, and other target-only actions use the same grouping. A group that
does not fit on one row puts each target on its own row, without separators;
Search targets use the same path emphasis as reads. Reads whose collapsed
content came from one target join as well: each target counts the lines read
from it as `(N lines)`, and a click opens a dialog with one page per invocation and `←`/`→`
for previous/next result. A read still streaming or failed stays its own row until it settles. Different actions and
other detail-bearing operations remain distinct. When one live invocation
reports several operations at once, such as a shell call classified as Skill,
Read and Search, they appear one at a time, 80 ms apart, in Main, Activity and the
roster summary; restored history shows at once. Child text is sanitized before layout, so it cannot emit terminal
controls. Inline Markdown code uses shell syntax colors with an accent for plain
tokens; fenced code retains language-specific highlighting. Decoration must not
change code text, wrapping, or source-aware copying. Recognized local-file Markdown links show their label as a terminal
hyperlink rather than exposing the raw destination syntax. Wrapped links retain their
destination and underline only on their text, never on row padding or gutters. A completed child
compaction appears as an event in the feed and as the agent's latest roster
activity; an attempted or failed compaction does not claim completion.
Read line spans display as `L25–46`. Each `Run` operation is its own row. In
the UI it reads `Running` from the host's command start, in
Main and in the agent's feed and roster summary, and `Ran` once the host
completes it. A yielded process stays `Running` across turns until it exits;
replayed history never shows `Running`.
A single-line command follows the verb. When it does not fit, each top-level
statement after `;`, `&&`, or `||` starts a row aligned with the first, and a
statement that is still too wide breaks at unquoted blanks with a muted ` \`
shell continuation, or after a pipe, two columns deeper; a word wider than the
row is cut without one. Fenced multiline `Run` previews sit beside the verb with
the code gutter. In Main a command or program preview keeps at most five wrapped rows, the
last a muted `… +N lines` count (under the code gutter for a program), so a one-line
command that wraps is bounded too; a click opens the whole source and retained
output in the output dialog, without expanding the transcript. Tabs in previews expand to four spaces. `Run JavaScript` previews always place source beneath
the heading with the same code gutter, whether the source has one line or many.
A confirmed nonzero command exit makes the verb red and adds `· exit N` after
the command, or on its own row when it does not fit or follows a multiline
program; the roster summary keeps a red `(exit N)`. When the host reports
aggregated output for that failure, the last five non-blank lines follow the
command under a muted dashed `┆` gutter, distinct from the code gutter, sanitized
and bounded per line. A muted `+N` in the verb column of the first output row
counts earlier lines, or a `┆ … N earlier lines` row when the count does not fit
there. In a pane shorter than 40 rows, open output shows only its last three
lines and counts the rest. While a command is
`Running`, the host's streamed output shows the same way as a rolling tail,
including an unfinished last line; a carriage return restarts its line, as a
progress line redraws. A burst rolls through rather than jumping to its end: each frame reveals a
quarter of the waiting lines, at least one, and an unfinished line shows once
nothing waits. Only a burst's latest 64 lines roll, so the tail's memory stays
bounded however much the command prints, and at most one update per frame is
drawn. A completion that arrives while a burst is still rolling, as from a
command that prints only when it exits, is held until the burst has rolled.
Output of an invocation, or tracked segment, whose operations are all instant
reads (`Read`, `Search`, `Inspect`, `List`, or a `Skill` read other than
`skills-mgr run`) shows its latest lines at once instead of rolling, and its
completion is never held; other `Run` output still rolls.
The host supplies one combined output stream per shell invocation. Its tail follows
the final displayed operation, including a Read or Skill row, rather than an earlier
Run row; it does not claim per-command output attribution. A command list tracked under
[REQ-EXECUTION-002](execution.md) instead shows each segment as its own operations,
appearing as it starts, with its own `Running` state, output tail, and exit. A failed
read or listing segment names its exit after its row. A segment the list never reached shows muted with
`· skipped`. A tracked terminal command keeps the host's combined tail after its last
segment shown. Each successful segment's output collapses independently.
Without a complete segment report, a failure spanning multiple displayed
operations shows one `Ran shell batch · exit N` row with the combined output.
It does not mark individual operations as failed. Restored history follows the
same rule because it has no live segment report.
Completion replaces it with the tail of the host's aggregated output. A
failure keeps that tail open. After a zero exit it stays open until the same
agent's next standalone event, such as a separate command, Skill or Read, then
collapses to one muted `┆ … +N lines` row once events pause for 750 ms after the
later of that event and the output's completion, so a quick run of commands
collapses together; a click on the command opens its retained output in the dialog. Output following a file or skill read
(`Read`, or `Skill` other than `skills-mgr run`) is collapsed as soon as the read
succeeds. Being what the agent read rather than a result to watch, it takes no
row of its own: a muted `(N lines)` follows the read's target, and a row too
narrow for it shortens the path rather than the count. Restored history shows zero-exit output
already collapsed. Output arriving after completion is ignored.
Command rows and their dialog titles append elapsed time only above 3 ms,
using milliseconds below a second and compact whole-second units thereafter
(`4ms`, `1s`, `1m10s`, `1h`). For tracked commands, the shell helper records
each command's start and end at its control boundaries and measures duration with
its monotonic clock. The elapsed suffix counts from that command's observed start while running and freezes
at its measured duration when complete. Supported single commands, including
timed commands and pipelines, use begin/EXIT duration rather than the host's
invocation duration, as specified in [REQ-EXECUTION-002](execution.md).
The dialog keeps the same elapsed suffix without extra timing metadata rows.
Timed commands retain their individual rows rather
than merging reads or folding staging into a commit. An EXIT boundary also
ends the active command when `exit` or `errexit` bypasses its normal end hook.
Completed observations retain timestamps and duration across resume and forks;
skipped commands have neither. A disconnected report supplies no invented end.
Untracked invocations use the host duration when complete.
Older per-command observations without timing omit it rather than borrowing the
invocation total. For batches without per-command boundaries, available host
timing appears once on an explicit `shell batch` row, which shows the live elapsed
time or completed duration and owns the combined output. The combined-output
dialog shows that same invocation duration; the total is never copied onto
individual command rows.

The shared content dialog captures keys and pointer events above both panes.
Clicking a recognized Markdown file link in Main, Activity, or Markdown dialog
content opens an existing regular local file in this dialog. Relative paths
require the current session's workspace metadata; absolute paths and local
`file:///` URLs are also supported. A `:line` suffix reveals that source line
when it exists. Paths inside the workspace use shared workspace-relative display;
external paths stay absolute. The body includes a selectable path row and source
content, with file-type syntax colors under the existing highlighting limit.
Terminal controls are sanitized for display; whole-page `y` copies the original
source bytes; dragging selects the visible path or content for selection copying. Reads
accept UTF-8 text up to 8 MiB; oversized, binary/non-UTF-8, or unreadable files
show red, copyable errors. Each opening reads current contents, without ongoing
file monitoring. Missing or unrecognized destinations retain their prior click
behavior. HTTP(S) links still copy their destinations in Main and Activity;
clicking them inside a dialog still does nothing.

Errors show a bounded first-line preview rather than an unbounded inline diagnostic.
A details link and click target appear only when that preview omits retained content;
a complete single-line diagnostic may wrap without gaining a redundant link.
Clicking an elided error opens its complete retained text,
including multiline JavaScript execution failures restored from history. `Ctrl-B !` opens
the newest error in the focused Main or Activity transcript; Left/Right navigates
its other retained errors, respecting the Activity agent filter. Error details
render literally rather than as Markdown, and use the dialog's full-page copy
and search controls. Terminal control sequences are sanitized for display and copy;
the retained host error and stock tool result remain unchanged.
Observed shell segments appear as command tabs, selectable by click or Left/Right.
Tab paths use the shared front-truncation format, retaining nearby directories and
filenames rather than an indistinguishable leading path prefix.
Each tab owns its retained output, status, scrolling, search and copy target;
newly started segments become available while the dialog is open. Read output
uses the file's syntax colors; numbered search matches color the path, line
number and matched source separately. Search titles retain pattern/path colors.
When per-command boundaries were not retained (including restored history,
terminal-only or lossy reports), the dialog labels the host buffer as combined
output instead of attributing it to the last command. It never guesses boundaries. It takes
at most 90% of each dimension, or the available screen below 60 columns, over
the panes faded to faint uncolored text. A theme-aware filled surface and
accent-colored frame separate the dialog from those panes; syntax, diff, and
selection colors remain visible within it.
The top-right `[×]` close button, `Esc`/`q`, or a click outside closes it. `↑`/`↓`, `j`/`k`, `PgUp`/`PgDn`,
`b`/space and `g`/`G` (Home/End) scroll; the wheel scrolls only the dialog.
All command output uses shared best-effort content-based syntax detection in
inline tails and dialogs, independently of the producing command. Known file
types and numbered search matches retain their explicit syntax hints. Unified
diffs reuse the diff pane’s per-language hunk syntax and added/deleted row
fills whether produced by `git diff`, `mchanges`, or another command. Rolling
tails use available retained headers and source context before selecting visible
rows; expanded output and dialogs use the same renderer. To keep large mixed
command output responsive, automatic syntax detection stops above 8 KiB of
retained context, not merely the selected tail. Explicit file/search hints and
unified diffs retain the 256 KiB highlighting limit. Unrecognized content and
buffers above their highlighting limit remain plain text; inferred syntax never
changes output bytes or line numbering.
Command titles and source bodies use syntax colors for their language; file-read
output uses the file type, including when it ends in blank lines. Copying remains
plain text. Skill-read output renders as Markdown, wrapping to the dialog width
without line numbers or output gutters; whole-page copying preserves the retained
Markdown source. Output from `skills-mgr run` remains literal command output.
Source uses a numbered solid gutter, command output a dashed gutter, and a
single-range read starts numbering at the requested first line. The header
shows the target, result position, ranges, retained line count and known exit.
Live output is marked `● live` and follows its tail; scrolling up pauses it,
and End or scrolling back to the bottom resumes. Opening or scrolling the
dialog does not change transcript follow state. Narrative dialogs refresh from
their source entry as text streams or completes, keeping their scroll and search
state; live narrative pages follow until the reader scrolls away.
`/` starts a case-insensitive substring search, Enter finds, and `n`/`N` find
next/previous matching lines. `y` copies the retained output of the current
page, or source when there is no output. Dragging within the body selects visible
text without line numbers, gutters, or frame padding. Selection uses a stable
snapshot while live output continues to arrive; dragging pauses dialog follow.
With a selection, the footer offers the shared [selection actions](native_ui.md):
Reference (`r`/`R`), Copy (Ctrl-C, also `c`/`C` or `y`), and Clear (Esc), with
bold keys and clickable hints matching the transcript. Only complete, visible
action hints accept clicks. Reference closes the dialog, focuses the composer, and inserts
the selected text as one undoable reference token at the caret, or as a plain-text
quote for an active question. Copy and Clear keep the dialog open; Esc clears
a selection before a later Esc closes the dialog. Wheel, arrow, page, and
Home/End scrolling preserve the frozen selection, including off-screen rows.
Changing pages, navigating search, or resizing clears it; End resumes live
following when there is no selection. The footer offers no drag-selection hint.
Output retention is separate from the animated display tail: each command has
a 1 MiB budget including line slots, with lines capped at 16 KiB. Older lines
are dropped with a visible count. The session has a 16 MiB retention budget,
releasing the oldest completed output first; running commands are never released
and can temporarily exceed the session budget. A released page says so and
shows its bounded display tail instead. Tracked segment output that exceeds the
between-frame buffer or loses attribution is released, and the feed uses the
host's combined output instead. Completion replaces streamed content
with the host aggregate when supplied. Restored commands retain their available
host aggregates, not streams that were never persisted.

A successful `mchanges --list` or `--summary` read shows its host output as
change rows laid out like confirmed edit rows rather than a tail; a `--summary`
read heads them with its `Diff` row (below): summary files
under their status verb (`Edited`, `Created`, `Deleted`, `Moved`, `Conflict`, or
`?` for incomplete evidence), list rows under their change ID, then shared path and
count columns with omitted zero counts and scaled bars, and statuses, reasons and
tool-managed tallies muted after them. These rows describe read history, not new
edits. At most 12 show, then a muted `… +N more`. A line it cannot read stays a
muted note, and output with no readable row keeps its ordinary tail; `--list`
rows collapse like other output.

A [version-control operation](activity.md#version-control-operations) is one row:
its verb, its heading, then a muted `· source`. `Commit` and `Stage` use gold;
`Diff`, `Status`, and `Check` use the read blue. A commit's heading is its subject,
with a leading `amend!`, `fixup!`, or `squash!` marker in amber; diff and status
headings are their scope, and paths after `in` use path emphasis. The heading is
cut with `…` so the source stays on the row; below 16 cells it wraps instead. A
nonzero exit makes the verb red and adds `· exit N`. Staging rows directly
before a commit of the same invocation fold into it when they lack individual
timing, since the commit names what it recorded. A timed, failed or skipped stage,
or one before a skipped commit, stays.
A commit reads `Committed` only once the host completed it and its output named the
commit. Successful output becomes rows beneath the heading instead of a tail, laid
out like `mchanges --summary` rows, with shared path and count columns, scaled
bars, and notes such as `binary` or `N lines`. Where output identifies a state,
Git commit and file-summary rows, and unified patch reads, use shared short file
statuses (`M`, `A`, `D`, `R`, `RM`, `UU`, or `?`) rather than status verbs.
SVN commit and summary rows retain their status verbs. Working-tree status rows
use a two-cell status code, staged state green and unstaged, untracked, or
conflicted state red.
A gold-led closing row follows any `… +N more`: a commit's hash, `on BRANCH`, and
totals; a status's branch, `↑N ↓N`, and `clean`; an svn revision `rN`; or a diff's
file count and totals when it has more files than show. These rows are the
operation's result, so they stay when output collapses; the output dialog keeps the
retained output, colored as a patch for `Diff`. The roster summary shows the verb,
heading, the rows' changed lines, and the source.
Unknown and zero exits add no failure label, and an unknown exit no output. Python, JavaScript (Node and Bun), and Perl interpreter previews use
their own syntax colors. `Search` patterns are styled as literal patterns,
not shell commands, while every target path uses the Search violet with path emphasis.
A final answer in journal-result form is laid out natively: a heading with its
answer count and recorded change totals, each question with its answers, then
this agent's recorded changes by path. The agent heading already names the
author, and an ordinary read-only result omits its empty change report. The
shared view keeps each question to one row so clipping reaches the answers;
the roster summary shows the first answer line without a leading list bullet.
Roster operation summaries retain syntax highlighting. Messages show `✉  to <recipient>` for outgoing and `✉  from <sender>` for incoming delivery,
with two spaces after the envelope because many fonts draw it wider than its cell,
relative to the entry's agent. Peers use `main` or the child display name; the owning
agent is not repeated. The message body gutter retains the sender's color. Start
blocks indent the assignment beneath `Started` without a `Spawn assignment:` label.
Assignment timestamps retain Codex’s original message creation time across follow-ups
and replay; collecting historical input does not reset those times. Queue retention
uses arrival time independently of the timestamp shown to the user. Other final answers remain
authored Markdown. Blank lines opening or closing a message, as some providers stream,
add no rows. Main completion previews retain the response excerpt and show
the linked assignment excerpt below its timestamp on separate, wrapped quote rows.
Each excerpt shows up to two content rows with an ellipsis when truncated;
Activity retains the complete response. When a retained child result contains
provider answers, exact replacement provenance combines their cards into one
complete enriched reply in Main and Activity. The assignment link and full reply
remain available. Host messages stay unchanged; missing replacement evidence keeps
the messages separate rather than guessing from matching text.

Authored Markdown tables render as compact bordered grids with emphasized headers,
column alignment, inline styles and links, and wrapped cells rather than clipped
source rows. Optional outer pipes, escaped pipes, and code spans are supported;
recognition requires a complete matching header delimiter row. Tables inside fenced
code stay literal, and blockquoted tables retain the quote rail. At widths too narrow for a
readable grid, body rows become separated header/value records so all columns remain
available. Record values and their wrapped continuations share a display-width-aware
label column; when that column would take more than half the pane, all labels
stack above their values. Rendering is derived from the current text and pane width, including
streaming updates and restored messages.

Completed `mermaid` fences render the bounded Codex 0.159.0 flowchart subset:
`flowchart`/`graph`, TD/TB/BT/LR/RL, rectangle/decision/stadium nodes, solid and
dashed directed/undirected/bidirectional edges, pipe and spaced directed labels,
and `&` endpoint groups expanded across chained edges. Quoted labels preserve
punctuation, delimiters, and semicolons. Each edge has a separate lane; crossings
are not junctions. No external renderer or process runs.

Unsupported syntax, HTML/entities, unsafe or non-additive-width labels, open
fences, and diagrams that cannot fit the pane retain the source code display,
never a partial diagram. Bounds are 16 KiB source, 16 nodes, 24 expanded edges,
24 references per group, 40 display cells per label, and 65,536 canvas cells.
Flowchart subgraphs and other Mermaid families are outside this port. Rendering
is recomputed from current text and width, including quoted blocks and replay.
The upstream subset and layout come from
[Codex 0.159.0](https://github.com/openai/codex/tree/687a119f0fcaace47e1f1abcc77cec6c813fd6da/codex-rs/mermaid).

### Agents roster and navigation

The main agent and its children appear as a canonical-path tree in observation order, with each agent's
current activity and an elapsed-time timer (`elapsed · age ago`, or `just now`).
The timer is absent before the first turn starts; opening or registering a thread
does not start it. Restored timers use retained turn timestamps, not thread creation
or metadata-update times. Elapsed time accumulates active work intervals, excluding
idle gaps between turns. Idle, unloaded, errored, and interrupted agents retain
their stopped total; subsequent active work resumes that total rather than
counting the pause. Response-age continues independently, including after late
usage notifications. Restoration sums closed turn intervals with known endpoints;
missing endpoints do not fabricate duration or revive a historical clock. The root summary shows its latest observed activity or a newer message addressed
to `/root`, or a dim `—` when neither is retained. Main activity is pane-only
and never copied back into its conversation. Visible provider reasoning summaries come from app-server items; raw and encrypted
reasoning are not exposed. Agent wait, start, resume, interrupt,
user-input waits, and MCP calls have explicit action labels. These describe
observed requests, not unobserved native execution or completion. Wait previews
include targets explicitly supplied in the call. Sibling and ancestor continuation guides
form a tree; rows whose parent is off-screen show their relative path instead. The roster stays in a separate region spanning the full width below Main
and the right column, with its own resizable divider. It shares local selection with the activity feed on the right;
no extra process, broker, or transport is needed. Each agent has one row: status, name,
and activity, then its metrics inline in fixed-width columns: context usage (defined
by the native layout contract), the timer, the agent's captured edit lines as
`+added -removed` in the Diff pane's colors, `↑ in ↓ out` tokens, estimated
USD cost to two decimal places, and provider-response turns as `T+N`. Edit lines
total every capture attributed to the agent, independent of the Diff caller filter. Files outside
the capturing workspace, such as rewritten scratch files, stay in the Diff pane but
are not counted; tool-managed captures still count. Unknown captures contribute no
activity counts; known captures from that agent remain counted. An agent without
known counts leaves the column blank. Activity counts are cumulative, not final
outcomes, and are not summed into the roster rule.
The rule instead shows `+N -N` from the saved composed project changes across
all scoped agents, independent of the Diff caller filter, roster folding, and
scrolling. Rewrites, moves, deletions, re-creations, and cross-agent edits compose
in durable capture order. Full cancellation shows `+0 -0`; no captured project
edits omit the net metric. Binary, incomplete, or inconsistent history shows
`?`, never a sum of separate edits or a claimed zero. Scratch-only captures
outside their capturing workspace are excluded. These counts describe retained
observed effects, not unobserved workspace edits or the Git index.
A changed token, cost, context, or edit-line value counts toward its new value over
half a second, easing out from the value on screen. Usage totals follow the eased
rows; the composed net outcome updates immediately without interpolation.
Values seen for the first time, including restored history, show at once;
unknown line counts never interpolate.
The roster shows no model label. When the row is too narrow for metrics beside a usable
activity summary, the metrics are omitted. Metrics are uniformly dim. Within a metric,
the part before its separator is right-aligned and the part after it left-aligned,
so separators line up across rows. Edit-line colors keep the dim intensity. Each observed role colors the existing centered status glyph without adding a
column; its shape still identifies status. Missing or conflicting role evidence
uses ordinary status colors. Timer, input,
output, cost, and turn slots are reserved before data arrives and do not resize
with changing digits or numeric formats. Selection shades the selected agent's rows in
place rather than reserving a marker column.
Tokens and cost are read from the canonical [per-thread usage totals](usage.md),
not accumulated separately by the roster. The roster follows their gap rules:
an accepted or transport-interrupted response without usable usage
shows the observed cost as a lower bound, `≥$N`, and later usage does not erase the
mark; definite rejections and non-generating prewarm leave no mark. When a thread has
no observed usage, its pricing is unavailable, or its totals are unavailable, the cost
is omitted rather than claimed as zero. Incomplete observed token totals carry `≥`;
an unknown historical window also marks observed roundtrips with `≥`. Restored
rows read retained canonical accounting even when the child stays idle. Missing
historical accounting remains blank until there is new observed consumption.
Explicitly reported zero input or output totals
remain valid. While a response streams, output grows by an estimate of about four
bytes of visible delta per token, refreshed every second; the provider's reported
usage replaces the estimate when the response ends. Hidden reasoning is not
estimated, and input changes only when usage is reported. An `exec` batch shows its latest
operation and the count of the others. When the separate roster is visible,
the activity pane gives its whole body to the feed. The native layout contract owns narrow-terminal and compact-roster placement.
Shared column widths derive from currently visible roster rows. Before hiding agents, cards compact to one row per agent, retaining
selected-agent metrics when space permits. Hidden rows have directional counts and responding/error counts on the
corresponding edge where space permits; the header always includes overall counts.
Counts follow each agent's status symbol, so a responding agent is never also
counted as an error.
The viewport moves only when selection leaves it, not to recenter each selection.
The separate roster header omits feed follow state; the feed header is `ACTIVITY`
with its filter and follow state. Feed-only controls do not offer clicking agents.
Narrative events use Main’s shared colored event headings, timestamps, and rails;
available timestamps show only where they fit, and unknown timestamps stay absent.
Child journal updates join adjacent operations and reasoning under the normal
colored agent heading, without a separate journal heading or nested journal rail.
Their previews remove the generated outer list indentation once. A task body
replaces its state/path/title summary; without a body, the preview retains the state
and title but omits the path. Authored lists and code retain their formatting.
The original message remains the detail and copy source, including omitted task
summaries. Consecutive operations and reasoning still group by agent. Narrative
previews keep up to five body rows below their heading, including in single-agent
mode; longer content ends with a hidden-row hint.
Operation source and output each keep up to five rows without clipping their status. Hovering a clipped
snippet underlines its hidden-line count. Clicking an operation opens the output
dialog when it carries retained output or additional source; narrative text,
including thinking and delegation assignments, opens in the same dialog only
when its presentation elides content or retains distinct additional detail.
Fully displayed short reasoning, diagnostics, and journal milestones have no
redundant underline hint or full-view click target. Neither action changes
transcript following. Roster rows are clickable: hovering highlights an agent; clicking it shows only that agent,
and clicking it again restores the shared feed. In the roster, `↑`/`↓` or
`k`/`j` move through all agents followed by each individual agent, stopping at
either end. Main's activity belongs to Main, so Activity neither shows nor counts
Main: roster navigation skips its row, and clicking it restores the shared feed.
`a` toggles the filter in Activity and Agents; the hint describes the next action
as `a all` when filtered and `a only` otherwise. `n`/`Tab` and `p` also select agents.
The roster mouse wheel scrolls its viewport without changing selection, filtering,
or feed follow state.
Hover underlines only the name; selection shades the row. Feed scrolling follows the same
line/page/home/end/follow contract as the [live diff](changes.md#live-terminal-view).
When a live running command's header would scroll away, it stays pinned above
the transcript until native completion. Main pins its own commands; Activity
pins only child commands matching its current agent filter, with existing agent
headings where space allows. Pins reserve their own rows and retain the command's
existing output-dialog click targets. Transcript scrolling, hover and click
targets, and cross-pane navigation remain aligned with the visible rows.
Where possible, at least one transcript row remains; a single-row pane instead
keeps one command header. If parallel command headers exceed the available
space, visible pins are limited in feed order. Pinning adds no controls or
persistent running state; replay cannot revive it.
Roster status symbols are
observed facts only:
`◐` an open provider response, `!` a latest error event, `✓` a plaintext
`FINAL_ANSWER` sent, and `·` otherwise. A final-answer summary does not repeat
the `✓`. No marker claims that an agent finished.
Agent colors derive from the canonical path, so the live diff pane uses the same
color for a caller.

Acceptance:

1. Main and child feeds render typed app-server activity without inserting generated
   operation commentary into model responses. Restored activity cannot revive work.
2. Requested, ran, pending, failed, and confirmed edits remain visually distinct; display
   grouping preserves source, outcome, paths, and observed counts.
3. Command output tails remain bounded, reflect host completion, and preserve the
   difference between unknown and nonzero exits.
4. Roster selection and scrolling preserve feed state; off-screen rows do not change
   shared column widths. Missing evidence never becomes a completion or cost claim.
5. Off-screen running command headers remain pinned within pane space and scope,
   preserve navigation and dialog targets, and release on native completion.
