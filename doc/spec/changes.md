# Observed changes and live view

## REQ-CHANGES-001 — Durable review of stock edits and command effects

Mekugi observes stock Codex `apply_patch` calls. Codex executes each call once;
Mekugi does not replace the tool, run a hook, apply a second patch, or alter the
argument or result. A complete argument may be projected as a provisional live
diff while streaming. Only the actual host result and resulting workspace
state determine a completed change record. For a Code Mode cell, outer script
completion alone does not prove a nested patch succeeded, even when file effects
are visible, so nested application remains unconfirmed. A cell that yields
remains unfinished until its host wait result is terminal.

The observer captures bounded pre-edit UTF-8 contents for paths named by a
complete patch. It later compares those snapshots with the resulting files.
If source cannot be captured completely, the affected review entry is marked
incomplete instead of inventing a diff. A successful host result may produce an
applied or no-op record. A failed result is never reported as applied, even if
some files changed; the visible partial difference remains reviewable and the
failure is retained in history. An unfinished call has no completed record.
Storage failure must not expose dependent review evidence as durable.

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

After the terminal host result, Mekugi compares captured paths and listed
destinations. Every non-declared command also receives a stateless change-time
sweep of the selected metadata directory, or its absolute workdir when no
metadata directory exists. Outside-root paths require a named scope. The
record states the outcome, the command labels, the host exit code when
visible, the command class, and its coverage:

| Host result | Status |
| --- | --- |
| Native exit 0, exact coverage, no overlap | `completed` |
| Native exit 0, overlap or incomplete coverage | `completed; attribution shared` or `completed; partial coverage` |
| Native nonzero exit, or an aborted or unrecognized result | `failed; observed effects` |
| Code Mode `Script completed` | `changes observed`; nested exit codes remain unavailable in `--history` |
| Code Mode `Script failed` or `Script terminated` | `failed; observed effects` |
| Yielded session or running cell | pending until a `write_stdin` result shows the exit or an unknown session, or a terminal `wait` result |

Coverage is `exact` only when all changed paths have captured baselines and the
required sweep completes without outside-scope findings. Unbased findings or
incomplete baselines make coverage `partial`; an unavailable or truncated sweep
makes it `unswept`. A deletion and an addition with
identical, nonempty content form one move. A created copy names its source when the
content matches. Binary content is shown as sizes and hashes rather than
rows, and a symlink change as its link target. A path that could not be
compared is incomplete. A command with no observed effect is retained without
a change ID. A successful edit receipt requires `completed`, `exact` coverage,
complete evidence, and no overlapping writer window.

The sweep compares inode change times with a marker's filesystem time, not the
router's wall clock. It is bounded to 100 ms and 50,000 entries. Network and FUSE
roots are unswept. It prunes VCS metadata and plain `.gitignore`, `.ignore`, and
`.rgignore` patterns even without a repository. Without governing ignore files,
dependency directories, Python bytecode caches, and directories containing
`CACHEDIR.TAG` or `pyvenv.cfg` are also pruned. Named ignored paths remain captured.
New inodes show unbased content labeled `new file or replacement`, never a guessed
Create action. Other unknown baselines show size/hash or unbased content when birth
time is unavailable. A changed directory with no explaining entry reports removed
or renamed entries without inventing names.

Swept sibling calls emitted in one response and completed in one request share
one record. Other overlapping writer windows are identified as `observed alongside`
call references or retained change IDs; their scoped paths and same-cell patch
paths are excluded from sweep evidence. Records describe changes observed during
a window, not proof of causation. Yielded windows remaining across turns become
background windows: they stop producing overlap tags, and later sweep findings
name the background session or cell. No record is finalized without a terminal
result. The overlap registry is process-local; restart preserves captured scope
but does not restore overlap or background tags.

Review origin is direct by default. Tools that choose content or paths, including
formatters, package managers, generators, and opaque commands, produce tool-managed
effects. Interpreter edits and declared file operations remain direct. An ambiguous
sweep finding is direct only when every undeclared statement is direct. Default
`mchanges` shows managed effects as rows, collapsing more than 20; explicit paths
and `--history` show their evidence. Summary counts distinguish tool-managed files,
and managed-only records are labeled in `--list`. Receipts group managed files into
one line; the saved DIFF view groups them into one display-only card per record.

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
launching Deno. Every provider-scoped command still receives the change sweep.

