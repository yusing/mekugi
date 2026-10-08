# Observed tool activity

## REQ-ACTIVITY-001 — Tool classification and observation

The [UI](native_ui.md) consumes app-server events for live activity and
history. These shared classification rules describe operations, not generated
conversation messages. [Activity presentation](activity_display.md) owns row
layout; [session notices](notices.md) own actionable diagnostics. Journals own
[authored progress](journal.md). No activity observer generates conversation commentary.

### Native event authority

Codex app-server supplies command, edit, collaboration, web, image, and other tool
items for Main and child Activity. Completed host items and turn events establish
execution state. The router does not regenerate operation messages from provider
Responses calls, interpret arbitrary JavaScript output as session evidence, or
reconstruct a parallel activity lifecycle from request history.

Nested JavaScript tool activity follows the host's event stream. Independent
`Promise.allSettled` calls remain parallel; presentation cannot serialize their
execution. Static source recognition used for [execution previews](execution.md)
and [change capture](changes.md) remains separate from native activity delivery.
The host emits no item for an `exec` cell itself. When a new cell result carries
the host's `Script failed` header, the router adds one error row to that agent's
activity with a bounded first-line preview of the host's trailing script error,
so a script that fails before any nested tool call still shows. The complete error
is retained separately for the shared content dialog. The result reaches the model
unchanged, and failures from earlier requests are not shown again. Resumed
history restores these rows from the retained rollout under
[UI resume](native_ui.md).
When a finished cell's result carries nothing beyond that header (and a failure's
script error), the model saw none of its nested results. Each nested
`exec_command` row with output then adds a muted `output not returned to the model`
note, identified through the native trace. A yielded cell, or one that printed
anything, adds no note. These notes last for the router's lifetime; restored
history does not show them.

### Shell command labels

A simple `cat`, valid `mcat` read, bounded `sed -n` print, `nl -ba FILE`, or
single-file `cat FILE | sed -n RANGES` or `nl -ba FILE | sed -n RANGES`
selection is labeled `Read`; literal `rg` is `Search`;
simple listings are `List`; and `inspect_file`, including its options and multiple
paths, is `Inspect`. Path globs and simple shell parameter references such as
`$HOME` and `${HOME}` are preserved as source without expansion; parameter
operators and executable substitutions normally remain `Run`. Bounded `sed -n`
reads are classified by their literal flags and print program, regardless of how
the input path is constructed. Dynamic operands remain visible verbatim and are
never evaluated by the display; `Read` describes the outer sed operation, not
the effects of any embedded substitutions. In-place edits and non-print scripts
do not qualify as reads.
A `cat` read piped to a bounded `head` retains its read label. `skills-mgr get` and `skills-mgr run` use `Skill`, including reference reads.
The skill name is bold; reference paths and run arguments retain normal weight.
`&&` chains show classified operations and unclassified `Run` neighbors in source
order, without claiming execution or success. Other unsupported compound commands
retain a `Run` preview instead of claiming a simpler operation. Mixed command scripts keep
every classified operation and show unclassified neighbors as `Run` in order.
`rg` and `grep` search previews show the query and target, omitting execution
flags and recognized output-only pipeline helpers such as `head`. `ls` listings
show only their path operands (`.` when none), omitting options and their values;
an abbreviated long option that may consume the next word remains `Run`. Discarded
stderr (`2>/dev/null`) is transparent to `find`, `rg`, `grep`, and `ls`. Unknown or
effectful pipeline stages retain the original `Run` preview. Shell execution
still receives the exact original command, including flags and pipes.
A plain literal `timeout DURATION` prefix preserves the operation label. The duration
may be a decimal number with an optional `s`, `m`, `h`, or `d` unit; timeout
options and dynamic durations remain `Run`. Execution keeps the original prefix.
Per-command `Run` excerpts omit statement-terminating semicolons; quoted
semicolons and other executable syntax remain visible.
Literal `printf` and `echo` section headings alongside other operations are
omitted as display decoration, including colon-ended labels and bordered headings.
Bare `printf` titles framed by leading and trailing newlines also qualify when
they are capitalized, multi-word text or an uppercase single-word section label,
containing only letters, spaces, hyphens, and slashes, without format arguments.
Newline-terminated literal `printf`
confirmations ending in `checks passed.` or `evidence remains available.` also
qualify when their capitalized text otherwise contains only letters, spaces,
hyphens, and slashes. Other unframed text, values, and data-bearing formats
remain visible.
Live and successful tracked heading segments are omitted too; failed ones remain
visible. Standalone, redirected, escaped, or dynamic headings remain `Run` operations.
Literal `mread` recovery calls omit activity entries rather than appearing as `Run`.
When the host reports a command directory other than the workspace the activity
is shown against, the invocation's first row ends with a muted `· in DIR`, using
the shared path display: a workspace subdirectory is relative, and an outside
directory stays absolute. That invocation's operation paths display relative to
DIR. A command in the workspace, or with an unknown directory or workspace, has
no directory label. Live and restored rows derive it from the same host item, and
adjacent reads in different directories do not merge.
Wait and input presentation follows typed host events and their command/item
identity. The activity observer does not reconstruct process or `exec` cell
state from request history, guess a command for an uncorrelated poll, or treat
printed output as continuation evidence. When a turn ends without a wait item
completion, its roster status becomes `Wait ended`, not `Waiting for agent`.
This does not claim that any child finished; a later host wait result replaces
it. Restoring a stopped turn applies the same rule without reviving the wait.

