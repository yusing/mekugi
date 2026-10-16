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
`(requested)` marker is not shown as source text. Once tracked writers finish,
observed file differences replace their intent with past-tense rows and captured
counts, even while following commands still run. Completion reporting uses actual
filesystem differences, independent of source prediction or interpreter syntax.
Computed targets use the existing workspace snapshot coverage. These rows describe observed
filesystem effects; they do not complete the host invocation or publish a durable
change ID. The terminal host result still owns retained change evidence.
Later observed endpoints replace earlier counts, including withdrawing them when
the files return to their baseline. A failed comparison is retried; it is not a no-op.
A successful tracked edit without observed differences shows
`Edit PATH via TOOL · ran`, including no-ops and unavailable capture.
Running and untracked
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
Output and timing of a successful edit command appear beneath an `Edited` row
naming the paths it wrote, not as a `Ran` row repeating the command. That row
opens the command's output dialog, the only place its source appears.
Live and restored grouped captures replace successful sibling edit intent even
when the sibling has a shell-batch timing row. That row retains its duration and
output; its presence does not imply failure.
A confirmed whole-directory removal shows
one `Deleted PATH/ • N files` row, using `file` for one, instead of descendant file
rows. The count includes known deleted files whose contents could not be captured;
content gaps keep a separate capture warning rather than expanding the directory
into file rows. Clicking it opens the invocation's retained file changes, starting
within that directory. Detailed captured evidence and its limits remain available
through `mchanges`.
A path too wide for its row gives way before the row's other parts: it drops
whole leading directories behind `…/`, keeping its nearest directories and file
name, or elides the middle of the name itself when that alone is too wide; below
a readable width it wraps instead. [Parsed operations](../../internal/activity/SPEC.md)
supply receipt source inheritance, unavailable-target labels and generated edit-body
omission. Tool-managed files occupy one row in their source's `Edited` group.
Main also folds repeated confirmed edits of one path within
a group into one row with summed counts; Activity keeps each invocation's rows,
since cross-pane navigation targets them.
Notes about output
not returned to the model align with command text on their operation's branch
and use muted, dimmed styling in either theme.
Symbol commands use the native CLI grammar for classification: definitions show
Read, and any reference query makes the invocation Search. Display retains the
original quoted arguments; malformed invocations keep the generic Run display.
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
Read and Search, they appear one at a time, 80 ms apart, in Main, child transcripts and the
roster summary; restored history shows at once. Child text is sanitized before layout, so it cannot emit terminal
controls. Inline Markdown code uses content-detected syntax colors; unrecognized
spans stay plain with the existing accent, which also colors plain tokens in detected
code. Fenced code retains language-specific highlighting. Decoration must not
change code text, wrapping, or source-aware copying. Recognized local-file Markdown links show their label as a terminal
hyperlink rather than exposing the raw destination syntax. Wrapped links retain their
destination and underline only on their text, never on row padding or gutters.
Raw file-shaped paths in authored text, including inline code,
use the same clickable styling only when the local target exists as a regular file,
resolved from session workspace metadata for relative paths. Missing paths and
slash-separated prose remain unlinked, without changing the source or copied text.
Paths with spaces can use an inline code span or an explicit Markdown link.
Agent names remain identifiers; fenced code stays literal.
A completed child
compaction appears as an event in the feed and as the agent's latest roster
activity; an attempted or failed compaction does not claim completion.
Read line spans display as `L25–46`. Each `Run` operation is its own row. In
the UI it reads `Running` from the host's command start, in
Main and in the agent's feed and roster summary, and `Ran` once the host
completes it. A yielded process stays `Running` across turns until it exits;
replayed history never shows `Running`.
Hook runs specialize the same Run presentation: `Hook Running`, `Hook Ran`,
`Hook Failed`, `Hook Blocked`, or `Hook Stopped`. An unfinished restored hook
reads `Hook Run` with `outcome unknown`. Rows name the hook event, the host's
status message describing what the hook does when it is configured, and the
configuration source, with `async` for asynchronous handlers. Hook runs do not
identify their configured command reliably, so rows never name one. Mekugi's own
hooks carry status messages: `Applying VCS guard`, or `Tracking shell segments`
without the guard, and `Restoring Mekugi context`. Codex's session-flag layer
(`/<session-flags>/config.toml`), which holds Mekugi's hooks and any user `-c`
hook flags, shows as `builtin`. Hook completion, independently of turn completion,
ends a running hook. Hook status is distinct from command exit; no exit code is
inferred. The output dialog also names the handler type. Host-supplied
structured output retains its warning, stop, feedback, context, and error labels
as plain text, using Run's bounded tail and retained-output dialog. Each context
body is highlighted by content detection without its label; other entries stay
plain. Default presentation omits hook runs of every outcome;
expanded events and expanded all show them. Host approval and question surfaces
are separate and unaffected. Successful output follows Run's collapse behavior;
failed, blocked, stopped, or error-bearing output stays open. Hook notifications do not expose command source, raw stdout, stderr, or
streaming output deltas. Mekugi retains observed hook runs in its
existing managed store for resume and offline replay, scoped to workspace and
thread. Missing observations cannot be reconstructed from Codex's rollout.
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
appearing as it starts, with its own `Running` state, output tail, and exit.
Shell setup scripts stay one Run event with their full source and combined output.
Scripts with recognized file edits keep their operation rows and hidden edit bodies. A failed
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
collapses to one muted `┆ … +N lines` row once events pause for 5 seconds after the
later of that event and the output's completion, so a quick run of commands
collapses together; a click on the command opens its retained output in the dialog. Output following a file or skill read
(`Read`, or `Skill` other than `skills-mgr run`) is collapsed as soon as the read
succeeds. Being what the agent read rather than a result to watch, it takes no
row of its own: a muted `(N lines)` follows the read's target, and a row too
narrow for it shortens the path rather than the count. Restored history shows zero-exit output
already collapsed. Output arriving after completion is ignored.
Command rows and their dialog titles append elapsed time only above 3 ms,
using truncated milliseconds below a second and truncated whole-second units thereafter
(`4ms`, `1s`, `1m10s`, `1h`). For tracked commands, the shell helper records
each command's start and end at its control boundaries and measures duration with
its monotonic clock. The elapsed suffix counts from that command's observed start while running and freezes
at its measured duration when complete. Supported single commands, including
timed commands and pipelines, use begin/EXIT duration rather than the host's
invocation duration, as specified in [REQ-EXECUTION-002](execution.md).
The dialog keeps the same elapsed suffix without extra timing metadata rows.
Timed commands retain their individual rows rather
than merging reads or folding staging into a commit. Successful Skill reads are
an exception: consecutive reads group like Read targets, with total elapsed time
on the group and each invocation's output and timing in the dialog.
An EXIT boundary also ends the active command when `exit` or `errexit` bypasses
its normal end hook.
Completed observations retain timestamps and duration across resume and forks;
skipped commands have neither. A disconnected report supplies no invented end.
Untracked invocations use the host duration when complete.
Older per-command observations without timing omit it rather than borrowing the
invocation total. For batches without per-command boundaries, available host
timing appears once on an explicit `shell batch` row, which shows the live elapsed
time or completed duration and owns the combined output. The combined-output
dialog shows that same invocation duration; the total is never copied onto
individual command rows.

