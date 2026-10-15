# Applied changes and live view

`mchanges` is the authoritative retained account of agent changes for users and
agent handoffs. It reports confirmed evidence and its limits, not the live Git
working tree or a claim that unobserved effects did not occur.

## REQ-CHANGES-001 — Durable review of stock edits and command effects

Mekugi observes stock Codex `apply_patch` calls. Codex executes each call once;
Mekugi does not replace the tool, run a hook, apply a second patch, or alter the
argument or host result. The
[duplicate-output projection](execution.md#duplicate-output-projection) may
reference repeated model-visible output only after observation and retention
have consumed the original text. A complete argument may be projected as a
provisional live diff while streaming. Only the actual host result and resulting workspace
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
complete evidence and no file differences remain in command history without a new change ID.
An unfinished call has no completed record. Storage failure must not expose
dependent review evidence as durable.

Completed host results first visible in a compaction request are reconciled before
provider forwarding can discard that input. This
includes stock patches and exec-observed writes. Observation
does not project replay carriers or edit notices into the forwarded compaction
payload. Missing workspace metadata may use only the unique retained workspace
owned by that thread; ambiguity cannot establish capture scope.

New records retain bounded file evidence inline. Workspace snapshots (see
Command effects) are private comparison state, not record evidence: Mekugi
produces no dependency fingerprints, Git baseline reconstructions, or shared
filesystem-snapshot objects. Historical snapshot-backed records remain
readable and retain their storage dependencies until their last owner expires.
Missing or corrupt historical evidence remains unavailable, never an empty diff.

New text captures retain up to ten unchanged lines on each side of a change for
historical Edit dialogs and individual-record reads. Nearby changes share their
context; file boundaries limit it. Older records and completed host-only diffs
show only their retained rows, never context borrowed from the current workspace
or another invocation. Composed net views and apply/revert hunk grouping remain
compact, so wider review context does not join independently replayable changes.

Patches inside one `exec` cell share a pre-cell/post-cell observation window.
Literal complete inputs name their baselines before execution. Host tracing
confirms individual outcomes and repeated literal call occurrences, without
evaluating JavaScript or learning a before-state after execution. A cell whose
command input is not literal is bounded by a workspace snapshot like any other
writer command; dynamic patch inputs that cannot be resolved from source before
dispatch are outside recorded coverage, not confirmed no-ops. Agents keep their
ordinary tools and workflows.
Moves whose endpoints overlap another patch are retained as separate endpoint
differences rather than inferred intermediate renames. Completed patches finalize
independently of sibling command processes that remain running.

### Command effects

Commands submitted through native Shell Mode (`!`) use the same change recorder.
Completed file effects update the saved Diff totals, pane, and Changes history,
including `mchanges revert` and `apply`. Submission acknowledgements and replayed
command history do not create captures; the matching terminal host item completes
the observation. Codex retains execution, output, and interruption ownership.

Mekugi records known edit sources in literal stock `tools.exec_command`
calls. These include literal text/file redirections (`cat`, `printf`,
`echo`, `tee`), coreutils file operations including `mv` and `rm`, in-place `sed`
and `perl`, formatters with individually named file targets, supported
source-derived Python and JavaScript writes, local VCS
operand edits, and `mchanges apply`/`revert`. Shell and interpreter parsing is
read-only: it derives edit operands without executing the program. Recognized
RTK wrappers and literal inline shells preserve the underlying source boundary.
Commands use their explicit shell or the request's session shell, never an
assumed shell. Relative paths require a known absolute working directory.

Paths named by an edit source receive pre-edit evidence. Git ignore status,
filename suffixes, directory names, and generated-file banners are not admission
rules for them. An agent's explicit edit to an ignored `FIXME.md` is recorded, as
is an explicitly authored fixture. An input script is not itself an edit target.
Recursive file operations may name trees.

Tracked Bash and `sh` commands also report paths that `mktemp` created, as
described in [execution](execution.md). Capture compares only each reported path
and its contents; it does not scan the temporary parent directory. Creation and
later deletion use ordinary retained changes, so their net diff cancels. Dry-run,
help, version, and failed invocations name nothing on disk and establish no
creation.

Effects the parsers cannot name are bounded by workspace snapshots, so agents
need not route edits through `apply_patch` to have them recorded. Every command
that is not a recognized read-only reader takes a checkpoint of the selected
workspace before it is forwarded, and its terminal result is compared with a
fresh checkpoint. Changed paths that no edit source named join the call's record
with the command's program as their source. Snapshots live in a private Git
directory and index under the replay store. They never write the user's
repository state or run its hooks or filters. User configuration is read only
to locate the global excludes file and is never applied to snapshot commands.
Snapshots record the bytes on disk: attributes that convert line endings,
encodings, or content through filters do not apply.

- Inside a Git repository the snapshot follows the repository's ignore rules,
  including `info/exclude` and the user's global excludes file, borrows its
  objects, and starts from a copy of its index. Ignored paths such as an ignored
  `node_modules/` are therefore never recorded unless an edit source names them;
  unignored installation output is recorded like any other change. An older
  private checkpoint cannot turn a now-ignored, untracked path into a
  recorded deletion. Files tracked by the repository remain eligible even when
  an ignore pattern matches them.
  Only the selected workspace below the repository's top level is compared. Other VCS
  metadata directories and the replay store are always excluded.
- Without a Git repository there are no authoritative ignore rules. A workspace
  of more than 20000 files takes no snapshot. Later checkpoints admit new files
  only from directories that gained at most 256 of them, whether the directory
  is new or already known, so an installation tree does not become an agent
  edit.

A checkpoint that is not ready within one second is skipped for that call, which
keeps only its source-named evidence; a slow first snapshot continues in the
background for later calls, and a failed one is retried after 30 seconds.
Submodule entries are not compared. A snapshot that cannot be compared is
reported in the call's scope diagnostic, and the call's coverage is partial
rather than exact. A call whose observation window was not seen by this router
process, such as one finishing after a restart, takes no snapshot comparison.

Snapshot state is bounded separately from record retention. A workspace's
private object store that grows past 512 MiB starts over, and a workspace no
router process has used for 30 days loses its private state, which its next
snapshot rebuilds; a call whose checkpoint was lost this way keeps only its
named evidence.

Capture holds and content reads remain bounded, for named operands and snapshot
comparisons alike. An unreadable target produces incomplete evidence, not
invented bytes. Unknown scope alone never allocates a change ID, change notice,
or reviewer handoff.
Complete named comparisons with no file differences also allocate no ID.
Temporary writes restored before return have no recoverable intermediate diff;
the command diagnostic retains repeated-write uncertainty without advertising
an empty change. Recovering intermediate bytes would require host per-write
facts; Mekugi does not instrument or rerun the user's process.

After a terminal host result, the recorder compares only the named targets and
explicit recursive destinations. A nonzero exit does not undo bytes already
written. Missing native outcomes do not invent exit codes. Yielded commands wait
for their terminal continuation; replay does not revive processes. Binary content
is retained as sizes and hashes and symlinks as link targets. Identical nonempty
deletions/additions may form a move, and a created copy may name its source.

Before/after evidence is not per-write attribution. A concurrent writer, hook,
or user edit changing a file while a command runs cannot be separated from that
command without host per-write facts. When calls overlap, the first to finish
records the snapshot changes it observed, and calls still running compare only
later changes to those paths, so one effect is recorded once and records
compose. Sibling calls may share one record and retain overlap diagnostics.
A command still running when a later turn begins takes no snapshot comparison,
because its window spans unrelated work.

Each completed known edit with changed or incomplete file evidence receives a
short durable change ID. Complete no-effect attempts and scope-only diagnostics
remain in call history without an ID. Historical observation-only and tool-managed
records remain available through explicit `--history`, but are absent from
authored diffs, counts, notices, saved Diff, and child handoffs. Admission uses
retained provenance, not file classification.

VCS commands that import, discard, or write out repository content retain their
effects as diagnostic history, not agent-authored changes. These effects are
excluded from authored counts, diffs, notices, saved Diff, live diff, and child
handoffs. This includes redirected or piped repository output.
Command observation uses the shared VCS classification to distinguish possible
writers from read-only commands. In a window containing both a VCS writer and an
interpreter edit, source-named interpreter targets remain authored; unnamed
effects cannot be separated from the VCS operation and remain diagnostic.
Recognized read-only VCS siblings do not exclude unnamed interpreter changes. New captures
persist this provenance for restart; existing retained provenance is not rewritten.

Root and child agents share their inherited namespace; forks and side threads
receive isolated visible records, and resume reads retained evidence after a
fresh router process. Allocation survives restart and never reuses another
workspace's IDs. Retention belongs to [REQ-ROUTER-001](router.md); replay never
repeats a host edit.

### Bounded reads, revert, and apply

The [change-history module](../../modules/changehistory/SPEC.md) owns retained
change selection, bounded reads, recovery, and apply/revert behavior. Host
observation still owns capture of those commands' effects; a recovery continuation
never repeats a mutation.

### Edit receipts

Completed file changes can publish a generated `Create` or `Edit` commentary
summary, including changes left by a command that exits nonzero. Classification
uses the same captured review files as `mchanges`, including whether a path
existed before the edit. Each summary carries a bounded copy of the captured
hunks. Unchanged and unfinished calls publish no edit summary. Move summaries
show both endpoints of moves, compressing shared directory prefixes as
`internal/router/{old.go=>new.go}`. These messages are user-only presentation,
not substituted tool results. After persistence, the
agent-visible completed tool response also receives a separate text part with
the change ID and `mchanges ID --summary` statistics. This bounded notice does
not replace the original host result or alter the user-facing edit display.
The separate duplicate-output projection may reference repeated host text in
model input; it leaves the appended change notice intact.
No-effect and unfinished calls receive no notice; partial edits report only
retained evidence. Continuations receive the notice when they finish the edit.

### Live terminal view

In an interactive terminal, Mekugi opens its integrated viewer on the
first streamable edit with a producible source hunk. The stream view shows concurrent
main-agent and child calls. It streams edits only: provisional `apply_patch`
diffs and recognized shell content writes, before completion. Stock `cat`
heredoc redirections are always streamed, including from a nested
`tools.exec_command` call whose arguments are
still arriving. Literal `tee` heredoc writes also stream. Copies, removals,
moves, formatter-only writes, and generic command effects do not open live source
cards: they have no arriving source content. Their completed evidence remains in
Activity and the saved diff. Independent source writes in the same call still
stream. A preview does not claim that Codex ran
or accepted an edit. A nested patch held in an immutable top-level literal
binding is rendered as the patch preview.
Literal Python `Path.write_text` and `open(..., "w").write` bodies and literal
JavaScript `writeFileSync`/`writeFile` bodies can be predicted without evaluation.
Python reads through `read_text()`, `open(path).read()`, and `with open(...)`
or `Path.open()` handles, with or without an `encoding` argument, support
`replace(A, B[, count])` with bounded known-string expressions for both operands.
Text reads normalize CRLF and CR by default; explicit literal `newline`
settings preserve input and control LF translation on writes.
Reads through one handle advance to EOF; separate opens start fresh.
Text-buffer assignments, chained replacements, and writes through `with`
handles are also supported. Known buffers support codepoint-based indexing,
slices, `len`, `index`/`find` and their reverse variants, `count`, concatenation,
bounded repetition, and string-buffer `+=`. Trimming, ASCII case conversion,
`split`/`splitlines`, and `join` can compose these values. Unicode case conversion
is not predicted. Synchronous `for` loops over bounded known string lists or
tuples, including named sequences, split-derived sequences, and flat
replacement-pair unpacking, follow each iteration in order. Literal string
dictionaries preserve insertion order and support key lookup and
`items`/`keys`/`values` iteration. Collections can be passed to helpers, but
collection mutation and computed dictionary entries are not predicted. Nested
loops share a 256-iteration bound; dynamic iteration, loop `else`, and
`break`/`continue` are not predicted. Statements are followed in order: a
reassigned path variable retargets only later statements, and a file written
earlier in the script is read back as its predicted content, so several edits
of one file compose. Top-level helper functions with plain and default
parameters are inlined at each call, with Python's local scoping and `global`
names. Supported defaults capture their values when the function is defined.
Writes through one open handle append in order; reopening in `w` mode truncates,
including empty contexts. Closed handles and concurrent handles on one file
where either is a writer are not predicted. Guards that only stop the script, such as
`if old not in text: raise SystemExit(...)`, `assert`, and `print`, do not
prevent a prediction. A Python edit script composed through a `cat` heredoc
previews these target-file changes rather than the script file. Reads and
in-memory string transformations do not establish edit intent: target-file
previews wait for an explicit write operation, including a writable `open`
mode. A completed read-only script previews its own source when created,
not changes to the files it reads.
This is a provisional prediction only: script creation does not claim that its
edits ran, and neither the script nor the host tool call is rewritten or
executed by the preview.
Before edit intent arrives, an unfinished Python heredoc has no source-file
preview while it holds only setup: imports, docstrings, `sys.path` calls, and
literal or path assignments. Once any other statement arrives, it streams as
ordinary Python source.
Unsupported buffer mutations, including other augmented operators and tuple rebinding,
are not predicted.
Regex replacement is excluded. Literal target content can appear before the
transporting interpreter statement or string closes. Only complete decoded
target lines and supported-language statement boundaries are revealed, including
separate statements sharing a source line and nested escaped target newlines;
delimiters inside target strings do not release unfinished statements. Only the
write still arriving is gated, at the arriving text's position in its projected
file, so replacement text that closes an enclosing block is judged in context.
Consecutive target units remain visibly distinct: a later unit does not start
its reveal while the preceding unit is still fading in. When input arrives
faster than that, one reveal covers several units, so the preview keeps pace
with arrival instead of releasing a backlog at completion.
An interpreter prediction is expressed as a stock patch and projected exactly as
a streaming `apply_patch` input, so both share gating, rendering, and
completion. While replacement text arrives, the patch ends at the arriving
text: removed rows of the region it replaces precede its arriving added rows.
Once that text closes, the preview uses normal minimal review hunks. Completed
captures retain the wider historical context described above. Because patch lines
carry no carriage returns or missing final newlines, the preview omits both; a
change only to a file's final newline shows no rows. A target path that a patch
header cannot express has no preview.
Command and script text is never displayed. A command that is not a
recognized edit has no card, and a later non-edit call keeps the last
displayed edit. A literal `workdir` resolves relative targets, as does a literal
`cd` step before the edit, including in an `a && b` chain, whose steps are
predicted as if each succeeds. Literal variable assignments, `set`, `echo`, and
`true` steps do not prevent a prediction. An edit with a computed `workdir`
has no target-file preview because its target cannot be resolved.
Recognized VCS effects have no pending or running live-diff card. Authored
targets in mixed calls remain visible, using the same provenance admission as
authored counts. Ordinary command watches do not open live source cards; only
recognized streamable content writes are eligible. Sockets and other non-file targets,
metadata-only changes, and unavailable or binary content do not open live cards;
their captured evidence remains available in Activity and saved Diff.
A `may write` footer distinguishes captured paths from additional
unknown write targets and stays anchored to the bottom of its card. Unknown
targets describe incomplete command-wide coverage, not a failure to resolve the
displayed file; for example, a literal edit followed by arbitrary test code
retains its known edit target without claiming to know every test side effect.

Completed shell segments also trigger an observed Activity receipt, independently
of live-card prediction. Arbitrary interpreter code and computed filenames use
the captured paths and private workspace checkpoint, with the same ignore,
content and isolation limits as terminal capture. A following long-running
command does not delay these rows. These intermediate observations neither claim
durable changes nor complete the host invocation. Live-card admission remains
limited to streamable source content.

While a writer window is open, display-only polling runs about every 500 ms over
captured paths, reading content only after a stat change. Source-card polling is bounded by
path, time, and content budgets; it never runs a workspace sweep or provider query.
Completed-boundary receipts can compare the existing private workspace checkpoint
for computed targets.
Changed-file cards say `observed so far` and disappear if the files
return to their captured state. When a live segment report uniquely matches the
writer's thread, turn and exact script, and the host invocation started after
the writer window opened, the card finishes after all recognized
streamable edit segments end or are skipped, independently
of a following test or other non-edit segment. A uniquely matched native Bash or
`sh` command completion also finishes its card when no segment report exists,
including commands with supported literal `env` prefixes, without waiting for
later commands in the same `exec` cell. Its final frame uses observed
files, not predicted content; later edit segments keep it open. Ambiguous
concurrent matches cannot retire each other's cards. Invocation-local guard
instrumentation does not prevent matching; another host item's identity cannot
supply completion. Without tracking, a literal edit's card finishes when its
captured targets match the fully projected edit. The completed card says
`observed`; it does not claim that the shell command or tests succeeded. Matching
uses the pre-call baseline, never a fresh baseline read after execution.
Untracked streamable edits still require terminal results, background
transition, native turn completion, or viewer shutdown to remove their live
preview when their observed files do not match the projection. Interruption retires only that thread and turn's previews and polling;
late frames and viewer resubscription cannot revive them. It neither cancels
other agents' previews nor finalizes an unfinished host call. Running previews
and predictions never become durable evidence, and replay does not restart
polling.

Query-based Mercurial scoping and literal `sed`/`perl` substitution prediction are
outside this delivery. Original stock result content remains unchanged alongside
the agent-visible change notice, except for the model-visible
duplicate-output projection. Retained evidence and the user-facing display
keep the full result.
Patch previews show projected source changes with the affected file's language
highlighting, not the `apply_patch` instruction envelope. If source matching
cannot establish that projection, the viewer must not fabricate a diff. A
completed unprojectable patch clears any earlier provisional card and leaves
failure reporting to the host tool result. While a later patch has no complete
change yet, its pending marker keeps the preceding projected diff visible
instead of blanking the stream; the next established projection replaces it.
This provisional display never changes Codex's original tool input or asserts
that the command ran.

A card header names the caller, then states the call with a roster glyph
rather than a word: `◐` while the call is arriving or running, `✓` once it
completes. An unprojectable edit opens no card. The
current file follows, styled like file navigation: its status and live `+N -N` line counts, with `N/M files` when the call edits
several. Deleted files do not open live source cards or count toward live
batch slots. A deletion-only call leaves the transcript visible. Deleted-file
evidence remains available in Activity and the saved diff.

The live batch header names the editing tool for its selected edit before the
file count, such as `apply_patch`, `python`, or `cat`, including edits nested
in `exec`. The enclosing executor is not the editing tool. Unknown editing
tool identity is omitted. Following a new edit or pinning another edit switches
the header to that edit's tool, not a sibling's.

The integrated `Diff preview` pane opens on its first producible source hunk, not on
an execution request, patch header, or status without a hunk. Agent activity can open
independently without reserving an empty live-input area. Explicitly focusing
the diff pane still opens it on demand.

Native transcript-replacing live docks defer a new caller's first reveal by
300 ms. Completion or withdrawal before reveal does not open a dock; captured
changes remain available through Activity and saved diff. Once visible, the
caller's burst updates without another reveal delay. Each completed call retires
after its own 1.5-second settling interval; running siblings and newer edits do
not retain its files. A pinned surviving file keeps its selection; if the selected
call expires, the dock resumes following the newest remaining file.

The router publishes to one in-process UI mailbox without waiting for rendering.
Replaceable previews retain only the latest frame per call; a mailbox that falls
behind resynchronizes from durable changes and retained display state.

Provider input arrives in bursts. The stream view reveals each call's received
input at its recent arrival rate, so the preview grows steadily rather than
jumping per burst; the reveal trails received input by at most a bounded
window where a complete line is available and completes on the call's final
input. Streaming frames advance one complete line per elapsed frame rather
than draining all line boundaries in a provider burst. A reveal held between
target units counts the frames it waited, so it can cover several lines; idle
time before a burst does not count. Bounded lag catch-up may skip ahead. After
the call finishes, queued units keep distinct reveals for about one second; the
remaining final input then appears at once. Shell/JavaScript input
that finishes before any target diff was published skips this catch-up and
publishes its final projection without manufacturing an active stream.
The reveal advances by whole decoded lines. An unfinished line stays
buffered, regardless of elapsed time; final input releases an unterminated tail.
Code-source previews also buffer unfinished syntax for Go, JavaScript, TypeScript,
Python, and Bash/sh. Complete statements inside open Go, JavaScript/TypeScript, Python, and shell
blocks can appear without waiting for the enclosing block to close; complete
JavaScript/TypeScript class and interface members count as statements. Open
object, array, and call literals remain one unfinished statement. A frame
with an unfinished statement retains the previous projection;
unknown formats use complete lines. Completed input bypasses this display gate,
including malformed source, without claiming execution success. Empty add-file
and heredoc headers retain the preceding diff until source arrives or empty-file
creation completes. Newly revealed changed rows fade their text in from partial
visibility over a settled row fill, and rows revealed together cascade in order;
context rows, line numbers, and diff markers appear settled. The fade is
display-only and never delays the underlying projection. A completed snapshot displays at its final colors
without re-fading retained rows. The first usable frame and completion redraw
immediately; intermediate deltas may
be coalesced. Card
positions stay stable: a
new call takes over a finished card's slot, preferring its own caller's. The
last displayed input stays visible while waiting for another call.
The line-number column of a card never narrows while its call streams.

The saved diff view uses completed known-edit patch and command evidence. It includes
changes from children that are visible to the parent. The viewer switches
to it after root completion, and back to stream for the
next prompt. The user can switch or browse without changing execution or durable evidence. Each mode's
footer reports what the other holds: live calls from the diff view, and
captured files from the stream view. A child caller's card label uses the
same color as that agent in the agents pane.

Replacement blocks emphasize removed and inserted words with stronger red and
green backgrounds, like git-delta, while preserving source syntax colors.
Unchanged words keep the subtle changed-row fill. Comparisons span consecutive
replacement rows so line reflow does not mark all retained words as changed.
Pure additions and removals keep their row fill. Large replacement blocks may
omit word emphasis to keep rendering bounded. This decoration changes no source
text, line coordinates, selection, or retained evidence. Source previews use
the same decoration for the replacement rows available in their visible window.

The saved diff navigator is a presentation index over captured files, not a
reordering of capture history. Wide panes show a persistent, collapsible left
dock with colored status and inline added/removed counts. `s` shows and focuses
the file/Changes navigator; pressing it again hides the navigator. Narrow
standalone panes use a full-width picker so code retains its reading width.
Narrow native panes stack the list above the diff; focusing the list enlarges
it without covering the diff.
The diff pane header omits the redundant file/path/row count; section headings
within the diff still identify each file.
Tree and flat choices remain stable for the viewer lifetime. Filtering shows
matching relative paths, including descendants of collapsed folders; clearing
it restores the tree's expansion and navigation position. Files use stable
path ordering, with folders first in tree mode. Next/previous file navigation
uses the matching set, revealing destinations inside collapsed folders.

The navigator and diff have independent viewports and keyboard focus. Arrow keys
act on the focused region. Keyboard focus is visible;
the active list row is shaded in place rather than marked by a separate arrow
column, leaving that cell available for file names and graph rows. Moving the
cursor onto a file, or onto a change (its first file), shows that file while
the list keeps focus; Enter opens it and focuses the diff. The row under the
pointer is underlined, apart from its tree or graph lanes. Clicking a list row
selects it while keeping list focus; clicking diff content focuses the diff. After Enter
opens a file, Esc returns to the list; after Enter on a branch sets its caller
filter, Esc restores the previous filter. Otherwise Esc closes help or the
filter, then leaves the list.
Mouse scrolling targets the region under the pointer. Opening a file from the
navigator or a change starts at its section heading. Next/previous file
navigation restores a position only where the reader stopped before jumping
away; scrolling past a file keeps none. Without a title row, the open file's
heading stays pinned while its content scrolls. Hunk navigation uses rendered
hunk boundaries and, like a file jump, keeps the position of a file it leaves.
Each change record keeps the canonical path of the agent that issued it and
the tool or program that wrote it (`apply_patch`, or the observed program such
as `sed` or `python3`). The navigator's Changes tab lists captured changes in
capture order as a graph with one lane per caller, branching from `main` at the
caller's first change; each row shows the change ID, source, file count, known
line counts. A single-file change always shows its file: nested below it
beside the diff, or in place of the count when the list is stacked with the
diff or covers it, where it has no file rows to expand. Missing line counts use `?`, not a command-failure glyph. A file's
section heading lists the changes it composes. Every saved authored capture participates
in composition regardless of command exit status. If retained contents cannot
form one coherent diff, the pane shows the individual captured edits with their
IDs instead of hiding them behind a composition error or inventing a net diff.
The saved Diff title reports unavailable line totals for such a fallback, or for
binary or incomplete history, rather than summing separate edits as a net outcome.
Next/previous change navigation
opens each file of each change in that order. A caller filter shows one
caller's changes: other callers' captures compose as baseline, so the
shown diff remains the exact net effect of that caller's edits, and the heading
counts the changes folded into it. Agents appear by display name: `main` for the root,
otherwise the path below it. Records from before attribution show an unknown
caller, which filters like any other. File rows in the tree, flat list, Changes
tab, and streaming title share one format: a colored git-style status, one
space, then the name. The status is `A`, `D`, `M`, `R` for a rename with no
content change, or `RM` for a rename with captured edits. `?` marks incomplete
evidence without claiming a confirmed modification; a rename
names its source as `old → new`. `UU` marks a net diff that still adds
`mchanges` revert or apply conflict markers, and clears once they are resolved.
Incoming updates refresh content while retaining the chosen file, navigator cursor, and top-row identity
where those entries still exist. Resize keeps the logical diff anchor and the
focused navigator entry visible. File status, known line counts, folder file
counts, and recent-update marks remain distinct from capture confirmation;
unknown counts are not presented as zero. Help is available without permanently
occupying code rows. These controls change no execution or durable evidence.

Mekugi owns the native terminal composition, while Codex app-server remains the
execution authority. The [UI contract](native_ui.md)
owns pane layout and input: Main on the left, Diff or Activity on the right,
and the child Agents roster below. `Ctrl-B` followed by `1`, `2`, `3`, or `4`
focuses those panes. Mouse dragging and prefix shortcuts resize panes and the
file navigator without changing evidence.

Diff and agents scrolling use one contract: arrows or `j`/`k` move one line,
PageUp/PageDown or `b`/Space move one page, Home/End or `g`/`G` go to the
beginning/end. The saved Diff has no follow mode or resume-follow shortcut. The wheel scrolls
diff and activity by three lines per event without changing keyboard focus.
Consecutive wheel events accumulate even before the next rendered frame. Stream cards retain
their source windows during manual scrolling and resume their live tips with `r`.
`Ctrl-C` in an auxiliary pane returns focus to Main. Bracketed paste enters the
native composer and cannot activate layout shortcuts. Main history remains
accessible through prefix PageUp/PageDown and the mouse wheel. Interactive
launches inside Herdr expose the invocation-local `HERDR_AGENT=codex` hint before
router startup, without Herdr commands or pane-management APIs.


The wrapper starts no pane processes and requires no external pane manager.
The UI consumes the bounded event hub directly; there are no live-view HTTP
endpoints or connection files in the running application. Terminal mode is
restored after child exit, cancellation, or rendering failure; all owned I/O is
joined. Redirected sessions retain stock input/output and inline activity.

Acceptance:

1. Nested stock patch calls retain exact arguments and host results; only eligible
   model-visible text may use the duplicate-output projection.
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
6. Live `cat`, `tee`, patch, and interpreter projections are presentation
   only and preserve stock PTY, yield, host result, and `write_stdin` behavior.
   The separate duplicate-output projection changes only eligible model input.
7. A literal nested command produces a record with the
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