### Version-control operations

Literal git, svn, and `mchanges` reads and commits are labeled by what they do rather
than `Run`. `git commit` and `svn commit`/`ci` are `Commit`; `git add` with paths,
`-A`, or `-u` is `Stage`; `git diff`, `git show`, `svn diff`, and `mchanges` patch,
`--net`, and `--summary` reads are `Diff`; `git diff --check` is `Check`; and
`git status` (long, short, or porcelain v1) and `svn status` are `Status`. The label
names the heading, any paths after `--` or as svn operands, and the source tool with
flags that change what the row shows, such as `git --stat` or `git -C DIR`.
A commit's heading is the first non-blank line of a literal `-m` message, a quoted
heredoc or here-string read through `-F -`, or the `"$(cat <<'EOF' … EOF)"` idiom
read as the text it substitutes; a message from a file or the existing commit has
no heading. A diff's heading is its scope: `working tree`, `staged`, the revisions
as written, `rN` for svn, or the mchanges IDs (`mine` when none).
Revision syntax such as `HEAD~2` is literal where its tilde cannot expand.
Dynamic words, globs, brace expansions, environment assignments, `git -c`,
options that write files, run external diff programs, open an editor, stage
interactively, or change the output form (`--dry-run`, `-p`, `-c`, `--output`,
`--ext-diff`, `-z`, `--porcelain=v2`, `svn st -u`/`-v`), redirections other than a
commit's message on stdin and discarded or merged stderr, `REV:PATH` reads,
`mchanges --list`/`--history`, and `mchanges revert`/`apply` remain `Run`.
Unknown Git diff/show options, custom pretty formats and patch prefixes, and
mixed file-summary formats likewise remain `Run` rather than misreading output.
Copy-detecting stat/numstat reads need `--summary` to distinguish copies from moves.

A successful command's own output supplies its rows: a unified diff's files with
counts taken within each hunk's stated extent, git's stat, numstat, shortstat,
name-status, and name-only forms, `svn diff --summarize`, status entries with their
branch and upstream distance, and the files `svn commit` sent with its revision.
Git patch copies show the destination as created, with a `copy of` note; the source
is not reported as moved. A `--stat` graph scaled to its width shows each file's
total, not a split. A git commit's output names its hash, branch, and totals; its files and counts come from a
read-only `git show --numstat --summary` of that hash in the command's host cwd. The
read runs off the UI loop, at most two at a time, bounded to two seconds and 4 MiB,
and cached by hash; until it returns, and without that cwd or a readable object, the
output's created, deleted, and moved files show without counts. Consecutive Git
`-C` options resolve in order. After a prior shell segment other than a recognized
VCS command, the initial cwd is no longer proven: enrichment is omitted unless
the commit specifies an absolute `-C` directory, including on replay.
Staging and checks, which print nothing on success, may precede a commit in one
invocation's combined output; any other list keeps the plain tail because its output
cannot be attributed. Tracked segments read their own unsanitized output. Output
that names no commit claims none, and unreadable output keeps its tail.

