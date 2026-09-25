# Applied changes and live view

## REQ-CHANGES-001 — Durable review of stock edits and command effects

Mekugi observes stock Codex `apply_patch` calls. Codex executes each call once;
Mekugi does not replace the tool, run a hook, apply a second patch, or alter the
argument or result. A complete argument may be projected as a provisional live
diff while streaming. Only the actual host result and resulting workspace
state determine a completed change record. A yielded call remains unfinished
until its host continuation is terminal. Saved file differences are applied
changes: a later command or test failure does not undo bytes already written.
Command exit codes and native tool results remain separate diagnostic facts.
They never gate whether a saved file difference participates in review,
composition, or rename tracking. There is no receipt-driven change-state
transition, including when reading older retained records.

The observer captures bounded pre-edit UTF-8 contents for paths named by a
complete patch and compares them with the resulting files. Missing source is
retained as incomplete file evidence rather than an invented diff. Calls with
no file differences remain in command history without a new change ID.
An unfinished call has no completed record. Storage failure must not expose
dependent review evidence as durable.

### Command effects

Mekugi also observes stock `exec_command` calls and literal `tools.exec_command`
calls in a Code Mode cell. Before Codex receives the call, Mekugi classifies
the Bash command text without running it. A command is declared when every
file it can write follows from literal text: output redirects, coreutils file
operations, in-place `sed` and `perl` substitutions, and version-control move
and remove forms, including glob operands, `cd`, and coreutils destination
rules. The command is read in its own `shell`, or else in the session shell
that the request's environment context names. A `cd` moves later operands only
where its failure stops them: the rest of an `&&` chain, or the statements after
`cd DIR || exit`. Any other shell, command substitution, dynamic word,
background job, change to command lookup, or unknown or path-qualified program
makes the command undeclared, as does a relative operand after a `cd` that may
have failed and a call for another environment. Non-neutral undeclared commands
and Code Mode cells with dynamic command calls are observed with an open scope.
Literal `rtk proxy` commands and recognized named RTK wrappers are classified and
attributed to the underlying command, not the output filter. Literal `rtk run -c`
and `--command` strings are inspected as nested shell source. Unsupported wrapper
forms remain open. `env -u`/`--unset` preserves underlying scope unless it removes
`PATH` or cannot be parsed. Code Mode `write_stdin` calls with literal empty or
omitted input are polling, not an additional writer.

For a declared command, Mekugi captures the derived paths before the call is
forwarded, within bounds on file count, file size, total and encoded size, and
time. A path past a bound is recorded as omitted. When an earlier statement can
change what a destination names, such as a directory it creates or removes,
every reading is captured. A recursive destination is listed so that files it
gains are found afterward. A write through a symlink captures the link's
target, and a dangling link is omitted. Capture never follows a final symlink
or blocks on a special file. Content that is not UTF-8 or contains NUL is kept
as size and hash, and a symlink as its target.

After the terminal host result, Mekugi compares only captured write paths and
explicitly listed destinations. It does not sweep the workspace or infer authorship
from timestamps. Unknown dynamic writers, tests, and generators without derived
output paths do not acquire unrelated filesystem changes. Capture timeout retains
a diagnostic, not a fabricated workspace-wide change.

The record keeps command labels, actual host exit codes when available, and capture
diagnostics for `--history`. A nonzero exit is a command failure, not a failed
file change. Native receipts retain per-tool results for Code Mode; absent receipts
do not invent exit codes or delay captured file changes. Yielded calls wait for
their terminal `write_stdin` or `wait` result before capture completes.

Coverage describes only the captured scope: complete comparisons are `exact`;
incomplete baselines are `partial`. A deletion and an addition with identical,
nonempty content form one move. A created copy names its source when the content
matches. Binary content is shown as sizes and hashes rather than rows, and a
symlink change as its link target. A path that could not be compared is incomplete.
A command with no observed effect is retained without a change ID. Edit summaries
describe saved file differences independently of the command exit status.

Sibling calls emitted in one response and completed in one request share
one record. Overlapping writer windows retain call references or change IDs for
diagnostics; same-cell patch paths belong to their patch records. No record is
finalized without a terminal result. The overlap registry is process-local;
restart preserves captured scope but does not revive running windows.

Capture is read-only and installs no hooks or configuration. Changes outside the
agent's derived write scope are excluded, including automatic hook edits to other
files. If a hook or external writer changes the same scoped file before the host
returns, before/after evidence cannot separate that writer's content from the
agent's edit. The recorder must not claim otherwise.