After a command runs for at least 500 ms, its row shows a muted
`(timeout DURATION)` suffix for a supported literal shell `timeout` prefix or
an explicit `exec_command.timeout_ms` input. Both use the same compact duration
format as elapsed time. Supported literal shell `timeout` prefixes are hidden
in ordinary command previews, including generic Run commands; whole setup and
heredoc scripts retain their source. Execution input stays unchanged.
Status timers, task timers, ages, and countdowns use this same format and
rounding rule. The replay position clock keeps its `MM:SS` notation.
Fast or skipped commands and rows without individual timing omit the suffix.
Parameter-derived suffixes appear only while the original invocation input is
available, so fresh resume omits them. Shell-derived suffixes remain available
from the saved command when its timing is known. `yield_time_ms` does not produce
a timeout suffix. Shell setup scripts kept in one Run event retain their supported
literal timeout bounds without splitting the script. A suffix that does not fit
wraps beneath its operation row.

The shared content dialog captures keys and pointer events above both panes.
Clicking a recognized file link in a transcript or Markdown dialog
content opens an existing regular local file in this dialog. Relative paths
require the current session's workspace metadata; absolute paths and local
`file:///` URLs are also supported. A `:line` or `:first-last` suffix appears
in the title, reveals the first source line in File view when it exists, and
highlights the included line numbers with a fill that includes their left padding
and stops before the gutter divider. The numbers and source stay in place.
Markdown files open in rendered
Markdown view by default, with Markdown and File tabs selectable by click or
Left/Right. File view retains numbered, syntax-colored source. Each view keeps
its own scroll and search state; whole-page copy in either view copies the original
file bytes. Other file types and read errors retain their single File view.
Paths inside the workspace use shared
workspace-relative display;
external paths stay absolute. The body includes a selectable path row and source
content, with file-type syntax colors under the existing highlighting limit.
Terminal controls are sanitized for display; whole-page `y` copies the original
source bytes; dragging selects the visible path or content for selection copying. Reads
accept UTF-8 text up to 8 MiB; oversized, binary/non-UTF-8, or unreadable files
show red, copyable errors. Each opening reads current contents, without ongoing
file monitoring. Missing local destinations are not clickable; their Markdown labels
remain visible. HTTP(S) links still copy their destinations in transcripts;
clicking them inside a dialog still does nothing.