Literal `find` tests feeding a supported writer through `-exec` or `xargs` derive
scopes without executing the writer. Known read-only `rg -l`, `grep -rl`, `fd`,
`git ls-files`, and `git grep -l` producer stages may run alone with bounded output
under the provider deadline. Executable preprocessing, output writers, and dynamic
producer arguments are rejected. Provider work shares a 200 ms budget within the
500 ms pre-call capture hold. A failed, unavailable, or timed-out provider degrades
to an open observation with its reason retained in history, never a host-call error.

Each completed observed call receives a short session-scoped change ID. Root
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

Go tests, generators, and fixers snapshot bounded existing contents of explicitly named
local package directories before execution, including subdirectories but excluding
built-in dependency and VCS directories. Root files take priority over descendants.
Snapshots share the existing file, byte, enumeration, and capture-time limits; they
are baseline hints, not a claim that the program cannot write elsewhere. An
existing managed package or formatter path enumerated but not baselined after a capture bound is checked
against the observation clock after the call, including when broad sweep ignore
rules hide it. An unchanged hint does not create a review file or incomplete
count. A changed or deleted hint has unknown before-content and partial
coverage, not an invented diff. If the file clock is incomparable, the
named scope remains incomplete instead of claiming it was unchanged. Unknown
flags stop package operand extraction, and test arguments after `-args` are not
packages. Import paths are not resolved by executing Go. The normal sweep still
reports new or outside-scope paths without inventing a baseline. Binary snapshots
retain size/hash evidence rather than text line counts.

Completed observations may supply a process-local last-seen content cache, bounded
to 64 MiB and 4,096 entries. Sweep diffs from that cache say `since last observed
(change ID)` and remain partial with unknown counts. They are not call baselines.
Deletion evicts content; restart loses the cache without losing durable records.

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
`--list` uses short agent-facing status and coverage labels instead of the
command transcript. It compresses consecutive comparable complete IDs and
shows known direct `+N -N` counts. Partial or unswept IDs remain separate;
`?` marks unknown direct counts; `shared` retains overlapping-writer
attribution, while `managed:N` counts tool-managed review
files excluded from the default diff. Pending and retired IDs remain visible;
sibling threads' IDs are not exposed. Explicit IDs can still be read across
agents in the shared namespace. The default view shows each status and unified
file diff. `--summary` gives added and removed line counts by path across
selected records, shortening paths inside the selected workspace. By default,
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
inconsistent, incomplete, unconfirmed, partial-coverage, or binary capture chains
fail rather than claim a complete net diff; ordinary reads preserve that evidence. Path filters apply to the composed files. An empty composition is explicit.
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
Completed patch observations without application confirmation display `changes
observed`, or `no changes observed` when no differences were captured. Incomplete
file evidence displays `observation incomplete`; its per-file reason remains
visible. Confirmed success, no-op, and rejection retain their outcome labels.
These are observation labels,
not success claims. Only unfinished work is `pending`. The live diff uses the same
labels. Confirmation limitations belong in `--history`, not a pending-looking
headline. For a Code Mode cell, an observed workspace effect remains application
unconfirmed because outer JavaScript completion does not prove the nested
`apply_patch` result. Direct stock patch success requires both its successful
host result and a complete workspace observation.

A confirmed successful edit can publish a generated `Create` or `Edit`
commentary summary. A completed Code Mode cell with a complete observed
workspace effect publishes the same summary for that observed effect; it
remains application unconfirmed in the retained evidence. Classification uses the same
observed review files as `mchanges`, including whether a path existed before
the edit. Each summary carries a bounded copy of the observed hunks. Failed,
unchanged, and unfinished calls do not publish a summary. These messages are
user-only presentation, not a tool result or application receipt.

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