Review origin is direct by default. Explicit formatter and dependency-manager
commands produce tool-managed effects. Interpreter edits and declared file
operations remain direct. Default `mchanges` shows managed effects as rows,
collapsing more than 20; explicit paths and `--history` show their evidence.
Summary counts distinguish tool-managed files, and managed-only records are
labeled in `--list`. Receipts group managed files into one line; the saved DIFF
view groups them into one display-only card per record.

Interpreter scope providers parse Python and JavaScript/TypeScript source without
evaluation. Literal eval arguments, stdin heredocs, and bounded script files are
supported. They derive literal filesystem writes, single-assignment path values,
and sequential top-level Python path reassignments. Known Python string
replacement is not treated as a filesystem rename. They also derive
path joins, and iteration scopes, including Python `Path.glob/rglob` and JavaScript
directory enumeration. Literal subprocess arguments are classified recursively
with a depth bound. Unresolved targets, dynamic evaluation/loading, and unknown
working-directory changes leave the scope open. Node and Deno write permissions
provide bounded scope hints; subprocess/native-code permissions reopen them.
Deno named permission sets are read from bounded local configuration, never by
launching Deno. Only the derived scope is compared after execution.

Literal `find` tests feeding a supported writer through `-exec` or `xargs` derive
scopes without executing the writer. Known read-only `rg -l`, `grep -rl`, `fd`,
`git ls-files`, and `git grep -l` producer stages may run alone with bounded output
under the provider deadline. Executable preprocessing, output writers, and dynamic
producer arguments are rejected. Provider work shares a 200 ms budget within the
500 ms pre-call capture hold. A failed, unavailable, or timed-out provider degrades
to an open observation with its reason retained in history, never a host-call error.

Each completed observation with changed or incomplete file evidence receives a
short session-scoped change ID. Complete no-effect attempts remain in retained
call history without allocating IDs. Legacy no-effect IDs remain explicitly
readable but are omitted from `--list`; compressed ranges do not bridge them. Root
and child agents share an inherited namespace; forks and side threads receive
an isolated copy of visible records, and resume can read durable records after
a fresh router process. Retention may expire inactive records according to
[REQ-ROUTER-001](router.md). Replay reads retained facts and never repeats a
host edit.

VCS providers scope local discard and history operations using read-only queries.
Git queries disable optional locks and filesystem monitors; diff queries disable
external diff and text conversion. Configured clean filters prevent worktree
comparison queries, leaving an open operand scope. Restore, checkout, hard reset,
stash, clean, patch application, and local revision changes are supported; remote
and iterative operations remain open. Clean expands reported directories and
preserves exclusion patterns; patch scopes include rename sources. SVN revert
uses offline status at the requested depth. Mercurial revert uses operands and
possible `.orig` backups only; Jujutsu uses operands without snapshotting queries.

Known formatter scopes expand operands by supported file extension. Dependency
manager scopes include their local manifests and lockfiles. These are tool-managed
effects. Direct paths take capture priority; a path also in a managed scope stays
direct with shared-origin attribution.

Explicit `go fix` package operands scope existing Go source files, within the
shared file, byte, enumeration, and capture-time limits. Tests and generators do
not imply ownership of their package directories. Import paths are not resolved
by executing Go. An existing formatter/fixer path enumerated but not baselined
after a capture bound is checked against a filesystem-clock marker after the call.
An unchanged path creates no review. A changed or deleted path has unknown
before-content and partial coverage, not an invented diff. An incomparable clock
leaves the named scope incomplete. No last-seen cache supplies substitute baselines.

### Bounded read command

The session-private `mchanges` executable lists the current thread's change
IDs or reads selected IDs and ranges:

```text
mchanges --list [--workspace DIR] [--max-tokens N]
mchanges [--mine | ID[..ID] ...] [--summary|--history|--net] [--workspace DIR] [--max-tokens N] [-- PATH ...]
mchanges revert|apply ID[..ID] ... [--workspace DIR] [--max-tokens N] [-- PATH ...]
```