Errors show a bounded first-line preview rather than an unbounded inline diagnostic.
A details link and click target appear only when that preview omits retained content;
a complete single-line diagnostic may wrap without gaining a redundant link.
Clicking an elided error opens its complete retained text,
including multiline JavaScript execution failures restored from history. `Ctrl-B !` opens
the newest error in the displayed transcript; Left/Right navigates
its other retained errors. Error details
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
output instead of attributing it to the last command. It never guesses boundaries.
It shows recognized edits with the same operation classification as the feed, rather than
showing their source as a Run script. Unresolved source stays visible, and the
combined buffer and invocation outcome remain separate from those requested edits.
Source-derived Run rows remain requested intent when execution was not observed.
The dialog takes
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
plain text. Skill-read output renders as Markdown in dialogs and fully expanded
transcripts, wrapping to the available body width
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
the selected text as one undoable reference token at the caret, including in an
active question answer. Copy and Clear keep the dialog open; Esc clears
a selection before a later Esc closes the dialog. Wheel, arrow, page, and
Home/End scrolling preserve the frozen selection, including off-screen rows.
Changing pages, navigating search, or resizing clears it; End resumes live
following when there is no selection. The footer offers no drag-selection hint.
[Retained activity output](../../internal/activity/SPEC.md#retained-output) owns
content bounds, completed outcomes and session retention independently of the
animated tail. Dropped lines have a visible count. A released page says so and
shows its bounded display tail instead. When tracked output loses attribution or
exceeds its between-frame buffer, the feed uses the host's combined output.
Dialogs observe current retained completion even while the feed's tail is rolling.

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
retained output, colored as a patch for `Diff`. In transcript output and the
output dialog, recognized unified diffs show distinct file sections with a clear
path heading and `+N -N` changed-line counts instead of raw `---` and `+++`
headers. Added and deleted files identify their real path rather than `/dev/null`;
renames retain both paths. Hunk content remains unchanged. Partial or unrecognized
diff output retains its available evidence without claiming complete file counts.
Retained raw output and copied source remain unchanged. The roster summary shows the verb,
heading, the rows' changed lines, and the source.
Unknown and zero exits add no failure label, and an unknown exit no output. Python, JavaScript (Node and Bun), and Perl interpreter previews use
their own syntax colors. `Search` patterns are styled as literal patterns,
not shell commands, while every target path uses the Search violet with path emphasis.
Roster operation summaries retain syntax highlighting. Messages show `✉  to <recipient>` for outgoing and `✉  from <sender>` for incoming delivery,
with two spaces after the envelope because many fonts draw it wider than its cell,
relative to the entry's agent. Peers use `main` or the child display name; the owning
agent is not repeated. The message body gutter retains the sender's color. Start
blocks indent the assignment beneath `Started` without a `Spawn assignment:` label.
Assignment timestamps retain Codex’s original message creation time across follow-ups
and replay; collecting historical input does not reset those times. Queue retention
uses arrival time independently of the timestamp shown to the user. Final answers remain
authored Markdown. Blank lines opening or closing a message, as some providers stream,
add no rows. Main completion previews retain the response excerpt and show
the linked assignment excerpt below its timestamp on separate, wrapped quote rows.
Each excerpt shows up to two content rows with an ellipsis when truncated;
the child transcript retains the complete response. When a retained child result contains
provider answers, exact replacement provenance combines their cards into one
complete enriched reply in Main and the child transcript. The assignment link and full reply
remain available. Host messages stay unchanged; missing replacement evidence keeps
the messages separate rather than guessing from matching text.

Authored Markdown headings follow Bun's terminal Markdown rendering. ATX
headings take one to six markers followed by a space or the line end, with any
closing marker sequence hidden; Setext `===` and `---` underlines make the
preceding paragraph line a level one or two heading. Titles are bold, and
levels one to five use magenta, cyan, yellow, green, and blue in the theme's
palette; level six keeps the default foreground. Inline code, bold text, and
links restore the heading style after their own styling. H1 adds a dimmed
double rule and H2 a dimmed single rule, matching the widest rendered title row
with a minimum of three cells. Reasoning summaries keep headings as plain bold
text without rules. Source-aware copying retains the original markers and
Setext underlines and excludes generated ATX rules.

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

Completed `mermaid` fences render an extended bounded Codex 0.159.0 flowchart subset:
`flowchart`/`graph`, TD/TB/BT/LR/RL, rectangle/decision/stadium nodes, solid and
dashed directed/undirected/bidirectional edges, pipe and spaced directed labels,
and `&` endpoint groups expanded across chained edges. Quoted labels preserve
punctuation, delimiters, and semicolons. Node labels support `<br>`, `<br/>`, and
`<br />` line breaks and wrap to fit the pane. LR/RL charts retain their horizontal
layout when it fits; otherwise they use TD/BT respectively, keeping node order
and every edge. Each edge has a separate lane; crossings
are not junctions. No external renderer or process runs.

Unsupported syntax, other HTML/entities, unsafe or non-additive-width labels, open
fences, and diagrams that cannot fit the pane retain the source code display,
never a partial diagram. Bounds are 16 KiB source, 16 nodes, 24 expanded edges,
24 references per group, 256 display cells per node label, 40 display cells per
edge label, and 65,536 canvas cells.
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
and the right column, with its own resizable divider. Its selection chooses the displayed transcript;
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
Tokens and cost are read from the canonical [per-thread usage totals](../../modules/usage/SPEC.md),
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
operation and the count of the others. The selected thread transcript occupies the main body above Main's composer.
The native layout contract owns narrow-terminal and compact-roster placement.
Shared column widths derive from currently visible roster rows. Before hiding agents, cards compact to one row per agent, retaining
selected-agent metrics when space permits. Hidden rows have directional counts and responding/error counts on the
corresponding edge where space permits; the header always includes overall counts.
Counts follow each agent's status symbol, so a responding agent is never also
counted as an error.
The viewport moves only when selection leaves it, not to recenter each selection.
Narrative events use Main’s shared colored event headings, timestamps, and rails;
available timestamps show only where they fit, and unknown timestamps stay absent.
Authored messages use the full-message transcript renderer. Collapsed reasoning
retains its body in the shared dialog.
Operation source and output each keep up to five rows without clipping their status. Hovering a clipped
snippet underlines its hidden-line count. Clicking an operation opens the output
dialog when it carries retained output or additional source; narrative text,
including thinking and delegation assignments, opens in the same dialog only
when its presentation elides content or retains distinct additional detail.
Fully displayed short reasoning and diagnostics have no
redundant underline hint or full-view click target. Neither action changes
transcript following. Roster rows are clickable: hovering highlights an agent; clicking a child switches
only the displayed transcript to that child. Clicking Main returns to Main.
The composer continues to target Main. In Agents, `↑`/`↓` or `k`/`j` move from
Main through each child, stopping at either end; `n`/`Tab` and `p` also navigate.
`a` returns to Main from a child or selects the current child from Main.
The roster mouse wheel scrolls its viewport without changing selection, filtering,
or feed follow state.
Hover underlines only the name; selection shades the row. PageUp/PageDown and wheel
scrolling pause transcript following; Esc resumes it. Frozen selections retain
their document-navigation controls.
Running command headers scroll with the transcript. Main's composer shows a
breathing running-process count, and `/ps` opens their live output in the shared dialog as
specified by the [UI contract](native_ui.md#running-processes).
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
5. Running command rows scroll normally; the composer count and `/ps` dialog
   reflect live commands and release them on native completion.