When an interactive Herdr pane is available, Mekugi opens the viewer on the
first observed editing or execution call. The stream view shows concurrent
main-agent and child calls, and can display provisional `apply_patch` and
stock `cat` heredoc diffs before completion. Literal `cp`, `mv`, `rm`, and
`tee` heredoc commands are predicted from current file contents. Interpreter programs can be
shown in their own language rather than as a shell wrapper. A preview does
not claim that Codex ran or accepted an edit. A Code Mode patch held in an
immutable top-level literal binding is rendered as the patch preview; its
escaped JavaScript source is not exposed as a streaming script while the
patch is incomplete.
Literal Python `Path.write_text` and `open(..., "w").write` bodies and literal
JavaScript `writeFileSync`/`writeFile` bodies can be predicted without evaluation.
Python same-path `read_text().replace(A, B[, count])` supports literal replacements;
regex replacement is excluded. Unsupported expressions remain source previews.
Scope cards list pending VCS restore, deletion, or switch targets when their
targets are known. Ordinary command watches remain hidden until a captured
file changes. A `may write` footer distinguishes scoped paths from unresolved
targets on visible cards.

While a writer window is open, display-only polling runs about every 500 ms over
captured paths, reading content only after a stat change. Polling is bounded by
path, time, and content budgets; it never runs a workspace sweep or provider query.
Changed-file cards say `RUNNING · observed so far` and disappear if the files
return to their captured state. Terminal results, background transition, or
viewer shutdown remove their live preview. Running previews and predictions
never become durable evidence, and replay does not restart polling.

Query-based Mercurial scoping and literal `sed`/`perl` substitution prediction are
outside this delivery. Model-visible command-change notices are also deferred;
stock result bytes remain unchanged.
Patch previews show projected source changes with the affected file's language
highlighting, not the `apply_patch` instruction envelope. If source matching
cannot establish that projection, the viewer must not fabricate a diff.
In the live input stream, literal `tools.exec_command` command strings inside
Code Mode are displayed while the JavaScript wrapper is still arriving.
Numbered `# tools.exec_command N` headers separate distinct tool calls; line
breaks within one command remain inside its header. Shell commands use Bash
colors, while literal interpreter `-c`, `-e`, and heredoc bodies use their own
language colors. This provisional display
never changes Codex's original tool input or asserts that the command ran.

Provider input arrives in bursts. The stream view reveals each call's received
input at its recent arrival rate, so the preview grows steadily rather than
jumping per burst; the reveal trails received input by at most a bounded
window and completes on the call's final input. The reveal advances by whole
units: edit previews by line, and command and script text by shell segment
(`&&`, `||`, `|`, `;`, or a line break). An unfinished unit stays buffered
until it completes, or is shown as it streams after about half a second.
Newly revealed rows fade in, and rows revealed together cascade in order;
the fade is display-only and never delays the underlying projection. The
first usable frame and completion redraw immediately; intermediate deltas may
be coalesced. Card
positions stay stable: a
new call takes over a finished card's slot, preferring its own caller's, and a
finished card leaves after a brief idle hold, even when no other call starts.
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
mouse scrolling targets the region under the pointer. File selection restores
its saved diff position, and hunk navigation uses rendered hunk boundaries.
Incoming updates retain the paused file, navigator cursor, and top-row identity
where those entries still exist. Resize keeps the logical diff anchor and the
focused navigator entry visible. File status, known line counts, folder file
counts, and recent-update marks remain distinct from capture confirmation;
unknown counts are not presented as zero. Help is available without permanently
occupying code rows. These controls change no execution or durable evidence.

Subagent activity uses a separate Mekugi agents pane, specified in
[REQ-COMMENTARY-001](commentary.md). Whichever pane opens first goes beside the
caller. The second joins it: in a tab at least 240 columns wide it gets its own
full-height column, and in a narrower tab it is stacked below the first. Either
way, the diff pane keeps 55% of the shared space. If the tab width is unavailable,
the panes stack. A failed or closed pane leaves the other one open.

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
   status from the outcome table, reviewable effects, and exact coverage. A
   nonzero exit never shows `completed` or publishes a receipt.
8. A yielded command is finalized only by its terminal continuation result.
   Replaying the same input neither re-reads the workspace nor allocates a
   second change ID.
9. Moves, copies, binary content, symlinks, and bounded or unreadable paths
   are represented without inventing rows. A link is reviewed by its target
   name; only a write through it reads the file it points to.
10. A destination that an earlier statement creates, removes, or fills is
    captured under every reading, and a `cd` whose failure would not stop later
    statements leaves their relative operands undeclared.