A bare `mchanges` or `--mine` selects every allocated ID owned by the calling
thread, including explicit markers for retired evidence. Forked threads inherit
their visible stream under the fork's identity; resume uses the durable identity.
`--list` shows IDs and counts, without execution outcomes or capture diagnostics.
It compresses consecutive comparable complete IDs and
shows known direct `+N -N` counts. Partial or unswept IDs remain separate;
`?` marks unknown direct counts, while `managed:N` counts tool-managed review
files excluded from the default diff. Pending and retired IDs remain visible;
sibling threads' IDs are not exposed. Explicit IDs can still be read across
agents in the shared namespace. The default view shows each ID and unified
file diff. `--summary` prefixes each path's added and removed line counts with the same
file status as the diff pane (`A`, `M`, `D`, `R`, `RM`, or `UU`), aggregating across
selected records in durable capture order, regardless of argument or author order.
A created-then-deleted file has no summary row, matching the empty saved diff.
Paths inside the selected workspace are shortened. By default,
managed files become one count row with a separate unavailable-count tally;
explicit path filters expand individual managed paths. Pending, retired and never-allocated selections get per-ID
status rows without hiding the remaining summary. It is not a net workspace diff;
binary or incomplete files have unknown counts. Numeric range ends such as
`amber1..3` are equivalent to `amber1..amber3`. A path operand belongs after `--`;
unknown options are diagnosed as options. Missing-ID errors distinguish retired
from never allocated, give the latest allocation for the stream, and identify
the selected workspace so callers can correct `--workspace`.

`--net` composes selected completed captures in recorded capture order using the
same review composition as the live view. It follows moves and emits canonical
absolute paths; it never reads the live workspace. Pending or retired history and
inconsistent, incomplete, partial-coverage, or binary capture chains
fail rather than claim a complete net diff; ordinary reads preserve that evidence.
Complete captured diffs remain composable regardless of command exit status,
including older retained records. Normal views
describe captured file differences, not command outcomes; execution and capture
diagnostics are available in `--history` only.
Path filters apply to the composed files. An empty composition is explicit.
Mutations still require explicit IDs; `--mine` never selects writes. `--history` includes the original observed patch or command input, the
host result, and for a command the observed scope. Paths after `--` filter review files without re-reading the
current filesystem. `--workspace ..` selects the owning workspace index when
the command runs from a subdirectory; paths after `--` only filter entries in
the selected record. `--max-tokens` bounds displayed output. When output is
omitted, an `mread` reference retrieves the retained remainder on complete-row
boundaries. The receipt freezes the selected attempts, allocation state and view
under the read lock, and retains those exact attempt dependencies. Appended
attempts or a pending change completing cannot change an existing continuation;
its original pending/retired markers remain visible. Older receipts without a
frozen selection retain their digest-check behavior. Pending, expired, or incomplete
history is explicit; no missing evidence becomes an empty successful diff.
In `--history`, a record with file changes is `applied`; a no-effect attempt
is `no changes`. The original command's results and errors are separate debug
information. Only unfinished work is `pending`.

Completed file changes can publish a generated `Create` or `Edit` commentary
summary, including changes left by a command that exits nonzero. Classification
uses the same captured review files as `mchanges`, including whether a path
existed before the edit. Each summary carries a bounded copy of the captured
hunks. Unchanged and unfinished calls publish no edit summary. These messages
are user-only presentation, not substituted tool results.

### Revert and apply

`mchanges revert` undoes the selected completed records in the workspace, latest
capture first; `mchanges apply` replays them in capture order. Paths after `--`
select files within the records. Records hold only hunk context, not source
files, so each hunk is located near its recorded position: first exactly, then
by its leading and trailing context, dropping outer or inner context lines while
at least one line anchors each side that is not a file boundary. A located
region that has drifted is merged three ways against the requested side.
Changes on one side apply cleanly; overlapping or adjacent changes on both sides
leave git-style `<<<<<<< workspace`, `=======`, and `>>>>>>> mchanges revert ID`
markers. A hunk whose context cannot be found is printed and left unapplied, not
guessed. A hunk already in the requested state is counted as already reverted
or applied. Undoing a creation whose file gained other content keeps the file
as a conflict, like git's modify/delete. Binary, incomplete, symlink (a link on
either recorded side), and unreadable entries are skipped with their reason. A file with a conflict takes
no further hunks from later records in the same command. All merges complete in
memory before the workspace is written; creations and updates are written
before removals, and a move's source is removed only after its destination is
written. A write that replaces a directory, or that needs a file removed where
its parent directory belongs, waits until the removals are done. A directory is
replaced only once it is empty, so a file swapped for a directory, or the
reverse, is restored without deleting unrelated files. Recreated files are written with mode 0644, since records do not hold
modes.

