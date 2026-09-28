# Native activity presentation

## REQ-ACTIVITY-DISPLAY-001 — Operation rows and agent feeds

The [native UI](native_ui.md) owns app-server event intake, lifecycle, history
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
`(requested)` marker is not shown as source text. A started patch that has not
completed, including one awaiting approval, ends with `· pending`; a `failed` or
`declined` patch uses a red verb and ends with `· failed` or `· declined`, named
once for the group rather than on every row. A different source or outcome
starts a new group. A source and outcome that do not fit after the first file
take their own row in the verb column.
File rows' line counts share one column within a group of more than one row when
the row fits, and zero counts are omitted. In an `Edited` group of more than one
row, an eight-cell bar after the counts scales each row's changed lines against
the group's largest.
A path too wide for its row keeps its file name and elides the middle of its
directory, or of the name itself when that alone is too wide; below a readable
width it wraps instead. A receipt's tool-managed files are one row in the
receipt source's `Edited` group. Unresolved edit targets remain an explicit
`paths unavailable` row in the same group. Main also folds repeated confirmed edits of one path within
a group into one row with summed counts; Activity keeps each invocation's rows,
since cross-pane navigation targets them.
They retain paths and line counts but omit diff bodies in the
pane; durable change evidence and inline receipts are unchanged. This omission
applies only to generated tool activity, not authored text. Output-reduction
summaries align with command text on their operation's branch and use muted,
dimmed styling in either theme.
Consecutive same-action target events by one agent collapse into one row, both
within a call and across calls. Reads join ranges of the same file; Inspect,
List, Search, and other target-only actions use the same grouping. A group that
does not fit on one row puts each target on its own row, without separators;
Search targets use the same path emphasis as reads. Different
actions and detail-bearing operations remain distinct. When one live invocation
reports several operations at once, such as a shell call classified as Skill,
Read and Search, they appear one at a time, 80 ms apart, in Main, Activity and the
roster summary; restored history shows at once. Child text is sanitized before layout, so it cannot emit terminal
controls. Local absolute-path Markdown links show their label as a terminal
hyperlink rather than exposing the raw destination syntax. A completed child
compaction appears as an event in the feed and as the agent's latest roster
activity; an attempted or failed compaction does not claim completion.
Read line spans display as `L25–46`. Each `Run` operation is its own row. In
the native app-server UI it reads `Running` from the host's command start, in
Main and in the agent's feed and roster summary, and `Ran` once the host
completes it. A yielded process stays `Running` across turns until it exits;
replayed history never shows `Running`.
A single-line command follows the verb. When it does not fit, each top-level
statement after `;`, `&&`, or `||` starts a row aligned with the first, and a
statement that is still too wide breaks at unquoted blanks with a muted ` \`
shell continuation, or after a pipe, two columns deeper; a word wider than the
row is cut without one. Fenced multiline `Run` previews sit beside the verb with
the code gutter. In Main a command or program preview keeps at most eight wrapped rows, the
last a muted `… +N lines` count (under the code gutter for a program), so a one-line
command that wraps is bounded too; a click opens the whole source and any collapsed
output, and another closes both. Tabs in previews expand to four spaces. `Run JavaScript` Code Mode previews always place source beneath
the heading with the same code gutter, whether the source has one line or many.
A confirmed nonzero command exit makes the verb red and adds `· exit N` after
the command, or on its own row when it does not fit or follows a multiline
program; the roster summary keeps a red `(exit N)`. When the host reports
aggregated output for that failure, the last five non-blank lines follow the
command under a muted dashed `┆` gutter, distinct from the code gutter, sanitized
and bounded per line, with a count of earlier lines. While a command is
`Running`, the host's streamed output shows the same way as a rolling tail,
including an unfinished last line; a carriage return restarts its line, as a
progress line redraws. A burst rolls through rather than jumping to its end: each frame reveals a
quarter of the waiting lines, at least one, and an unfinished line shows once
nothing waits. Only a burst's latest 64 lines roll, so the tail's memory stays
bounded however much the command prints, and at most one update per frame is
drawn. A completion that arrives while a burst is still rolling, as from a
command that prints only when it exits, is held until the burst has rolled.
The host supplies one combined output stream per shell invocation. Its tail follows
the final displayed operation, including a Read or Skill row, rather than an earlier
Run row; it does not claim per-command output attribution.
Completion replaces it with the tail of the host's aggregated output. A
failure keeps that tail open. After a zero exit it stays open until the same
agent's next standalone event, such as a separate command, Skill or Read, then
collapses to one muted `┆ … +N lines` row once events pause for 750 ms after the
later of that event and the output's completion, so a quick run of commands
collapses together; a click on the command opens
it again and another collapses it. Restored history shows zero-exit output
already collapsed. Output arriving after completion is ignored.
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
blocks indent the assignment beneath `Started` without a `Spawn assignment:` label. Other final answers remain
authored Markdown. Blank lines opening or closing a message, as some providers stream,
add no rows. Main completion previews retain the response excerpt and show
the linked assignment excerpt below its timestamp on separate, wrapped quote rows.
Each excerpt shows up to two content rows with an ellipsis when truncated;
Activity retains the complete response.

### Agents roster and navigation

The main agent and its children appear as a canonical-path tree in observation order, with each agent's
current activity and an elapsed-time timer (`elapsed · age ago`, or `just now`; `—` until
its first response completes). Elapsed time stops at the last response while the agent
is not responding; the age keeps counting. The root summary shows its latest observed activity or a newer message addressed
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
`+added -removed` in the Diff pane's colors, `↑ in ↓ out` tokens, estimated USD cost
to two decimal places, and provider-response turns as `T+N`. Edit lines total every
capture attributed to the agent, independent of the Diff caller filter; an incomplete
capture shows `?` rather than a partial count, and an agent without captures leaves
the column blank. The roster rule totals them with the other session totals.
A changed token, cost, context, or edit-line value counts toward its new value over
half a second, easing out from the value on screen, and the rule's totals follow the
eased rows. Values seen for the first time, including restored history, show at once;
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
Tokens and cost are read from the canonical per-thread usage totals that also produce the
[usage report](usage.md), not accumulated separately by the roster. The roster therefore follows the
report's gap rules: an accepted or transport-interrupted response without usable usage
shows the observed cost as a lower bound, `≥$N`, and later usage does not erase the
mark; definite rejections and non-generating prewarm leave no mark. When a thread has
no observed usage, its pricing is unavailable, or its totals are unavailable, the cost
is omitted rather than claimed as zero. Explicitly reported zero input or output totals
remain valid. While a response streams, output grows by an estimate of about four
bytes of visible delta per token, refreshed every second; the provider's reported
usage replaces the estimate when the response ends. Hidden reasoning is not
estimated, and input changes only when usage is reported. A Code Mode batch shows its latest
operation and the count of the others. When the separate roster is visible,
the activity pane gives its whole body to the feed. The native layout contract owns narrow-terminal and compact-roster placement.
Shared column widths derive from currently visible roster rows. Before hiding agents, cards compact to one row per agent, retaining
selected-agent metrics when space permits. Hidden rows have directional counts and responding/error counts on the
corresponding edge where space permits; the header always includes overall counts.
Counts follow each agent's status symbol, so a responding agent is never also
counted as an error.
The viewport moves only when selection leaves it, not to recenter each selection.
The separate roster header omits feed follow state; the feed header is `ACTIVITY`
with its filter and follow state. Feed-only controls do not offer clicking agents. The feed groups
consecutive entries by agent under a colored heading. In the shared view it clips
long entries, and its only mode shows one agent in full. Hovering a clipped
snippet underlines its hidden-line count; clicking it expands it in place and
pauses following so it stays put, and clicking it again clips it. Roster rows are
clickable: hovering highlights an agent; clicking it shows only that agent,
and clicking it again restores the shared feed. In the roster, `↑`/`↓` or
`k`/`j` move through all agents followed by each individual agent, stopping at
either end. Main's activity belongs to Main, so Activity neither shows nor counts
Main: roster navigation skips its row, and clicking it restores the shared feed. `o` toggles the filter. `n`/`Tab` and `p` also select agents. The roster mouse wheel
scrolls its viewport without changing selection, filtering, or feed follow state.
Hover underlines only the name; selection shades the row. Feed scrolling follows the same
line/page/home/end/follow contract as the [live diff](changes.md#live-terminal-view).
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
2. Requested, pending, failed, and confirmed edits remain visually distinct; display
   grouping preserves source, outcome, paths, and observed counts.
3. Command output tails remain bounded, reflect host completion, and preserve the
   difference between unknown and nonzero exits.
4. Roster selection and scrolling preserve feed state; off-screen rows do not change
   shared column widths. Missing evidence never becomes a completion or cost claim.