### Program and edit previews

Whole-program previews use fenced code blocks with the selected interpreter
language. Literal interpreter wrappers, including `python -c`,
`python - <<'PY'`, and `node -e`, show a multi-line program rather than a Bash
wrapper, under a header naming the command with `…` for the program, as
`Ran python3 -c …` or `Ran python3 -`. A one-line program shows as the literal
command, which names its interpreter. A lone `-` with nothing redirected to
stdin adds a muted `· program read from stdin`. This same projector supplies provisional streaming previews and completed
operation displays. It never evaluates shell expansions or implies that a
command succeeded. Source line breaks and indentation remain intact.

Literal, statically scoped `sed -i` edits use the shared capture parser to show
`Edit` intent immediately, without waiting for a retained receipt. This requested
operation has no applied counts; the captured `mchanges` evidence later replaces
it in place with the observed file changes. Read-only `sed` is not an edit.
`cat` output redirections also show edit intent for simple variable targets,
keeping the target expression unevaluated and the heredoc body out of Run cards.
Literal, statically scoped `rm` commands show requested `Delete` intent.
Literal, statically scoped `mv` commands show requested `Move` intent with the
source and destination operands. Explicit directory targets show each source's
destination within that directory. An unforced two-operand destination remains
the requested operand; only captured evidence establishes the resulting paths.
A receipt replaces edit intent without removing neighboring operations in a
mixed script; combined host output remains attached to the final operation.

Supported Python and JavaScript writes with source-named targets also show
requested `Edit` intent. If no target can be resolved, the interpreter keeps its
normal `Run` row and command source/output dialog. Partial target resolution
keeps the named requested edits plus a `Run` source row for unresolved effects.
A successful tracked edit segment reports that its writer command ran, including
successful no-ops, as soon as the segment completes, even while following commands
are running. This does not confirm a file change or supply counts. Running and
untracked edit intent remains requested. Retained file evidence later replaces
successful intent with observed changes. Tracked failed and skipped edit segments
keep their outcomes visible, even when sibling edits have a receipt.

Stock `apply_patch`, including transparent nested tool calls using immutable
literal patch bindings, does not emit a bare `Edit` label, a generic `Run`
preview, or its patch body into child activity. After the host result and
workspace outcome are recorded, authenticated successful edit receipts
classify each changed path as
`Create`, `Edit`, `Delete`, or `Move` with added and removed line counts and a
bounded diff of the observed hunks. A completed transparent `exec` cell
reports its complete observed workspace effect the same way, without claiming
nested patch success.
Classification uses the same review files as `mchanges`; it does not guess from
the command text. Paths inside the workspace display relatively; outside paths
remain absolute. Incomplete captures show unavailable counts. Failed and
unfinished patches produce no successful edit summary, and repeated receipts
are deduplicated. The live pane separately owns full provisional and completed
diff display under [REQ-CHANGES-001](changes.md).
Completed native file-change items can open their own host-supplied diffs before
the containing `exec` batch exits. Durable captured review remains preferred
when available; this immediate navigation neither snapshots the workspace nor
publishes completed change evidence. Pending, failed, and declined host items
do not supply a completed-diff navigation fallback.

### Other tools and grouping

Native MCP items retain server/tool identity; other tool items use their typed
host identity and available arguments. Unknown calls remain visible without claiming
an inferred operation. [Native activity presentation](activity_display.md) owns grouping,
clipping, and agent headings. Source item identity deduplicates live and restored
activity without altering provider responses or the host's replay history.

Acceptance:

1. Native calls keep their original inputs, results, execution order, and continuation
   authority while their host events receive useful labels.
2. Parallel `Promise.allSettled` operations remain concurrent. Host event identity,
   not guessed JavaScript or printed results, establishes their activity state.
3. Edit intent is not successful edit evidence. Confirmed summaries require retained
   host results and observed workspace effects under [changes](changes.md).
4. Unknown calls preserve their identity and arguments; projection neither executes
   source nor claims agent completion.