Each touched file reports its state relative to recorded mchanges history, not
version control. The file's retained captures, in store-wide capture order, are
composed with the command's own effect, following moves, deletion, and
re-creation. `clean` means the composed net change is empty. Otherwise the line
shows ` M`, ` A`, ` D`, or ` R` and the net `+N -N` rows. `UU` marks conflicts,
and `??` marks a file whose stat is unknown: its content before the command
disagrees with the composed history, such as after an unobserved edit; its
history cannot be read; or its write failed. History loading is limited to
records connected to the selected paths through moves, and a failure there
degrades the stat rather than blocking the mutation. The command exits 1 when
any file conflicts, is skipped, or leaves a hunk unapplied. The summary or undo
line comes first so that paging cannot hide it. A continuation retains the
report as plain output and never repeats the mutation; when the report cannot
be paged or retained, it is printed in full with the reason on stderr.

The router classifies `mchanges revert` and `apply` as declared writers whose
scope is every path named by the selected records, read from the change index
before the call is forwarded. The host runs the command once; its observed
effects become a new change record like any declared command. A revert is
therefore revertable, and a clean command prints `undo: mchanges apply ID ...`
or `undo: mchanges revert ID ...`, with the same paths after `--`. When some hunk was already in the requested
state, skipped, or conflicted, the inverse command is not an exact undo, so the
report instead points to reverting the command's own change. Without a readable
change index, or when the subcommand or a mutation operand is dynamic, the
command is observed with an open scope. A read with dynamic operands stays
neutral.

### Live terminal view

In an interactive terminal, Mekugi opens its integrated viewer on the
first observed editing or execution call. The stream view shows concurrent
main-agent and child calls. It streams edits only: provisional `apply_patch`
diffs and the file effects of shell commands, before completion. Stock `cat`
heredoc redirections are always streamed, including from a native
`exec_command` or Code Mode `tools.exec_command` call whose arguments are
still arriving. Literal `cp`, `mv`, `rm`, and `tee` heredoc commands are
predicted from current file contents. A preview does not claim that Codex ran
or accepted an edit. A Code Mode patch held in an immutable top-level literal
binding is rendered as the patch preview.
Literal Python `Path.write_text` and `open(..., "w").write` bodies and literal
JavaScript `writeFileSync`/`writeFile` bodies can be predicted without evaluation.
Python same-path `read_text().replace(A, B[, count])` supports literal replacements;
straight-line text-buffer assignments and chained literal replacements are also
supported. A Python edit script composed through a `cat` heredoc previews these
target-file changes rather than the script file. While it arrives, a changed
read buffer can be shown before its final write statement. This is a provisional
prediction only: script creation does not claim that its edits ran, and neither
the script nor the host tool call is rewritten or executed by the preview.
Before edit intent arrives, an unfinished Python heredoc has no source-file
preview; ordinary Python file creation is displayed when the heredoc closes.
Unsupported buffer mutations, including augmented assignment and tuple rebinding,
are not predicted.
Regex replacement is excluded. An interpreter heredoc still arriving is
predicted on a best-effort basis by closing its unfinished content literal.
Command and script text is never displayed. A command that is not a
recognized edit has no card, and a later non-edit call keeps the last
displayed edit. A literal `workdir` resolves relative targets; when an edit's
`workdir` is computed, the card reports that its target cannot be resolved.
Scope cards list pending VCS restore, deletion, or switch targets when their
targets are known. Ordinary command watches remain hidden until a captured
file changes. A `may write` footer distinguishes scoped paths from unresolved
targets on visible cards.

While a writer window is open, display-only polling runs about every 500 ms over
captured paths, reading content only after a stat change. Polling is bounded by
path, time, and content budgets; it never runs a workspace sweep or provider query.
Changed-file cards say `observed so far` and disappear if the files
return to their captured state. Terminal results, background transition, or
viewer shutdown remove their live preview. Running previews and predictions
never become durable evidence, and replay does not restart polling.

Query-based Mercurial scoping and literal `sed`/`perl` substitution prediction are
outside this delivery. Model-visible command-change notices are also deferred;
stock result bytes remain unchanged.
Patch previews show projected source changes with the affected file's language
highlighting, not the `apply_patch` instruction envelope. If source matching
cannot establish that projection, the viewer must not fabricate a diff.
This provisional display never changes Codex's original tool input or asserts
that the command ran.

A card header names the caller, then states the call with a roster glyph
rather than a word: `◐` while the call is arriving or running, `✓` once it
completes, and `!` with the reason when the edit cannot be projected. The
current file follows, styled like file navigation: its status and live `+N -N` line counts, with `N/M files` when the call edits
several.

The integrated live-input pane opens on its first displayable preview, not on
an execution or launch request with no stream content. Agent activity can open
independently without reserving an empty live-input area. Explicitly focusing
the diff pane still opens it on demand.

The router publishes to one in-process UI mailbox without waiting for rendering.
Replaceable previews retain only the latest frame per call; a mailbox that falls
behind resynchronizes from durable changes and retained display state.

Provider input arrives in bursts. The stream view reveals each call's received
input at its recent arrival rate, so the preview grows steadily rather than
jumping per burst; the reveal trails received input by at most a bounded
window and completes on the call's final input. The reveal advances by whole
lines. An unfinished line stays buffered until it completes, or is shown as it
streams after about half a second. Newly revealed rows fade in from partial visibility, and rows
revealed together cascade in order; the fade is display-only and never delays
the underlying projection. A completed snapshot displays at its final colors
without re-fading retained rows. The first usable frame and completion redraw
immediately; intermediate deltas may
be coalesced. Card
positions stay stable: a
new call takes over a finished card's slot, preferring its own caller's. The
last displayed input stays visible while waiting for another call.
The line-number column of a card never narrows while its call streams.

The saved diff view uses completed observed patch and command outcomes. It includes
changes from children that are visible to the parent. The viewer switches
to it after the root's usage and journal flush, and back to stream for the
next prompt. The user can switch, scroll, pause following, resume, or flush
visible cards without changing execution or durable evidence. Each mode's
footer reports what the other holds: live calls from the diff view, and
unreviewed files from the stream view. A child caller's card label uses the
same color as that agent in the agents pane.

The saved diff navigator is a presentation index over unreviewed files, not a
reordering of capture history. Wide panes show a persistent, collapsible left
dock with colored status and inline added/removed counts; `s` hides it. Narrow
panes use `s` to toggle a full-width picker so code retains its reading width.
The diff pane header omits the redundant file/path/row count; section headings
within the diff still identify each file.
Tree and flat choices remain stable for the viewer lifetime. Filtering shows
matching relative paths, including descendants of collapsed folders; clearing
it restores the tree's expansion and navigation position. Files use stable
path ordering, with folders first in tree mode. Next/previous file navigation
uses the matching set, revealing destinations inside collapsed folders.

The navigator and diff have independent viewports. Keyboard focus is visible;
the active list row is shaded in place rather than marked by a separate arrow
column, leaving that cell available for file names and graph rows.
Mouse scrolling targets the region under the pointer. Opening a file from the
navigator or a change starts at its section heading. Next/previous file
navigation restores a position only where the reader stopped before jumping
away; scrolling past a file keeps none. Without a title row, the open file's
heading stays pinned while its content scrolls. Hunk navigation uses rendered
hunk boundaries and, like a file jump, keeps the position of a file it leaves.
Each change record keeps the canonical path of the agent that issued it and
the tool or program that wrote it (`apply_patch`, or the observed program such
as `sed` or `python3`). The navigator's Changes tab lists unreviewed changes in
capture order as a graph with one lane per caller, branching from `main` at the
caller's first change; each row shows the change ID, source, file count, known
line counts. Missing line counts use `?`, not a command-failure glyph. A file's
section heading lists the changes it composes. Every saved capture participates
in composition regardless of command exit status. If retained contents cannot
form one coherent diff, the pane shows the individual captured edits with their
IDs instead of hiding them behind a composition error or inventing a net diff.
Next/previous change navigation
opens each file of each change in that order. A caller filter shows one
caller's changes: other callers' captures compose as reviewed baseline, so the
shown diff remains the exact net effect of that caller's edits, and the heading
counts the changes folded into it. Flushing under a filter reviews only the
shown caller's captures. Agents appear by display name: `main` for the root,
otherwise the path below it. Records from before attribution show an unknown
caller, which filters like any other. File rows in the tree, flat list, Changes
tab, and streaming title share one format: a colored git-style status, one
space, then the name. The status is `A`, `D`, `M`, `R` for a rename with no
content change, or `RM` for a rename with edits or uncaptured content; a rename
names its source as `old → new`. `UU` marks a net diff that still adds
`mchanges` revert or apply conflict markers, and clears once they are resolved.
Incoming updates retain the paused file, navigator cursor, and top-row identity
where those entries still exist. Resize keeps the logical diff anchor and the
focused navigator entry visible. File status, known line counts, folder file
counts, and recent-update marks remain distinct from capture confirmation;
unknown counts are not presented as zero. Help is available without permanently
occupying code rows. These controls change no execution or durable evidence.

Mekugi owns the Codex PTY and terminal composition. Codex remains the execution
and approval authority. The diff occupies the right side; the agents view,
specified in [REQ-COMMENTARY-001](commentary.md), shares that side vertically.
The separate roster spans the full terminal width below both columns, with a draggable
divider; the column border ends at that divider.
Mouse dragging resizes the main split, the diff/agents split, and the file dock.
`Ctrl-B` followed by arrows resizes the main splits, and `[`/`]` resizes the
file dock. `Ctrl-B` followed by `1`, `2`, `3`, or `4` focuses Codex, diff, agents, or roster;
up/down in roster focus resizes its height. A click focuses the clicked pane. Small terminals show only the focused pane.
Pane sizes are bounded to keep content usable; resizing never changes evidence.

Diff and agents scrolling use one contract: arrows or `j`/`k` move one line,
PageUp/PageDown or `b`/Space move one page, Home/End or `g`/`G` go to the
beginning/end. These actions pause following; `r` resumes it. The wheel scrolls
diff and activity by three lines per event without changing keyboard focus.
Consecutive wheel events accumulate even before the next rendered frame. Stream cards retain
their source windows during manual scrolling and resume their live tips with `r`.
`Ctrl-C` in an auxiliary pane returns focus to Codex, rather than terminating it.
Bracketed paste is routed intact to Codex and cannot activate layout shortcuts.
Codex clipboard OSC 52 writes pass through to the host terminal once, including
fragmented sequences. Codex title (OSC 0/2) and progress (OSC 9) signals also
reach the host terminal once, preserving outer terminal-manager status detection.
Interactive launches inside Herdr expose the invocation-local `HERDR_AGENT=codex`
wrapper hint before router startup, without Herdr commands or pane-management APIs.
Main-screen history remains accessible through prefix
PageUp/PageDown and, when Codex has not captured mouse events, the wheel. Ordinary
Codex input returns to live output. The bounded 10,000-row history and final
screen are restored outside the wrapper's alternate screen on exit.

The wrapper starts no pane processes and requires no external pane manager.
The UI consumes the bounded event hub directly; there are no live-view HTTP
endpoints or connection files in the running application. Terminal mode is
restored after child exit, cancellation, or rendering failure; all owned I/O is
joined. Redirected sessions retain stock input/output and inline activity.

Acceptance:

1. Direct and Code Mode patch calls retain exact stock arguments and results.
2. A streaming preview appears before completion but does not create success
   evidence or a change ID before the host result.
3. Successful, failed, no-op, and partial outcomes are distinguished by
   result and workspace state. Read failures remain visibly incomplete.
4. `mchanges` lists the caller's IDs and reads completed records by ID, range, path, summary, and history
   through the authenticated frontend, with bounded `mread` continuation.
   `mchanges revert` and `apply` merge selected records into the workspace, leave
   markers for conflicts, report each file relative to recorded history, and are
   themselves recorded as revertable changes.
5. Child handoff and resume use durable ownership, not a live process; replay
   never executes an edit again.
6. Live `cat`, file-operation, and interpreter projections are presentation
   only and preserve stock PTY, yield, result, and `write_stdin` behavior.
7. A declared native or literal Code Mode command produces a record with the
   actual command outcome and reviewable scoped effects. A nonzero exit is retained
   in debug history; saved edits still publish their change receipt.
8. A yielded command is finalized only by its terminal continuation result.
   Replaying the same input neither re-reads the workspace nor allocates a
   second change ID.
9. Moves, copies, binary content, symlinks, and bounded or unreadable paths
   are represented without inventing rows. A link is reviewed by its target
   name; only a write through it reads the file it points to.
10. A destination that an earlier statement creates, removes, or fills is
    captured under every reading, and a `cd` whose failure would not stop later
    statements leaves their relative operands undeclared.
